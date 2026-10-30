package v0

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	insightMocks "bloomary-api/internal/mocks/domain/insight"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/stories"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestTelegramWebhookRejectsRequestsWithoutTheSecret(t *testing.T) {
	// Verifies: BE-009
	bot := telegramMocks.NewMockBotStory(t)
	svc := &service{telegramBotStory: bot, config: Config{TelegramWebhookSecret: "webhook-secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(`{"update_id":1,"message":{"from":{"id":1},"chat":{"id":1,"type":"private"},"text":"hi"}}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestTelegramWebhookPassesVoiceUpdateToBot(t *testing.T) {
	// Verifies: BE-010
	handled := make(chan telegramDomain.Update, 1)
	bot := telegramMocks.NewMockBotStory(t)
	bot.EXPECT().HandleUpdate(mock.Anything, mock.Anything).Run(func(_ context.Context, update telegramDomain.Update) { handled <- update }).Return(nil)
	svc := &service{telegramBotStory: bot, config: Config{TelegramWebhookSecret: "webhook-secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(
		`{"update_id":1,"message":{"message_id":5,"from":{"id":77,"is_bot":false,"username":"kat"},"chat":{"id":77,"type":"private"},"voice":{"file_id":"f-1","file_unique_id":"u","duration":12,"mime_type":"audio/ogg","file_size":2048}}}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "webhook-secret")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	select {
	case update := <-handled:
		require.Equal(t, int64(77), update.Message.From.ID)
		require.Equal(t, "private", update.Message.ChatType)
		require.Equal(t, &telegramDomain.Voice{FileID: "f-1", MimeType: "audio/ogg", Duration: 12, FileSize: 2048}, update.Message.Voice)
		require.Equal(t, int64(1), update.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("update was not handled")
	}
}

func TestTelegramLinkStartReturnsDeepLinkForSignedInMember(t *testing.T) {
	// Verifies: BE-009
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	links := telegramMocks.NewMockLinkStory(t)
	links.EXPECT().Start(mock.Anything, "member-1").Return(telegramDomain.LinkStart{DeepLink: "https://t.me/BloomaryBot?start=l_x", Number: 42, ExpiresAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}, nil)
	svc := &service{linkedIn: linkedIn, telegramLinkStory: links}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/link?memberId=member-1", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()

	svc.TelegramLink(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var body telegramLinkResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, telegramLinkResponse{DeepLink: "https://t.me/BloomaryBot?start=l_x", Number: 42, ExpiresAt: "2026-10-30T12:00:00Z"}, body)
}

func TestTelegramWebhookStoresVoiceReplyToPreviewAsEditRequest(t *testing.T) {
	// Verifies: BE-017
	links := telegramMocks.NewMockLinkRepository(t)
	links.EXPECT().MarkUpdateSeen(mock.Anything, int64(2), mock.Anything).Return(true, nil)
	links.EXPECT().ByTelegramUser(mock.Anything, int64(77)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 77, TelegramUserID: 77}, nil)
	insights := insightMocks.NewMockRepository(t)
	insights.EXPECT().ReserveVoice(mock.Anything, "member-1", 8, mock.Anything, mock.Anything).Return(true, nil)
	gate := telegramMocks.NewMockGate(t)
	gate.EXPECT().DownloadFile(mock.Anything, "f-1", mock.Anything).Return([]byte("ogg"), nil)
	transcriber := telegramMocks.NewMockTranscriber(t)
	transcriber.EXPECT().Transcribe(mock.Anything, mock.Anything, "audio/ogg").Return(telegramDomain.Transcript{Text: "Make it shorter and drop the last line"}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	notice := reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", Status: reviewDomain.NoticeSent, MessageID: 5}
	reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(5)).Return(notice, nil)
	reviews.EXPECT().NoticeByID(mock.Anything, "member-1", "n-1").Return(notice, nil)
	reviews.EXPECT().CountEditRequestsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	created := make(chan reviewDomain.EditRequest, 1)
	reviews.EXPECT().CreateEditRequest(mock.Anything, mock.Anything).Run(func(_ context.Context, e reviewDomain.EditRequest) { created <- e }).Return(nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{ID: "job-1", Status: scheduledDomain.Pending}, nil)
	replied := make(chan string, 1)
	gate.EXPECT().SendMessage(mock.Anything, mock.Anything).Run(func(_ context.Context, m telegramDomain.OutgoingMessage) { replied <- m.Text }).Return(9, nil)
	replies, err := stories.NewEditReplies(reviews, jobs, draftMocks.NewMockRepository(t))
	require.NoError(t, err)
	bot, err := stories.NewTelegramBot(links, gate, transcriber, insights, accountMocks.NewMockRepository(t), "https://platform.bloomary.life",
		stories.TelegramBotHandlers{Replies: []telegramDomain.ReplyHandler{replies}, VoiceMembers: telegramDomain.MemberSet{"member-1": true}})
	require.NoError(t, err)
	svc := &service{telegramBotStory: bot, config: Config{TelegramWebhookSecret: "webhook-secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(
		`{"update_id":2,"message":{"message_id":6,"from":{"id":77,"is_bot":false},"chat":{"id":77,"type":"private"},"reply_to_message":{"message_id":5},"voice":{"file_id":"f-1","duration":8,"mime_type":"audio/ogg","file_size":2048}}}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "webhook-secret")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	select {
	case edit := <-created:
		require.Equal(t, "draft-1", edit.DraftID)
		require.Equal(t, "n-1", edit.NoticeID)
		require.Equal(t, "Make it shorter and drop the last line", edit.Text)
		require.Equal(t, "voice", edit.Source)
	case <-time.After(2 * time.Second):
		t.Fatal("edit request was not stored")
	}
	select {
	case text := <-replied:
		require.Equal(t, "Got it: “Make it shorter and drop the last line”\n\nWhen your AI app rewrites the post, the new preview comes here.", text)
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
}

type recordingBot struct {
	mu      sync.Mutex
	handled []string
	done    chan struct{}
}

func (b *recordingBot) HandleUpdate(_ context.Context, update telegramDomain.Update) error {
	if update.Message.Text == "panic" {
		panic("boom")
	}
	if update.Message.Text == "slow" {
		time.Sleep(50 * time.Millisecond)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handled = append(b.handled, update.Message.Text)
	if len(b.handled) == 3 {
		close(b.done)
	}
	return nil
}

func TestTelegramUpdateQueueKeepsOrderPerChatAndSurvivesPanics(t *testing.T) {
	// Verifies: BE-022
	bot := &recordingBot{done: make(chan struct{})}
	queue := newTelegramUpdateQueue(bot, 4, 10)
	message := func(text string) telegramDomain.Update {
		return telegramDomain.Update{Message: &telegramDomain.IncomingMessage{ChatID: 1, Text: text}}
	}

	log, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)
	ctx := log.ToContext(context.Background())

	for _, text := range []string{"slow", "panic", "make it shorter", "no, keep length"} {
		require.True(t, queue.enqueue(ctx, message(text)))
	}

	select {
	case <-bot.done:
	case <-time.After(2 * time.Second):
		t.Fatal("updates were not handled")
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	require.Equal(t, []string{"slow", "make it shorter", "no, keep length"}, bot.handled)
}

func TestTelegramUpdateQueueRefusesWhenFull(t *testing.T) {
	// Verifies: BE-022
	queue := newTelegramUpdateQueue(&recordingBot{done: make(chan struct{})}, 1, 0)

	require.False(t, queue.enqueue(context.Background(), telegramDomain.Update{Message: &telegramDomain.IncomingMessage{ChatID: 1}}))
}

func TestTelegramWebhookAnswers503WhenTheQueueIsClosed(t *testing.T) {
	// Verifies: BE-022
	svc := &service{telegramBotStory: telegramMocks.NewMockBotStory(t), config: Config{TelegramWebhookSecret: "webhook-secret"}}
	svc.DrainTelegram(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(
		`{"update_id":3,"message":{"message_id":5,"from":{"id":77,"is_bot":false},"chat":{"id":77,"type":"private"},"text":"idea"}}`))
	log, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)
	request = request.WithContext(log.ToContext(request.Context()))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "webhook-secret")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code, "Telegram resends after a non-2xx answer")
}

func TestTelegramUpdateQueueDrainsBeforeClosing(t *testing.T) {
	// Verifies: BE-022
	bot := &recordingBot{done: make(chan struct{})}
	queue := newTelegramUpdateQueue(bot, 1, 10)
	for _, text := range []string{"slow", "a", "b"} {
		require.True(t, queue.enqueue(context.Background(), telegramDomain.Update{Message: &telegramDomain.IncomingMessage{ChatID: 1, Text: text}}))
	}

	queue.drainAndClose(context.Background())

	bot.mu.Lock()
	defer bot.mu.Unlock()
	require.Equal(t, []string{"slow", "a", "b"}, bot.handled)
	require.False(t, queue.enqueue(context.Background(), telegramDomain.Update{Message: &telegramDomain.IncomingMessage{ChatID: 1}}))
}

func TestTelegramLinkStatusSaysWhetherVoiceIsAllowed(t *testing.T) {
	// Verifies: BE-021
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	links := telegramMocks.NewMockLinkStory(t)
	links.EXPECT().Status(mock.Anything, "member-1").Return(telegramDomain.Link{}, telegramDomain.ErrNotLinked)
	links.EXPECT().VoiceAllowed("member-1").Return(false)
	svc := &service{linkedIn: linkedIn, telegramLinkStory: links}
	request := httptest.NewRequest(http.MethodGet, "/api/telegram/link?memberId=member-1", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()

	svc.TelegramLink(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"linked":false,"voice":false}`, response.Body.String())
}
