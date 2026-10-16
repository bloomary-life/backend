package v0

import (
	domain "bloomary-api/internal/domain/notification"
	"bloomary-api/internal/platform/logger"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (s *service) NotificationToken(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPut {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req struct {
		MemberID    string `json:"memberId"`
		Platform    string `json:"platform"`
		Token       string `json:"token"`
		Environment string `json:"environment"`
		Enabled     bool   `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Platform != "ios" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid notification registration")
		return
	}
	_, memberID, ok := s.requireLinkedInMember(w, r, logger.Fields{"route": "/api/notifications/token"}, bearerToken(r.Header.Get("Authorization")), req.MemberID)
	if !ok {
		return
	}
	if _, err := s.accessStory.Resolve(r.Context(), memberID, time.Now()); err != nil {
		logger.FromContext(r.Context()).WithError(err).Error("notification_account_not_active")
		writeAPIError(w, http.StatusForbidden, "account_unavailable", "Reconnect LinkedIn before enabling notifications.")
		return
	}
	if s.config.Notifications == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "Push notifications are not configured.")
		return
	}
	if err := s.config.Notifications.Register(r.Context(), domain.Device{MemberID: memberID, Token: req.Token, Environment: req.Environment}, req.Enabled); err != nil {
		logger.FromContext(r.Context()).WithError(err).Error("notification_registration_failed")
		status := http.StatusBadRequest
		if errors.Is(err, domain.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeAPIError(w, status, "registration_failed", "Notification registration failed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
