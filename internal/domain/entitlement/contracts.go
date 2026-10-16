package entitlement

import (
	"context"
	"time"
)

type AccessStory interface {
	Resolve(ctx context.Context, memberID string, now time.Time) (Entitlement, error)
}
