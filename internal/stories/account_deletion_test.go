package stories

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	accountDomain "bloomary-api/internal/domain/account"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	streakMocks "bloomary-api/internal/mocks/domain/daily"
	"bloomary-api/internal/platform/logger"
)

type AccountDeletionStorySuite struct {
	suite.Suite
	Ctx context.Context
}

func TestAccountDeletionStorySuite(t *testing.T) {
	suite.Run(t, new(AccountDeletionStorySuite))
}

func (s *AccountDeletionStorySuite) SetupTest() {
	log, _ := logger.New(logger.Output(io.Discard))
	s.Ctx = log.ToContext(context.Background())
}

func (s *AccountDeletionStorySuite) TestDisconnectLinkedIn_Success() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(nil)
	draftRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(nil)
	accountRepo.On("Delete", s.Ctx, "member-123").Return(nil)

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DisconnectLinkedIn(s.Ctx, accountDomain.DisconnectLinkedInInput{
		MemberID: "member-123",
	})

	s.Require().NoError(err)
	dailyRepo.AssertExpectations(s.T())
	draftRepo.AssertExpectations(s.T())
	accountRepo.AssertExpectations(s.T())
}

func (s *AccountDeletionStorySuite) TestDisconnectLinkedIn_EmptyMemberID() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DisconnectLinkedIn(s.Ctx, accountDomain.DisconnectLinkedInInput{
		MemberID: "",
	})

	s.Error(err)
	s.Contains(err.Error(), "member ID is required")
	dailyRepo.AssertNotCalled(s.T(), "DeleteByMemberID")
}

func (s *AccountDeletionStorySuite) TestDisconnectLinkedIn_DailyDeleteFails() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(errors.New("db error"))

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DisconnectLinkedIn(s.Ctx, accountDomain.DisconnectLinkedInInput{
		MemberID: "member-123",
	})

	s.Error(err)
	s.Contains(err.Error(), "delete daily data")
	draftRepo.AssertNotCalled(s.T(), "DeleteByMemberID")
	accountRepo.AssertNotCalled(s.T(), "Delete")
}

func (s *AccountDeletionStorySuite) TestDisconnectLinkedIn_DraftDeleteFails() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(nil)
	draftRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(errors.New("db error"))

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DisconnectLinkedIn(s.Ctx, accountDomain.DisconnectLinkedInInput{
		MemberID: "member-123",
	})

	s.Error(err)
	s.Contains(err.Error(), "delete drafts")
	accountRepo.AssertNotCalled(s.T(), "Delete")
}

func (s *AccountDeletionStorySuite) TestDisconnectLinkedIn_AccountDeleteFails() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(nil)
	draftRepo.On("DeleteByMemberID", s.Ctx, "member-123").Return(nil)
	accountRepo.On("Delete", s.Ctx, "member-123").Return(errors.New("db error"))

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DisconnectLinkedIn(s.Ctx, accountDomain.DisconnectLinkedInInput{
		MemberID: "member-123",
	})

	s.Error(err)
	s.Contains(err.Error(), "delete account")
}

func (s *AccountDeletionStorySuite) TestDeleteAccount_Success() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-456").Return(nil)
	draftRepo.On("DeleteByMemberID", s.Ctx, "member-456").Return(nil)
	accountRepo.On("Delete", s.Ctx, "member-456").Return(nil)

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DeleteAccount(s.Ctx, accountDomain.DeleteAccountInput{
		MemberID: "member-456",
	})

	s.Require().NoError(err)
	dailyRepo.AssertExpectations(s.T())
	draftRepo.AssertExpectations(s.T())
	accountRepo.AssertExpectations(s.T())
}

func (s *AccountDeletionStorySuite) TestDeleteAccount_DeletesLinkedDraftImages() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	draftImages := new(draftMocks.MockObjectStore)
	accountRepo := new(accountMocks.MockRepository)

	dailyRepo.On("DeleteByMemberID", s.Ctx, "member-456").Return(nil)
	draftRepo.On("Sync", s.Ctx, "member-456", mock.Anything, mock.Anything, entitlementDomain.ProDraftLimit).Return(
		[]draftDomain.Draft{
			{
				DraftID: "draft-1",
				Image: &draftDomain.DraftImage{
					ObjectKey: "draft-images/test/linked/member-456/draft-1/image-1",
				},
			},
		},
		nil,
	)
	draftImages.On("Delete", s.Ctx, "draft-images/test/linked/member-456/draft-1/image-1").Return(nil)
	draftRepo.On("DeleteByMemberID", s.Ctx, "member-456").Return(nil)
	accountRepo.On("Delete", s.Ctx, "member-456").Return(nil)

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo, draftImages)
	s.Require().NoError(err)

	err = story.DeleteAccount(s.Ctx, accountDomain.DeleteAccountInput{
		MemberID: "member-456",
	})

	s.Require().NoError(err)
	dailyRepo.AssertExpectations(s.T())
	draftRepo.AssertExpectations(s.T())
	draftImages.AssertExpectations(s.T())
	accountRepo.AssertExpectations(s.T())
}

func (s *AccountDeletionStorySuite) TestDeleteAccount_EmptyMemberID() {
	dailyRepo := new(streakMocks.MockRepository)
	draftRepo := new(draftMocks.MockRepository)
	accountRepo := new(accountMocks.MockRepository)

	story, err := NewAccountDeletionStory(dailyRepo, draftRepo, accountRepo)
	s.Require().NoError(err)

	err = story.DeleteAccount(s.Ctx, accountDomain.DeleteAccountInput{
		MemberID: "",
	})

	s.Error(err)
	s.Contains(err.Error(), "member ID is required")
	dailyRepo.AssertNotCalled(s.T(), "DeleteByMemberID")
}
