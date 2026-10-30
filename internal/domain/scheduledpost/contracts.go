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
	Cancel(context.Context, string, string) error
	// Approve releases the member's held schedule with this ID; sql.ErrNoRows when it is no longer active.
	Approve(ctx context.Context, memberID, id string) error
	// HoldForApproval holds the draft's pending or retrying schedule again after an MCP change.
	HoldForApproval(ctx context.Context, memberID, draftID string) error
}

type Story interface {
	Schedule(context.Context, CreateInput) (Post, error)
	Status(context.Context, string, string) (Post, error)
	ListActive(context.Context, string) ([]Post, error)
	Cancel(context.Context, string, string) error
	RunWorker(context.Context)
}
