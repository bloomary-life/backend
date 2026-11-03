package crosspost

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrNeedsReconnect    = errors.New("crosspost: connection needs reconnecting")
	ErrContentRejected   = errors.New("crosspost: network rejected the content")
	ErrInvalidTransition = errors.New("crosspost: invalid status change")
	// ErrOutcomeUnknown: failure after the create/publish request was sent → needs_review, never auto-retried.
	ErrOutcomeUnknown = errors.New("crosspost: outcome unknown after the publish request was sent")

	ErrNotFound = errors.New("crosspost: not found")
	// ErrVersionConflict: the record changed since it was read ("This post changed. Reload to see the newest version.").
	ErrVersionConflict = errors.New("crosspost: record changed since it was read")
	// ErrOpenPublication: the draft already has an open publication.
	ErrOpenPublication = errors.New("crosspost: draft already has an open publication")
	ErrAlreadyApproved = errors.New("crosspost: publication already approved")
)

// RateLimitedError: the network asked to wait; the delivery goes back to scheduled at now + RetryAfter.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e RateLimitedError) Error() string {
	return fmt.Sprintf("crosspost: rate limited, retry after %s", e.RetryAfter)
}

// NetworkError is a network failure in a domain category (Err: ErrNeedsReconnect, ErrContentRejected, …) with the
// network's own code, for LastErrorCode. It carries no URL, token or network message text.
type NetworkError struct {
	Err     error
	Network string
	Code    string
}

func (e NetworkError) Error() string { return e.Err.Error() + " (" + e.Network + " " + e.Code + ")" }
func (e NetworkError) Unwrap() error { return e.Err }

// ErrorCode is LastErrorCode for err: category + network code, e.g. "content_rejected:x:403".
func ErrorCode(err error) string {
	category := "error"
	var limited RateLimitedError
	switch {
	case errors.Is(err, ErrNeedsReconnect):
		category = "needs_reconnect"
	case errors.Is(err, ErrContentRejected):
		category = "content_rejected"
	case errors.Is(err, ErrOutcomeUnknown):
		category = "outcome_unknown"
	case errors.As(err, &limited):
		category = "rate_limited"
	}
	var network NetworkError
	if errors.As(err, &network) {
		return category + ":" + network.Network + ":" + network.Code
	}
	return category
}
