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
}

type Story interface {
	Schedule(context.Context, CreateInput) (Post, error)
	Status(context.Context, string, string) (Post, error)
	ListActive(context.Context, string) ([]Post, error)
	Cancel(context.Context, string, string) error
	RunWorker(context.Context)
}
