package stories

import (
	"context"
	"fmt"

	accountDomain "bloomary-api/internal/domain/account"
	dailyDomain "bloomary-api/internal/domain/daily"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	"bloomary-api/internal/platform/logger"
)

type accountDeletionStory struct {
	dailyRepo   dailyDomain.Repository
	draftRepo   draftDomain.Repository
	draftImages draftDomain.ObjectStore
	accountRepo accountDomain.Repository
	// beforeDrafts run first (cross-posting: its deliveries and media go before the drafts they came from).
	beforeDrafts []accountDomain.MemberDataDeleter
	memberData   []accountDomain.MemberDataDeleter
}

// NewAccountDeletionStory creates a new AccountDeletionStory implementation.
func NewAccountDeletionStory(
	dailyRepo dailyDomain.Repository,
	draftRepo draftDomain.Repository,
	accountRepo accountDomain.Repository,
	draftImages ...draftDomain.ObjectStore,
) (accountDomain.AccountDeletionStory, error) {
	return NewAccountDeletionStoryWithMemberData(dailyRepo, draftRepo, accountRepo, nil, draftImages...)
}

// NewAccountDeletionStoryWithMemberData also deletes each feature's member data (insights, Telegram link, reviews).
func NewAccountDeletionStoryWithMemberData(
	dailyRepo dailyDomain.Repository,
	draftRepo draftDomain.Repository,
	accountRepo accountDomain.Repository,
	memberData []accountDomain.MemberDataDeleter,
	draftImages ...draftDomain.ObjectStore,
) (accountDomain.AccountDeletionStory, error) {
	return NewAccountDeletionStoryWithSteps(dailyRepo, draftRepo, accountRepo, nil, memberData, draftImages...)
}

// NewAccountDeletionStoryWithSteps also runs beforeDrafts ahead of the draft step (DeleteCrosspostDataStory).
func NewAccountDeletionStoryWithSteps(
	dailyRepo dailyDomain.Repository,
	draftRepo draftDomain.Repository,
	accountRepo accountDomain.Repository,
	beforeDrafts []accountDomain.MemberDataDeleter,
	memberData []accountDomain.MemberDataDeleter,
	draftImages ...draftDomain.ObjectStore,
) (accountDomain.AccountDeletionStory, error) {
	if dailyRepo == nil {
		return nil, fmt.Errorf("account deletion story: daily repo is required")
	}
	if draftRepo == nil {
		return nil, fmt.Errorf("account deletion story: draft repo is required")
	}
	if accountRepo == nil {
		return nil, fmt.Errorf("account deletion story: account repo is required")
	}

	var imageStore draftDomain.ObjectStore
	if len(draftImages) > 0 {
		imageStore = draftImages[0]
	}

	return &accountDeletionStory{
		dailyRepo:    dailyRepo,
		draftRepo:    draftRepo,
		draftImages:  imageStore,
		accountRepo:  accountRepo,
		beforeDrafts: beforeDrafts,
		memberData:   memberData,
	}, nil
}

// DisconnectLinkedIn disconnects a LinkedIn account.
// It cleans up LinkedIn-specific data (daily post counts, drafts, account).
func (s *accountDeletionStory) DisconnectLinkedIn(ctx context.Context, input accountDomain.DisconnectLinkedInInput) error {
	if input.MemberID == "" {
		return fmt.Errorf("member ID is required")
	}

	if err := s.deleteAllData(ctx, input.MemberID); err != nil {
		logger.FromContext(ctx).WithError(err).Error("linkedin_disconnect_failed")
		return fmt.Errorf("disconnect failed: %w", err)
	}

	logger.FromContext(ctx).Info("linkedin_disconnected")
	return nil
}

// DeleteAccount deletes a user account and all associated data.
func (s *accountDeletionStory) DeleteAccount(ctx context.Context, input accountDomain.DeleteAccountInput) error {
	if input.MemberID == "" {
		return fmt.Errorf("member ID is required")
	}

	if err := s.deleteAllData(ctx, input.MemberID); err != nil {
		logger.FromContext(ctx).WithError(err).Error("account_deletion_failed")
		return fmt.Errorf("account deletion failed: %w", err)
	}

	logger.FromContext(ctx).Info("account_deleted")
	return nil
}

func (s *accountDeletionStory) deleteAllData(ctx context.Context, memberID string) error {
	if err := s.dailyRepo.DeleteByMemberID(ctx, memberID); err != nil {
		return fmt.Errorf("delete daily data: %w", err)
	}

	for _, data := range s.beforeDrafts {
		if err := data.DeleteByMemberID(ctx, memberID); err != nil {
			return fmt.Errorf("delete member data before drafts: %w", err)
		}
	}

	if s.draftImages != nil {
		drafts, err := s.draftRepo.Sync(ctx, memberID, nil, nil, entitlementDomain.ProDraftLimit)
		if err != nil {
			return fmt.Errorf("load draft images: %w", err)
		}
		if err := deleteObjectKeys(ctx, s.draftImages, draftImageObjectKeys(drafts, nil)); err != nil {
			return fmt.Errorf("delete draft images: %w", err)
		}
	}

	if err := s.draftRepo.DeleteByMemberID(ctx, memberID); err != nil {
		return fmt.Errorf("delete drafts: %w", err)
	}

	for _, data := range s.memberData {
		if err := data.DeleteByMemberID(ctx, memberID); err != nil {
			return fmt.Errorf("delete member data: %w", err)
		}
	}

	if err := s.accountRepo.Delete(ctx, memberID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	return nil
}
