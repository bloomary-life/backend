package stories

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	accountDomain "bloomary-api/internal/domain/account"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	trialDomain "bloomary-api/internal/domain/trial"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	trialMocks "bloomary-api/internal/mocks/domain/trial"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type CompleteLinkedInAuthStorySuite struct {
	suite.Suite
	Ctx context.Context
}

func TestCompleteLinkedInAuthStorySuite(t *testing.T) {
	suite.Run(t, new(CompleteLinkedInAuthStorySuite))
}

func (s *CompleteLinkedInAuthStorySuite) SetupTest() {
	log, err := logger.New()
	s.Require().NoError(err)
	s.Ctx = log.ToContext(context.Background())
}

func (s *CompleteLinkedInAuthStorySuite) newStory(
	linkedInGate linkedinDomain.Gate,
	accountRepo accountWriter,
	trialRepo trialDomain.Repository,
	flows accountDomain.OAuthStateStore,
) accountDomain.LinkedInAuthStory {
	if flows == nil {
		flows = new(accountMocks.MockOAuthStateStore)
	}
	story, err := NewLinkedInAuthStory(linkedInGate, accountRepo, trialRepo, flows)
	s.Require().NoError(err)
	return story
}

func (s *CompleteLinkedInAuthStorySuite) TestCompleteSuccess() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)

	mockLinkedInGate.On("ExchangeCode", s.Ctx, "auth-code", "http://localhost/callback", "verifier").
		Return(linkedinDomain.Token{
			AccessToken: "access-token-123",
			TokenType:   "Bearer",
			Scope:       "r_liteprofile r_emailaddress",
			ExpiresIn:   5184000,
			StatusCode:  200,
		}, nil)

	mockLinkedInGate.On("UserInfo", s.Ctx, "access-token-123").
		Return(linkedinDomain.Profile{
			MemberID:   "member-123",
			Name:       "John Doe",
			Email:      "john@example.com",
			PictureURL: "https://example.com/photo.jpg",
		}, nil)

	mockAccountRepo.On("Insert", s.Ctx,
		mock.MatchedBy(func(t accountDomain.Token) bool {
			return t.Scope == "r_liteprofile r_emailaddress" && t.ExpiresIn == 5184000
		}),
		mock.MatchedBy(func(p accountDomain.Profile) bool {
			return p.MemberID == "member-123" && p.Email == "john@example.com"
		}),
	).Return(nil)

	mockTrialRepo.On("Ensure", s.Ctx, "john@example.com", "member-123", mock.Anything).
		Return(trialDomain.Record{}, nil)

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, nil)

	result, err := story.CompleteAuth(s.Ctx, accountDomain.CompleteAuthInput{
		Code:         "auth-code",
		RedirectURI:  "http://localhost/callback",
		CodeVerifier: "verifier",
		StartedAt:    time.Now(),
	})

	s.Require().NoError(err)
	s.Equal("access-token-123", result.AccessToken)
	s.Equal("Bearer", result.TokenType)
	s.Equal("r_liteprofile r_emailaddress", result.Scope)
	s.Equal(int64(5184000), result.ExpiresIn)
	s.Equal(200, result.StatusCode)
	s.Equal("member-123", result.Profile.MemberID)
	s.Equal("John Doe", result.Profile.Name)
	s.False(result.ConnectedAt.IsZero())

	mockLinkedInGate.AssertExpectations(s.T())
	mockAccountRepo.AssertExpectations(s.T())
	mockTrialRepo.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestCompleteTokenExchangeFails() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)

	mockLinkedInGate.On("ExchangeCode", s.Ctx, "bad-code", "http://localhost/callback", "verifier").
		Return(linkedinDomain.Token{}, perror.New(perror.ErrCodeBadGateway, errors.New("exchange failed")))

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, nil)

	_, err := story.CompleteAuth(s.Ctx, accountDomain.CompleteAuthInput{Code: "bad-code", RedirectURI: "http://localhost/callback", CodeVerifier: "verifier", StartedAt: time.Now()})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeBadGateway))

	mockLinkedInGate.AssertExpectations(s.T())
	mockAccountRepo.AssertNotCalled(s.T(), "Insert")
	mockTrialRepo.AssertNotCalled(s.T(), "Ensure")
}

func (s *CompleteLinkedInAuthStorySuite) TestCompleteUserInfoFails() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)

	mockLinkedInGate.On("ExchangeCode", s.Ctx, "auth-code", "http://localhost/callback", "verifier").
		Return(linkedinDomain.Token{
			AccessToken: "access-token-123",
			StatusCode:  200,
		}, nil)

	mockLinkedInGate.On("UserInfo", s.Ctx, "access-token-123").
		Return(linkedinDomain.Profile{}, perror.New(perror.ErrCodeBadGateway, errors.New("userinfo failed")))

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, nil)

	_, err := story.CompleteAuth(s.Ctx, accountDomain.CompleteAuthInput{Code: "auth-code", RedirectURI: "http://localhost/callback", CodeVerifier: "verifier", StartedAt: time.Now()})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeBadGateway))

	mockLinkedInGate.AssertExpectations(s.T())
	mockAccountRepo.AssertNotCalled(s.T(), "Insert")
	mockTrialRepo.AssertNotCalled(s.T(), "Ensure")
}

