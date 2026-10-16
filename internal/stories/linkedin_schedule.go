package stories

import (
	"context"
	"fmt"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	notificationDomain "bloomary-api/internal/domain/notification"
	scheduleDomain "bloomary-api/internal/domain/scheduledpost"
	"bloomary-api/internal/platform/logger"
)

type linkedInScheduleStory struct {
	notifications notificationDomain.Service
	jobs          scheduleDomain.Repository
	accounts      accountDomain.Repository
	drafts        draftDomain.Repository
	objects       draftDomain.ObjectStore
	access        entitlementDomain.AccessStory
	linkedin      linkedinDomain.Gate
	posts         linkedinDomain.LinkedInPostStory
	interval      time.Duration
	maxAhead      time.Duration
	maxAheadDays  int
}

const scheduleMinAhead = 5 * time.Minute

func NewLinkedInScheduleStory(jobs scheduleDomain.Repository, accounts accountDomain.Repository, drafts draftDomain.Repository, objects draftDomain.ObjectStore, access entitlementDomain.AccessStory, linkedin linkedinDomain.Gate, posts linkedinDomain.LinkedInPostStory, interval time.Duration, maxAheadDays int, notifications ...notificationDomain.Service) (scheduleDomain.Story, error) {
	if jobs == nil || accounts == nil || drafts == nil || objects == nil || access == nil || linkedin == nil || posts == nil {
		return nil, fmt.Errorf("schedule dependencies are required")
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxAheadDays <= 0 {
		maxAheadDays = 31
	}
	var notifier notificationDomain.Service
	if len(notifications) > 0 {
		notifier = notifications[0]
	}
	return &linkedInScheduleStory{notifications: notifier, jobs: jobs, accounts: accounts, drafts: drafts, objects: objects, access: access, linkedin: linkedin, posts: posts, interval: interval, maxAhead: time.Duration(maxAheadDays) * 24 * time.Hour, maxAheadDays: maxAheadDays}, nil
}

func (s *linkedInScheduleStory) Schedule(ctx context.Context, in scheduleDomain.CreateInput) (scheduleDomain.Post, error) {
	now := time.Now().UTC()
	entitlement, err := s.access.Resolve(ctx, in.MemberID, now)
	if err != nil {
		return scheduleDomain.Post{}, fmt.Errorf("resolve scheduling entitlement: %w", err)
	}
	if entitlement.Tier != entitlementDomain.TierPro && entitlement.Tier != entitlementDomain.TierTrial {
		return scheduleDomain.Post{}, scheduleDomain.ErrProRequired
	}
	in.ScheduledAt = in.ScheduledAt.UTC()
	if in.ScheduledAt.Before(now.Add(scheduleMinAhead)) || in.ScheduledAt.After(now.Add(s.maxAhead)) {
		return scheduleDomain.Post{}, fmt.Errorf("scheduled time must be between 5 minutes and %d days from now", s.maxAheadDays)
	}
	if _, err := s.drafts.ByID(ctx, in.MemberID, in.DraftID); err != nil {
		return scheduleDomain.Post{}, fmt.Errorf("draft not found")
	}
	token, err := s.accounts.ActiveByMemberID(ctx, in.MemberID)
	if err != nil || token.AccessToken == "" {
		return scheduleDomain.Post{}, fmt.Errorf("reconnect LinkedIn and update your token before scheduling")
	}
	if !token.ExpiresAt.IsZero() && token.ExpiresAt.Before(in.ScheduledAt) {
		return scheduleDomain.Post{}, fmt.Errorf("Your LinkedIn token expires before this post. Reconnect LinkedIn to update your token, then schedule again.")
	}
	return s.jobs.Schedule(ctx, in)
}

func (s *linkedInScheduleStory) Status(ctx context.Context, memberID, id string) (scheduleDomain.Post, error) {
	return s.jobs.ByID(ctx, memberID, id)
}

func (s *linkedInScheduleStory) ListActive(ctx context.Context, memberID string) ([]scheduleDomain.Post, error) {
	return s.jobs.ListActive(ctx, memberID)
}

func (s *linkedInScheduleStory) RunWorker(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for s.publishOne(ctx) {
		}
	}
}

