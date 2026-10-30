package stories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	accountDomain "bloomary-api/internal/domain/account"
	insightDomain "bloomary-api/internal/domain/insight"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"

	"github.com/google/uuid"
)

const (
	botNotLinked = "This Telegram is not linked to Bloomary yet. Open Bloomary, go to Settings → Telegram and press Connect: %s/settings"
	botHelp      = "Send me a voice note or a text: each one is saved as an insight in your Bloomary account. Your AI app reads insights through the Bloomary connector and turns them into drafts.\n\n📅 Planned posts lists your scheduled posts. To change a post, reply to its preview with a voice note or text.\n\n/unlink disconnects this Telegram."
	botSaved     = "Saved — I'll send you the post preview in a few minutes."
	botSavedTail = "\n\nI'll send you the post preview in a few minutes."
	keyPlanned   = "📅 Planned posts"
	keyHelp      = "❓ Help"
	botUnlinked  = "Unlinked. Your insights stay in Bloomary."
	botLimit     = "Daily insight limit reached. Try again tomorrow."
	botNoVoice   = "Voice notes are not available right now. Send the idea as text."
	botVoiceFail = "Could not transcribe this voice note. Send it again or as text."
	snippetRunes = 200
)

// botKeyboard is the persistent reply keyboard: the main way to use the bot.
var botKeyboard = [][]string{{keyPlanned, keyHelp}}

// BotCommands is the command menu registered with setMyCommands.
var BotCommands = []telegramDomain.Command{{Command: "planned", Description: "Scheduled posts with previews"}, {Command: "help", Description: "How the bot works"}}

// TelegramBotHandlers are the optional parts of the bot: inline buttons, commands and replies to its messages.
type TelegramBotHandlers struct {
	Replies   telegramDomain.ReplyHandler
	Callbacks []telegramDomain.CallbackHandler
	Commands  []telegramDomain.CommandHandler
}

var linkNoncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

type telegramBot struct {
	links       telegramDomain.LinkRepository
	bot         telegramDomain.Gate
	transcriber telegramDomain.Transcriber
	insights    insightDomain.Repository
	accounts    accountDomain.Repository
	replies     telegramDomain.ReplyHandler
	callbacks   []telegramDomain.CallbackHandler
	commands    []telegramDomain.CommandHandler
	platformURL string
}

var _ telegramDomain.BotStory = (*telegramBot)(nil)

// NewTelegramBot builds the bot story. transcriber may be nil: voice notes then get a text-only reply.
func NewTelegramBot(
	links telegramDomain.LinkRepository, bot telegramDomain.Gate, transcriber telegramDomain.Transcriber,
	insights insightDomain.Repository, accounts accountDomain.Repository, platformURL string, handlers TelegramBotHandlers,
) (telegramDomain.BotStory, error) {
	if links == nil || bot == nil || insights == nil || accounts == nil {
		return nil, fmt.Errorf("telegram bot story: links, bot gate, insights and accounts are required")
	}
	return &telegramBot{links: links, bot: bot, transcriber: transcriber, insights: insights, accounts: accounts,
		replies: handlers.Replies, callbacks: handlers.Callbacks, commands: handlers.Commands, platformURL: strings.TrimRight(platformURL, "/")}, nil
}

func (s *telegramBot) HandleUpdate(ctx context.Context, update telegramDomain.Update) error {
	if update.Callback != nil {
		return s.handleCallback(ctx, *update.Callback)
	}
	message := update.Message
	if message == nil || message.ChatType != "private" || message.From.IsBot {
		return nil
	}
	text := strings.TrimSpace(message.Text)
	if strings.HasPrefix(text, "/start l_") {
		return s.askLink(ctx, message.ChatID, strings.TrimPrefix(text, "/start l_"))
	}
	link, err := s.links.ByTelegramUser(ctx, message.From.ID)
	if errors.Is(err, telegramDomain.ErrNotLinked) {
		return s.reply(ctx, message.ChatID, fmt.Sprintf(botNotLinked, s.platformURL))
	}
	if err != nil {
		return err
	}
	command := strings.SplitN(text, "@", 2)[0]
	if command == keyPlanned {
		command = "/planned"
	}
	for _, handler := range s.commands {
		if command == handler.Command() {
			return handler.HandleCommand(ctx, link, message.ChatID)
		}
	}
	switch {
	case text == "/unlink":
		if err := s.links.DeleteByMemberID(ctx, link.MemberID); err != nil {
			return err
		}
		return s.reply(ctx, message.ChatID, botUnlinked)
	case strings.HasPrefix(text, "/"), text == keyHelp, text == keyPlanned:
		return s.reply(ctx, message.ChatID, botHelp)
	case message.ReplyToMessageID != 0 && s.replies != nil:
		handled, err := s.handleReply(ctx, link, message)
		if handled || err != nil {
			return err
		}
	}
	switch {
	case message.Voice != nil:
		return s.saveVoice(ctx, link, message)
	case text != "":
		return s.saveInsight(ctx, link, message.ChatID, text, insightDomain.SourceText, botSaved)
	default:
		return s.reply(ctx, message.ChatID, botHelp)
	}
}

