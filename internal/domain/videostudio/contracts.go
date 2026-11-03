package videostudio

import (
	"context"
	"io"
	"time"
)

type VideoRequestRepository interface {
	Create(ctx context.Context, request VideoRequest) error
	// Save updates the request only when it is still at request.Version (ErrVersionConflict otherwise).
	Save(ctx context.Context, request VideoRequest) error
	ByID(ctx context.Context, memberID, id string) (VideoRequest, error)
	// ByMessage finds the request one of whose Telegram messages has this ID in this chat.
	ByMessage(ctx context.Context, chatID, messageID int64) (VideoRequest, error)
	// ByStatus lists every request in this status, oldest first (the voice-over job reads "voicing").
	ByStatus(ctx context.Context, status RequestStatus) ([]VideoRequest, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// StudioRetention deletes what the retention table (market/docs/legal/ropa.md rows 3, 4, 8) no longer keeps.
type StudioRetention interface {
	// FinishedBefore lists requests approved, discarded, cancelled or failed, last changed before cutoff, that
	// still hold media keys (producer uploads, voice-over).
	FinishedBefore(ctx context.Context, cutoff time.Time) ([]VideoRequest, error)
	// DeleteCreatedBefore deletes requests created before cutoff with their producer jobs.
	DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int, error)
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
	// Leased returns the job when leaseToken holds its lease (ErrJobCancelled, ErrLeaseLost otherwise).
	Leased(ctx context.Context, id, leaseToken string) (ProducerJob, error)
	// StartedSince lists when the member's jobs that start work (JobKind.StartsWork) were created since then.
	StartedSince(ctx context.Context, memberID string, since time.Time) ([]time.Time, error)
	// OverTime lists unfinished, uncancelled jobs first claimed before claimedBefore (past the time limit).
	OverTime(ctx context.Context, claimedBefore time.Time) ([]ProducerJob, error)
	// WaitingSince lists unclaimed jobs created before then (the producer may be offline).
	WaitingSince(ctx context.Context, before time.Time) ([]ProducerJob, error)
}

// MediaStore is where the producer uploads and the backend checks and copies studio files (R2).
type MediaStore interface {
	// Key is the object key of one studio file of a request (e.g. "<job>-master", "approved-master").
	Key(memberID, requestID, name string) string
	UploadURL(ctx context.Context, key, contentType string) (string, time.Time, error)
	DownloadURL(ctx context.Context, key string) (string, time.Time, error)
	Size(ctx context.Context, key string) (int64, error)
	// SHA256 streams the object to hash it.
	SHA256(ctx context.Context, key string) (string, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Read(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key, contentType string, data []byte) error
	Copy(ctx context.Context, sourceKey, destinationKey string) error
	Delete(ctx context.Context, key string) error
}

type EventKind string

const (
	EventScenarioReady   EventKind = "scenario_ready"
	EventNeedsPaidGo     EventKind = "needs_paid_go"
	EventVideoReady      EventKind = "video_ready"
	EventCaptionsUpdated EventKind = "captions_updated"
	EventFailed          EventKind = "failed"
	EventTimeLimit       EventKind = "time_limit"
	EventOffline         EventKind = "offline"
	EventOnline          EventKind = "online"
)

// StudioNotifier tells the member what happened to a request (adapter: the Telegram studio); it may update
// request.Messages and saves them itself.
type StudioNotifier interface {
	RequestEvent(ctx context.Context, request VideoRequest, event EventKind) error
}

// VoiceOver makes the voice-over audio (adapter: gates/gemini/tts.go) and returns its object key.
type VoiceOver interface {
	Speak(ctx context.Context, text string) (audioKey string, err error)
}

// StudioAllowance: the studio allowlist now; at phase 7 the plan rule.
type StudioAllowance interface {
	Allowed(ctx context.Context, memberID string) (allowed bool, reason string)
}
