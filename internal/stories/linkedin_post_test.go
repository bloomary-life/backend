package stories

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	trialDomain "bloomary-api/internal/domain/trial"
	"bloomary-api/internal/metrics"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	appleiapMocks "bloomary-api/internal/mocks/domain/appleiap"
	streakMocks "bloomary-api/internal/mocks/domain/daily"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	paddleEntitlementMocks "bloomary-api/internal/mocks/domain/paddle_entitlement"
	trialMocks "bloomary-api/internal/mocks/domain/trial"
	"bloomary-api/internal/platform/logger"
)

type mockQuotaConsumer struct {
	mock.Mock
}

func (m *mockQuotaConsumer) Consume(ctx context.Context, memberID string, now time.Time) (bool, error) {
	args := m.Called(ctx, memberID, now)
	return args.Bool(0), args.Error(1)
}

type mockProductEvents struct {
	mock.Mock
}

func (m *mockProductEvents) RecordPostPublished(ctx context.Context, memberID string, now time.Time) error {
	args := m.Called(ctx, memberID, now)
	return args.Error(0)
}

type LinkedInPostStorySuite struct {
	suite.Suite
	Ctx context.Context
}

func TestLinkedInPostStorySuite(t *testing.T) {
	suite.Run(t, new(LinkedInPostStorySuite))
}

func (s *LinkedInPostStorySuite) SetupTest() {
	log, _ := logger.New(logger.Output(io.Discard))
	s.Ctx = log.ToContext(context.Background())
}

func (s *LinkedInPostStorySuite) newStory(
	linkedIn *linkedinMocks.MockGate,
	quota *mockQuotaConsumer,
	accountRepo *accountMocks.MockRepository,
	entitlementRepo *paddleEntitlementMocks.MockRepository,
	trialRepo *trialMocks.MockRepository,
	dailyRepo *streakMocks.MockRepository,
	productEvents *mockProductEvents,
) linkedinDomain.LinkedInPostStory {
	appleRepo := appleiapMocks.NewMockEntitlementRepository(s.T())
	appleRepo.On("LatestEntitlement", mock.Anything, mock.Anything).
		Return(appleiapDomain.Entitlement{}, false, nil).
		Maybe()
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil).Maybe()
	access, err := NewEntitlementAccessStory(accountRepo, entitlementRepo, appleRepo, trialRepo, dailyRepo)
	s.Require().NoError(err)
	story, err := NewLinkedInPostStory(
		linkedIn, quota, access, dailyRepo, productEvents, metrics.ProductCounters{},
	)
	s.Require().NoError(err)
	return story
}

func (s *LinkedInPostStorySuite) setupEntitlementMocks(
	accountRepo *accountMocks.MockRepository,
	entitlementRepo *paddleEntitlementMocks.MockRepository,
	trialRepo *trialMocks.MockRepository,
	isPro bool,
	accessUntil time.Time,
) {
	accountRepo.On("ByMemberID", s.Ctx, "member-123").
		Return(accountDomain.Profile{MemberID: "member-123", Email: "test@example.com"}, nil)

	if isPro {
		entitlementRepo.On("ByMemberID", s.Ctx, "member-123").
			Return(paddleDomain.Entitlement{AccessUntil: time.Now().Add(24 * time.Hour)}, nil)
	} else {
		entitlementRepo.On("ByMemberID", s.Ctx, "member-123").
			Return(paddleDomain.Entitlement{AccessUntil: accessUntil}, nil)
		trialRepo.On("Ensure", s.Ctx, "test@example.com", "member-123", mock.AnythingOfType("time.Time")).
			Return(trialDomain.Record{ExpiresAt: accessUntil}, nil)
	}
}

