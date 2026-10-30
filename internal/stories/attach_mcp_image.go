package stories

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"

	"github.com/google/uuid"
)

// LinkedIn image posts accept these formats; the type is sniffed from the bytes, not trusted from the caller.
var mcpImageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true}

type AttachMCPImage struct {
	access  entitlementDomain.AccessStory
	drafts  draftDomain.Repository
	objects draftDomain.ObjectStore
	fetcher draftDomain.RemoteImageFetcher
}

var _ draftDomain.AttachMCPImageStory = (*AttachMCPImage)(nil)

func NewAttachMCPImage(access entitlementDomain.AccessStory, drafts draftDomain.Repository, objects draftDomain.ObjectStore, fetcher draftDomain.RemoteImageFetcher) (*AttachMCPImage, error) {
	if access == nil || drafts == nil || objects == nil || fetcher == nil {
		return nil, fmt.Errorf("attach MCP image: access, drafts, object store and fetcher are required")
	}
	return &AttachMCPImage{access: access, drafts: drafts, objects: objects, fetcher: fetcher}, nil
}

func (s *AttachMCPImage) Attach(ctx context.Context, in draftDomain.AttachImageInput) (draftDomain.Draft, error) {
	if _, err := requireMCPPaid(ctx, s.access, in.MemberID); err != nil {
		return draftDomain.Draft{}, err
	}
	draft, err := s.drafts.ByID(ctx, in.MemberID, in.DraftID)
	if err != nil {
		return draftDomain.Draft{}, ErrMCPDraftNotFound
	}
	if draft.Status != "draft" {
		return draftDomain.Draft{}, ErrMCPDraftNotEditable
	}
	data, err := s.imageBytes(ctx, in)
	if err != nil {
		return draftDomain.Draft{}, err
	}
	mimeType := http.DetectContentType(data)
	if !mcpImageTypes[mimeType] {
		return draftDomain.Draft{}, errors.New("image must be JPEG, PNG or GIF")
	}
	if declared := strings.ToLower(strings.TrimSpace(in.MimeType)); declared != "" && declared != mimeType {
		return draftDomain.Draft{}, fmt.Errorf("mime_type %s does not match the image bytes (%s)", declared, mimeType)
	}
	sum := sha256.Sum256(data)
	image := draftDomain.UpsertImage{ImageID: uuid.NewString(), MimeType: mimeType, SizeBytes: int64(len(data)), ChecksumSHA256: hex.EncodeToString(sum[:])}
	image.ObjectKey = s.objects.LinkedObjectKey(in.MemberID, in.DraftID, image.ImageID)
	metadata := map[string]string{"member-id": in.MemberID, "draft-id": in.DraftID, "image-id": image.ImageID,
		"checksum-sha256": image.ChecksumSHA256, "size-bytes": strconv.FormatInt(image.SizeBytes, 10)}
	if err := s.objects.Put(ctx, image.ObjectKey, mimeType, data, metadata); err != nil {
		return draftDomain.Draft{}, fmt.Errorf("store image: %w", err)
	}
	previous, err := s.drafts.SetImage(ctx, in.MemberID, in.DraftID, image)
	if err != nil {
		_ = s.objects.Delete(ctx, image.ObjectKey)
		return draftDomain.Draft{}, err
	}
	if previous != "" && previous != image.ObjectKey {
		_ = s.objects.Delete(ctx, previous)
	}
	return s.drafts.ByID(ctx, in.MemberID, in.DraftID)
}

func (s *AttachMCPImage) imageBytes(ctx context.Context, in draftDomain.AttachImageInput) ([]byte, error) {
	maxBytes := s.objects.MaxUploadBytes()
	hasURL, hasData := strings.TrimSpace(in.ImageURL) != "", strings.TrimSpace(in.DataBase64) != ""
	if hasURL == hasData {
		return nil, errors.New("send exactly one of image_url and data_base64")
	}
	if hasURL {
		return s.fetcher.Fetch(ctx, strings.TrimSpace(in.ImageURL), maxBytes)
	}
	encoded := strings.TrimSpace(in.DataBase64)
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > maxBytes+2 {
		return nil, draftDomain.ErrImageTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("data_base64 is not valid base64")
	}
	if int64(len(data)) > maxBytes {
		return nil, draftDomain.ErrImageTooLarge
	}
	return data, nil
}
