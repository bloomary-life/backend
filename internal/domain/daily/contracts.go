package daily

import (
	"context"
	"time"
)

type Repository interface {
	ConsumeQuota(ctx context.Context, memberID string, now time.Time, limit int) (bool, error)
	MarkPosted(ctx context.Context, memberID string, now time.Time) error
	TotalPosts(ctx context.Context, memberID string) (int, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type QuotaConsumer interface {
	Consume(ctx context.Context, memberID string, now time.Time) (bool, error)
}
