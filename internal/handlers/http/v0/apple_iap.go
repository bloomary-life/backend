package v0

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type appleTransactionSyncRequest struct {
	MemberID          string `json:"memberId"`
	SignedTransaction string `json:"signedTransaction"`
}

type appleTransactionSyncResponse struct {
	Status      string `json:"status"`
	IsPro       bool   `json:"isPro"`
	AccessUntil string `json:"accessUntil,omitempty"`
}

type appleNotificationRequest struct {
	SignedPayload string `json:"signedPayload"`
}

type appleNotificationResponse struct {
	Status string `json:"status"`
}

func (s *service) AppleTransactionSync(w http.ResponseWriter, r *http.Request) {
	route := "/api/apple/transactions/sync"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var req appleTransactionSyncRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}
	req.MemberID = strings.TrimSpace(req.MemberID)
	req.SignedTransaction = strings.TrimSpace(req.SignedTransaction)

	accessToken := bearerToken(r.Header.Get("Authorization"))
	fields := logger.Fields{
		"route":                  route,
		"has_member_id":          req.MemberID != "",
		"has_signed_transaction": req.SignedTransaction != "",
	}
	_, memberID, ok := s.requireLinkedInMember(w, r, fields, accessToken, req.MemberID)
	if !ok {
		return
	}

	result, err := s.appleTransactionSync.SyncTransaction(
		r.Context(), appleiapDomain.SyncTransactionInput{MemberID: memberID, SignedTransaction: req.SignedTransaction},
	)
	if err != nil {
		status := http.StatusInternalServerError
		code := "apple_transaction_sync_failed"
		description := "apple transaction sync failed"
		if perror.Is(err, perror.ErrCodeRejected) {
			status = http.StatusBadRequest
			code = "invalid_apple_transaction"
			description = "invalid apple transaction"
		}
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", code)
		writeAPIError(w, status, code, description)
		return
	}

	var accessUntil string
	if !result.Entitlement.AccessUntil.IsZero() {
		accessUntil = result.Entitlement.AccessUntil.Format(time.RFC3339)
	}
	logger.SetFields(r.Context(), fields).SetField("outcome", "apple_transaction_synced")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(appleTransactionSyncResponse{
		Status:      "synced",
		IsPro:       result.IsPro,
		AccessUntil: accessUntil,
	})
}

func (s *service) AppleNotifications(w http.ResponseWriter, r *http.Request) {
	route := "/api/apple/notifications"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var req appleNotificationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}
	req.SignedPayload = strings.TrimSpace(req.SignedPayload)

	fields := logger.Fields{
		"route":              route,
		"has_signed_payload": req.SignedPayload != "",
	}
	if err := s.appleNotifications.ProcessNotification(
		r.Context(), appleiapDomain.NotificationInput{SignedPayload: req.SignedPayload},
	); err != nil {
		status := http.StatusInternalServerError
		code := "apple_notification_failed"
		description := "apple notification failed"
		if perror.Is(err, perror.ErrCodeRejected) {
			status = http.StatusBadRequest
			code = "invalid_apple_notification"
			description = "invalid apple notification"
		}
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", code)
		writeAPIError(w, status, code, description)
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "apple_notification_processed")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(appleNotificationResponse{Status: "processed"})
}