func (s *telegramBot) askLink(ctx context.Context, chatID int64, nonce string) error {
	if !linkNoncePattern.MatchString(nonce) {
		return s.reply(ctx, chatID, "This link is not valid. Start again in Bloomary → Settings → Telegram.")
	}
	attempt, err := s.links.AttemptByNonce(ctx, nonce, time.Now().UTC())
	if err != nil {
		return s.reply(ctx, chatID, "This link expired. Start again in Bloomary → Settings → Telegram.")
	}
	account := "your Bloomary account"
	if profile, err := s.accounts.ByMemberID(ctx, attempt.MemberID); err == nil && strings.TrimSpace(profile.Name) != "" {
		account = "the Bloomary account of " + strings.TrimSpace(profile.Name)
	}
	choices, err := linkChoices(attempt.Number)
	if err != nil {
		return err
	}
	row := make([]telegramDomain.Button, 0, len(choices))
	for _, n := range choices {
		row = append(row, telegramDomain.Button{Text: strconv.Itoa(n), CallbackData: "tl:" + strconv.Itoa(n) + ":" + nonce})
	}
	_, err = s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID,
		Text:    "Link this Telegram to " + account + "?\n\nTo confirm, tap the number shown in Bloomary. Not you? Tap No.",
		Buttons: [][]telegramDomain.Button{row, {{Text: "No", CallbackData: "tl:no:" + nonce}}}})
	return err
}

func (s *telegramBot) handleCallback(ctx context.Context, callback telegramDomain.Callback) error {
	answer := ""
	defer func() {
		if err := s.bot.AnswerCallback(ctx, callback.ID, answer); err != nil {
			logger.FromContext(ctx).WithError(err).Info("telegram_answer_callback_failed")
		}
	}()
	if strings.HasPrefix(callback.Data, "tl:") {
		text, err := s.confirmLink(ctx, callback)
		if err != nil {
			return err
		}
		if err := s.bot.EditMessageText(ctx, callback.ChatID, callback.MessageID, text); err != nil {
			return err
		}
		if strings.HasPrefix(text, "Linked") {
			return s.reply(ctx, callback.ChatID, botHelp)
		}
		return nil
	}
	link, err := s.links.ByTelegramUser(ctx, callback.From.ID)
	if err != nil {
		answer = "Link this Telegram to Bloomary first."
		return nil
	}
	for _, handler := range s.callbacks {
		if strings.HasPrefix(callback.Data, handler.Prefix()) {
			answer, err = handler.HandleCallback(ctx, link, callback)
			return err
		}
	}
	answer = "This button is no longer active."
	return nil
}

