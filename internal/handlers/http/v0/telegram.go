package v0

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

const telegramUpdateTimeout = 2 * time.Minute

type telegramUser struct {
	Username string `json:"username"`
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
}

type telegramChat struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type telegramUpdate struct {
	Message *struct {
		From  *telegramUser `json:"from"`
		Voice *struct {
			FileID   string `json:"file_id"`
			MimeType string `json:"mime_type"`
			Duration int    `json:"duration"`
			FileSize int64  `json:"file_size"`
		} `json:"voice"`
		ReplyToMessage *struct {
			From      *telegramUser `json:"from"`
			MessageID int64         `json:"message_id"`
		} `json:"reply_to_message"`
		Text string       `json:"text"`
		Chat telegramChat `json:"chat"`
	} `json:"message"`
	CallbackQuery *struct {
		Message *struct {
			Chat      telegramChat `json:"chat"`
			MessageID int64        `json:"message_id"`
		} `json:"message"`
		ID   string       `json:"id"`
		Data string       `json:"data"`
		From telegramUser `json:"from"`
	} `json:"callback_query"`
	UpdateID int64 `json:"update_id"`
}

type telegramLinkResponse struct {
	DeepLink  string `json:"deepLink,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	Username  string `json:"username,omitempty"`
	LinkedAt  string `json:"linkedAt,omitempty"`
	Number    int    `json:"number,omitempty"`
	Linked    bool   `json:"linked"`
}

// TelegramWebhook accepts Bot API updates. Only requests carrying the webhook secret set with
// setWebhook are accepted; the update is queued and handled after the response, in order per chat.
func (s *service) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if s.telegramBotStory == nil {
		writeAPIError(w, http.StatusNotFound, "telegram_disabled", "Telegram bot is not configured")
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if s.config.TelegramWebhookSecret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(s.config.TelegramWebhookSecret)) != 1 {
		writeAPIError(w, http.StatusUnauthorized, "invalid_secret", "invalid webhook secret")
		return
	}
	var raw telegramUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid update")
		return
	}
	update := toTelegramUpdate(raw)
	if update.Message == nil && update.Callback == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.telegramQueueOnce.Do(func() {
		s.telegramQueue = newTelegramUpdateQueue(s.telegramBotStory, telegramWorkers, telegramMaxQueued)
	})
	if !s.telegramQueue.enqueue(context.WithoutCancel(r.Context()), update) {
		logger.FromContext(r.Context()).Error("telegram_update_dropped_queue_full")
	}
	w.WriteHeader(http.StatusOK)
}

const (
	// telegramWorkers bounds updates handled at once (each voice note holds up to 20 MB in memory).
	telegramWorkers = 4
	// telegramMaxQueued bounds updates waiting in memory; more are dropped and logged.
	telegramMaxQueued = 500
)

// telegramUpdateQueue handles updates one at a time per chat, in arrival order, with at most `workers`
// chats in flight. A panic in one update is logged and does not stop the API.
type telegramUpdateQueue struct {
	bot     telegramDomain.BotStory
	slots   chan struct{}
	mu      sync.Mutex
	pending map[int64][]queuedUpdate
	queued  int
	max     int
}

type queuedUpdate struct {
	ctx    context.Context
	update telegramDomain.Update
}

func newTelegramUpdateQueue(bot telegramDomain.BotStory, workers, maxQueued int) *telegramUpdateQueue {
	return &telegramUpdateQueue{bot: bot, slots: make(chan struct{}, workers), pending: map[int64][]queuedUpdate{}, max: maxQueued}
}

// enqueue adds the update to its chat's queue and starts a drainer for the chat when none runs; false when full.
func (q *telegramUpdateQueue) enqueue(ctx context.Context, update telegramDomain.Update) bool {
	chat := updateChatID(update)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.queued >= q.max {
		return false
	}
	q.queued++
	_, running := q.pending[chat]
	q.pending[chat] = append(q.pending[chat], queuedUpdate{ctx: ctx, update: update})
	if !running {
		go q.drain(chat)
	}
	return true
}

func (q *telegramUpdateQueue) drain(chat int64) {
	for {
		q.mu.Lock()
		items := q.pending[chat]
		if len(items) == 0 {
			delete(q.pending, chat)
			q.mu.Unlock()
			return
		}
		next := items[0]
		q.pending[chat] = items[1:]
		q.queued--
		q.mu.Unlock()
		q.slots <- struct{}{}
		q.handle(next)
		<-q.slots
	}
}

func (q *telegramUpdateQueue) handle(item queuedUpdate) {
	ctx, cancel := context.WithTimeout(item.ctx, telegramUpdateTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.FromContext(ctx).Error(fmt.Sprintf("telegram_update_panic: %v", recovered))
		}
	}()
	if err := q.bot.HandleUpdate(ctx, item.update); err != nil {
		logger.FromContext(ctx).WithError(err).Error("telegram_update_failed")
	}
}

func updateChatID(update telegramDomain.Update) int64 {
	if update.Message != nil {
		return update.Message.ChatID
	}
	if update.Callback != nil {
		return update.Callback.ChatID
	}
	return 0
}

// TelegramLink starts (POST), reads (GET) or removes (DELETE) the signed-in member's Telegram link.
func (s *service) TelegramLink(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if s.telegramLinkStory == nil {
		writeAPIError(w, http.StatusNotFound, "telegram_disabled", "Telegram bot is not configured")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	_, memberID, ok := s.requireMember(w, r, logger.Fields{"route": "/api/telegram/link"}, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		start, err := s.telegramLinkStory.Start(r.Context(), memberID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "telegram_link_failed", "Telegram link could not be started")
			return
		}
		writeJSON(w, telegramLinkResponse{DeepLink: start.DeepLink, Number: start.Number, ExpiresAt: start.ExpiresAt.UTC().Format(time.RFC3339)})
	case http.MethodDelete:
		if err := s.telegramLinkStory.Unlink(r.Context(), memberID); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "telegram_unlink_failed", "Telegram could not be unlinked")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		link, err := s.telegramLinkStory.Status(r.Context(), memberID)
		if errors.Is(err, telegramDomain.ErrNotLinked) {
			writeJSON(w, telegramLinkResponse{})
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "telegram_status_failed", "Telegram status could not be loaded")
			return
		}
		writeJSON(w, telegramLinkResponse{Linked: true, Username: link.Username, LinkedAt: link.LinkedAt.UTC().Format(time.RFC3339)})
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func toTelegramUpdate(raw telegramUpdate) telegramDomain.Update {
	update := telegramDomain.Update{ID: raw.UpdateID}
	if m := raw.Message; m != nil && m.From != nil {
		message := &telegramDomain.IncomingMessage{ChatID: m.Chat.ID, ChatType: m.Chat.Type, Text: m.Text,
			From: telegramDomain.Sender{ID: m.From.ID, Username: m.From.Username, IsBot: m.From.IsBot}}
		if m.Voice != nil {
			message.Voice = &telegramDomain.Voice{FileID: m.Voice.FileID, MimeType: m.Voice.MimeType, Duration: m.Voice.Duration, FileSize: m.Voice.FileSize}
		}
		if m.ReplyToMessage != nil {
			message.ReplyToMessageID = m.ReplyToMessage.MessageID
			message.ReplyToBot = m.ReplyToMessage.From != nil && m.ReplyToMessage.From.IsBot
		}
		update.Message = message
	}
	if c := raw.CallbackQuery; c != nil && c.Message != nil {
		update.Callback = &telegramDomain.Callback{ID: c.ID, Data: c.Data, ChatID: c.Message.Chat.ID, MessageID: c.Message.MessageID,
			From: telegramDomain.Sender{ID: c.From.ID, Username: c.From.Username, IsBot: c.From.IsBot}}
	}
	return update
}
