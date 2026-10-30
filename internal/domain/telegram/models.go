package telegram

import (
	"errors"
	"time"
)

var (
	ErrNotLinked       = errors.New("telegram account is not linked")
	ErrLinkNotFound    = errors.New("telegram link attempt not found or expired")
	ErrBotDisabled     = errors.New("the Bloomary Telegram bot is not configured")
	ErrTranscriberOff  = errors.New("voice transcription is not configured")
	ErrFileTooLarge    = errors.New("telegram file is too large")
	ErrTranscriptEmpty = errors.New("no speech found in the voice note")
)

const LinkAttemptTTL = 10 * time.Minute

// Link ties one Telegram user to one Bloomary member; the bot writes to ChatID.
type Link struct {
	LinkedAt       time.Time
	MemberID       string
	Username       string
	TelegramUserID int64
	ChatID         int64
}

// LinkAttempt is started in the signed-in web app; the bot confirms it when the member taps Number.
type LinkAttempt struct {
	ExpiresAt time.Time
	Nonce     string
	MemberID  string
	Number    int
}

type LinkStart struct {
	ExpiresAt time.Time
	DeepLink  string
	Number    int
}

type Sender struct {
	Username string
	ID       int64
	IsBot    bool
}

type Voice struct {
	FileID   string
	MimeType string
	Duration int
	FileSize int64
}

type IncomingMessage struct {
	Voice    *Voice
	Text     string
	ChatType string
	From     Sender
	ChatID   int64
	// ReplyToMessageID is the bot message this one replies to (0 when it is not a reply).
	ReplyToMessageID int64
}

type Callback struct {
	ID        string
	Data      string
	From      Sender
	ChatID    int64
	MessageID int64
}

// Update is one webhook delivery; at most one of Message and Callback is set.
type Update struct {
	Message  *IncomingMessage
	Callback *Callback
}

// Button is an inline keyboard button; exactly one of CallbackData, URL and CopyText is set.
type Button struct {
	Text         string
	CallbackData string
	URL          string
	CopyText     string
}

// OutgoingMessage has inline Buttons or a persistent reply Keyboard (button texts), not both.
type OutgoingMessage struct {
	Text     string
	Buttons  [][]Button
	Keyboard [][]string
	ChatID   int64
}

type Photo struct {
	Data     []byte
	MimeType string
	Caption  string
	ChatID   int64
}

// Command is one entry of the bot's command menu (setMyCommands).
type Command struct {
	Command     string
	Description string
}

// ReplyClaim is a reply that a ReplyHandler takes; Refusal, when set, is sent instead of saving it.
type ReplyClaim struct {
	ID      string
	Refusal string
}

type Transcript struct {
	Text     string
	Language string
}
