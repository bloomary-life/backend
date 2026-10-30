package insight

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("insight not found")

// ErrDailyLimit stops a member from storing (or transcribing) more insights than the daily cap.
var ErrDailyLimit = errors.New("daily insight limit reached")

type Source string

const (
	SourceText  Source = "text"
	SourceVoice Source = "voice"
)

const (
	MaxTextRunes    = 4096
	DailyLimit      = 60
	DailyVoiceLimit = 20
	// DailyVoiceSeconds caps the audio sent to transcription per member per 24 hours (Deepgram bills by the minute).
	DailyVoiceSeconds = 3600
	DefaultListLimit  = 100
	MaxVoiceSeconds   = 600
	MaxVoiceFileBytes = 20 << 20
)

// Insight is a raw idea a member sent to the Bloomary bot; their own AI app turns it into a draft.
type Insight struct {
	CreatedAt time.Time
	UsedAt    *time.Time
	ID        string
	MemberID  string
	Text      string
	Source    Source
	DraftID   string
}

type ListFilter struct {
	Since      time.Time
	MemberID   string
	Limit      int
	UnusedOnly bool
}
