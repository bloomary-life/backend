package v0

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	dailyDomain "bloomary-api/internal/domain/daily"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	metricsDomain "bloomary-api/internal/domain/metrics"
	"bloomary-api/internal/platform/logger"
)

type linkedInPostResponse struct {
	Status string `json:"status"`
	PostID string `json:"postId"`
}

func (s *service) LinkedInPost(w http.ResponseWriter, r *http.Request) {
	route := "/api/linkedin/post"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLinkedInUploadBytes)
	if err := r.ParseMultipartForm(maxLinkedInUploadBytes); err != nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	accessToken := bearerToken(r.Header.Get("Authorization"))
	if accessToken == "" {
		accessToken = strings.TrimSpace(r.FormValue("accessToken"))
	}

	memberID := strings.TrimSpace(r.FormValue("memberId"))
	commentary := cleanCommentary(r.FormValue("commentary"))
	files := r.MultipartForm.File["attachment"]
	fields := logger.Fields{
		"route": route, "has_member_id": memberID != "", "text_len": len(commentary),
		"has_text": commentary != "", "attachment_count": len(files), "attachment_bytes": multipartBytes(files),
	}

	accessToken, memberID, ok := s.requireLinkedInMember(w, r, fields, accessToken, memberID)
	if !ok {
		return
	}

	// Gated here, not in CreatePost: the scheduler also calls CreatePost and must
	// keep publishing posts a member scheduled before their access lapsed.
	if memberID != demoMemberID {
		entitlement, err := s.entitlementByMemberID(r.Context(), memberID)
		if err != nil {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "entitlement_failed")
			writeAPIError(w, http.StatusInternalServerError, "linkedin_post_failed", "linkedin post failed")
			return
		}
		if entitlement.Tier != entitlementDomain.TierPro && entitlement.Tier != entitlementDomain.TierTrial {
			logger.SetFields(r.Context(), fields).SetField("outcome", "pro_required")
			writeAPIError(w, http.StatusPaymentRequired, "pro_required", "Publishing needs an active plan.")
			return
		}
	}

	if commentary == "" && len(files) == 0 {
		logger.SetFields(r.Context(), fields).SetField("outcome", "empty_post")
		writeAPIError(w, http.StatusBadRequest, "empty_post", "post is empty")
		return
	}

	if len(files) > 1 {
		logger.SetFields(r.Context(), fields).SetField("outcome", "too_many_attachments")
		writeAPIError(w, http.StatusBadRequest, "too_many_attachments", "only one attachment is supported")
		return
	}

	var attachments []linkedinDomain.PostAttachment
	if len(files) > 0 {
		fh := files[0]
		file, err := fh.Open()
		if err != nil {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")
			writeAPIError(w, http.StatusBadRequest, "bad_request", "cannot read attachment")
			return
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")
			writeAPIError(w, http.StatusBadRequest, "bad_request", "cannot read attachment")
			return
		}
		ct := fh.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		attachments = []linkedinDomain.PostAttachment{
			{Filename: fh.Filename, ContentType: ct, Data: data},
		}
	}

	output, err := s.linkedInPostStory.CreatePost(r.Context(), linkedinDomain.CreatePostInput{
		AccessToken: accessToken,
		MemberID:    memberID,
		Commentary:  commentary,
		Visibility:  "PUBLIC",
		Attachments: attachments,
	})
	if err != nil {
		if errors.Is(err, dailyDomain.ErrDailyLimitExceeded) {
			s.config.ProductCounters.RecordRateLimit(metricsDomain.RateLimitDailyLinkedIn)
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "linkedin_daily_limit_exceeded")
			writeAPIError(w, http.StatusTooManyRequests, "linkedin_daily_limit_exceeded", "linkedin daily request limit exceeded")
			return
		}
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "linkedin_post_failed")
		writeAPIError(w, http.StatusBadGateway, "linkedin_post_failed", "linkedin post failed")
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "published").SetField("post_id", output.PostID)
	if draftID := draftDomain.NormalizeID(r.FormValue("draftId")); draftID != "" && s.scheduledPostStory != nil {
		if err := s.scheduledPostStory.Cancel(r.Context(), memberID, draftID); err != nil {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("schedule_cancel", "failed")
		}
	}

	response := linkedInPostResponse{Status: "published", PostID: output.PostID}

	s.setPlatformCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func cleanCommentary(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(
		func(r rune) rune {
			switch {
			case r == '\u200b' || r == '\u2028' || r == '\u2029':
				return '\n'
			case (r < ' ' && r != '\n' && r != '\t') || r == '\u007f':
				return -1
			default:
				return r
			}
		}, value,
	)
	return strings.TrimSpace(value)
}

func multipartBytes(files []*multipart.FileHeader) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}
