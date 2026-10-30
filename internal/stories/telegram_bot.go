package stories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	accountDomain "bloomary-api/internal/domain/account"
	insightDomain "bloomary-api/internal/domain/insight"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"

	"github.com/google/uuid"
)

const (
	botNotLinked    = "This Telegram isn't linked to Bloomary yet. Sign in to Bloomary with LinkedIn, then open Settings → Telegram → Connect Telegram: %s/settings"
	botHelpVoice    = "Send me a voice note (up to 10 minutes) or text: each one is saved as a post idea. When your AI app turns it into a post, the preview comes here.\n\nReply to a news story from your AI app to turn your take into a post.\nReply to a preview (voice or text) to change it. Tap Discard to drop a draft, or Skip to stop a scheduled post.\n\n📅 My posts shows your scheduled posts and recent drafts.\nTo disconnect, open Bloomary → Settings → Telegram → Disconnect."
	botHelpText     = "Send me a text: each one is saved as a post idea. When your AI app turns it into a post, the preview comes here.\n\nReply to a news story from your AI app to turn your take into a post.\nReply to a preview to change it. Tap Discard to drop a draft, or Skip to stop a scheduled post.\n\n📅 My posts shows your scheduled posts and recent drafts.\nTo disconnect, open Bloomary → Settings → Telegram → Disconnect."
	botSavedTail    = "When your AI app turns it into a post, the preview comes here."
	botSaved        = "Saved as an idea. " + botSavedTail
	botSavedReply   = "Saved as a new idea. (To change a post, reply to its preview instead.)"
	botNumbers      = "Got your weekly numbers. They go into Sunday's report."
	keyPlanned      = "📅 My posts"
	keyPlannedOld   = "📅 Planned posts"
	keyHelp         = "❓ Help"
	botUnlinked     = "Unlinked. Ideas you sent stay in your Bloomary account; connected AI apps can still read them."
	botTextLimit    = "That's 60 ideas in 24 hours, the limit. Send more in a few hours."
	botVoiceLimit   = "That's the voice limit for 24 hours (20 notes or 60 minutes). Send this one as text."
	botNoVoice      = "Voice notes aren't available yet. Send the idea as text."
	botVoiceFail    = "Could not transcribe this voice note. Send it again or as text."
	botVoiceEmpty   = "I couldn't hear anything in that voice note. Send it again or type it."
	botUnsupported  = "I can read voice notes and text only. Send the idea as one of those."
	botFailed       = "Something went wrong and this wasn't saved. Send it again in a minute."
	snippetRunes    = 200
	notLinkedRepeat = time.Hour
)

// botCommandPattern is a whole message that is one command (/help, /planned@bot); "/r/golang is ..." is an idea.
var botCommandPattern = regexp.MustCompile(`^/[a-z_]+(@\w+)?$`)

// weeklyNumbersPattern matches a weekly numbers message ("numbers: ...", or "Numbers, ..." from voice); "Numbers show …" is an idea.
var weeklyNumbersPattern = regexp.MustCompile(`(?i)^\s*numbers\s*[:,]`)

// botKeyboard is the persistent reply keyboard: the main way to use the bot.
var botKeyboard = [][]string{{keyPlanned, keyHelp}}

// BotCommands is the command menu registered with setMyCommands: empty, the keyboard buttons are the way in
// (/planned and /help still work when typed).
var BotCommands = []telegramDomain.Command{}

// TelegramBotHandlers are the optional parts of the bot: inline buttons, commands and replies to its messages
// (each reply goes to the first handler that claims it, e.g. post previews, then trend messages).
type TelegramBotHandlers struct {
	Replies   []telegramDomain.ReplyHandler
	Callbacks []telegramDomain.CallbackHandler
	Commands  []telegramDomain.CommandHandler
	// VoiceMembers may send voice notes (Deepgram); everyone else gets text-only replies.
	VoiceMembers telegramDomain.MemberSet
	// NumbersMembers get the weekly-numbers reply; for others a "numbers:" message is a plain idea.
	NumbersMembers telegramDomain.MemberSet
}

var linkNoncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

type telegramBot struct {
	links       telegramDomain.LinkRepository
	bot         telegramDomain.Gate
	transcriber telegramDomain.Transcriber
	insights    insightDomain.Repository
	accounts    accountDomain.Repository
	replies     []telegramDomain.ReplyHandler
	callbacks   []telegramDomain.CallbackHandler
	commands    []telegramDomain.CommandHandler
	voice       telegramDomain.MemberSet
	numbers     telegramDomain.MemberSet
	platformURL string
	// notLinkedAt throttles the "not linked" answer to one per Telegram user per notLinkedTTL; each ID is
	// forgotten notLinkedTTL after it was stored (the privacy policy says at most one hour).
	notLinkedMu  sync.Mutex
	notLinkedAt  map[int64]time.Time
	notLinkedTTL time.Duration
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
		replies: handlers.Replies, callbacks: handlers.Callbacks, commands: handlers.Commands, platformURL: strings.TrimRight(platformURL, "/"),
		voice: handlers.VoiceMembers, numbers: handlers.NumbersMembers,
		notLinkedAt: map[int64]time.Time{}, notLinkedTTL: notLinkedRepeat}, nil
}

func (s *telegramBot) HandleUpdate(ctx context.Context, update telegramDomain.Update) error {
	if update.ID != 0 {
		fresh, err := s.links.MarkUpdateSeen(ctx, update.ID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !fresh {
			return nil
		}
	}
	if update.Callback != nil {
		return s.handleCallback(ctx, *update.Callback)
	}
	message := update.Message
	if message == nil || message.ChatType != "private" || message.From.IsBot {
		return nil
	}
	err := s.handleMessage(ctx, message)
	if err != nil {
		// The update was answered with 200 before it was handled, so Telegram will not resend it: tell the member.
		if replyErr := s.reply(ctx, message.ChatID, botFailed); replyErr != nil {
			logger.FromContext(ctx).WithError(replyErr).Info("telegram_failure_reply_failed")
		}
	}
	return err
}

func (s *telegramBot) handleMessage(ctx context.Context, message *telegramDomain.IncomingMessage) error {
	text := strings.TrimSpace(message.Text)
	if strings.HasPrefix(text, "/start l_") {
		return s.askLink(ctx, message.ChatID, strings.TrimPrefix(text, "/start l_"))
	}
	link, err := s.links.ByTelegramUser(ctx, message.From.ID)
	if errors.Is(err, telegramDomain.ErrNotLinked) {
		return s.replyNotLinked(ctx, message)
	}
	if err != nil {
		return err
	}
	isCommand := botCommandPattern.MatchString(text)
	command := ""
	if isCommand {
		command = strings.SplitN(text, "@", 2)[0]
	}
	if text == keyPlanned || text == keyPlannedOld {
		command = "/planned"
	}
	for _, handler := range s.commands {
		if command == handler.Command() {
			return handler.HandleCommand(ctx, link, message.ChatID)
		}
	}
	switch {
	case command == "/unlink":
		if err := s.links.DeleteByMemberID(ctx, link.MemberID); err != nil {
			return err
		}
		_, err := s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: message.ChatID, Text: botUnlinked, RemoveKeyboard: true})
		return err
	case isCommand, text == keyHelp:
		return s.reply(ctx, message.ChatID, s.help(link.MemberID))
	case message.ReplyToMessageID != 0 && len(s.replies) > 0:
		handled, err := s.handleReply(ctx, link, message)
		if handled || err != nil {
			return err
		}
	}
	// A reply to a bot message nobody claims (e.g. a runner's skip message) is a new idea, with a hint.
	saved := botSaved
	if message.ReplyToBot {
		saved = botSavedReply
	}
	switch {
	case message.Voice != nil:
		return s.saveVoice(ctx, link, message)
	case text != "":
		return s.saveInsight(ctx, link, message.ChatID, text, insightDomain.SourceText, s.confirmation(link.MemberID, text, saved))
	default:
		return s.reply(ctx, message.ChatID, botUnsupported)
	}
}