func (s *LinkedInPostStorySuite) TestCreatePost_TextOnly_Success() {
	linkedIn := new(linkedinMocks.MockGate)
	quota := new(mockQuotaConsumer)
	accountRepo := new(accountMocks.MockRepository)
	entitlementRepo := new(paddleEntitlementMocks.MockRepository)
	trialRepo := new(trialMocks.MockRepository)
	dailyRepo := new(streakMocks.MockRepository)
	productEvents := new(mockProductEvents)

	s.setupEntitlementMocks(accountRepo, entitlementRepo, trialRepo, true, time.Time{})
	quota.On("Consume", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(true, nil)
	linkedIn.On("PublishPost", s.Ctx, "access-token", mock.MatchedBy(func(p linkedinDomain.Post) bool {
		return p.MemberID == "member-123" && p.Commentary == "Hello world" && p.Visibility == "PUBLIC"
	})).Return("urn:li:post:123", nil)
	dailyRepo.On("MarkPosted", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)
	productEvents.On("RecordPostPublished", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)

	story := s.newStory(linkedIn, quota, accountRepo, entitlementRepo, trialRepo, dailyRepo, productEvents)

	output, err := story.CreatePost(s.Ctx, linkedinDomain.CreatePostInput{
		AccessToken: "access-token",
		MemberID:    "member-123",
		Commentary:  "Hello world",
		Visibility:  "PUBLIC",
	})

	s.Require().NoError(err)
	s.Equal("urn:li:post:123", output.PostID)
	linkedIn.AssertExpectations(s.T())
	quota.AssertExpectations(s.T())
}

func (s *LinkedInPostStorySuite) TestCreatePost_WithImageAttachment_Success() {
	linkedIn := new(linkedinMocks.MockGate)
	quota := new(mockQuotaConsumer)
	accountRepo := new(accountMocks.MockRepository)
	entitlementRepo := new(paddleEntitlementMocks.MockRepository)
	trialRepo := new(trialMocks.MockRepository)
	dailyRepo := new(streakMocks.MockRepository)
	productEvents := new(mockProductEvents)

	s.setupEntitlementMocks(accountRepo, entitlementRepo, trialRepo, true, time.Time{})

	quota.On("Consume", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(true, nil)
	linkedIn.On("UploadMedia", s.Ctx, "access-token",
		mock.MatchedBy(func(u linkedinDomain.Upload) bool {
			return u.Owner == "urn:li:person:member-123" && u.Kind == linkedinDomain.UploadKindImage
		}),
		"image/png",
		mock.AnythingOfType("*bytes.Reader"),
	).Return("urn:li:media:456", nil)
	linkedIn.On("PublishPost", s.Ctx, "access-token", mock.MatchedBy(func(p linkedinDomain.Post) bool {
		return p.MediaURN == "urn:li:media:456" && p.MediaTitle == "photo.png"
	})).Return("urn:li:post:789", nil)
	dailyRepo.On("MarkPosted", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)
	productEvents.On("RecordPostPublished", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)

	story := s.newStory(linkedIn, quota, accountRepo, entitlementRepo, trialRepo, dailyRepo, productEvents)

	output, err := story.CreatePost(s.Ctx, linkedinDomain.CreatePostInput{
		AccessToken: "access-token",
		MemberID:    "member-123",
		Commentary:  "Check this out",
		Visibility:  "PUBLIC",
		Attachments: []linkedinDomain.PostAttachment{
			{Filename: "photo.png", ContentType: "image/png", Data: []byte("fake-image-data")},
		},
	})

	s.Require().NoError(err)
	s.Equal("urn:li:post:789", output.PostID)
	linkedIn.AssertExpectations(s.T())
}

func (s *LinkedInPostStorySuite) TestCreatePost_LapsedEntitlementPublishesUnsigned_Success() {
	linkedIn := new(linkedinMocks.MockGate)
	quota := new(mockQuotaConsumer)
	accountRepo := new(accountMocks.MockRepository)
	entitlementRepo := new(paddleEntitlementMocks.MockRepository)
	trialRepo := new(trialMocks.MockRepository)
	dailyRepo := new(streakMocks.MockRepository)
	productEvents := new(mockProductEvents)

	expiredTrial := time.Now().Add(-time.Hour)
	s.setupEntitlementMocks(accountRepo, entitlementRepo, trialRepo, false, expiredTrial)
	quota.On("Consume", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(true, nil)
	linkedIn.On("PublishPost", s.Ctx, "access-token", mock.MatchedBy(func(post linkedinDomain.Post) bool {
		return post.Commentary == "Hello from trial"
	})).
		Return("urn:li:post:trial", nil)
	dailyRepo.On("MarkPosted", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)
	productEvents.On("RecordPostPublished", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)

	story := s.newStory(linkedIn, quota, accountRepo, entitlementRepo, trialRepo, dailyRepo, productEvents)

	output, err := story.CreatePost(s.Ctx, linkedinDomain.CreatePostInput{
		AccessToken: "access-token",
		MemberID:    "member-123",
		Commentary:  "Hello from trial",
		Visibility:  "PUBLIC",
	})

	s.Require().NoError(err)
	s.Equal("urn:li:post:trial", output.PostID)
}

func (s *LinkedInPostStorySuite) TestCreatePost_UploadMedia_Success() {
	linkedIn := new(linkedinMocks.MockGate)
	quota := new(mockQuotaConsumer)
	accountRepo := new(accountMocks.MockRepository)
	entitlementRepo := new(paddleEntitlementMocks.MockRepository)
	trialRepo := new(trialMocks.MockRepository)
	dailyRepo := new(streakMocks.MockRepository)
	productEvents := new(mockProductEvents)

	s.setupEntitlementMocks(accountRepo, entitlementRepo, trialRepo, true, time.Time{})

	quota.On("Consume", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(true, nil)
	linkedIn.On("UploadMedia", s.Ctx, "access-token",
		mock.MatchedBy(func(u linkedinDomain.Upload) bool {
			return u.Kind == linkedinDomain.UploadKindDocument
		}),
		"application/pdf",
		mock.MatchedBy(func(r *bytes.Reader) bool {
			data := make([]byte, r.Len())
			_, _ = r.Read(data)
			return bytes.Equal(data, []byte("pdf-bytes"))
		}),
	).Return("urn:li:media:doc", nil)
	linkedIn.On("PublishPost", s.Ctx, "access-token", mock.MatchedBy(func(p linkedinDomain.Post) bool {
		return p.MediaURN == "urn:li:media:doc" && p.MediaTitle == "report.pdf"
	})).Return("urn:li:post:doc", nil)
	dailyRepo.On("MarkPosted", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)
	productEvents.On("RecordPostPublished", s.Ctx, "member-123", mock.AnythingOfType("time.Time")).Return(nil)

	story := s.newStory(linkedIn, quota, accountRepo, entitlementRepo, trialRepo, dailyRepo, productEvents)

	output, err := story.CreatePost(s.Ctx, linkedinDomain.CreatePostInput{
		AccessToken: "access-token",
		MemberID:    "member-123",
		Commentary:  "Read my report",
		Visibility:  "PUBLIC",
		Attachments: []linkedinDomain.PostAttachment{
			{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("pdf-bytes")},
		},
	})

	s.Require().NoError(err)
	s.Equal("urn:li:post:doc", output.PostID)
}
