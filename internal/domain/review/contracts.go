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
	// CountNoticesSince counts notices sent by the AI app (requested false) or asked for by the member (requested true).
	CountNoticesSince(ctx context.Context, memberID string, since time.Time, requested bool) (int, error)
	SetNoticeMessage(ctx context.Context, memberID, id string, messageID, photoMessageID int64) error
	// NoticeByMessage finds the notice whose Telegram preview (or its image) is messageID; ErrNotFound when none.
	NoticeByMessage(ctx context.Context, memberID string, messageID int64) (Notice, error)
	CreateEditRequest(ctx context.Context, edit EditRequest) error
	ListEditRequests(ctx context.Context, memberID string, unappliedOnly bool) ([]EditRequest, error)
	// MarkEditApplied sets applied_at once; ErrNotFound when missing, ErrEditApplied when already applied.
	MarkEditApplied(ctx context.Context, memberID, id string, at time.Time) (EditRequest, error)
	CountEditRequestsSince(ctx context.Context, memberID string, since time.Time) (int, error)
	CreateSuggestion(ctx context.Context, suggestion Suggestion) error
	RateSuggestion(ctx context.Context, memberID, id string, rating int, at time.Time) error
	MarkSuggestionPosted(ctx context.Context, memberID, id string, at time.Time) (Suggestion, error)
	ListSuggestions(ctx context.Context, memberID string, since time.Time) ([]Suggestion, error)
	CountSuggestionsSince(ctx context.Context, memberID string, since time.Time) (int, error)
	SetSuggestionMessage(ctx context.Context, memberID, id string, messageID int64) error
	SuggestionByID(ctx context.Context, memberID, id string) (Suggestion, error)
	// SuggestionByMessage finds the suggestion sent as Telegram message messageID; ErrNotFound when none.
	SuggestionByMessage(ctx context.Context, memberID string, messageID int64) (Suggestion, error)
	// SaveFirstComment stores (or replaces) the comment to send the member when the draft's post publishes.
	SaveFirstComment(ctx context.Context, memberID, draftID, text string, at time.Time) error
	// FirstComment returns the draft's first comment; ErrNotFound when none.
	FirstComment(ctx context.Context, memberID, draftID string) (string, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// NotifyScheduledStory tells the member in Telegram that a post is scheduled, with Edit and Skip.
type NotifyScheduledStory interface {
	Notify(ctx context.Context, memberID, draftID, note string) (Notice, error)
	// NotifyDraft sends the preview of an unscheduled draft: no time, no Skip.
	NotifyDraft(ctx context.Context, memberID, draftID, note string) (Notice, error)
}

// EditRequestsStory lets the member's AI app read edit requests and mark them applied.
type EditRequestsStory interface {
	List(ctx context.Context, memberID string, unappliedOnly bool) ([]EditRequest, error)
	MarkApplied(ctx context.Context, memberID, id string) (EditRequest, error)
}

// FirstCommentStory stores the first comment for a scheduled draft (links allowed: it never goes into the post).
type FirstCommentStory interface {
	Save(ctx context.Context, memberID, draftID, text string) error
}

type SendSuggestionStory interface {
	Send(ctx context.Context, input SuggestionInput) (Suggestion, error)
}

type ListFeedbackStory interface {
	List(ctx context.Context, memberID string, since time.Time) (Feedback, error)
}