func (s *CompleteLinkedInAuthStorySuite) TestCompleteAccountInsertFails() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)

	mockLinkedInGate.On("ExchangeCode", s.Ctx, "auth-code", "http://localhost/callback", "verifier").
		Return(linkedinDomain.Token{AccessToken: "access-token-123", StatusCode: 200}, nil)
	mockLinkedInGate.On("UserInfo", s.Ctx, "access-token-123").
		Return(linkedinDomain.Profile{MemberID: "member-123", Email: "john@example.com"}, nil)

	mockAccountRepo.On("Insert", s.Ctx, mock.Anything, mock.Anything).
		Return(errors.New("database error"))

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, nil)

	_, err := story.CompleteAuth(s.Ctx, accountDomain.CompleteAuthInput{Code: "auth-code", RedirectURI: "http://localhost/callback", CodeVerifier: "verifier", StartedAt: time.Now()})

	s.Error(err)
	s.Contains(err.Error(), "database error")

	mockLinkedInGate.AssertExpectations(s.T())
	mockAccountRepo.AssertExpectations(s.T())
	mockTrialRepo.AssertNotCalled(s.T(), "Ensure")
}

func (s *CompleteLinkedInAuthStorySuite) TestNewLinkedInAuthStory_ValidatesTrialRepo() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)

	_, err := NewLinkedInAuthStory(mockLinkedInGate, mockAccountRepo, nil, new(accountMocks.MockOAuthStateStore))
	s.Error(err)
	s.Contains(err.Error(), "trial repo is required")
}

func (s *CompleteLinkedInAuthStorySuite) TestStartCLIAuth_Success() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	mockFlows.On("Start", "web").Return("test-state-123", nil)

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	result, err := story.StartCLIAuth(s.Ctx, accountDomain.StartCLIAuthInput{
		ClientID:    "test-client-id",
		RedirectURI: "http://localhost/callback",
		Scope:       "r_liteprofile r_emailaddress",
		Fresh:       false,
		Client:      "web",
	})

	s.Require().NoError(err)
	s.Equal("test-state-123", result.State)
	s.Contains(result.AuthURL, "https://www.linkedin.com/oauth/v2/authorization")
	s.Contains(result.AuthURL, "client_id=test-client-id")
	s.Contains(result.AuthURL, "state=test-state-123")
	s.Contains(result.AuthURL, "scope=r_liteprofile+r_emailaddress")
	s.NotContains(result.AuthURL, "prompt=login")
	s.Equal("/api/linkedin/cli/result?state=test-state-123", result.PollURL)

	mockFlows.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestStartCLIAuth_Fresh() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	mockFlows.On("Start", "").Return("test-state-456", nil)

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	result, err := story.StartCLIAuth(s.Ctx, accountDomain.StartCLIAuthInput{
		ClientID:    "test-client-id",
		RedirectURI: "http://localhost/callback",
		Scope:       "r_liteprofile",
		Fresh:       true,
	})

	s.Require().NoError(err)
	s.Contains(result.AuthURL, "prompt=login")
	s.Contains(result.AuthURL, "max_age=0")

	mockFlows.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestGetCLIResult_AlreadyDone() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	done := make(chan struct{})
	close(done)
	mockFlows.On("Get", "test-state").Return(&accountDomain.OAuthState{Done: done})
	mockFlows.On("ConsumeDone", "test-state").Return([]byte(`{"accessToken":"token-123"}`))

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	result, err := story.GetCLIResult(s.Ctx, accountDomain.GetCLIResultInput{
		State:       "test-state",
		WaitSeconds: 0,
	})

	s.Require().NoError(err)
	s.False(result.Pending)
	s.Equal([]byte(`{"accessToken":"token-123"}`), result.RawResult)

	mockFlows.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestGetCLIResult_PendingNoWait() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	done := make(chan struct{})
	mockFlows.On("Get", "test-state").Return(&accountDomain.OAuthState{Done: done})

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	result, err := story.GetCLIResult(s.Ctx, accountDomain.GetCLIResultInput{
		State:       "test-state",
		WaitSeconds: 0,
	})

	s.Require().NoError(err)
	s.True(result.Pending)
	s.Nil(result.RawResult)

	mockFlows.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestGetCLIResult_UnknownState() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	mockFlows.On("Get", "unknown-state").Return(nil)

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	_, err := story.GetCLIResult(s.Ctx, accountDomain.GetCLIResultInput{
		State: "unknown-state",
	})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))
	s.Contains(err.Error(), "unknown or expired state")

	mockFlows.AssertExpectations(s.T())
}

func (s *CompleteLinkedInAuthStorySuite) TestGetCLIResult_EmptyState() {
	mockLinkedInGate := new(linkedinMocks.MockGate)
	mockAccountRepo := new(accountMocks.MockRepository)
	mockTrialRepo := new(trialMocks.MockRepository)
	mockFlows := new(accountMocks.MockOAuthStateStore)

	story := s.newStory(mockLinkedInGate, mockAccountRepo, mockTrialRepo, mockFlows)

	_, err := story.GetCLIResult(s.Ctx, accountDomain.GetCLIResultInput{
		State: "",
	})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))
	s.Contains(err.Error(), "state is required")

	mockFlows.AssertNotCalled(s.T(), "Get")
}
