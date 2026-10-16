package v0

import (
	"encoding/json"
	"net/http"
	"strings"

	"bloomary-api/internal/platform/logger"
)

func (s *service) PaddlePortal(w http.ResponseWriter, r *http.Request) {
	const route = "/api/paddle/portal"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
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
	url, err := s.paddleBilling.PortalURL(r.Context(), memberID)
	if err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "paddle_portal_failed")
		writeAPIError(w, http.StatusBadGateway, "paddle_portal_failed", "billing portal is temporarily unavailable")
		return
	}
	if url == "" {
		writeAPIError(w, http.StatusNotFound, "paddle_customer_not_found", "no Paddle subscription found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": url})
}
