package stories

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	dailyDomain "bloomary-api/internal/domain/daily"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	metricsDomain "bloomary-api/internal/domain/metrics"
	"bloomary-api/internal/platform/logger"
)

type linkedInPostStory struct {
	linkedIn      linkedinDomain.Gate
	quota         dailyDomain.QuotaConsumer
	access        entitlementDomain.AccessStory
	dailyRepo     dailyDomain.Repository
	productEvents metricsDomain.PostPublishRepository
	counters      metricsDomain.Recorder
}

func NewLinkedInPostStory(
	linkedIn linkedinDomain.Gate,
	quota dailyDomain.QuotaConsumer,
	access entitlementDomain.AccessStory,
	dailyRepo dailyDomain.Repository,
	productEvents metricsDomain.PostPublishRepository,
	counters metricsDomain.Recorder,
) (linkedinDomain.LinkedInPostStory, error) {
	if linkedIn == nil {
		return nil, fmt.Errorf("linkedin post story: linkedin gate is required")
	}
	if quota == nil {
		return nil, fmt.Errorf("linkedin post story: quota consumer is required")
	}
	if access == nil {
		return nil, fmt.Errorf("linkedin post story: access story is required")
	}
	if dailyRepo == nil {
		return nil, fmt.Errorf("linkedin post story: daily repo is required")
	}
	if productEvents == nil {
		return nil, fmt.Errorf("linkedin post story: product events repo is required")
	}
	if counters == nil {
		return nil, fmt.Errorf("linkedin post story: counters recorder is required")
	}

	return &linkedInPostStory{
		linkedIn:      linkedIn,
		quota:         quota,
		access:        access,
		dailyRepo:     dailyRepo,
		productEvents: productEvents,
		counters:      counters,
	}, nil
}

func (s *linkedInPostStory) CreatePost(
	ctx context.Context, input linkedinDomain.CreatePostInput,
) (linkedinDomain.CreatePostOutput, error) {
	now := time.Now()
	if _, err := s.access.Resolve(ctx, input.MemberID, now); err != nil {
		return linkedinDomain.CreatePostOutput{}, fmt.Errorf("resolve post entitlement: %w", err)
	}

	post := linkedinDomain.Post{
		MemberID:   input.MemberID,
		Commentary: input.Commentary,
		Visibility: input.Visibility,
	}

	if len(input.Attachments) > 0 {
		att := input.Attachments[0]
		mediaURN, err := s.uploadMedia(ctx, input.AccessToken, input.MemberID, att)
		if err != nil {
			return linkedinDomain.CreatePostOutput{}, err
		}
		post.MediaTitle = att.Filename
		post.MediaURN = mediaURN
	}

	if err := s.consumeQuota(ctx, input.MemberID, now); err != nil {
		return linkedinDomain.CreatePostOutput{}, err
	}

	postID, err := s.linkedIn.PublishPost(ctx, input.AccessToken, post)
	if err != nil {
		s.counters.RecordPostPublish(metricsDomain.ResultError)
		return linkedinDomain.CreatePostOutput{}, fmt.Errorf("publish failed: %w", err)
	}

	if err := s.productEvents.RecordPostPublished(ctx, input.MemberID, now); err != nil {
		logger.FromContext(ctx).WithError(err).Error("record_product_post_publish_failed")
	}

	if err := s.dailyRepo.MarkPosted(ctx, input.MemberID, now.UTC()); err != nil {
		logger.FromContext(ctx).WithError(err).Error("mark_linkedin_posted_failed")
	}

	s.counters.RecordPostPublish(metricsDomain.ResultSuccess)

	output := linkedinDomain.CreatePostOutput{
		PostID:      postID,
		PublishedAt: now,
	}

	return output, nil
}

func (s *linkedInPostStory) uploadMedia(
	ctx context.Context, accessToken, memberID string, att linkedinDomain.PostAttachment,
) (string, error) {
	kind := determineUploadKind(att.Filename, att.ContentType)
	if kind == "" {
		return "", fmt.Errorf("unsupported linkedin attachment type")
	}

	for range 2 {
		if err := s.consumeQuota(ctx, memberID, time.Now()); err != nil {
			return "", err
		}
	}

	return s.linkedIn.UploadMedia(
		ctx, accessToken, linkedinDomain.Upload{
			Owner: "urn:li:person:" + memberID,
			Kind:  kind,
		}, att.ContentType, bytes.NewReader(att.Data),
	)
}

func (s *linkedInPostStory) consumeQuota(ctx context.Context, memberID string, now time.Time) error {
	allowed, err := s.quota.Consume(ctx, memberID, now.UTC())
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: %s", dailyDomain.ErrDailyLimitExceeded, memberID)
	}
	return nil
}

func determineUploadKind(filename, contentType string) linkedinDomain.UploadKind {
	if isLinkedInImage(filename, contentType) {
		return linkedinDomain.UploadKindImage
	}
	if isLinkedInDocument(filename, contentType) {
		return linkedinDomain.UploadKindDocument
	}
	return ""
}

func isLinkedInImage(filename, contentType string) bool {
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	}
	return false
}

func isLinkedInDocument(filename, contentType string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx":
		return true
	}
	return contentType == "application/pdf" ||
		contentType == "application/msword" ||
		contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		contentType == "application/vnd.ms-powerpoint" ||
		contentType == "application/vnd.openxmlformats-officedocument.presentationml.presentation"
}
