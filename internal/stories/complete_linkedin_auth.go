package stories

import (
	"context"
	"fmt"
	"net/url"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	trialDomain "bloomary-api/internal/domain/trial"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

const linkedInAuthCodeUpstreamError = "upstream_error"

type completeLinkedInAuthStory struct {
	linkedInGate linkedinDomain.Gate
	accountRepo  accountWriter
	trialRepo    trialDomain.Repository
	flows        accountDomain.OAuthStateStore
}

type accountWriter interface {
	Insert(context.Context, accountDomain.Token, accountDomain.Profile) error
}

// NewLinkedInAuthStory creates a new LinkedInAuthStory implementation.
func NewLinkedInAuthStory(
	linkedInGate linkedinDomain.Gate,
	accountRepo accountWriter,
	trialRepo trialDomain.Repository,
	flows accountDomain.OAuthStateStore,
) (accountDomain.LinkedInAuthStory, error) {
	if linkedInGate == nil {
		return nil, fmt.Errorf("linkedin auth story: linkedin gate is required")
	}
	if accountRepo == nil {
		return nil, fmt.Errorf("linkedin auth story: account repo is required")
	}
	if trialRepo == nil {
		return nil, fmt.Errorf("linkedin auth story: trial repo is required")
	}
	if flows == nil {
		return nil, fmt.Errorf("linkedin auth story: oauth state store is required")
	}

	return &completeLinkedInAuthStory{
		linkedInGate: linkedInGate,
		accountRepo:  accountRepo,
		trialRepo:    trialRepo,
		flows:        flows,
	}, nil
}

// CompleteAuth completes the OAuth flow with an authorization code.
func (s *completeLinkedInAuthStory) CompleteAuth(
	ctx context.Context, input accountDomain.CompleteAuthInput,
) (accountDomain.CompleteAuthOutput, error) {
	token, err := s.linkedInGate.ExchangeCode(ctx, input.Code, input.RedirectURI, input.CodeVerifier)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("linkedin_token_exchange_failed")
		if perror.Is(err, perror.ErrCodeRateLimitHit) {
			return accountDomain.CompleteAuthOutput{}, err
		}

		if perror.Is(err, perror.ErrCodeRejected) {
			return accountDomain.CompleteAuthOutput{}, perror.New(
				linkedInAuthCodeUpstreamError,
				err,
				"linkedin token exchange failed",
			)
		}

		return accountDomain.CompleteAuthOutput{}, perror.New(
			perror.ErrCodeBadGateway,
			err,
			"linkedin token exchange failed",
		)
	}

	if token.AccessToken == "" {
		return accountDomain.CompleteAuthOutput{}, perror.New(
			perror.ErrCodeBadGateway, fmt.Errorf("linkedin token response missing access token"),
		)
	}

	logger.FromContext(ctx).WithFields(
		logger.Fields{
			"has_token": token.AccessToken != "", "has_scope": token.Scope != "",
			"status": token.StatusCode, "duration": time.Since(input.StartedAt).Round(time.Millisecond).String(),
		},
	).Info("linkedin_token_exchange_completed")

	profile, err := s.linkedInGate.UserInfo(ctx, token.AccessToken)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("linkedin_userinfo_failed")
		return accountDomain.CompleteAuthOutput{}, perror.New(
			perror.ErrCodeBadGateway, err, "linkedin userinfo failed",
		)
	}

	logger.FromContext(ctx).WithFields(
		logger.Fields{
			"has_member_id": profile.MemberID != "", "has_email": profile.Email != "",
			"has_name": profile.Name != "", "has_picture_url": profile.PictureURL != "",
			"duration": time.Since(input.StartedAt).Round(time.Millisecond).String(),
		},
	).Info("linkedin_userinfo_completed")

	if err := s.accountRepo.Insert(
		ctx,
		accountDomain.Token{
			AccessToken: token.AccessToken,
			Scope:       token.Scope,
			ExpiresIn:   token.ExpiresIn,
		},
		accountDomain.Profile{
			MemberID:   profile.MemberID,
			Name:       profile.Name,
			PictureURL: profile.PictureURL,
			Email:      profile.Email,
		},
	); err != nil {
		logger.FromContext(ctx).WithError(err).Error("linkedin_auth_store_failed")
		return accountDomain.CompleteAuthOutput{}, err
	}

	if _, err := s.trialRepo.Ensure(ctx, profile.Email, profile.MemberID, time.Now().UTC()); err != nil {
		logger.FromContext(ctx).WithError(err).Error("linkedin_trial_store_failed")
		return accountDomain.CompleteAuthOutput{}, err
	}

	logger.FromContext(ctx).WithFields(
		logger.Fields{
			"has_member_id": profile.MemberID != "",
			"duration":      time.Since(input.StartedAt).Round(time.Millisecond).String(),
		},
	).Info("linkedin_auth_completed")

	return accountDomain.CompleteAuthOutput{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
		ExpiresIn:   token.ExpiresIn,
		StatusCode:  token.StatusCode,
		Profile: accountDomain.Profile{
			MemberID:   profile.MemberID,
			Name:       profile.Name,
			PictureURL: profile.PictureURL,
			Email:      profile.Email,
		},
		ConnectedAt: time.Now().UTC(),
	}, nil
}

