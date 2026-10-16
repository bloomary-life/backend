package v0

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	"bloomary-api/internal/platform/logger"
)

type draftSyncRequest struct {
	Upserts []draftSyncUpsert `json:"upserts,omitempty"`
	Deletes []string          `json:"deletes,omitempty"`
}

type draftSyncUpsert struct {
	DraftID   string          `json:"draftId"`
	Body      string          `json:"body"`
	UpdatedAt string          `json:"updatedAt"`
	Image     *draftSyncImage `json:"image,omitempty"`
	// RemoveImage drops the stored draft image when image is omitted.
	RemoveImage bool `json:"removeImage,omitempty"`
}

type draftSyncResponse struct {
	Drafts []draftSyncDraft `json:"drafts"`
}

type draftSyncDraft struct {
	DraftID   string          `json:"draftId"`
	Body      string          `json:"body"`
	Status    string          `json:"status"`
	Image     *draftSyncImage `json:"image,omitempty"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

type draftSyncImage struct {
	ImageID        string `json:"imageId"`
	DownloadURL    string `json:"downloadUrl,omitempty"`
	MimeType       string `json:"mimeType"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSHA256"`
}

func (s *service) DraftSync(w http.ResponseWriter, r *http.Request) {
	route := "/api/drafts/sync"
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		logger.SetFields(r.Context(), logger.Fields{"route": route}).SetField("outcome", "method_not_allowed")

		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	memberID := strings.TrimSpace(r.URL.Query().Get("memberId"))
	fields := logger.Fields{"route": route, "has_member_id": memberID != ""}
	_, memberID, ok := s.requireMember(w, r, fields, bearerToken(r.Header.Get("Authorization")), memberID, http.StatusForbidden)
	if !ok {
		return
	}

	var req draftSyncRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")

		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	domainUpserts, err := draftUpserts(req.Upserts)
	if err != nil {
		logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", "bad_request")

		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}

	drafts, err := s.syncDraftsStory.Sync(
		r.Context(), memberID, domainUpserts, req.Deletes, s.draftImagesStore,
	)
	if err != nil {
		writeDraftSyncError(w, r, fields, err)
		return
	}

	logger.SetFields(r.Context(), fields).
		SetField("outcome", "synced").
		SetField("draft_count", len(drafts))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(draftSyncResponse{Drafts: apiDrafts(drafts)})
}

func writeDraftSyncError(w http.ResponseWriter, r *http.Request, fields logger.Fields, err error) {
	typedErr := normalizeLinkedInAuthError(err)
	logger.SetFields(r.Context(), fields, logger.Err(err)).SetField("outcome", typedErr.Code)

	switch typedErr.Code {
	case draftDomain.ErrCodeDraftTooLong, draftDomain.ErrCodeInvalidDraft, draftDomain.ErrCodeInvalidImage,
		draftDomain.ErrCodeImageChecksumMismatch:
		writeAPIError(w, http.StatusUnprocessableEntity, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeImageNotFound:
		writeAPIError(w, http.StatusNotFound, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeImageOwnershipMismatch:
		writeAPIError(w, http.StatusForbidden, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeProRequired:
		writeAPIError(w, http.StatusPaymentRequired, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeImageTooLarge:
		writeAPIError(w, http.StatusRequestEntityTooLarge, typedErr.Code, typedErr.Desc)
	case draftDomain.ErrCodeTooManyDraft:
		writeAPIError(w, http.StatusConflict, typedErr.Code, typedErr.Desc)
	default:
		writeAPIError(w, http.StatusInternalServerError, "draft_sync_failed", "draft sync failed")
	}
}

func draftUpserts(upserts []draftSyncUpsert) ([]draftDomain.Upsert, error) {
	out := make([]draftDomain.Upsert, 0, len(upserts))
	for _, upsert := range upserts {
		var updatedAt time.Time
		if upsert.UpdatedAt != "" {
			var err error
			updatedAt, err = time.Parse(time.RFC3339Nano, upsert.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("parse updatedAt for draft %q: %w", upsert.DraftID, err)
			}
		}
		out = append(out, draftDomain.Upsert{
			DraftID:     upsert.DraftID,
			Body:        upsert.Body,
			Image:       domainDraftImage(upsert.Image),
			UpdatedAt:   updatedAt,
			RemoveImage: upsert.RemoveImage && upsert.Image == nil,
		})
	}

	return out, nil
}

func domainDraftImage(image *draftSyncImage) *draftDomain.UpsertImage {
	if image == nil {
		return nil
	}
	return &draftDomain.UpsertImage{
		ImageID:        image.ImageID,
		MimeType:       image.MimeType,
		SizeBytes:      image.SizeBytes,
		ChecksumSHA256: image.ChecksumSHA256,
	}
}

func apiDrafts(drafts []draftDomain.Draft) []draftSyncDraft {
	out := make([]draftSyncDraft, 0, len(drafts))
	for _, draft := range drafts {
		out = append(
			out,
			draftSyncDraft{
				DraftID:   draft.DraftID,
				Body:      draft.Body,
				Status:    draft.Status,
				Image:     apiDraftImage(draft.Image),
				CreatedAt: draft.CreatedAt.UTC().Format(time.RFC3339Nano),
				UpdatedAt: draft.UpdatedAt.UTC().Format(time.RFC3339Nano),
			},
		)
	}

	return out
}

func apiDraftImage(image *draftDomain.DraftImage) *draftSyncImage {
	if image == nil {
		return nil
	}
	return &draftSyncImage{
		ImageID:        image.ImageID,
		DownloadURL:    image.DownloadURL,
		MimeType:       image.MimeType,
		SizeBytes:      image.SizeBytes,
		ChecksumSHA256: image.ChecksumSHA256,
	}
}
