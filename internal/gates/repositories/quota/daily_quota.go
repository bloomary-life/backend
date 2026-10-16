package quota

import (
	"context"
	"fmt"
	"strings"
	"time"

	dailyDomain "bloomary-api/internal/domain/daily"
)

type DailyQuota struct {
	repo  dailyDomain.Repository
	limit int
}

func NewDailyQuota(repo dailyDomain.Repository, limit int) (*DailyQuota, error) {
	if repo == nil {
		return nil, fmt.Errorf("daily quota: daily repo is required")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("daily quota: limit must be positive")
	}

	return &DailyQuota{
		repo:  repo,
		limit: limit,
	}, nil
}

func (q *DailyQuota) Consume(ctx context.Context, memberID string, now time.Time) (bool, error) {
	memberID = strings.TrimSpace(memberID)
	if memberID == "" {
		return true, nil
	}

	return q.repo.ConsumeQuota(ctx, memberID, now, q.limit)
}