// replyNotLinked answers an unlinked user at most once per notLinkedRepeat, without the bot keyboard.
func (s *telegramBot) replyNotLinked(ctx context.Context, message *telegramDomain.IncomingMessage) error {
	now, userID, chatID := time.Now(), message.From.ID, message.ChatID
	s.notLinkedMu.Lock()
	if _, seen := s.notLinkedAt[userID]; seen {
		s.notLinkedMu.Unlock()
		return nil
	}
	s.notLinkedAt[userID] = now
	s.notLinkedMu.Unlock()
	s.forgetNotLinkedAfter(userID)
	_, err := s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: fmt.Sprintf(botNotLinked, s.platformURL)})
	return err
}

// forgetNotLinkedAfter deletes the user ID after notLinkedTTL. The timer holds only the ID, never the message (its
// text, username or voice), so nothing else of an unlinked user's message stays in memory.
func (s *telegramBot) forgetNotLinkedAfter(userID int64) {
	time.AfterFunc(s.notLinkedTTL, func() {
		s.notLinkedMu.Lock()
		delete(s.notLinkedAt, userID)
		s.notLinkedMu.Unlock()
	})
}

// confirmation is the save reply: weekly numbers get their own for members with a weekly report.
func (s *telegramBot) confirmation(memberID, text, saved string) string {
	if s.numbers[memberID] && weeklyNumbersPattern.MatchString(text) {
		return botNumbers
	}
	return saved
}

