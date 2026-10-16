package stories

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	draftDomain "bloomary-api/internal/domain/draft"
	perror "bloomary-api/internal/platform/error"

	"github.com/google/uuid"
)

var sha256HexPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type draftImages struct {
	store draftDomain.ObjectStore
}

var _ draftDomain.ImageStory = (*draftImages)(nil)

func NewDraftImages(store draftDomain.ObjectStore) (draftDomain.ImageStory, error) {
	if store == nil {
		return nil, fmt.Errorf("draft images story: object store is required")
	}

	return &draftImages{store: store}, nil
}

func (s *draftImages) PresignUpload(ctx context.Context, input draftDomain.PresignUploadInput) (draftDomain.PresignUpload, error) {
	if err := validatePresignUpload(input, s.store.MaxUploadBytes()); err != nil {
		return draftDomain.PresignUpload{}, err
	}

	imageID := uuid.NewString()
	objectKey := s.store.StagingObjectKey(input.MemberID, input.DraftID, imageID)
	headers := map[string]string{
		"Content-Type":               input.MimeType,
		"x-amz-meta-member-id":       input.MemberID,
		"x-amz-meta-draft-id":        input.DraftID,
		"x-amz-meta-image-id":        imageID,
		"x-amz-meta-checksum-sha256": strings.ToLower(input.ChecksumSHA256),
		"x-amz-meta-size-bytes":      strconv.FormatInt(input.SizeBytes, 10),
	}
	uploadURL, expiresAt, err := s.store.PresignPut(
		ctx, draftDomain.PresignPutInput{ObjectKey: objectKey, Headers: headers},
	)
	if err != nil {
		return draftDomain.PresignUpload{}, perror.New(perror.ErrCodeBadGateway, err, "draft image presign failed")
	}

	return draftDomain.PresignUpload{
		ImageID:        imageID,
		ObjectKey:      objectKey,
		UploadURL:      uploadURL,
		Headers:        headers,
		MaxSizeBytes:   s.store.MaxUploadBytes(),
		ExpiresAt:      expiresAt,
		ChecksumSHA256: strings.ToLower(input.ChecksumSHA256),
	}, nil
}

func (s *draftImages) PresignDownload(ctx context.Context, input draftDomain.PresignDownloadInput) (draftDomain.PresignDownload, error) {
	if err := validatePresignDownload(input); err != nil {
		return draftDomain.PresignDownload{}, err
	}

	objectKey := s.store.LinkedObjectKey(input.MemberID, input.DraftID, input.ImageID)
	if _, err := s.store.Stat(ctx, objectKey); err != nil {
		return draftDomain.PresignDownload{}, perror.New(draftDomain.ErrCodeImageNotFound, err, "draft image was not found")
	}
	downloadURL, expiresAt, err := s.store.PresignGet(ctx, objectKey)
	if err != nil {
		return draftDomain.PresignDownload{}, perror.New(perror.ErrCodeBadGateway, err, "draft image download presign failed")
	}

	return draftDomain.PresignDownload{
		ImageID:     input.ImageID,
		DownloadURL: downloadURL,
		ExpiresAt:   expiresAt,
	}, nil
}

func validatePresignUpload(input draftDomain.PresignUploadInput, maxBytes int64) error {
	if strings.TrimSpace(input.MemberID) == "" ||
		strings.TrimSpace(input.DraftID) == "" ||
		strings.TrimSpace(input.MimeType) == "" ||
		strings.TrimSpace(input.ChecksumSHA256) == "" {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image metadata is required")
	}
	if !strings.HasPrefix(strings.ToLower(input.MimeType), "image/") {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image mime type is invalid")
	}
	if input.SizeBytes <= 0 {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image size is invalid")
	}
	if maxBytes > 0 && input.SizeBytes > maxBytes {
		return perror.New(draftDomain.ErrCodeImageTooLarge, nil, "draft image is too large")
	}
	if !sha256HexPattern.MatchString(input.ChecksumSHA256) {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image checksum is invalid")
	}

	return nil
}

func validatePresignDownload(input draftDomain.PresignDownloadInput) error {
	if strings.TrimSpace(input.MemberID) == "" ||
		strings.TrimSpace(input.DraftID) == "" ||
		strings.TrimSpace(input.ImageID) == "" {
		return perror.New(draftDomain.ErrCodeInvalidImage, nil, "draft image metadata is required")
	}

	return nil
}
