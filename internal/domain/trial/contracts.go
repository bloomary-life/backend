package trial

import (
	"context"
	"time"
)

type Repository interface {
	Ensure(ctx context.Context, email, memberID string, now time.Time) (Record, error)
}
