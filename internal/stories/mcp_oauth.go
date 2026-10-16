package stories

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	entitlementDomain "bloomary-api/internal/domain/entitlement"
	mcpDomain "bloomary-api/internal/domain/mcp"
)

var ErrMCPAuthorization = mcpDomain.ErrInvalidAuthorization

type mcpAuthorizationStory struct {
	state            mcpDomain.AuthorizationStateStore
	grants           mcpDomain.GrantStore
	sessions         mcpDomain.AppSessionResolver
	access           entitlementDomain.AccessStory
	signer           mcpDomain.AccessTokenSigner
	issuer, resource string
}

func NewMCPAuthorizationStory(state mcpDomain.AuthorizationStateStore, sessions mcpDomain.AppSessionResolver, grants mcpDomain.GrantStore, access entitlementDomain.AccessStory, signer mcpDomain.AccessTokenSigner, issuer, resource string) (mcpDomain.AuthorizationStory, error) {
	if state == nil || sessions == nil || grants == nil || access == nil || signer == nil || issuer == "" || resource == "" {
		return nil, errors.New("MCP authorization story dependencies are required")
	}
	return &mcpAuthorizationStory{state: state, sessions: sessions, grants: grants, access: access, signer: signer, issuer: issuer, resource: resource}, nil
}

func (s *mcpAuthorizationStory) Start(ctx context.Context, in mcpDomain.StartInput) error {
	if in.State == "" || in.Authorization.ClientID == "" || in.Authorization.RedirectURI == "" || !in.ExpiresAt.After(in.Authorization.CreatedAt) {
		return ErrMCPAuthorization
	}
	return s.state.SaveAuthorization(ctx, in.State, in.Authorization, in.ExpiresAt)
}

func (s *mcpAuthorizationStory) ResumeSession(ctx context.Context, in mcpDomain.ResumeSessionInput) (mcpDomain.Consent, error) {
	memberID, err := s.sessions.ResolveMember(ctx, in.SessionCredential)
	if err != nil || memberID == "" {
		return mcpDomain.Consent{}, mcpDomain.ErrNoAppSession
	}
	paid, err := s.hasPaidAccess(ctx, memberID, in.Now)
	if err != nil {
		return mcpDomain.Consent{}, err
	}
	if !paid {
		return mcpDomain.Consent{}, mcpDomain.ErrInvalidAuthorization
	}
	consent := mcpDomain.Consent{ID: in.ConsentID, Authorization: in.Authorization, MemberID: memberID, SessionCredential: in.SessionCredential, ExpiresAt: in.Now.Add(5 * time.Minute)}
	if consent.ID == "" {
		return mcpDomain.Consent{}, mcpDomain.ErrInvalidAuthorization
	}
	return consent, s.state.SaveConsent(ctx, consent)
}

func (s *mcpAuthorizationStory) CompleteLogin(ctx context.Context, in mcpDomain.CompleteLoginInput) (mcpDomain.Consent, error) {
	auth, err := s.state.TakeAuthorization(ctx, in.State, in.Now)
	if err != nil {
		return mcpDomain.Consent{}, ErrMCPAuthorization
	}
	credential := in.SessionCredential
	memberID, err := s.sessions.ResolveMember(ctx, credential)
	if err != nil || memberID == "" {
		if in.MemberID == "" {
			return mcpDomain.Consent{}, ErrMCPAuthorization
		}
		credential, err = s.sessions.EstablishMemberSession(ctx, in.MemberID, in.Now.Add(30*24*time.Hour))
		if err != nil {
			return mcpDomain.Consent{}, err
		}
		memberID, err = s.sessions.ResolveMember(ctx, credential)
		if err != nil || memberID == "" {
			return mcpDomain.Consent{}, ErrMCPAuthorization
		}
	}
	paid, err := s.hasPaidAccess(ctx, memberID, in.Now)
	if err != nil {
		return mcpDomain.Consent{}, err
	}
	if !paid {
		return mcpDomain.Consent{}, ErrMCPAuthorization
	}
	consent := mcpDomain.Consent{ID: in.ConsentID, Authorization: auth, MemberID: memberID, SessionCredential: credential, ExpiresAt: in.Now.Add(5 * time.Minute)}
	if consent.ID == "" {
		return mcpDomain.Consent{}, ErrMCPAuthorization
	}
	return consent, s.state.SaveConsent(ctx, consent)
}

