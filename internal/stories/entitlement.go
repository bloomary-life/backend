package stories

import (
	"context"
	"fmt"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	dailyDomain "bloomary-api/internal/domain/daily"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	trialDomain "bloomary-api/internal/domain/trial"
)

// AppleReviewDemoMemberID is the fixed member behind the App Review demo login.
// It has no account row, so it never gets a stored trial.
const AppleReviewDemoMemberID = "apple-review-demo"

type entitlementAccessStory struct {
	accounts     accountDomain.Repository
	entitlements paddleDomain.Repository
	apple        appleiapDomain.EntitlementRepository
	trials       trialDomain.Repository
	daily        dailyDomain.Repository
}

func NewEntitlementAccessStory(
	accounts accountDomain.Repository,
	entitlements paddleDomain.Repository,
	apple appleiapDomain.EntitlementRepository,
	trials trialDomain.Repository,
	daily dailyDomain.Repository,
) (entitlementDomain.AccessStory, error) {
	if accounts == nil {
		return nil, fmt.Errorf("entitlement access story: account repo is required")
	}
	if entitlements == nil {
		return nil, fmt.Errorf("entitlement access story: entitlement repo is required")
	}
	if apple == nil {
		return nil, fmt.Errorf("entitlement access story: apple entitlement repo is required")
	}
	if trials == nil {
		return nil, fmt.Errorf("entitlement access story: trial repo is required")
	}
	if daily == nil {
		return nil, fmt.Errorf("entitlement access story: daily repo is required")
	}

	return &entitlementAccessStory{accounts: accounts, entitlements: entitlements, apple: apple, trials: trials, daily: daily}, nil
}

func (s *entitlementAccessStory) Resolve(
	ctx context.Context, memberID string, now time.Time,
) (entitlementDomain.Entitlement, error) {
	if memberID == AppleReviewDemoMemberID {
		return s.resolveDemo(ctx, now.UTC())
	}

	profile, err := s.accounts.ByMemberID(ctx, memberID)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}

	postCount, err := s.daily.TotalPosts(ctx, memberID)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}

	paddleEntitlement, err := s.entitlements.ByMemberID(ctx, memberID)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}

	appleEntitlement, appleFound, err := s.apple.LatestEntitlement(ctx, memberID)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}

	nowUTC := now.UTC()
	paddleLive := paddleHasAccess(paddleEntitlement, nowUTC)
	appleLive := appleFound && appleHasAccess(appleEntitlement, nowUTC)

	if paddleLive || appleLive {
		accessUntil, source := appleEntitlement.AccessUntil, entitlementDomain.SubscriptionSourceApple
		if paddleLive && (!appleLive || paddleEntitlement.AccessUntil.After(accessUntil)) {
			accessUntil, source = paddleEntitlement.AccessUntil, entitlementDomain.SubscriptionSourcePaddle
		}
		return entitlementDomain.Entitlement{
			AccessUntil:        accessUntil,
			IsPro:              true,
			SubscriptionSource: source,
			Tier:               entitlementDomain.TierPro,
			DraftLimit:         entitlementDomain.ProDraftLimit,
			PostCount:          postCount,
		}, nil
	}

	trialRecord, err := s.trials.Ensure(ctx, profile.Email, profile.MemberID, nowUTC)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}

	// Nothing is live, so the grant that ran longest names the member's source
	// and end date. Unconditionally falling back to the trial here made an
	// ex-Paddle subscriber come back labelled "trial" with a long-past trial
	// expiry, and the read-only copy cannot tell "your trial ended" from "your
	// subscription ended" without the real source.
	accessUntil, source := trialRecord.ExpiresAt, entitlementDomain.SubscriptionSourceTrial
	if paddleEntitlement.AccessUntil.After(accessUntil) {
		accessUntil, source = paddleEntitlement.AccessUntil, entitlementDomain.SubscriptionSourcePaddle
	}
	if appleFound && appleNamesSource(appleEntitlement) && appleEntitlement.AccessUntil.After(accessUntil) {
		accessUntil, source = appleEntitlement.AccessUntil, entitlementDomain.SubscriptionSourceApple
	}

	// A running trial is full Pro; only lapsed falls back to the free limit.
	tier, draftLimit := entitlementDomain.TierLapsed, entitlementDomain.FreeDraftLimit
	if source == entitlementDomain.SubscriptionSourceTrial && nowUTC.Before(accessUntil) {
		tier, draftLimit = entitlementDomain.TierTrial, entitlementDomain.ProDraftLimit
	}

	return entitlementDomain.Entitlement{
		AccessUntil:        accessUntil,
		SubscriptionSource: source,
		Tier:               tier,
		DraftLimit:         draftLimit,
		PostCount:          postCount,
	}, nil
}

// resolveDemo gives App Review full trial access with the paywall reachable,
// and Pro once its sandbox purchase has synced (guideline 2.1(b)).
func (s *entitlementAccessStory) resolveDemo(ctx context.Context, now time.Time) (entitlementDomain.Entitlement, error) {
	appleEntitlement, appleFound, err := s.apple.LatestEntitlement(ctx, AppleReviewDemoMemberID)
	if err != nil {
		return entitlementDomain.Entitlement{}, err
	}
	if appleFound && appleHasAccess(appleEntitlement, now) {
		return entitlementDomain.Entitlement{
			AccessUntil:        appleEntitlement.AccessUntil,
			IsPro:              true,
			SubscriptionSource: entitlementDomain.SubscriptionSourceApple,
			Tier:               entitlementDomain.TierPro,
			DraftLimit:         entitlementDomain.ProDraftLimit,
		}, nil
	}
	return entitlementDomain.Entitlement{
		SubscriptionSource: entitlementDomain.SubscriptionSourceTrial,
		Tier:               entitlementDomain.TierTrial,
		DraftLimit:         entitlementDomain.ProDraftLimit,
	}, nil
}

// appleNamesSource reports whether an Apple record may name the member's access
// source once it has stopped granting access. A revoked or refunded transaction
// is access taken back, not access that ran out, so it must not present the
// member as a lapsed subscriber.
func appleNamesSource(e appleiapDomain.Entitlement) bool {
	return !e.AccessUntil.IsZero() &&
		e.Status != appleiapDomain.StatusRevoked &&
		e.Status != appleiapDomain.StatusRefunded
}

// appleHasAccess returns true if the Apple IAP entitlement is currently active
// or in a grace period.
func appleHasAccess(e appleiapDomain.Entitlement, now time.Time) bool {
	if e.AccessUntil.IsZero() || !now.Before(e.AccessUntil) {
		return false
	}
	return e.Status == appleiapDomain.StatusActive || e.Status == appleiapDomain.StatusGracePeriod
}

// paddleHasAccess returns true if the Paddle/web entitlement is currently active.
func paddleHasAccess(e paddleDomain.Entitlement, now time.Time) bool {
	return !e.AccessUntil.IsZero() && now.Before(e.AccessUntil)
}
