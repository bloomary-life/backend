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
