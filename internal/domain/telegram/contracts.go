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
	AnswerCallback(ctx context.Context, callbackID, text string) error
	DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error)
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
}

// LinkStory is the web side of linking: start an attempt, read and remove the link.
type LinkStory interface {
	Start(ctx context.Context, memberID string) (LinkStart, error)
	Status(ctx context.Context, memberID string) (Link, error)
	Unlink(ctx context.Context, memberID string) error
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

// Notifier sends a member a Telegram message through their link.
type Notifier interface {
	Notify(ctx context.Context, memberID string, message OutgoingMessage, photo *Photo) (int64, error)
}
