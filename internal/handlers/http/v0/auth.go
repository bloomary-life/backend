package v0

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

const (
	demoTokenPrefix       = "apple-review:"
	demoMemberID          = "apple-review-demo"
	linkedInTokenCacheTTL = 60 * 24 * time.Hour // LinkedIn access tokens are valid for ~2 months.
)

// linkedInTokenVerificationCache caches verified LinkedIn access tokens so the
// backend does not call LinkedIn userinfo repeatedly for the same token.
// Entries expire after linkedInTokenCacheTTL.
type linkedInTokenVerificationCache struct {
	mu    sync.RWMutex
	items map[string]linkedInTokenVerificationEntry
	ttl   time.Duration
}

type linkedInTokenVerificationEntry struct {
	memberID  string
	expiresAt time.Time
}

func newLinkedInTokenVerificationCache(ttl time.Duration) *linkedInTokenVerificationCache {
	return &linkedInTokenVerificationCache{
		items: make(map[string]linkedInTokenVerificationEntry),
		ttl:   ttl,
	}
}

func (c *linkedInTokenVerificationCache) get(accessToken string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	entry, ok := c.items[accessToken]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.items, accessToken)
		c.mu.Unlock()
		return "", false
	}
	return entry.memberID, true
}

func (c *linkedInTokenVerificationCache) set(accessToken, memberID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[accessToken] = linkedInTokenVerificationEntry{
		memberID:  memberID,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *linkedInTokenVerificationCache) setWithTTL(accessToken, memberID string, ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[accessToken] = linkedInTokenVerificationEntry{
		memberID:  memberID,
		expiresAt: time.Now().Add(ttl),
	}
}

func (c *linkedInTokenVerificationCache) remove(accessToken string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, accessToken)
}

// verifyLinkedInMember confirms the access token belongs to the given member
// by calling LinkedIn userinfo and comparing the returned member id.
func (s *service) verifyLinkedInMember(ctx context.Context, accessToken, memberID string) (bool, error) {
	profile, err := s.linkedIn.UserInfo(ctx, accessToken)
	if err != nil {
		return false, err
	}

	return profile.MemberID == memberID, nil
}

// tryDemoAuth checks if the token is a valid Apple review demo token.
// Returns (accessToken, memberID, true) if valid, or ("", "", false) if not a demo token.
func (s *service) tryDemoAuth(r *http.Request, fields logger.Fields, accessToken, memberID string) (string, string, bool, bool) {
	if s.config.AppleReviewCode == "" {
		return "", "", false, false
	}
	if !strings.HasPrefix(accessToken, demoTokenPrefix) {
		return "", "", false, false
	}
	code := strings.TrimPrefix(accessToken, demoTokenPrefix)
	if code != s.config.AppleReviewCode || memberID != demoMemberID {
		logger.SetFields(r.Context(), fields).SetField("outcome", "invalid_demo_auth")
		return "", "", false, true
	}
	return accessToken, memberID, true, true
}

func (s *service) requireLinkedInMember(
	w http.ResponseWriter, r *http.Request, fields logger.Fields, accessToken, memberID string,
) (string, string, bool) {
	return s.requireMember(w, r, fields, accessToken, memberID, http.StatusUnauthorized)
}

// requireMember verifies LinkedIn membership and returns (accessToken, memberID, ok).
// authFailStatus is the HTTP status to return on authentication failure (401 or 403).
func (s *service) requireMember(
	w http.ResponseWriter, r *http.Request, fields logger.Fields, accessToken, memberID string, authFailStatus int,
) (string, string, bool) {
	accessToken = strings.TrimSpace(accessToken)
	memberID = strings.TrimSpace(memberID)
	if accessToken == "" || memberID == "" {
		logger.SetFields(r.Context(), fields).SetField("outcome", "missing_linkedin_auth")
		writeAPIError(w, http.StatusBadRequest, "missing_linkedin_auth", "missing linkedin auth")
		return "", "", false
	}

	if tok, mid, ok, wasDemo := s.tryDemoAuth(r, fields, accessToken, memberID); wasDemo {
		if !ok {
			writeAPIError(w, authFailStatus, "invalid_demo_auth", "invalid demo auth")
			return "", "", false
		}
		return tok, mid, true
	}

	if cachedMemberID, ok := s.verifiedTokens.get(accessToken); ok {
		if cachedMemberID == memberID {
			logger.SetFields(r.Context(), fields).SetField("outcome", "linkedin_auth_cache_hit")
			return accessToken, memberID, true
		}
		logger.SetFields(r.Context(), fields).SetField("outcome", "invalid_linkedin_auth")
		writeAPIError(w, authFailStatus, "invalid_linkedin_auth", "invalid linkedin auth")
		return "", "", false
	}

	matched, err := s.verifyLinkedInMember(r.Context(), accessToken, memberID)
	if err != nil {
		if perror.Is(err, perror.ErrCodeRejected) {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "invalid_linkedin_auth")
			writeAPIError(w, authFailStatus, "invalid_linkedin_auth", "invalid linkedin auth")
			return "", "", false
		}

		if perror.Is(err, perror.ErrCodeRateLimitHit) {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "rate_limited")
			writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many linkedin auth requests")
			return "", "", false
		}

		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "linkedin_auth_failed")
		writeAPIError(w, http.StatusBadGateway, perror.ErrCodeBadGateway, "linkedin auth verification failed")
		return "", "", false
	}

	if !matched {
		logger.SetFields(r.Context(), fields).SetField("outcome", "invalid_linkedin_auth")
		writeAPIError(w, authFailStatus, "invalid_linkedin_auth", "invalid linkedin auth")
		return "", "", false
	}

	s.verifiedTokens.set(accessToken, memberID)
	return accessToken, memberID, true
}