// StartCLIAuth initiates CLI authentication flow.
func (s *completeLinkedInAuthStory) StartCLIAuth(
	ctx context.Context, input accountDomain.StartCLIAuthInput,
) (accountDomain.StartCLIAuthOutput, error) {
	state, err := s.flows.Start(input.Client)
	if err != nil {
		return accountDomain.StartCLIAuthOutput{}, fmt.Errorf("failed to generate state: %w", err)
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {input.ClientID},
		"redirect_uri":          {input.RedirectURI},
		"state":                 {state},
		"scope":                 {input.Scope},
		"enable_extended_login": {"true"},
	}
	if input.Fresh {
		params.Set("prompt", "login")
		params.Set("max_age", "0")
	}

	authURL := "https://www.linkedin.com/oauth/v2/authorization?" + params.Encode()
	pollURL := "/api/linkedin/cli/result?state=" + state

	return accountDomain.StartCLIAuthOutput{
		AuthURL: authURL,
		State:   state,
		PollURL: pollURL,
	}, nil
}

// GetCLIResult retrieves the result of a CLI authentication flow.
func (s *completeLinkedInAuthStory) GetCLIResult(
	ctx context.Context, input accountDomain.GetCLIResultInput,
) (accountDomain.GetCLIResultOutput, error) {
	if input.State == "" {
		return accountDomain.GetCLIResultOutput{}, perror.New(
			perror.ErrCodeRejected, fmt.Errorf("state is required"), "state is required",
		)
	}

	flow := s.flows.Get(input.State)
	if flow == nil {
		return accountDomain.GetCLIResultOutput{}, perror.New(
			perror.ErrCodeRejected, fmt.Errorf("unknown or expired state"), "unknown or expired state",
		)
	}

	// Check if already done (non-blocking)
	select {
	case <-flow.Done:
		result := s.flows.ConsumeDone(input.State)
		return accountDomain.GetCLIResultOutput{RawResult: result}, nil
	default:
	}

	// If no wait requested, return pending immediately
	if input.WaitSeconds <= 0 {
		return accountDomain.GetCLIResultOutput{Pending: true}, nil
	}

	// Wait for completion or timeout
	timer := time.NewTimer(time.Duration(input.WaitSeconds) * time.Second)
	defer timer.Stop()

	select {
	case <-flow.Done:
		result := s.flows.ConsumeDone(input.State)
		return accountDomain.GetCLIResultOutput{RawResult: result}, nil
	case <-timer.C:
		return accountDomain.GetCLIResultOutput{Pending: true}, nil
	case <-ctx.Done():
		return accountDomain.GetCLIResultOutput{Pending: true}, nil
	}
}
