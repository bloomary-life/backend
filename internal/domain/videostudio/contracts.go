package videostudio

import (
	"context"
	"time"
)

type VideoRequestRepository interface {
	Create(ctx context.Context, request VideoRequest) error
	// Save updates the request only when it is still at request.Version (ErrVersionConflict otherwise).
	Save(ctx context.Context, request VideoRequest) error
	ByID(ctx context.Context, memberID, id string) (VideoRequest, error)
	// ByMessage finds the request one of whose Telegram messages has this ID in this chat.
	ByMessage(ctx context.Context, chatID, messageID int64) (VideoRequest, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// ProducerJobQueue is the database job queue the producer on Kat's Mac pulls from.
type ProducerJobQueue interface {
	Enqueue(ctx context.Context, job ProducerJob) error
	// Claim updates last seen and leases the oldest waiting job (ErrNotFound when none).
	Claim(ctx context.Context, now time.Time, lease time.Duration) (ProducerJob, error)
	// Heartbeat extends the lease; ErrJobCancelled when Kat cancelled, ErrLeaseLost on a wrong or expired token.
	Heartbeat(ctx context.Context, id, leaseToken string, until time.Time) error
	Complete(ctx context.Context, id, leaseToken string) error
	Fail(ctx context.Context, id, leaseToken, reason string) error
	Cancel(ctx context.Context, requestID string) error
	LastSeen(ctx context.Context) (time.Time, error)
}

// VoiceOver makes the voice-over audio (adapter: gates/gemini/tts.go) and returns its object key.
type VoiceOver interface {
	Speak(ctx context.Context, text string) (audioKey string, err error)
}

// StudioAllowance: the studio allowlist now; at phase 7 the plan rule.
type StudioAllowance interface {
	Allowed(ctx context.Context, memberID string) (allowed bool, reason string)
}
