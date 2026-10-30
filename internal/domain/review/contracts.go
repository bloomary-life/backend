package review

import (
	"context"
	"time"
)

type Repository interface {
	CreateNotice(ctx context.Context, notice Notice) error
	NoticeByID(ctx context.Context, memberID, id string) (Notice, error)
	// SkipNotice marks a sent notice skipped; ErrNotFound when it is not in status sent.
	SkipNotice(ctx context.Context, memberID, id string, at time.Time) (Notice, error)
	ListNotices(ctx context.Context, memberID string, since time.Time) ([]Notice, error)
	CountNoticesSince(ctx context.Context, memberID string, since time.Time) (int, error)
	CreateSuggestion(ctx context.Context, suggestion Suggestion) error
	RateSuggestion(ctx context.Context, memberID, id string, rating int, at time.Time) error
	MarkSuggestionPosted(ctx context.Context, memberID, id string, at time.Time) error
	ListSuggestions(ctx context.Context, memberID string, since time.Time) ([]Suggestion, error)
	CountSuggestionsSince(ctx context.Context, memberID string, since time.Time) (int, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// NotifyScheduledStory tells the member in Telegram that a post is scheduled, with Edit and Skip.
type NotifyScheduledStory interface {
	Notify(ctx context.Context, memberID, draftID, note string) (Notice, error)
}

type SendSuggestionStory interface {
	Send(ctx context.Context, input SuggestionInput) (Suggestion, error)
}

type ListFeedbackStory interface {
	List(ctx context.Context, memberID string, since time.Time) (Feedback, error)
}
