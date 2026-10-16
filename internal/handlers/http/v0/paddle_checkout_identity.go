package v0

import (
	"encoding/json"
	"net/http"
	"strings"

	"bloomary-api/internal/platform/logger"
)

func (s *service) PaddleCheckoutIdentity(w http.ResponseWriter, r *http.Request) {
	const route = "/api/paddle/checkout-identity"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	accessToken := bearerToken(r.Header.Get("Authorization"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireLinkedInMember(w, r, fields, accessToken, memberID)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"memberId":  memberID,
		"signature": signPaddleMemberID(memberID, s.config.PaddleWebhookSecret),
	})
}
