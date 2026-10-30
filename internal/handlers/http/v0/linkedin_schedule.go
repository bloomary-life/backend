package v0

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	"bloomary-api/internal/platform/logger"
)

type scheduleRequest struct {
	ReviewConfirmed bool   `json:"reviewConfirmed"`
	DraftID         string `json:"draftId"`
	ScheduledAt     string `json:"scheduledAt"`
}

type scheduleResponse struct {
	ScheduleID  string `json:"scheduleId"`
	DraftID     string `json:"draftId,omitempty"`
	Status      string `json:"status"`
	ScheduledAt string `json:"scheduledAt"`
	Message     string `json:"message,omitempty"`
}

func (s *service) LinkedInSchedule(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	_, memberID, ok := s.requireMember(w, r, logger.Fields{"route": "/api/linkedin/schedule"}, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		draftID := draftDomain.NormalizeID(r.URL.Query().Get("draftId"))
		if draftID == "" {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "draftId is required")
			return
		}
		if err := s.scheduledPostStory.Cancel(r.Context(), memberID, draftID); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "schedule_cancel_failed", "schedule could not be cancelled")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req scheduleRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DraftID == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "draftId and scheduledAt are required")
		return
	}
	scheduledAt, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_scheduled_at", "scheduledAt must be an ISO 8601 timestamp with timezone")
		return
	}
	job, err := s.scheduledPostStory.Schedule(r.Context(), scheduledDomain.CreateInput{MemberID: memberID, DraftID: draftDomain.NormalizeID(req.DraftID), ScheduledAt: scheduledAt, ReviewConfirmed: req.ReviewConfirmed})
	if err != nil {
		if errors.Is(err, scheduledDomain.ErrProRequired) {
			writeAPIError(w, http.StatusPaymentRequired, "pro_required", err.Error())
			return
		}
		writeAPIError(w, http.StatusConflict, "schedule_rejected", err.Error())
		return
	}
	writeScheduleResponse(w, job)
}

func (s *service) LinkedInScheduleStatus(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	_, memberID, ok := s.requireMember(w, r, logger.Fields{"route": "/api/linkedin/schedule/status"}, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("scheduleId"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "scheduleId is required")
		return
	}
	job, err := s.scheduledPostStory.Status(r.Context(), memberID, id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "schedule_not_found", "scheduled post not found")
		return
	}
	writeScheduleResponse(w, job)
}

func (s *service) LinkedInSchedules(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	_, memberID, ok := s.requireMember(w, r, logger.Fields{"route": "/api/linkedin/schedules"}, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}
	posts, err := s.scheduledPostStory.ListActive(r.Context(), memberID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "schedule_list_failed", "scheduled posts could not be loaded")
		return
	}
	response := make([]scheduleResponse, 0, len(posts))
	for _, post := range posts {
		response = append(response, scheduleResponse{ScheduleID: post.ID, DraftID: post.DraftID, Status: string(post.Status), ScheduledAt: post.ScheduledAt.UTC().Format(time.RFC3339), Message: post.Error})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Schedules []scheduleResponse `json:"schedules"`
	}{Schedules: response})
}

func writeScheduleResponse(w http.ResponseWriter, job scheduledDomain.Post) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(scheduleResponse{ScheduleID: job.ID, DraftID: job.DraftID, Status: string(job.Status), ScheduledAt: job.ScheduledAt.UTC().Format(time.RFC3339), Message: job.Error})
}