func (s *mcpAuthorizationStory) FailLogin(ctx context.Context, in mcpDomain.FailLoginInput) (mcpDomain.Authorization, error) {
	return s.state.TakeAuthorization(ctx, in.State, in.Now)
}

func (s *mcpAuthorizationStory) DecideConsent(ctx context.Context, in mcpDomain.DecideConsentInput) (mcpDomain.AuthorizationCode, error) {
	consent, err := s.state.TakeConsent(ctx, in.ConsentID, in.Now)
	if err != nil || !in.Approve || !in.Now.Before(consent.ExpiresAt) {
		return mcpDomain.AuthorizationCode{}, ErrMCPAuthorization
	}
	memberID, err := s.sessions.ResolveMember(ctx, in.SessionCredential)
	if err != nil || memberID == "" || memberID != consent.MemberID {
		return mcpDomain.AuthorizationCode{}, ErrMCPAuthorization
	}
	paid, err := s.hasPaidAccess(ctx, consent.MemberID, in.Now)
	if err != nil {
		return mcpDomain.AuthorizationCode{}, err
	}
	if !paid {
		return mcpDomain.AuthorizationCode{}, ErrMCPAuthorization
	}
	auth := consent.Authorization
	code := mcpDomain.AuthorizationCode{Value: in.Code, ClientID: auth.ClientID, RedirectURI: auth.RedirectURI, Challenge: auth.Challenge, Resource: auth.Resource, MemberID: consent.MemberID, State: auth.State, Scopes: auth.Scopes, ExpiresAt: in.CodeExpiresAt}
	if code.Value == "" {
		return mcpDomain.AuthorizationCode{}, ErrMCPAuthorization
	}
	return code, s.state.SaveCode(ctx, code)
}

func (s *mcpAuthorizationStory) DenyConsent(ctx context.Context, in mcpDomain.DenyConsentInput) (mcpDomain.Authorization, error) {
	consent, err := s.state.TakeConsent(ctx, in.ConsentID, in.Now)
	if err != nil || !in.Now.Before(consent.ExpiresAt) {
		return mcpDomain.Authorization{}, ErrMCPAuthorization
	}
	return consent.Authorization, nil
}

func (s *mcpAuthorizationStory) ExchangeCode(ctx context.Context, in mcpDomain.ExchangeCodeInput) (mcpDomain.IssuedToken, error) {
	code, err := s.state.TakeCode(ctx, in.Code, in.Now)
	if err != nil || !in.Now.Before(code.ExpiresAt) || code.ClientID != in.ClientID || code.RedirectURI != in.RedirectURI || (in.Resource != "" && in.Resource != code.Resource) || !validMCPVerifier(in.Verifier, code.Challenge) {
		return mcpDomain.IssuedToken{}, ErrMCPAuthorization
	}
	paid, err := s.hasPaidAccess(ctx, code.MemberID, in.Now)
	if err != nil {
		return mcpDomain.IssuedToken{}, err
	}
	if !paid {
		return mcpDomain.IssuedToken{}, ErrMCPAuthorization
	}
	expires := in.Now.Add(30 * 24 * time.Hour)
	grantBytes := make([]byte, 32)
	if _, err := rand.Read(grantBytes); err != nil {
		return mcpDomain.IssuedToken{}, err
	}
	grantID := hex.EncodeToString(grantBytes)
	if err := s.grants.CreateGrant(ctx, mcpDomain.Grant{ID: grantID, MemberID: code.MemberID, ClientID: code.ClientID, Scopes: code.Scopes, CreatedAt: in.Now}); err != nil {
		return mcpDomain.IssuedToken{}, err
	}
	token, err := s.signer.Sign(ctx, mcpDomain.SignInput{Issuer: s.issuer, MemberID: code.MemberID, GrantID: grantID, Audience: code.Resource, IssuedAt: in.Now, ExpiresAt: expires, Scopes: code.Scopes})
	return mcpDomain.IssuedToken{Value: token, ExpiresAt: expires, Scopes: code.Scopes}, err
}

func (s *mcpAuthorizationStory) hasPaidAccess(ctx context.Context, memberID string, now time.Time) (bool, error) {
	if memberID == "" || memberID == "apple-review-demo" {
		return false, nil
	}
	entitlement, err := s.access.Resolve(ctx, memberID, now)
	return entitlement.Tier == entitlementDomain.TierPro || entitlement.Tier == entitlementDomain.TierTrial, err
}

func validMCPVerifier(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]) == challenge
}
