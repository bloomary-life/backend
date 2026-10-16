package v0

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	feedbackDomain "bloomary-api/internal/domain/feedback"
	limiterDomain "bloomary-api/internal/domain/limiter"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type feedbackRequest struct {
	Name              string               `json:"name"`
	LinkedInID        string               `json:"linkedInId"`
	Email             string               `json:"email"`
	Title             string               `json:"title"`
	Message           string               `json:"message"`
	RecaptchaResponse string               `json:"recaptchaResponse,omitempty"`
	Attachments       []feedbackAttachment `json:"attachments,omitempty"`
}

type feedbackAttachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

func (s *service) Feedback(w http.ResponseWriter, r *http.Request) {
	s.handleFeedback(w, r, "/api/feedback", "")
}

func (s *service) SupportFeedback(w http.ResponseWriter, r *http.Request) {
	s.setSupportCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.handleFeedback(w, r, "/api/support/feedback", "Support form")
}

func (s *service) handleFeedback(w http.ResponseWriter, r *http.Request, route, defaultName string) {
	if !s.acceptFeedbackRequest(w, r, route) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	if defaultName != "" {
		req.Name = defaultName
	}
	authorized := false
	if accessToken := bearerToken(r.Header.Get("Authorization")); accessToken != "" {
		_, _, ok := s.requireMember(w, r, logger.Fields{"route": route}, accessToken, req.LinkedInID, http.StatusUnauthorized)
		if !ok {
			return
		}
		authorized = true
	}
	req.Name = s.feedbackName(r.Context(), bearerToken(r.Header.Get("Authorization")), req.Name, req.LinkedInID)
	req.Message = strings.TrimSpace(req.Message)

	fields := logger.Fields{
		"route": route, "has_linkedin_id": strings.TrimSpace(req.LinkedInID) != "",
		"has_email": strings.Contains(req.Email, "@"), "has_title": strings.TrimSpace(req.Title) != "",
		"message_len": len(req.Message), "attachment_count": len(req.Attachments),
		"attachment_bytes": feedbackAttachmentBytes(req.Attachments),
	}

	if req.Message == "" {
		logger.SetFields(r.Context(), fields).SetField("outcome", "empty_message")
		writeAPIError(w, http.StatusBadRequest, "empty_message", "message is empty")
		return
	}

	var attachments []feedbackDomain.Attachment
	for _, att := range req.Attachments {
		attachments = append(attachments, feedbackDomain.Attachment{
			Filename: att.Filename,
			Content:  att.Content,
		})
	}

	err := s.feedbackStory.SubmitFeedback(r.Context(), feedbackDomain.Input{
		Authorized:        authorized,
		Name:              req.Name,
		LinkedInID:        req.LinkedInID,
		Email:             req.Email,
		Title:             req.Title,
		Message:           req.Message,
		RecaptchaResponse: req.RecaptchaResponse,
		ClientIP:          extractClientIP(r),
		Attachments:       attachments,
	})
	if err != nil {
		if perror.Is(err, perror.ErrCodeRejected) {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "recaptcha_failed")
			writeAPIError(w, http.StatusForbidden, "recaptcha_failed", "recaptcha failed")
			return
		}
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "feedback_send_failed")
		writeAPIError(w, http.StatusBadGateway, "feedback_send_failed", "feedback send failed")
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "sent")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}

// feedbackName marks the sender as unverified unless a valid token proves the claimed LinkedIn ID.
func (s *service) feedbackName(ctx context.Context, accessToken, name, linkedInID string) string {
	linkedInID = strings.TrimSpace(linkedInID)
	if linkedInID == "" {
		return name
	}
	if accessToken != "" {
		if cachedMemberID, ok := s.verifiedTokens.get(accessToken); ok && cachedMemberID == linkedInID {
			return name
		}
		if matched, err := s.verifyLinkedInMember(ctx, accessToken, linkedInID); err == nil && matched {
			s.verifiedTokens.set(accessToken, linkedInID)
			return name
		}
	}
	return "[unverified] " + name
}

func (s *service) acceptFeedbackRequest(w http.ResponseWriter, r *http.Request, route string) bool {
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return false
	}

	if !s.feedbackLimiter.TryConsume(limiterDomain.TypeFeedback, extractClientIP(r), time.Now()) {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "rate_limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return false
	}

	return true
}

func (s *service) setSupportCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", s.config.SupportAllowedOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	w.Header().Set("Vary", "Origin")
}

func feedbackAttachmentBytes(attachments []feedbackAttachment) int {
	total := 0
	for _, attachment := range attachments {
		total += len(attachment.Content)
	}
	return total
}
