package stories

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	perror "bloomary-api/internal/platform/error"
)

const (
	MaxDraftBodyRunes = 4000
)

type preparedDraftImage struct {
	stagingKey string
	linkedKey  string
}

type SyncDrafts struct {
	access entitlementDomain.AccessStory
	drafts draftDomain.Repository
}

func NewSyncDrafts(
	access entitlementDomain.AccessStory,
	drafts draftDomain.Repository,
) (*SyncDrafts, error) {
	if access == nil {
		return nil, fmt.Errorf("sync drafts story: access story is required")
	}
	if drafts == nil {
		return nil, fmt.Errorf("sync drafts story: draft repo is required")
	}

	return &SyncDrafts{access: access, drafts: drafts}, nil
}

func (s *SyncDrafts) Sync(
	ctx context.Context, memberID string, upserts []draftDomain.Upsert, deletes []string, images draftDomain.ObjectStore,
) ([]draftDomain.Draft, error) {
	entitlement, err := s.access.Resolve(ctx, memberID, time.Now())
	if err != nil {
		return nil, perror.New(perror.ErrCodeStoreFailed, err, "draft sync failed")
	}
	draftLimit := entitlement.DraftLimit
	if draftLimit <= 0 {
		draftLimit = entitlementDomain.FreeDraftLimit
	}

	upserts = normalizedUpserts(upserts)
	deletes = normalizedDraftIDs(deletes)
	// A lapsed member's archive is write-once: pull stays open and a new draft
	// may still be saved under the free limit, but nothing already stored can be
	// edited or deleted. Without this the read-only state would be client-side
	// theater: the upserts and deletes below would still land.
	if !entitlement.HasDraftAccess() && len(upserts)+len(deletes) > 0 {
		if err := s.rejectLapsedChanges(ctx, memberID, upserts, deletes); err != nil {
			return nil, err
		}
	}
	if len(upserts)+len(deletes) == 0 {
		drafts, err := s.drafts.Sync(ctx, memberID, nil, nil, draftLimit)
		if err != nil {
			return nil, perror.New(perror.ErrCodeStoreFailed, err, "draft sync failed")
		}
		if err := presignDraftImages(ctx, images, drafts); err != nil {
			return nil, err
		}
		return drafts, nil
	}

	purgeImageIDs := append([]string{}, deletes...)
	for _, upsert := range upserts {
		if upsert.RemoveImage && upsert.Image == nil {
			purgeImageIDs = append(purgeImageIDs, upsert.DraftID)
		}
	}
	if len(purgeImageIDs) > 0 && images != nil {
		existing, err := s.drafts.Sync(ctx, memberID, nil, nil, draftLimit)
		if err != nil {
			return nil, perror.New(perror.ErrCodeStoreFailed, err, "draft sync failed")
		}
		if err := deleteObjectKeys(ctx, images, draftImageObjectKeys(existing, purgeImageIDs)); err != nil {
			return nil, err
		}
	}

	preparedImages := make([]preparedDraftImage, 0)
	for index := range upserts {
		draft := &upserts[index]
		if draft.DraftID == "" {
			return nil, perror.New(draftDomain.ErrCodeInvalidDraft, nil, "draft id is required")
		}

		if utf8.RuneCountInString(draft.Body) > MaxDraftBodyRunes {
			return nil, perror.New(draftDomain.ErrCodeDraftTooLong, nil, "draft body is too long")
		}
		if draft.Image != nil {
			prepared, err := prepareImage(ctx, images, memberID, draft)
			if err != nil {
				return nil, err
			}
			preparedImages = append(preparedImages, prepared)
		}
	}

	drafts, err := s.drafts.Sync(ctx, memberID, upserts, deletes, draftLimit)
	if err != nil {
		for _, image := range preparedImages {
			_ = images.Delete(ctx, image.linkedKey)
		}
	}
	if errors.Is(err, draftDomain.ErrTooManyDrafts) {
		return nil, perror.New(draftDomain.ErrCodeTooManyDraft, err, "too many synced drafts")
	}

	if err != nil {
		return nil, perror.New(perror.ErrCodeStoreFailed, err, "draft sync failed")
	}

	if err := presignDraftImages(ctx, images, drafts); err != nil {
		return nil, err
	}
	for _, image := range preparedImages {
		_ = images.Delete(ctx, image.stagingKey)
	}

	return drafts, nil
}

// rejectLapsedChanges refuses any delete and any upsert of a draft that is
// already stored (or repeated within the request), so drafts cannot be edited
// or rotated to stay under the free limit.
func (s *SyncDrafts) rejectLapsedChanges(
	ctx context.Context, memberID string, upserts []draftDomain.Upsert, deletes []string,
) error {
	if len(deletes) > 0 {
		return perror.New(draftDomain.ErrCodeProRequired, nil, "draft archive is read-only")
	}
	existing, err := s.drafts.ListByMemberID(ctx, memberID)
	if err != nil {
		return perror.New(perror.ErrCodeStoreFailed, err, "draft sync failed")
	}
	stored := make(map[string]bool, len(existing)+len(upserts))
	for _, draft := range existing {
		stored[draft.DraftID] = true
	}
	for _, upsert := range upserts {
		if stored[upsert.DraftID] {
			return perror.New(draftDomain.ErrCodeProRequired, nil, "draft archive is read-only")
		}
		stored[upsert.DraftID] = true
	}

	return nil
}

