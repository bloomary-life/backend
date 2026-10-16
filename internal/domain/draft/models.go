package draft

import (
	"errors"
	"strings"
	"time"
)

var ErrTooManyDrafts = errors.New("too many synced drafts")
var ErrDraftBodyTooLong = errors.New("draft body is too long")

const (
	ErrCodeInvalidDraft           = "invalid_draft"
	ErrCodeDraftTooLong           = "draft_body_too_long"
	ErrCodeTooManyDraft           = "too_many_drafts"
	ErrCodeProRequired            = "pro_required"
	ErrCodeInvalidImage           = "invalid_draft_image"
	ErrCodeImageNotFound          = "draft_image_not_found"
	ErrCodeImageTooLarge          = "draft_image_too_large"
	ErrCodeImageChecksumMismatch  = "draft_image_checksum_mismatch"
	ErrCodeImageOwnershipMismatch = "draft_image_ownership_mismatch"
)

type Draft struct {
	DraftID   string
	Body      string
	Status    string
	Image     *DraftImage
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Upsert struct {
	DraftID   string
	Body      string
	Image     *UpsertImage
	UpdatedAt time.Time
	// RemoveImage drops the draft's stored image when Image is nil.
	RemoveImage bool
}

type DraftImage struct {
	ImageID        string
	DraftID        string
	ObjectKey      string
	DownloadURL    string
	MimeType       string
	SizeBytes      int64
	ChecksumSHA256 string
}

type UpsertImage struct {
	ImageID        string
	ObjectKey      string
	MimeType       string
	SizeBytes      int64
	ChecksumSHA256 string
}

type PresignUploadInput struct {
	MemberID       string
	DraftID        string
	MimeType       string
	SizeBytes      int64
	ChecksumSHA256 string
}

type PresignUpload struct {
	ImageID        string
	ObjectKey      string
	UploadURL      string
	Headers        map[string]string
	MaxSizeBytes   int64
	ExpiresAt      time.Time
	ChecksumSHA256 string
}

type PresignDownloadInput struct {
	MemberID string
	DraftID  string
	ImageID  string
}

type PresignDownload struct {
	ImageID     string
	DownloadURL string
	ExpiresAt   time.Time
}

type PresignPutInput struct {
	ObjectKey string
	Headers   map[string]string
}

type ObjectMetadata struct {
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	Metadata    map[string]string
}

// NormalizeID is the canonical draft ID: clients sent the same UUID in both cases.
func NormalizeID(draftID string) string {
	return strings.ToLower(strings.TrimSpace(draftID))
}