func (s *linkedInScheduleStory) publishOne(ctx context.Context) bool {
	job, err := s.jobs.ClaimDue(ctx, time.Now().UTC())
	if err != nil {
		return false
	}
	token, err := s.accounts.ActiveByMemberID(ctx, job.MemberID)
	if err != nil || token.AccessToken == "" || (!token.ExpiresAt.IsZero() && !token.ExpiresAt.After(time.Now())) {
		_ = s.update(ctx, job, scheduleDomain.Failed, "", "LinkedIn token expired or is invalid. Reconnect LinkedIn to update your token, then schedule the post again.")
		return true
	}
	profile, err := s.linkedin.UserInfo(ctx, token.AccessToken)
	if err != nil || profile.MemberID != job.MemberID {
		_ = s.update(ctx, job, scheduleDomain.Failed, "", "LinkedIn token expired or is invalid. Reconnect LinkedIn to update your token, then schedule the post again.")
		return true
	}
	draft, err := s.drafts.ByID(ctx, job.MemberID, job.DraftID)
	if err != nil {
		_ = s.update(ctx, job, scheduleDomain.DeletedDraft, "", "Draft was deleted before the scheduled time.")
		return true
	}
	var attachments []linkedinDomain.PostAttachment
	if draft.Image != nil {
		data, readErr := s.objects.Read(ctx, draft.Image.ObjectKey)
		if readErr != nil {
			return s.retry(ctx, job, readErr)
		}
		attachments = []linkedinDomain.PostAttachment{{Filename: draft.Image.ImageID, ContentType: draft.Image.MimeType, Data: data}}
	}
	result, err := s.posts.CreatePost(ctx, linkedinDomain.CreatePostInput{AccessToken: token.AccessToken, MemberID: job.MemberID, Commentary: draft.Body, Visibility: "PUBLIC", Attachments: attachments})
	if err != nil {
		if strings.Contains(err.Error(), "publish failed:") {
			_ = s.update(ctx, job, scheduleDomain.NeedsReview, "", err.Error())
			return true
		}
		return s.retry(ctx, job, err)
	}
	if err := s.update(ctx, job, scheduleDomain.Processing, result.PostID, ""); err != nil {
		return true
	}
	if err := s.drafts.MarkPublished(ctx, job.MemberID, job.DraftID); err != nil {
		_ = s.update(ctx, job, scheduleDomain.NeedsReview, result.PostID, "Post published; draft status needs reconciliation.")
		return true
	}
	_ = s.update(ctx, job, scheduleDomain.Published, result.PostID, "")
	return true
}

func (s *linkedInScheduleStory) retry(ctx context.Context, job scheduleDomain.Post, cause error) bool {
	if strings.Contains(cause.Error(), "daily limit") {
		_ = s.update(ctx, job, scheduleDomain.Failed, "", cause.Error())
		return true
	}
	next := time.Now().UTC().Add(2 * time.Minute)
	if err := s.jobs.Retry(ctx, job.ID, next, cause.Error()); err != nil {
		_ = s.update(ctx, job, scheduleDomain.Failed, "", cause.Error())
	}
	return true
}

func (s *linkedInScheduleStory) update(ctx context.Context, job scheduleDomain.Post, status scheduleDomain.Status, postID, message string) error {
	if err := s.jobs.Update(ctx, job.ID, status, postID, message); err != nil {
		logger.FromContext(ctx).WithError(err).Error("scheduled_post_status_update_failed")
		return err
	}
	if s.notifications != nil && (status == scheduleDomain.Published || status == scheduleDomain.Failed || status == scheduleDomain.NeedsReview || status == scheduleDomain.DeletedDraft) {
		s.notifications.Notify(ctx, job.MemberID, job.ID, string(status))
	}
	return nil
}