func prepareImage(
	ctx context.Context, images draftDomain.ObjectStore, memberID string, draft *draftDomain.Upsert,
) (preparedDraftImage, error) {
	if images == nil {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image storage is not configured")
	}
	image := draft.Image
	image.ImageID = strings.TrimSpace(image.ImageID)
	image.MimeType = strings.TrimSpace(image.MimeType)
	image.ChecksumSHA256 = strings.ToLower(strings.TrimSpace(image.ChecksumSHA256))
	if image.ImageID == "" || image.MimeType == "" || image.ChecksumSHA256 == "" {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image metadata is required")
	}
	if !strings.HasPrefix(strings.ToLower(image.MimeType), "image/") {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image mime type is invalid")
	}
	if image.SizeBytes <= 0 {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image size is invalid")
	}
	if image.SizeBytes > images.MaxUploadBytes() {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeImageTooLarge, nil, "draft image is too large")
	}
	if !sha256HexPattern.MatchString(image.ChecksumSHA256) {
		return preparedDraftImage{}, perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image checksum is invalid")
	}

	stagingKey := images.StagingObjectKey(memberID, draft.DraftID, image.ImageID)
	if err := verifyUploadedImage(ctx, images, stagingKey, memberID, draft.DraftID, image); err != nil {
		return preparedDraftImage{}, err
	}

	linkedKey := images.LinkedObjectKey(memberID, draft.DraftID, image.ImageID)
	if err := images.Copy(ctx, stagingKey, linkedKey); err != nil {
		return preparedDraftImage{}, perror.New(perror.ErrCodeBadGateway, err, "draft image link failed")
	}
	image.ObjectKey = linkedKey

	return preparedDraftImage{stagingKey: stagingKey, linkedKey: linkedKey}, nil
}

func verifyUploadedImage(
	ctx context.Context, images draftDomain.ObjectStore, objectKey, memberID, draftID string, image *draftDomain.UpsertImage,
) error {
	metadata, err := images.Stat(ctx, objectKey)
	if err != nil {
		return perror.New(draftDomain.ErrCodeImageNotFound, err, "draft image upload was not found")
	}
	if metadata.ContentType != image.MimeType || metadata.SizeBytes != image.SizeBytes {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image metadata does not match upload")
	}
	if metaValue(metadata.Metadata, "member-id") != memberID ||
		metaValue(metadata.Metadata, "draft-id") != draftID ||
		metaValue(metadata.Metadata, "image-id") != image.ImageID {
		return perror.New(draftDomain.ErrCodeImageOwnershipMismatch, nil, "draft image ownership does not match")
	}
	if metaValue(metadata.Metadata, "checksum-sha256") != image.ChecksumSHA256 ||
		metaValue(metadata.Metadata, "size-bytes") != strconv.FormatInt(image.SizeBytes, 10) {
		return perror.New(draftDomain.ErrCodeImageChecksumMismatch, nil, "draft image checksum does not match")
	}

	return nil
}

func presignDraftImages(ctx context.Context, images draftDomain.ObjectStore, drafts []draftDomain.Draft) error {
	if images == nil {
		return nil
	}
	for index := range drafts {
		if drafts[index].Image == nil || drafts[index].Image.ObjectKey == "" {
			continue
		}
		downloadURL, _, err := images.PresignGet(ctx, drafts[index].Image.ObjectKey)
		if err != nil {
			return perror.New(perror.ErrCodeBadGateway, err, "draft image download presign failed")
		}
		drafts[index].Image.DownloadURL = downloadURL
	}

	return nil
}

func metaValue(metadata map[string]string, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if value := metadata[key]; value != "" {
		return value
	}
	return metadata["x-amz-meta-"+key]
}

func draftImageObjectKeys(drafts []draftDomain.Draft, draftIDs []string) []string {
	includeAll := len(draftIDs) == 0
	allowed := make(map[string]bool, len(draftIDs))
	for _, draftID := range draftIDs {
		allowed[strings.TrimSpace(draftID)] = true
	}

	keys := make([]string, 0, len(drafts))
	for _, draft := range drafts {
		if !includeAll && !allowed[draft.DraftID] {
			continue
		}
		if draft.Image == nil || strings.TrimSpace(draft.Image.ObjectKey) == "" {
			continue
		}
		keys = append(keys, strings.TrimSpace(draft.Image.ObjectKey))
	}

	return keys
}

func deleteObjectKeys(ctx context.Context, images draftDomain.ObjectStore, objectKeys []string) error {
	deleted := make(map[string]bool, len(objectKeys))
	for _, objectKey := range objectKeys {
		objectKey = strings.TrimSpace(objectKey)
		if objectKey == "" || deleted[objectKey] {
			continue
		}
		deleted[objectKey] = true
		if err := images.Delete(ctx, objectKey); err != nil {
			return perror.New(perror.ErrCodeBadGateway, err, "draft image delete failed")
		}
	}

	return nil
}

func normalizedUpserts(upserts []draftDomain.Upsert) []draftDomain.Upsert {
	out := make([]draftDomain.Upsert, 0, len(upserts))
	for _, draft := range upserts {
		draft.DraftID = draftDomain.NormalizeID(draft.DraftID)
		out = append(out, draft)
	}

	return out
}

func normalizedDraftIDs(draftIDs []string) []string {
	out := make([]string, 0, len(draftIDs))
	for _, draftID := range draftIDs {
		draftID = draftDomain.NormalizeID(draftID)
		if draftID == "" {
			continue
		}

		out = append(out, draftID)
	}

	return out
}
