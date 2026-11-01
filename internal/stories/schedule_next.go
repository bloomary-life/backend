package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

const scheduleNextPrefix = "sn:"

// postSlots and postSlotEnds are the web app's best posting windows (platform/app.js weekdayPostTimes), start and
// end hour, indexed by time.Weekday, in Europe/Nicosia.
var (
	postSlots    = [7]int{10, 9, 10, 9, 10, 9, 10}
	postSlotEnds = [7]int{12, 11, 12, 11, 12, 10, 12}
)

const slotSearchDays = 14

// nextPostSlot is the earliest moment in a posting window from now on, on a Cyprus day with no other pending or
// retry post of the member (BE-025): inside today's open window that is now. If every day
// of the next two weeks is taken, the last slot looked at.
func nextPostSlot(now time.Time, active []scheduledDomain.Post) time.Time {
	taken := map[string]bool{}
	for _, job := range active {
		if stillScheduled(job.Status) {
			taken[job.ScheduledAt.In(postTimeZone).Format("2006-01-02")] = true
		}
	}
	local := now.In(postTimeZone)
	var slot time.Time
	for d := 0; d <= slotSearchDays; d++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+d, 0, 0, 0, 0, postTimeZone)
		slot = time.Date(day.Year(), day.Month(), day.Day(), postSlots[day.Weekday()], 0, 0, 0, postTimeZone)
		end := time.Date(day.Year(), day.Month(), day.Day(), postSlotEnds[day.Weekday()], 0, 0, 0, postTimeZone)
		if slot.Before(now) {
			slot = now
		}
		if !slot.Before(end) || taken[day.Format("2006-01-02")] {
			continue
		}
		return slot
	}
	return slot
}

// ScheduleNextCallbacks handles "📅 Schedule (next slot)" under a draft preview ("sn:<notice id>", BE-025): the
// draft is scheduled at the member's next free posting slot, its first comment kept, and a scheduled preview
// replaces the draft preview.
type ScheduleNextCallbacks struct {
	reviews  reviewDomain.Repository
	drafts   draftDomain.Repository
	schedule scheduledDomain.Story
	comments reviewDomain.FirstCommentStory
	notify   *NotifyScheduled
	now      func() time.Time
}

var _ telegramDomain.CallbackHandler = (*ScheduleNextCallbacks)(nil)

// NewScheduleNextCallbacks builds the handler; comments may be nil (then a first comment is not carried over).
func NewScheduleNextCallbacks(reviews reviewDomain.Repository, drafts draftDomain.Repository, schedule scheduledDomain.Story,
	comments reviewDomain.FirstCommentStory, notify *NotifyScheduled) (*ScheduleNextCallbacks, error) {
	if reviews == nil || drafts == nil || schedule == nil || notify == nil {
		return nil, fmt.Errorf("schedule next callbacks: reviews, drafts, schedule story and notify story are required")
	}
	return &ScheduleNextCallbacks{reviews: reviews, drafts: drafts, schedule: schedule, comments: comments, notify: notify, now: time.Now}, nil
}

func (s *ScheduleNextCallbacks) Prefix() string { return scheduleNextPrefix }

func (s *ScheduleNextCallbacks) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	notice, err := s.reviews.NoticeByID(ctx, link.MemberID, strings.TrimPrefix(callback.Data, scheduleNextPrefix))
	if err != nil || notice.Status != reviewDomain.NoticeSent || notice.ScheduleID != "" {
		return "This preview is no longer active.", nil
	}
	draft, err := s.drafts.ByID(ctx, link.MemberID, notice.DraftID)
	if err != nil || draft.Status != "draft" {
		return "This draft can't be scheduled any more.", nil
	}
	active, err := s.schedule.ListActive(ctx, link.MemberID)
	if err != nil {
		return "", err
	}
	for _, job := range active {
		if job.DraftID == notice.DraftID && stillScheduled(job.Status) {
			return "Already scheduled: " + formatPostTime(job.ScheduledAt), nil
		}
	}
	at := nextPostSlot(s.now().UTC(), active)
	job, err := s.schedule.Schedule(ctx, scheduledDomain.CreateInput{MemberID: link.MemberID, DraftID: notice.DraftID, ScheduledAt: at.UTC()})
	if err != nil {
		return truncateAnswer("Not scheduled: " + err.Error()), nil
	}
	if notice.FirstComment != "" && s.comments != nil {
		if err := s.comments.Save(ctx, link.MemberID, notice.DraftID, job.ID, notice.FirstComment); err != nil {
			logger.FromContext(ctx).WithError(err).Info("schedule_next_first_comment_failed")
		}
	}
	answer := "Scheduled: " + formatPostTime(job.ScheduledAt)
	if _, err := s.notify.Notify(ctx, link.MemberID, notice.DraftID, "Scheduled from Telegram."); err != nil {
		if errors.Is(err, reviewDomain.ErrDailyLimit) {
			return answer + ". Today's preview limit is reached; see it in My posts.", nil
		}
		logger.FromContext(ctx).WithError(err).Info("schedule_next_preview_failed")
	}
	return answer, nil
}

// truncateAnswer keeps a callback answer within Telegram's 200-character limit.
func truncateAnswer(text string) string {
	if r := []rune(text); len(r) > 190 {
		return string(r[:189]) + "…"
	}
	return text
}
