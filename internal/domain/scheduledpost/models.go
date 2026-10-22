package scheduledpost

import (
	"errors"
	"time"
)

var ErrProRequired = errors.New("Bloomary Pro is required to schedule posts")

type Status string

const (
	Pending      Status = "pending"
	Processing   Status = "processing"
	Retry        Status = "retry"
	Published    Status = "published"
	Failed       Status = "failed"
	NeedsReview  Status = "needs_review"
	DeletedDraft Status = "deleted_draft"
	Cancelled    Status = "cancelled"
)

type Post struct {
	ID             string
	MemberID       string
	DraftID        string
	ScheduledAt    time.Time
	Status         Status
	LinkedInPostID string
	Error          string
}

type CreateInput struct {
	ReviewConfirmed bool
	MemberID        string
	DraftID         string
	ScheduledAt     time.Time
}
