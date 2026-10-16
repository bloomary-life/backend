package metrics

import (
	"context"
	"time"
)

// PostPublishRepository records post publish events for metrics.
type PostPublishRepository interface {
	RecordPostPublished(ctx context.Context, memberID string, now time.Time) error
}

// MetricsRepository provides data for product gauge calculations.
type MetricsRepository interface {
	CountConnectedUsers(ctx context.Context) (int, error)
	CountSignupsSince(ctx context.Context, since time.Time) (int, error)
	CountActivatedSince(ctx context.Context, since time.Time) (int, error)
	CountActiveSince(ctx context.Context, since time.Time) (int, error)
	CountUsersWithMinPostsSince(ctx context.Context, minPosts int, since time.Time) (int, error)
}
