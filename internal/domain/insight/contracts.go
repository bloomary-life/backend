package insight

import (
	"context"
	"time"
)

type Repository interface {
	Create(ctx context.Context, insight Insight) error
	List(ctx context.Context, filter ListFilter) ([]Insight, error)
	MarkUsed(ctx context.Context, memberID, insightID, draftID string, at time.Time) (Insight, error)
	CountSince(ctx context.Context, memberID string, source Source, since time.Time) (int, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type ListMCPInsightsStory interface {
	List(ctx context.Context, filter ListFilter) ([]Insight, error)
}

type MarkMCPInsightUsedStory interface {
	MarkUsed(ctx context.Context, memberID, insightID, draftID string) (Insight, error)
}
