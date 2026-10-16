package v0

import (
	"net/http"
	"strings"
	"time"

	limiterDomain "bloomary-api/internal/domain/limiter"
	"bloomary-api/internal/platform/logger"
)

// DraftExport streams the member's whole draft archive as a zip. No tier check:
// export stays open in every access state, lapsed included.
func (s *service) DraftExport(w http.ResponseWriter, r *http.Request) {
	route := "/api/drafts/export"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if !s.exportLimiter.TryConsume(limiterDomain.TypeDraftExport, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}

	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireMember(w, r, fields, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="bloomary-drafts.zip"`)
	// The zip streams straight into the response, so a failure after the first
	// byte can only be logged; the client sees a truncated archive.
	if err := s.exportDraftsStory.Export(r.Context(), memberID, w); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "draft_export_failed")
		return
	}
	logger.SetFields(r.Context(), fields).SetField("outcome", "exported")
}
