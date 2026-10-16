package v0

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	limiterDomain "bloomary-api/internal/domain/limiter"
	metricsDomain "bloomary-api/internal/domain/metrics"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/stories"
)

type entitlementResponse struct {
	MaxDraftBodyRunes  int    `json:"maxDraftBodyRunes"`
	MaxImageBytes      int64  `json:"maxImageBytes"`
	Tier               string `json:"tier"`
	IsPro              bool   `json:"isPro"`
	// Always false. The post signature is retired, but shipped iOS builds decode
	// needsSignature as a required key, so dropping it would make them fail the
	// whole entitlement response and lose isPro / draftLimit / accessUntil.
	NeedsSignature     bool   `json:"needsSignature"`
	DraftLimit         int    `json:"draftLimit"`
	PostCount          int    `json:"postCount"`
	AccessUntil        string `json:"accessUntil,omitempty"`
	SubscriptionSource string `json:"subscriptionSource,omitempty"`
}

func (s *service) Entitlement(w http.ResponseWriter, r *http.Request) {
	route := "/api/entitlement"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if !s.entitlementLimiter.TryConsume(limiterDomain.TypeEntitlement, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		s.config.ProductCounters.RecordRateLimit(metricsDomain.RateLimitEntitlement)
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}

	accessToken := bearerToken(r.Header.Get("Authorization"))
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireLinkedInMember(w, r, fields, accessToken, memberID)
	if !ok {
		return
	}

	entitlement, err := s.entitlementByMemberID(r.Context(), memberID)
	if errors.Is(err, accountDomain.ErrNotFound) {
		logger.SetFields(r.Context(), fields).SetField("outcome", "unknown_member")
		writeAPIError(w, http.StatusForbidden, "unknown_member", "unknown member")
		return
	}

	if err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "entitlement_failed")
		writeAPIError(w, http.StatusInternalServerError, "entitlement_failed", "entitlement load failed")
		return
	}

	var maxImageBytes int64
	if s.draftImagesStore != nil {
		maxImageBytes = s.draftImagesStore.MaxUploadBytes()
	}
	var accessUntil string
	if !entitlement.AccessUntil.IsZero() {
		accessUntil = entitlement.AccessUntil.Format(time.RFC3339)
	}

	logger.SetFields(r.Context(), fields).
		SetField("outcome", "entitlement_loaded").
		SetField("tier", entitlement.Tier).
		SetField("is_pro", entitlement.IsPro).
		SetField("has_access", entitlement.IsPro)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(
		entitlementResponse{
			MaxDraftBodyRunes:  stories.MaxDraftBodyRunes,
			MaxImageBytes:      maxImageBytes,
			Tier:               entitlement.Tier,
			IsPro:              entitlement.IsPro,
			DraftLimit:         entitlement.DraftLimit,
			PostCount:          entitlement.PostCount,
			AccessUntil:        accessUntil,
			SubscriptionSource: entitlement.SubscriptionSource,
		},
	)
}

func (s *service) entitlementByMemberID(ctx context.Context, memberID string) (entitlementDomain.Entitlement, error) {
	return s.accessStory.Resolve(ctx, memberID, time.Now())
}
