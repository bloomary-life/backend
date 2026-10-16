package v0

import (
	"encoding/json"
	"net/http"
	"strings"

	accountDomain "bloomary-api/internal/domain/account"
	"bloomary-api/internal/platform/logger"
)

type disconnectRequest struct {
	LinkedInID  string `json:"linkedInId"`
	AccessToken string `json:"accessToken,omitempty"`
}

func (s *service) LinkedInDisconnect(w http.ResponseWriter, r *http.Request) {
	route := "/api/linkedin/disconnect"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var req disconnectRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	req.LinkedInID = strings.TrimSpace(req.LinkedInID)
	if req.LinkedInID == "" {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "missing_linkedin_id")
		writeAPIError(w, http.StatusBadRequest, "missing_linkedin_id", "linkedin id is missing")
		return
	}

	accessToken := bearerToken(r.Header.Get("Authorization"))
	if accessToken == "" {
		accessToken = strings.TrimSpace(req.AccessToken)
	}

	fields := logger.Fields{"route": route, "has_linkedin_id": req.LinkedInID != ""}
	_, linkedInID, ok := s.requireLinkedInMember(w, r, fields, accessToken, req.LinkedInID)
	if !ok {
		return
	}

	if s.config.Notifications != nil {
		if err := s.config.Notifications.DeleteMember(r.Context(), linkedInID); err != nil {
			logger.FromContext(r.Context()).WithError(err).Error("notification_account_cleanup_failed")
			writeAPIError(w, http.StatusInternalServerError, "cleanup_failed", "Notification cleanup failed; try again.")
			return
		}
	}
	if err := s.revokeWebSessions(w, linkedInID); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "session_revoke_failed")
		writeAPIError(w, http.StatusInternalServerError, "session_revoke_failed", "Session cleanup failed; try again.")
		return
	}
	if err := s.accountDeletionStory.DisconnectLinkedIn(r.Context(), accountDomain.DisconnectLinkedInInput{
		MemberID:    linkedInID,
		AccessToken: accessToken,
	}); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "disconnect_failed")
		writeAPIError(w, http.StatusInternalServerError, "disconnect_failed", "disconnect failed")
		return
	}

	s.verifiedTokens.remove(accessToken)

	logger.SetFields(r.Context(), fields).SetField("outcome", "disconnected")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
}

func (s *service) AccountDelete(w http.ResponseWriter, r *http.Request) {
	route := "/api/account/delete"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var req disconnectRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	req.LinkedInID = strings.TrimSpace(req.LinkedInID)
	if req.LinkedInID == "" {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "missing_linkedin_id")
		writeAPIError(w, http.StatusBadRequest, "missing_linkedin_id", "linkedin id is missing")
		return
	}

	accessToken := bearerToken(r.Header.Get("Authorization"))
	if accessToken == "" {
		accessToken = strings.TrimSpace(req.AccessToken)
	}

	fields := logger.Fields{"route": route, "has_linkedin_id": req.LinkedInID != ""}
	_, linkedInID, ok := s.requireLinkedInMember(w, r, fields, accessToken, req.LinkedInID)
	if !ok {
		return
	}

	if s.config.Notifications != nil {
		if err := s.config.Notifications.DeleteMember(r.Context(), linkedInID); err != nil {
			logger.FromContext(r.Context()).WithError(err).Error("notification_account_cleanup_failed")
			writeAPIError(w, http.StatusInternalServerError, "cleanup_failed", "Notification cleanup failed; try again.")
			return
		}
	}
	if err := s.revokeWebSessions(w, linkedInID); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "session_revoke_failed")
		writeAPIError(w, http.StatusInternalServerError, "session_revoke_failed", "Session cleanup failed; try again.")
		return
	}
	if err := s.accountDeletionStory.DeleteAccount(r.Context(), accountDomain.DeleteAccountInput{
		MemberID: linkedInID,
	}); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "account_delete_failed")
		writeAPIError(w, http.StatusInternalServerError, "account_delete_failed", "account deletion failed")
		return
	}

	s.verifiedTokens.remove(accessToken)

	logger.SetFields(r.Context(), fields).SetField("outcome", "deleted")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}
