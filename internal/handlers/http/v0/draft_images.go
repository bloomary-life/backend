package v0

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type draftImagePresignUploadRequest struct {
	DraftID        string `json:"draftId"`
	MimeType       string `json:"mimeType"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSHA256"`
}

type draftImagePresignUploadResponse struct {
	ImageID        string            `json:"imageId"`
	ObjectKey      string            `json:"objectKey"`
	UploadURL      string            `json:"uploadUrl"`
	Headers        map[string]string `json:"headers"`
	MaxSizeBytes   int64             `json:"maxSizeBytes"`
	ExpiresAt      string            `json:"expiresAt"`
	ChecksumSHA256 string            `json:"checksumSHA256"`
}

type draftImagePresignDownloadRequest struct {
	DraftID string `json:"draftId"`
	ImageID string `json:"imageId"`
}

type draftImagePresignDownloadResponse struct {
	ImageID     string `json:"imageId"`
	DownloadURL string `json:"downloadUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

func (s *service) DraftImagePresignUpload(w http.ResponseWriter, r *http.Request) {
	route := "/api/draft-images/presign-upload"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.draftImagesStory == nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "object_store_not_configured")
		writeAPIError(w, http.StatusServiceUnavailable, "object_store_not_configured", "draft image storage is not configured")
		return
	}

	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireMember(w, r, fields, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}

	var req draftImagePresignUploadRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	entitlement, err := s.entitlementByMemberID(r.Context(), memberID)
	if err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "entitlement_failed")
		writeAPIError(w, http.StatusInternalServerError, "draft_image_presign_failed", "draft image presign failed")
		return
	}
	// Staging an image only matters if the draft that references it can be
	// pushed, and a lapsed member can push only a new draft under the free
	// limit. Gating it here keeps the frozen archive from being grown from the
	// side.
	if !entitlement.HasDraftAccess() {
		drafts, err := s.syncDraftsStory.Sync(r.Context(), memberID, nil, nil, nil)
		if err != nil {
			logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "draft_list_failed")
			writeAPIError(w, http.StatusInternalServerError, "draft_image_presign_failed", "draft image presign failed")
			return
		}
		if draftStored(drafts, draftDomain.NormalizeID(req.DraftID)) {
			logger.SetFields(r.Context(), fields).SetField("outcome", draftDomain.ErrCodeProRequired)
			writeAPIError(w, http.StatusPaymentRequired, draftDomain.ErrCodeProRequired, "draft archive is read-only")
			return
		}
		if len(drafts) >= entitlement.DraftLimit {
			logger.SetFields(r.Context(), fields).SetField("outcome", draftDomain.ErrCodeTooManyDraft)
			writeAPIError(w, http.StatusConflict, draftDomain.ErrCodeTooManyDraft, "too many synced drafts")
			return
		}
	}

	out, err := s.draftImagesStory.PresignUpload(
		r.Context(),
		draftDomain.PresignUploadInput{
			MemberID:       memberID,
			DraftID:        draftDomain.NormalizeID(req.DraftID),
			MimeType:       strings.TrimSpace(req.MimeType),
			SizeBytes:      req.SizeBytes,
			ChecksumSHA256: strings.TrimSpace(req.ChecksumSHA256),
		},
	)
	if err != nil {
		writeDraftImageError(w, r, fields, err)
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "presigned")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apiDraftImagePresignUpload(out))
}

func (s *service) DraftImagePresignDownload(w http.ResponseWriter, r *http.Request) {
	route := "/api/draft-images/presign-download"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.draftImagesStory == nil {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "object_store_not_configured")
		writeAPIError(w, http.StatusServiceUnavailable, "object_store_not_configured", "draft image storage is not configured")
		return
	}

	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireMember(w, r, fields, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}

	var req draftImagePresignDownloadRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	out, err := s.draftImagesStory.PresignDownload(
		r.Context(),
		draftDomain.PresignDownloadInput{
			MemberID: memberID,
			DraftID:  draftDomain.NormalizeID(req.DraftID),
			ImageID:  strings.TrimSpace(req.ImageID),
		},
	)
	if err != nil {
		writeDraftImageError(w, r, fields, err)
		return
	}

	logger.SetFields(r.Context(), fields).SetField("outcome", "presigned")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apiDraftImagePresignDownload(out))
}

func draftStored(drafts []draftDomain.Draft, draftID string) bool {
	for _, draft := range drafts {
		if draft.DraftID == draftID {
			return true
		}
	}
	return false
}

func writeDraftImageError(w http.ResponseWriter, r *http.Request, fields logger.Fields, err error) {
	typedErr := normalizeLinkedInAuthError(err)
	logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", typedErr.Code)

	switch typedErr.Code {
	case draftDomain.ErrCodeInvalidImage:
		writeAPIError(w, http.StatusUnprocessableEntity, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeImageNotFound:
		writeAPIError(w, http.StatusNotFound, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeImageTooLarge:
		writeAPIError(w, http.StatusRequestEntityTooLarge, typedErr.Code, typedErr.Desc)
	case perror.ErrCodeBadGateway:
		writeAPIError(w, http.StatusBadGateway, typedErr.Code, typedErr.Desc)
	default:
		writeAPIError(w, http.StatusInternalServerError, "draft_image_presign_failed", "draft image presign failed")
	}
}

func apiDraftImagePresignUpload(out draftDomain.PresignUpload) draftImagePresignUploadResponse {
	return draftImagePresignUploadResponse{
		ImageID:        out.ImageID,
		ObjectKey:      out.ObjectKey,
		UploadURL:      out.UploadURL,
		Headers:        out.Headers,
		MaxSizeBytes:   out.MaxSizeBytes,
		ExpiresAt:      out.ExpiresAt.UTC().Format(time.RFC3339Nano),
		ChecksumSHA256: out.ChecksumSHA256,
	}
}

func apiDraftImagePresignDownload(out draftDomain.PresignDownload) draftImagePresignDownloadResponse {
	return draftImagePresignDownloadResponse{
		ImageID:     out.ImageID,
		DownloadURL: out.DownloadURL,
		ExpiresAt:   out.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}
