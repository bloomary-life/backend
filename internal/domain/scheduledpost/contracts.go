package scheduledpost

import (
	"context"
	"time"
)

type Repository interface {
	Schedule(context.Context, CreateInput) (Post, error)
	ByID(context.Context, string, string) (Post, error)
	ListActive(context.Context, string) ([]Post, error)
	ClaimDue(context.Context, time.Time) (Post, error)
	Update(context.Context, string, Status, string, string) error
	Retry(context.Context, string, time.Time, string) error
	// Cancel reports whether a pending, retry or needs_review job was cancelled.
	Cancel(context.Context, string, string) (bool, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type Story interface {
	Schedule(context.Context, CreateInput) (Post, error)
	Status(context.Context, string, string) (Post, error)
	ListActive(context.Context, string) ([]Post, error)
	Cancel(context.Context, string, string) error
	RunWorker(context.Context)
}

// PublishedHook runs after a scheduled post is published on LinkedIn (status published, LinkedInPostID set).
type PublishedHook interface {
	PostPublished(ctx context.Context, post Post)
}

// PublishedHooks is implemented by a Story that can run a hook after each publish; set it before RunWorker starts.
type PublishedHooks interface {
	OnPublished(hook PublishedHook)
}
