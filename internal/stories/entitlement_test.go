package stories

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	trialDomain "bloomary-api/internal/domain/trial"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	appleiapMocks "bloomary-api/internal/mocks/domain/appleiap"
	dailyMocks "bloomary-api/internal/mocks/domain/daily"
	paddleEntitlementMocks "bloomary-api/internal/mocks/domain/paddle_entitlement"
	trialMocks "bloomary-api/internal/mocks/domain/trial"
)

func TestEntitlementAccessStoryUsesAppleEntitlement(t *testing.T) {
	now := time.Now().UTC()
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{AccessUntil: now.Add(24 * time.Hour), Status: appleiapDomain.StatusActive}, true, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.True(t, entitlement.IsPro)
	require.Equal(t, entitlementDomain.TierPro, entitlement.Tier)
	require.Equal(t, entitlementDomain.SubscriptionSourceApple, entitlement.SubscriptionSource)
}

func TestEntitlementAccessStoryKeepsTrialFallback(t *testing.T) {
	now := time.Now().UTC()
	trial := trialDomain.Record{ExpiresAt: now.Add(14 * 24 * time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{}, false, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierTrial, entitlement.Tier)
	require.Equal(t, trial.ExpiresAt, entitlement.AccessUntil)
	require.Equal(t, entitlementDomain.SubscriptionSourceTrial, entitlement.SubscriptionSource)
	require.Equal(t, entitlementDomain.ProDraftLimit, entitlement.DraftLimit)
}

func TestEntitlementAccessStoryExpiredTrialIsLapsed(t *testing.T) {
	now := time.Now().UTC()
	trial := trialDomain.Record{ExpiresAt: now.Add(-time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{}, false, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierLapsed, entitlement.Tier)
}

// An ex-Paddle subscriber used to fall through to the trial branch and come
// back labelled "trial" with their long-past trial expiry, so the read-only copy
// could not tell "your trial ended" from "your subscription ended".
func TestEntitlementAccessStoryExpiredWebSubscriptionStaysLapsedSubscriber(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-72 * time.Hour)
	trial := trialDomain.Record{ExpiresAt: now.Add(-30 * 24 * time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{AccessUntil: ended}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{}, false, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierLapsed, entitlement.Tier)
	require.False(t, entitlement.IsPro)
	require.Equal(t, ended, entitlement.AccessUntil)
	require.Equal(t, entitlementDomain.SubscriptionSourcePaddle, entitlement.SubscriptionSource)
	require.Equal(t, entitlementDomain.FreeDraftLimit, entitlement.DraftLimit)
}

func TestEntitlementAccessStoryExpiredAppleSubscriptionStaysLapsedSubscriber(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-24 * time.Hour)
	trial := trialDomain.Record{ExpiresAt: now.Add(-30 * 24 * time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{AccessUntil: ended, Status: appleiapDomain.StatusExpired}, true, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierLapsed, entitlement.Tier)
	require.Equal(t, ended, entitlement.AccessUntil)
	require.Equal(t, entitlementDomain.SubscriptionSourceApple, entitlement.SubscriptionSource)
}

// A refund is access taken back, not access that ran out, so it must not present
// the member as a lapsed subscriber.
func TestEntitlementAccessStoryRefundedAppleSubscriptionDoesNotNameSource(t *testing.T) {
	now := time.Now().UTC()
	trial := trialDomain.Record{ExpiresAt: now.Add(-30 * 24 * time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{AccessUntil: now.Add(-time.Hour), Status: appleiapDomain.StatusRefunded}, true, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierLapsed, entitlement.Tier)
	require.Equal(t, entitlementDomain.SubscriptionSourceTrial, entitlement.SubscriptionSource)
}

// A subscription that lapsed while the trial was still running must not lock the
// member out early: the trial carries them to its own expiry.
func TestEntitlementAccessStoryRunningTrialOutlastsLapsedSubscription(t *testing.T) {
	now := time.Now().UTC()
	trial := trialDomain.Record{ExpiresAt: now.Add(10 * 24 * time.Hour)}
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{AccessUntil: now.Add(-time.Hour)}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{}, false, nil)
	trialRepo.On("Ensure", mock.Anything, "member@example.com", "member-1", mock.AnythingOfType("time.Time")).
		Return(trial, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.Equal(t, entitlementDomain.TierTrial, entitlement.Tier)
	require.Equal(t, trial.ExpiresAt, entitlement.AccessUntil)
	require.Equal(t, entitlementDomain.SubscriptionSourceTrial, entitlement.SubscriptionSource)
}

func TestEntitlementAccessStoryKeepsWebEntitlement(t *testing.T) {
	now := time.Now().UTC()
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{AccessUntil: now.Add(24 * time.Hour)}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{}, false, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.True(t, entitlement.IsPro)
	require.Equal(t, entitlementDomain.SubscriptionSourcePaddle, entitlement.SubscriptionSource)
}

func TestEntitlementAccessStoryUsesLatestWebOrAppleAccessUntil(t *testing.T) {
	now := time.Now().UTC()
	accountRepo := accountMocks.NewMockRepository(t)
	webRepo := paddleEntitlementMocks.NewMockRepository(t)
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	trialRepo := trialMocks.NewMockRepository(t)
	accountRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(accountDomain.Profile{MemberID: "member-1", Email: "member@example.com"}, nil)
	webRepo.On("ByMemberID", mock.Anything, "member-1").
		Return(paddleDomain.Entitlement{AccessUntil: now.Add(24 * time.Hour)}, nil)
	appleRepo.On("LatestEntitlement", mock.Anything, "member-1").
		Return(appleiapDomain.Entitlement{AccessUntil: now.Add(48 * time.Hour), Status: appleiapDomain.StatusActive}, true, nil)
	dailyRepo := dailyMocks.NewMockRepository(t)
	dailyRepo.On("TotalPosts", mock.Anything, mock.Anything).Return(1, nil)
	access, err := NewEntitlementAccessStory(accountRepo, webRepo, appleRepo, trialRepo, dailyRepo)
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), "member-1", now)

	require.NoError(t, err)
	require.True(t, entitlement.IsPro)
	require.Equal(t, now.Add(48*time.Hour), entitlement.AccessUntil)
	require.Equal(t, entitlementDomain.SubscriptionSourceApple, entitlement.SubscriptionSource)
}

func TestEntitlementAccessStoryDemoWithoutPurchaseIsTrial(t *testing.T) {
	now := time.Now().UTC()
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	appleRepo.On("LatestEntitlement", mock.Anything, AppleReviewDemoMemberID).
		Return(appleiapDomain.Entitlement{}, false, nil)
	access, err := NewEntitlementAccessStory(accountMocks.NewMockRepository(t), paddleEntitlementMocks.NewMockRepository(t), appleRepo, trialMocks.NewMockRepository(t), dailyMocks.NewMockRepository(t))
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), AppleReviewDemoMemberID, now)

	require.NoError(t, err)
	require.False(t, entitlement.IsPro)
	require.Equal(t, entitlementDomain.TierTrial, entitlement.Tier)
	require.Equal(t, entitlementDomain.ProDraftLimit, entitlement.DraftLimit)
}

func TestEntitlementAccessStoryDemoWithSandboxPurchaseIsPro(t *testing.T) {
	now := time.Now().UTC()
	appleRepo := appleiapMocks.NewMockEntitlementRepository(t)
	appleRepo.On("LatestEntitlement", mock.Anything, AppleReviewDemoMemberID).
		Return(appleiapDomain.Entitlement{AccessUntil: now.Add(5 * time.Minute), Status: appleiapDomain.StatusActive}, true, nil)
	access, err := NewEntitlementAccessStory(accountMocks.NewMockRepository(t), paddleEntitlementMocks.NewMockRepository(t), appleRepo, trialMocks.NewMockRepository(t), dailyMocks.NewMockRepository(t))
	require.NoError(t, err)

	entitlement, err := access.Resolve(context.Background(), AppleReviewDemoMemberID, now)

	require.NoError(t, err)
	require.True(t, entitlement.IsPro)
	require.Equal(t, entitlementDomain.TierPro, entitlement.Tier)
	require.Equal(t, entitlementDomain.SubscriptionSourceApple, entitlement.SubscriptionSource)
}
