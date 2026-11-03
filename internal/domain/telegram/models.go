package telegram

import (
	"errors"
	"io"
	"strings"
	"time"
)

var (
	ErrNotLinked       = errors.New("telegram account is not linked")
	ErrLinkNotFound    = errors.New("telegram link attempt not found or expired")
	ErrBotDisabled     = errors.New("the Bloomary Telegram bot is not configured")
	ErrTranscriberOff  = errors.New("voice transcription is not configured")
	ErrFileTooLarge    = errors.New("telegram file is too large")
	ErrTranscriptEmpty = errors.New("no speech found in the voice note")
	// ErrMessageGone: the message to edit was deleted in the chat; send a new one instead.
	ErrMessageGone = errors.New("telegram message to edit not found")
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
	// ReplyToBot is true when the replied message was sent by a bot (this bot: the chat is private).
	ReplyToBot bool
}

// MemberSet is a set of member IDs from configuration (e.g. who may send voice notes).
type MemberSet map[string]bool

// NewMemberSet builds a set from IDs, ignoring blanks.
func NewMemberSet(ids []string) MemberSet {
	set := MemberSet{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	return set
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
	// ID is Telegram's update_id, used to drop repeated deliveries (0 when unknown).
	ID int64
}

// UpdateMemory is how long update IDs are kept to drop repeated deliveries.
const UpdateMemory = 24 * time.Hour

// Button is an inline keyboard button; exactly one of CallbackData, URL and CopyText is set.
type Button struct {
	Text         string
	CallbackData string
	URL          string
	CopyText     string
}

// OutgoingMessage has inline Buttons, a persistent reply Keyboard (button texts) or RemoveKeyboard, not more than one.
type OutgoingMessage struct {
	Text     string
	Buttons  [][]Button
	Keyboard [][]string
	ChatID   int64
	// RemoveKeyboard takes the persistent keyboard away (after unlinking).
	RemoveKeyboard bool
	// Silent delivers the message without a notification sound.
	Silent bool
	// Entities format parts of Text (offsets in UTF-16 code units); Telegram text is never parsed as HTML.
	Entities []Entity
	// ForceReply opens a reply to this message in the member's client, with Placeholder in the input field.
	ForceReply *ForceReply
}

// Entity is a Telegram message entity, e.g. "expandable_blockquote"; Offset and Length count UTF-16 code units.
type Entity struct {
	Type   string
	Offset int
	Length int
}

// ForceReply: Placeholder is at most 64 characters.
type ForceReply struct {
	Placeholder string
}

// Video is uploaded from Body (the bot uploads at most 50 MB); Caption is at most 1024 UTF-16 units.
type Video struct {
	Body     io.Reader
	Caption  string
	Entities []Entity
	Buttons  [][]Button
	ChatID   int64
	Seconds  int
	Width    int
	Height   int
	Silent   bool
}

// Document is a file sent as a document (e.g. the scenario as a .txt).
type Document struct {
	Data     []byte
	FileName string
	MimeType string
	Caption  string
	ChatID   int64
	Silent   bool
}

// MessageEdit replaces a message's text (a video or photo message's caption when Caption is set), its entities and
// its buttons (none = removed).
type MessageEdit struct {
	Text      string
	Entities  []Entity
	Buttons   [][]Button
	ChatID    int64
	MessageID int64
	Caption   bool
}

type Photo struct {
	Data     []byte
	MimeType string
	Caption  string
	ChatID   int64
	// Buttons is the inline keyboard under a photo sent with its post as the caption (BE-026).
	Buttons [][]Button
}

// Delivery is what a Notifier sent: the message and, when a photo went before it, the photo's message.
type Delivery struct {
	MessageID      int64
	PhotoMessageID int64
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