func (s *telegramBot) confirmLink(ctx context.Context, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(callback.Data, ":")
	if len(parts) != 3 || !linkNoncePattern.MatchString(parts[2]) {
		return "This link is not valid. Start again in Bloomary → Settings → Telegram.", nil
	}
	attempt, err := s.links.ConsumeAttempt(ctx, parts[2], time.Now().UTC())
	if err != nil {
		return "This link expired. Start again in Bloomary → Settings → Telegram.", nil
	}
	if parts[1] == "no" {
		return "Cancelled. Nothing changed.", nil
	}
	if parts[1] != strconv.Itoa(attempt.Number) {
		return "That number does not match Bloomary. Nothing changed; start again in Bloomary → Settings → Telegram.", nil
	}
	err = s.links.SaveLink(ctx, telegramDomain.Link{TelegramUserID: callback.From.ID, ChatID: callback.ChatID,
		MemberID: attempt.MemberID, Username: callback.From.Username, LinkedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	return "Linked to Bloomary.", nil
}

func (s *telegramBot) saveVoice(ctx context.Context, link telegramDomain.Link, message *telegramDomain.IncomingMessage) error {
	if s.transcriber != nil && message.Voice.Duration <= insightDomain.MaxVoiceSeconds && message.Voice.FileSize <= insightDomain.MaxVoiceFileBytes {
		if ok, err := s.underLimit(ctx, link.MemberID, insightDomain.SourceVoice, insightDomain.DailyVoiceLimit); err != nil || !ok {
			if err != nil {
				return err
			}
			return s.reply(ctx, message.ChatID, botLimit)
		}
	}
	text, failure := s.transcribe(ctx, message)
	if failure != "" {
		return s.reply(ctx, message.ChatID, failure)
	}
	return s.saveInsight(ctx, link, message.ChatID, text, insightDomain.SourceVoice, "Saved: “"+snippet(text)+"”"+botSavedTail)
}

// handleReply passes a reply to one of the bot's messages to the reply handler; handled is false when the
// handler does not own the replied message, so the message is saved as a normal insight.
func (s *telegramBot) handleReply(ctx context.Context, link telegramDomain.Link, message *telegramDomain.IncomingMessage) (bool, error) {
	claim, ok, err := s.replies.Claim(ctx, link, message.ReplyToMessageID)
	if err != nil || !ok {
		return false, err
	}
	if claim.Refusal != "" {
		return true, s.reply(ctx, message.ChatID, claim.Refusal)
	}
	text, source := strings.TrimSpace(message.Text), string(insightDomain.SourceText)
	if message.Voice != nil {
		var failure string
		if text, failure = s.transcribe(ctx, message); failure != "" {
			return true, s.reply(ctx, message.ChatID, failure)
		}
		source = string(insightDomain.SourceVoice)
	}
	if text == "" {
		return true, s.reply(ctx, message.ChatID, editHint)
	}
	confirmation, err := s.replies.Save(ctx, link, claim, text, source)
	if err != nil {
		return true, err
	}
	return true, s.reply(ctx, message.ChatID, confirmation)
}

// transcribe returns the voice note's text, or the reply to send when it cannot be transcribed.
func (s *telegramBot) transcribe(ctx context.Context, message *telegramDomain.IncomingMessage) (string, string) {
	if s.transcriber == nil {
		return "", botNoVoice
	}
	if message.Voice.Duration > insightDomain.MaxVoiceSeconds || message.Voice.FileSize > insightDomain.MaxVoiceFileBytes {
		return "", "Voice notes up to 10 minutes only. Split it into shorter notes."
	}
	// The audio stays in memory for this request only: it is never written to disk or the database.
	audio, err := s.bot.DownloadFile(ctx, message.Voice.FileID, insightDomain.MaxVoiceFileBytes)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("telegram_voice_download_failed")
		return "", botVoiceFail
	}
	transcript, err := s.transcriber.Transcribe(ctx, audio, message.Voice.MimeType)
	clear(audio)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("telegram_voice_transcription_failed")
		return "", botVoiceFail
	}
	return transcript.Text, ""
}

func (s *telegramBot) saveInsight(ctx context.Context, link telegramDomain.Link, chatID int64, text string, source insightDomain.Source, confirmation string) error {
	if source == insightDomain.SourceText {
		ok, err := s.underLimit(ctx, link.MemberID, "", insightDomain.DailyLimit)
		if err != nil {
			return err
		}
		if !ok {
			return s.reply(ctx, chatID, botLimit)
		}
	}
	if utf8.RuneCountInString(text) > insightDomain.MaxTextRunes {
		text = string([]rune(text)[:insightDomain.MaxTextRunes])
	}
	err := s.insights.Create(ctx, insightDomain.Insight{ID: uuid.NewString(), MemberID: link.MemberID, Text: text, Source: source, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return s.reply(ctx, chatID, confirmation)
}

// underLimit counts the member's insights of the last 24 hours (source "" counts all).
func (s *telegramBot) underLimit(ctx context.Context, memberID string, source insightDomain.Source, limit int) (bool, error) {
	count, err := s.insights.CountSince(ctx, memberID, source, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		return false, err
	}
	return count < limit, nil
}

func (s *telegramBot) reply(ctx context.Context, chatID int64, text string) error {
	_, err := s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: text, Keyboard: botKeyboard})
	return err
}

// linkChoices returns the right number and three other two-digit numbers, shuffled.
func linkChoices(number int) ([]int, error) {
	choices := []int{number}
	for len(choices) < 4 {
		n, err := randomNumber(10, 99)
		if err != nil {
			return nil, err
		}
		if !containsInt(choices, n) {
			choices = append(choices, n)
		}
	}
	for i := len(choices) - 1; i > 0; i-- {
		j, err := randomNumber(0, i)
		if err != nil {
			return nil, err
		}
		choices[i], choices[j] = choices[j], choices[i]
	}
	return choices, nil
}

func containsInt(values []int, value int) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func snippet(text string) string {
	if utf8.RuneCountInString(text) <= snippetRunes {
		return text
	}
	return string([]rune(text)[:snippetRunes]) + "…"
}
