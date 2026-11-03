package stories

import (
	"context"
	"fmt"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	"bloomary-api/internal/platform/logger"
)

type loggedAccountDeletion struct {
	accountDomain.AccountDeletionStory
	log accountDomain.DeletionLog
}

// NewLoggedAccountDeletion appends each deleted member to the deletion log after the deletion succeeds (delete
// account and LinkedIn disconnect both delete every member record).
func NewLoggedAccountDeletion(story accountDomain.AccountDeletionStory, log accountDomain.DeletionLog) (accountDomain.AccountDeletionStory, error) {
	if story == nil || log == nil {
		return nil, fmt.Errorf("logged account deletion: story and deletion log are required")
	}
	return &loggedAccountDeletion{AccountDeletionStory: story, log: log}, nil
}

func (s *loggedAccountDeletion) DeleteAccount(ctx context.Context, input accountDomain.DeleteAccountInput) error {
	if err := s.AccountDeletionStory.DeleteAccount(ctx, input); err != nil {
		return err
	}
	return s.record(ctx, input.MemberID)
}

func (s *loggedAccountDeletion) DisconnectLinkedIn(ctx context.Context, input accountDomain.DisconnectLinkedInInput) error {
	if err := s.AccountDeletionStory.DisconnectLinkedIn(ctx, input); err != nil {
		return err
	}
	return s.record(ctx, input.MemberID)
}

func (s *loggedAccountDeletion) record(ctx context.Context, memberID string) error {
	if err := s.log.Append(ctx, memberID, time.Now().UTC()); err != nil {
		logger.FromContext(ctx).WithError(err).Error("deletion_log_append_failed")
		return fmt.Errorf("account deleted, deletion log not written: %w", err)
	}
	return nil
}

// ReplayDeletions deletes again every member in the deletion log deleted after since (the restored backup's time),
// through the same account deletion members use. It does not append to the log again. Returns the member IDs replayed.
func ReplayDeletions(ctx context.Context, log accountDomain.DeletionLog, deletion accountDomain.AccountDeletionStory, since time.Time) ([]string, error) {
	deleted, err := log.Since(ctx, since)
	if err != nil {
		return nil, err
	}
	replayed := make([]string, 0, len(deleted))
	for _, member := range deleted {
		if err := deletion.DeleteAccount(ctx, accountDomain.DeleteAccountInput{MemberID: member.MemberID}); err != nil {
			return replayed, fmt.Errorf("replay deletion of %s: %w", member.MemberID, err)
		}
		replayed = append(replayed, member.MemberID)
	}
	return replayed, nil
}