func (s *telegramBot) help(memberID string) string {
	if s.voice[memberID] && s.transcriber != nil {
		return botHelpVoice
	}
	return botHelpText
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
		if err := s.bot.AnswerCallback(ctx, callback.ID, answer, answer == editHowTo); err != nil {
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
			memberID := ""
			if link, err := s.links.ByTelegramUser(ctx, callback.From.ID); err == nil {
				memberID = link.MemberID
			}
			return s.reply(ctx, callback.ChatID, s.help(memberID))
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
		return "That number doesn't match Bloomary. Press Connect Telegram in Bloomary to get a new one.", nil
	}
	err = s.links.SaveLink(ctx, telegramDomain.Link{TelegramUserID: callback.From.ID, ChatID: callback.ChatID,
		MemberID: attempt.MemberID, Username: callback.From.Username, LinkedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	return "Linked to Bloomary.", nil
}

func (s *telegramBot) saveVoice(ctx context.Context, link telegramDomain.Link, message *telegramDomain.IncomingMessage) error {
	text, failure, err := s.transcribe(ctx, link, message)
	if err != nil {
		return err
	}
	if failure != "" {
		return s.reply(ctx, message.ChatID, failure)
	}
	return s.saveInsight(ctx, link, message.ChatID, text, insightDomain.SourceVoice, s.confirmation(link.MemberID, text, "Saved: “"+snippet(text)+"”\n\n"+botSavedTail))
}

// handleReply passes a reply to one of the bot's messages to the first reply handler that owns it; handled is
// false when none does, so the message is saved as a normal insight.
func (s *telegramBot) handleReply(ctx context.Context, link telegramDomain.Link, message *telegramDomain.IncomingMessage) (bool, error) {
	var handler telegramDomain.ReplyHandler
	var claim telegramDomain.ReplyClaim
	for _, candidate := range s.replies {
		found, ok, err := candidate.Claim(ctx, link, message.ReplyToMessageID)
		if err != nil {
			return false, err
		}
		if ok {
			handler, claim = candidate, found
			break
		}
	}
	if handler == nil {
		return false, nil
	}
	if claim.Refusal != "" {
		return true, s.reply(ctx, message.ChatID, claim.Refusal)
	}
	text, source := strings.TrimSpace(message.Text), string(insightDomain.SourceText)
	if message.Voice != nil {
		var failure string
		var err error
		if text, failure, err = s.transcribe(ctx, link, message); err != nil || failure != "" {
			if err != nil {
				return true, err
			}
			return true, s.reply(ctx, message.ChatID, failure)
		}
		source = string(insightDomain.SourceVoice)
	}
	if text == "" {
		return true, s.reply(ctx, message.ChatID, botUnsupported)
	}
	confirmation, err := handler.Save(ctx, link, claim, text, source)
	if err != nil {
		return true, err
	}
	s.confirm(ctx, message.ChatID, confirmation)
	return true, nil
}

// transcribe returns the voice note's text, or the reply to send when it cannot be transcribed. Every attempt
// that reaches Deepgram is reserved first (count and minutes per 24 hours), so failed or empty ones count too.
func (s *telegramBot) transcribe(ctx context.Context, link telegramDomain.Link, message *telegramDomain.IncomingMessage) (string, string, error) {
	// Voice goes to Deepgram (US) only for allow-listed members until its DPA is accepted.
	if s.transcriber == nil || !s.voice[link.MemberID] {
		return "", botNoVoice, nil
	}
	if message.Voice.Duration > insightDomain.MaxVoiceSeconds || message.Voice.FileSize > insightDomain.MaxVoiceFileBytes {
		return "", "Voice notes up to 10 minutes only. Split it into shorter notes.", nil
	}
	now := time.Now().UTC()
	ok, err := s.insights.ReserveVoice(ctx, link.MemberID, reservedSeconds(message.Voice), now.Add(-24*time.Hour), now)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", botVoiceLimit, nil
	}
	// The audio stays in memory for this request only: it is never written to disk or the database.
	audio, err := s.bot.DownloadFile(ctx, message.Voice.FileID, insightDomain.MaxVoiceFileBytes)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("telegram_voice_download_failed")
		return "", botVoiceFail, nil
	}
	transcript, err := s.transcriber.Transcribe(ctx, audio, message.Voice.MimeType)
	clear(audio)
	if errors.Is(err, telegramDomain.ErrTranscriptEmpty) || (err == nil && strings.TrimSpace(transcript.Text) == "") {
		return "", botVoiceEmpty, nil
	}
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("telegram_voice_transcription_failed")
		return "", botVoiceFail, nil
	}
	return strings.TrimSpace(transcript.Text), "", nil
}

func (s *telegramBot) saveInsight(ctx context.Context, link telegramDomain.Link, chatID int64, text string, source insightDomain.Source, confirmation string) error {
	if source == insightDomain.SourceText {
		ok, err := s.underLimit(ctx, link.MemberID, "", insightDomain.DailyLimit)
		if err != nil {
			return err
		}
		if !ok {
			return s.reply(ctx, chatID, botTextLimit)
		}
	}
	if utf8.RuneCountInString(text) > insightDomain.MaxTextRunes {
		text = string([]rune(text)[:insightDomain.MaxTextRunes])
	}
	err := s.insights.Create(ctx, insightDomain.Insight{ID: uuid.NewString(), MemberID: link.MemberID, Text: text, Source: source, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	s.confirm(ctx, chatID, confirmation)
	return nil
}

// confirm sends the reply after a successful save; a failure is only logged, so the member is never told
// "wasn't saved" (and never resends) when the idea is already stored.
func (s *telegramBot) confirm(ctx context.Context, chatID int64, text string) {
	if err := s.reply(ctx, chatID, text); err != nil {
		logger.FromContext(ctx).WithError(err).Info("telegram_confirmation_failed")
	}
}

// reservedSeconds is the audio length counted against the daily cap: the duration the client reports, but at
// least the file size at 16 kbit/s (2,000 bytes a second), since the client sets the duration.
func reservedSeconds(voice *telegramDomain.Voice) int {
	return max(voice.Duration, int(voice.FileSize/2000), 1)
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
