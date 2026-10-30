package review

import (
	"context"
	"time"

	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

type Repository interface {
	CreateApproval(ctx context.Context, approval Approval) error
	ApprovalByID(ctx context.Context, memberID, id string) (Approval, error)
	// DecideApproval moves a pending approval to status; ErrNotFound when it is not pending.
	DecideApproval(ctx context.Context, memberID, id string, status ApprovalStatus, at time.Time) (Approval, error)
	SupersedePending(ctx context.Context, memberID, draftID string, at time.Time) error
	ListApprovals(ctx context.Context, memberID string, since time.Time) ([]Approval, error)
	CountApprovalsSince(ctx context.Context, memberID string, since time.Time) (int, error)
	CreateSuggestion(ctx context.Context, suggestion Suggestion) error
	RateSuggestion(ctx context.Context, memberID, id string, rating int, at time.Time) error
	MarkSuggestionPosted(ctx context.Context, memberID, id string, at time.Time) error
	ListSuggestions(ctx context.Context, memberID string, since time.Time) ([]Suggestion, error)
	CountSuggestionsSince(ctx context.Context, memberID string, since time.Time) (int, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// ScheduleMCPDraftStory schedules a draft for the member's AI app; the post waits for approval.
type ScheduleMCPDraftStory interface {
	Schedule(ctx context.Context, memberID, draftID string, publishAt time.Time) (scheduledDomain.Post, error)
}

type RequestApprovalStory interface {
	Request(ctx context.Context, memberID, draftID, note string) (Approval, error)
}

// DraftChangeHold puts an MCP-changed draft back on hold and retires its open previews.
type DraftChangeHold interface {
	HoldForApproval(ctx context.Context, memberID, draftID string) error
}

type SendSuggestionStory interface {
	Send(ctx context.Context, input SuggestionInput) (Suggestion, error)
}

type ListFeedbackStory interface {
	List(ctx context.Context, memberID string, since time.Time) (Feedback, error)
}
