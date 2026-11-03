package account

import (
	"context"
	"time"
)

// DeletedMember is one line of the deletion log.
type DeletedMember struct {
	At       time.Time
	MemberID string
}

// DeletionLog records deleted accounts outside the database, so a restore of an older backup can delete them again
// (ops notes/backup-restore.md).
type DeletionLog interface {
	Append(ctx context.Context, memberID string, at time.Time) error
	// Since lists the members deleted after the given time, oldest first.
	Since(ctx context.Context, since time.Time) ([]DeletedMember, error)
}
