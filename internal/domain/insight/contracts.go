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
	// ReserveVoice records one transcription attempt of seconds of audio, atomically, only while the member's
	// attempts since `since` stay under DailyVoiceLimit and their seconds within DailyVoiceSeconds.
	ReserveVoice(ctx context.Context, memberID string, seconds int, since, at time.Time) (bool, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type ListMCPInsightsStory interface {
	List(ctx context.Context, filter ListFilter) ([]Insight, error)
}

type MarkMCPInsightUsedStory interface {
	MarkUsed(ctx context.Context, memberID, insightID, draftID string) (Insight, error)
}
