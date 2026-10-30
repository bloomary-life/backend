package telegram

import (
	"context"
	"time"
)

// Gate is the Telegram Bot API.
type Gate interface {
	SendMessage(ctx context.Context, message OutgoingMessage) (int64, error)
	SendPhoto(ctx context.Context, photo Photo) (int64, error)
	EditMessageText(ctx context.Context, chatID, messageID int64, text string) error
	// AnswerCallback shows text as a short toast, or as a dialog the member closes when alert is true.
	AnswerCallback(ctx context.Context, callbackID, text string, alert bool) error
	DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error)
	SetMyCommands(ctx context.Context, commands []Command) error
}

// Transcriber turns a voice note into text; the audio is never stored.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (Transcript, error)
}

type LinkRepository interface {
	StartAttempt(ctx context.Context, attempt LinkAttempt) error
	// ConsumeAttempt deletes the attempt and returns it if it had not expired by now.
	ConsumeAttempt(ctx context.Context, nonce string, now time.Time) (LinkAttempt, error)
	AttemptByNonce(ctx context.Context, nonce string, now time.Time) (LinkAttempt, error)
	SaveLink(ctx context.Context, link Link) error
	ByTelegramUser(ctx context.Context, telegramUserID int64) (Link, error)
	ByMemberID(ctx context.Context, memberID string) (Link, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
	// MarkUpdateSeen records a webhook update ID; false when it was already seen (Telegram resent it).
	MarkUpdateSeen(ctx context.Context, updateID int64, at time.Time) (bool, error)
}

// LinkStory is the web side of linking: start an attempt, read and remove the link.
type LinkStory interface {
	Start(ctx context.Context, memberID string) (LinkStart, error)
	Status(ctx context.Context, memberID string) (Link, error)
	Unlink(ctx context.Context, memberID string) error
	// VoiceAllowed reports whether the member may send voice notes to the bot.
	VoiceAllowed(memberID string) bool
}

// BotStory handles one webhook update from Telegram.
type BotStory interface {
	HandleUpdate(ctx context.Context, update Update) error
}

// CallbackHandler handles inline-button presses of a linked member whose data starts with Prefix.
// The returned text is shown to the member as the callback answer.
type CallbackHandler interface {
	Prefix() string
	HandleCallback(ctx context.Context, link Link, callback Callback) (string, error)
}

// ReplyHandler takes a linked member's reply to one of the bot's messages, e.g. an edit request for a post preview.
type ReplyHandler interface {
	// Claim reports whether the reply targets a message this handler owns (ok); it reads no audio.
	Claim(ctx context.Context, link Link, replyToMessageID int64) (claim ReplyClaim, ok bool, err error)
	// Save stores the reply text (source "text" or "voice") and returns the confirmation for the member.
	Save(ctx context.Context, link Link, claim ReplyClaim, text, source string) (string, error)
}

// CommandHandler answers a linked member's slash command (e.g. /planned) or its reply-keyboard button.
type CommandHandler interface {
	Command() string
	HandleCommand(ctx context.Context, link Link, chatID int64) error
}

// Notifier sends a member a Telegram message through their link.
type Notifier interface {
	Notify(ctx context.Context, memberID string, message OutgoingMessage, photo *Photo) (Delivery, error)
	// Replace sets the text of an earlier message to the member and removes its buttons.
	Replace(ctx context.Context, memberID string, messageID int64, text string) error
}
