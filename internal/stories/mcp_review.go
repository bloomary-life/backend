package stories

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

const noticeHeader = "LinkedIn post, scheduled. It goes out at the time below unless you tap Skip. Reply to this message (voice or text) to change it."

const draftHeader = "LinkedIn post draft, not scheduled. Reply to this message (voice or text) to change it. To post it, open it in Bloomary."

// Telegram counts message length in UTF-16 code units; a message holds at most 4096.
const telegramMessageLimit = 4096

// NotifyScheduled tells the member in Telegram that a post is scheduled: preview, time, Edit link and Skip.
// The post publishes at its time unless the member taps Skip.
type NotifyScheduled struct {
	access      entitlementDomain.AccessStory
	drafts      draftDomain.Repository
	jobs        scheduledDomain.Repository
	reviews     reviewDomain.Repository
	objects     draftDomain.ObjectStore
	notifier    telegramDomain.Notifier
	platformURL string
}

// NewNotifyScheduled builds the story; objects may be nil when draft images are not configured.
func NewNotifyScheduled(access entitlementDomain.AccessStory, drafts draftDomain.Repository, jobs scheduledDomain.Repository, reviews reviewDomain.Repository,
	objects draftDomain.ObjectStore, notifier telegramDomain.Notifier, platformURL string) (*NotifyScheduled, error) {
	if access == nil || drafts == nil || jobs == nil || reviews == nil || notifier == nil {
		return nil, fmt.Errorf("notify scheduled: access, drafts, schedules, reviews and notifier are required")
	}
	return &NotifyScheduled{access: access, drafts: drafts, jobs: jobs, reviews: reviews, objects: objects, notifier: notifier, platformURL: strings.TrimRight(platformURL, "/")}, nil
}

func (s *NotifyScheduled) Notify(ctx context.Context, memberID, draftID, note string) (reviewDomain.Notice, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Notice{}, err
	}
	if utf8.RuneCountInString(note) > reviewDomain.MaxNoteRunes {
		return reviewDomain.Notice{}, fmt.Errorf("note is longer than %d characters", reviewDomain.MaxNoteRunes)
	}
	draft, err := s.drafts.ByID(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	job, err := s.activeSchedule(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Notice{}, err
	}
	return s.send(ctx, memberID, draft, job, note, false)
}

// NotifyDraft sends the preview of an unscheduled, unpublished draft (draft-only mode): no time and no Skip.
func (s *NotifyScheduled) NotifyDraft(ctx context.Context, memberID, draftID, note string) (reviewDomain.Notice, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Notice{}, err
	}
	if utf8.RuneCountInString(note) > reviewDomain.MaxNoteRunes {
		return reviewDomain.Notice{}, fmt.Errorf("note is longer than %d characters", reviewDomain.MaxNoteRunes)
	}
	draft, err := s.drafts.ByID(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	if draft.Status != "draft" {
		return reviewDomain.Notice{}, ErrMCPDraftNotEditable
	}
	return s.send(ctx, memberID, draft, scheduledDomain.Post{}, note, false)
}

// Preview sends the preview of one of the member's scheduled posts on request in the bot (/planned → Preview).
func (s *NotifyScheduled) Preview(ctx context.Context, memberID, scheduleID string) (reviewDomain.Notice, error) {
	job, err := s.jobs.ByID(ctx, memberID, scheduleID)
	if err != nil || !stillScheduled(job.Status) {
		return reviewDomain.Notice{}, reviewDomain.ErrNotScheduled
	}
	draft, err := s.drafts.ByID(ctx, memberID, job.DraftID)
	if err != nil {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	return s.send(ctx, memberID, draft, job, "", true)
}

// send stores a notice and sends the preview; a reply to the preview message (or its image) becomes an edit request.
// requested previews (the member's Preview taps) count against their own daily limit, not the AI app's.
func (s *NotifyScheduled) send(ctx context.Context, memberID string, draft draftDomain.Draft, job scheduledDomain.Post, note string, requested bool) (reviewDomain.Notice, error) {
	now := time.Now().UTC()
	count, err := s.reviews.CountNoticesSince(ctx, memberID, now.Add(-24*time.Hour), requested)
	if err != nil {
		return reviewDomain.Notice{}, err
	}
	limit := reviewDomain.DailyNoticeLimit
	if requested {
		limit = reviewDomain.DailyPreviewLimit
	}
	if count >= limit {
		return reviewDomain.Notice{}, reviewDomain.ErrDailyLimit
	}
	id, err := randomHex(16)
	if err != nil {
		return reviewDomain.Notice{}, err
	}
	notice := reviewDomain.Notice{ID: id, MemberID: memberID, DraftID: draft.DraftID, ScheduleID: job.ID, ScheduledAt: job.ScheduledAt,
		ProposedText: draft.Body, Note: strings.TrimSpace(note), Status: reviewDomain.NoticeSent, CreatedAt: now, Requested: requested}
	if err := s.reviews.CreateNotice(ctx, notice); err != nil {
		return reviewDomain.Notice{}, err
	}
	var photo *telegramDomain.Photo
	if draft.Image != nil && s.objects != nil {
		data, err := s.objects.Read(ctx, draft.Image.ObjectKey)
		if err != nil {
			return reviewDomain.Notice{}, fmt.Errorf("read draft image: %w", err)
		}
		photo = &telegramDomain.Photo{Data: data, MimeType: draft.Image.MimeType, Caption: "Image for the post below"}
	}
	header, first := noticeHeader, []telegramDomain.Button{{Text: "✏️ How to edit", CallbackData: previewPrefix + "e"}, {Text: "Skip", CallbackData: "sk:" + id}}
	if job.ID == "" {
		header, first[1].Text = draftHeader, "Discard"
	}
	message := telegramDomain.OutgoingMessage{Text: noticeText(header, notice), Buttons: [][]telegramDomain.Button{
		first, {{Text: "Edit in Bloomary", URL: s.platformURL + "/editor?draft=" + url.QueryEscape(draft.DraftID)}},
	}}
	sent, err := s.notifier.Notify(ctx, memberID, message, photo)
	if err != nil {
		if errors.Is(err, telegramDomain.ErrNotLinked) {
			return reviewDomain.Notice{}, reviewDomain.ErrNotLinked
		}
		return reviewDomain.Notice{}, fmt.Errorf("send Telegram notice: %w", err)
	}
	notice.MessageID, notice.PhotoMessageID = sent.MessageID, sent.PhotoMessageID
	if err := s.reviews.SetNoticeMessage(ctx, memberID, id, sent.MessageID, sent.PhotoMessageID); err != nil {
		return reviewDomain.Notice{}, err
	}
	return notice, nil
}

func (s *NotifyScheduled) activeSchedule(ctx context.Context, memberID, draftID string) (scheduledDomain.Post, error) {
	jobs, err := s.jobs.ListActive(ctx, memberID)
	if err != nil {
		return scheduledDomain.Post{}, err
	}
	for _, job := range jobs {
		if job.DraftID == draftID && stillScheduled(job.Status) {
			return job, nil
		}
	}
	return scheduledDomain.Post{}, reviewDomain.ErrNotScheduled
}

// stillScheduled is true while the post has not started going out and was not cancelled. needs_review is
// not: the post may already be on LinkedIn (published but not recorded, or interrupted mid-publish).
func stillScheduled(status scheduledDomain.Status) bool {
	return status == scheduledDomain.Pending || status == scheduledDomain.Retry
}

const (
	skipTooLate     = "Too late: the post is already going out on LinkedIn."
	skipNeedsReview = "This post may already be on LinkedIn — check it in Bloomary."
	skipGone        = "This post is no longer scheduled."
)

// SkipCallbacks handles Skip ("sk:<notice id>"): it cancels the post's schedule unless it already went out.
// On a draft preview the same button is Discard: the preview is marked skipped and the draft stays.
type SkipCallbacks struct {
	reviews reviewDomain.Repository
	jobs    scheduledDomain.Repository
	bot     telegramDomain.Gate
}

var _ telegramDomain.CallbackHandler = (*SkipCallbacks)(nil)

func NewSkipCallbacks(reviews reviewDomain.Repository, jobs scheduledDomain.Repository, bot telegramDomain.Gate) (*SkipCallbacks, error) {
	if reviews == nil || jobs == nil || bot == nil {
		return nil, fmt.Errorf("skip callbacks: reviews, schedules and bot gate are required")
	}
	return &SkipCallbacks{reviews: reviews, jobs: jobs, bot: bot}, nil
}

func (s *SkipCallbacks) Prefix() string { return "sk:" }

func (s *SkipCallbacks) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	notice, err := s.reviews.NoticeByID(ctx, link.MemberID, strings.TrimPrefix(callback.Data, "sk:"))
	if err != nil {
		return skipGone, nil
	}
	if notice.Status == reviewDomain.NoticeSkipped {
		return "Already done.", nil
	}
	result := "Discarded: the draft stays in Bloomary."
	if notice.ScheduleID != "" {
		job, err := s.jobs.ByID(ctx, link.MemberID, notice.ScheduleID)
		if err != nil {
			return skipGone, nil
		}
		switch job.Status {
		case scheduledDomain.Pending, scheduledDomain.Retry:
		case scheduledDomain.NeedsReview:
			return skipNeedsReview, nil
		case scheduledDomain.Published, scheduledDomain.Processing:
			return skipTooLate, nil
		default:
			return skipGone, nil
		}
		cancelled, err := s.jobs.Cancel(ctx, link.MemberID, notice.DraftID)
		if err != nil {
			return "", err
		}
		if !cancelled {
			// The worker claimed the post between the status check and the cancel.
			return skipTooLate, nil
		}
		result = "Skipped: the schedule is cancelled, the draft stays in Bloomary."
	}
	if _, err := s.reviews.SkipNotice(ctx, link.MemberID, notice.ID, time.Now().UTC()); err != nil {
		return "", err
	}
	if err := s.bot.EditMessageText(ctx, callback.ChatID, callback.MessageID, noticeText(result, notice)); err != nil {
		logger.FromContext(ctx).WithError(err).Info("telegram_edit_notice_failed")
	}
	return result, nil
}

// SendSuggestion sends a comment idea with Open post, Copy, rating 1-5 and Posted buttons.
type SendSuggestion struct {
	access   entitlementDomain.AccessStory
	reviews  reviewDomain.Repository
	notifier telegramDomain.Notifier
}

func NewSendSuggestion(access entitlementDomain.AccessStory, reviews reviewDomain.Repository, notifier telegramDomain.Notifier) (*SendSuggestion, error) {
	if access == nil || reviews == nil || notifier == nil {
		return nil, fmt.Errorf("send suggestion: access, reviews and notifier are required")
	}
	return &SendSuggestion{access: access, reviews: reviews, notifier: notifier}, nil
}

func (s *SendSuggestion) Send(ctx context.Context, in reviewDomain.SuggestionInput) (reviewDomain.Suggestion, error) {
	if _, err := requireMCPPaid(ctx, s.access, in.MemberID); err != nil {
		return reviewDomain.Suggestion{}, err
	}
	if in.Kind != "comment" {
		return reviewDomain.Suggestion{}, reviewDomain.ErrUnknownKind
	}
	target, err := url.Parse(strings.TrimSpace(in.TargetURL))
	if err != nil || target.Scheme != "https" || (target.Host != "www.linkedin.com" && target.Host != "linkedin.com") || target.User != nil {
		return reviewDomain.Suggestion{}, reviewDomain.ErrInvalidTarget
	}
	text, postContext := strings.TrimSpace(in.Text), strings.TrimSpace(in.Context)
	if text == "" || utf8.RuneCountInString(text) > reviewDomain.MaxSuggestionRunes || utf8.RuneCountInString(postContext) > reviewDomain.MaxContextRunes {
		return reviewDomain.Suggestion{}, fmt.Errorf("text must be 1-%d characters and context at most %d", reviewDomain.MaxSuggestionRunes, reviewDomain.MaxContextRunes)
	}
	now := time.Now().UTC()
	count, err := s.reviews.CountSuggestionsSince(ctx, in.MemberID, now.Add(-24*time.Hour))
	if err != nil {
		return reviewDomain.Suggestion{}, err
	}
	if count >= reviewDomain.DailySuggestionLimit {
		return reviewDomain.Suggestion{}, reviewDomain.ErrDailyLimit
	}
	id, err := randomHex(16)
	if err != nil {
		return reviewDomain.Suggestion{}, err
	}
	suggestion := reviewDomain.Suggestion{ID: id, MemberID: in.MemberID, Kind: in.Kind, TargetURL: target.String(), Text: text, Context: postContext, CreatedAt: now}
	if err := s.reviews.CreateSuggestion(ctx, suggestion); err != nil {
		return reviewDomain.Suggestion{}, err
	}
	first := []telegramDomain.Button{{Text: "Open post", URL: suggestion.TargetURL}}
	if utf8.RuneCountInString(text) <= reviewDomain.CopyTextLimit {
		first = append(first, telegramDomain.Button{Text: "Copy comment", CopyText: text})
	}
	ratings := make([]telegramDomain.Button, 0, 5)
	for rating := 1; rating <= 5; rating++ {
		ratings = append(ratings, telegramDomain.Button{Text: strconv.Itoa(rating), CallbackData: "sg:" + id + ":" + strconv.Itoa(rating)})
	}
	body := suggestionText(suggestion)
	if utf16Len(body+suggestionRateLine) <= telegramMessageLimit {
		body += suggestionRateLine
	}
	message := telegramDomain.OutgoingMessage{Text: body, Buttons: [][]telegramDomain.Button{
		first, ratings, {{Text: "Posted", CallbackData: "sg:" + id + ":p"}},
	}}
	if _, err := s.notifier.Notify(ctx, in.MemberID, message, nil); err != nil {
		if errors.Is(err, telegramDomain.ErrNotLinked) {
			return reviewDomain.Suggestion{}, reviewDomain.ErrNotLinked
		}
		return reviewDomain.Suggestion{}, fmt.Errorf("send Telegram suggestion: %w", err)
	}
	return suggestion, nil
}

const suggestionRateLine = "\n\nRate the draft 1 (bad) to 5 (posted as is):"

// suggestionText is the comment idea message without the rating line.
func suggestionText(s reviewDomain.Suggestion) string {
	head := "Comment idea"
	if s.Context != "" {
		head += "\n" + s.Context
	}
	return fitTelegram(head+"\n\n", s.Text, "")
}

// SuggestionCallbacks stores a rating ("sg:<id>:1".."5") or the posted flag ("sg:<id>:p"); Posted marks the message.
type SuggestionCallbacks struct {
	reviews reviewDomain.Repository
	bot     telegramDomain.Gate
}

var _ telegramDomain.CallbackHandler = (*SuggestionCallbacks)(nil)

func NewSuggestionCallbacks(reviews reviewDomain.Repository, bot telegramDomain.Gate) (*SuggestionCallbacks, error) {
	if reviews == nil || bot == nil {
		return nil, fmt.Errorf("suggestion callbacks: review repository and bot gate are required")
	}
	return &SuggestionCallbacks{reviews: reviews, bot: bot}, nil
}

func (s *SuggestionCallbacks) Prefix() string { return "sg:" }

func (s *SuggestionCallbacks) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(callback.Data, ":")
	if len(parts) != 3 {
		return "This button is no longer active.", nil
	}
	now := time.Now().UTC()
	if parts[2] == "p" {
		suggestion, err := s.reviews.MarkSuggestionPosted(ctx, link.MemberID, parts[1], now)
		if err != nil {
			return "This suggestion is no longer active.", nil
		}
		if err := s.bot.EditMessageText(ctx, callback.ChatID, callback.MessageID, fitTelegram("✅ Posted\n\n", suggestionText(suggestion), "")); err != nil {
			logger.FromContext(ctx).WithError(err).Info("telegram_edit_suggestion_failed")
		}
		return "Marked as posted.", nil
	}
	rating, err := strconv.Atoi(parts[2])
	if err != nil || rating < 1 || rating > 5 {
		return "This button is no longer active.", nil
	}
	if err := s.reviews.RateSuggestion(ctx, link.MemberID, parts[1], rating, now); err != nil {
		return "This suggestion is no longer active.", nil
	}
	return "Rated " + parts[2] + " of 5.", nil
}

// ListFeedback returns what happened to notified posts (with the member's edits) and suggestion ratings, so the AI app can learn.
type ListFeedback struct {
	access  entitlementDomain.AccessStory
	reviews reviewDomain.Repository
	drafts  draftDomain.Repository
	jobs    scheduledDomain.Repository
}

func NewListFeedback(access entitlementDomain.AccessStory, reviews reviewDomain.Repository, drafts draftDomain.Repository, jobs scheduledDomain.Repository) (*ListFeedback, error) {
	if access == nil || reviews == nil || drafts == nil || jobs == nil {
		return nil, fmt.Errorf("list feedback: access, reviews, drafts and schedules are required")
	}
	return &ListFeedback{access: access, reviews: reviews, drafts: drafts, jobs: jobs}, nil
}

func (s *ListFeedback) List(ctx context.Context, memberID string, since time.Time) (reviewDomain.Feedback, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Feedback{}, err
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-reviewDomain.FeedbackWindow)
	}
	notices, err := s.reviews.ListNotices(ctx, memberID, since)
	if err != nil {
		return reviewDomain.Feedback{}, err
	}
	suggestions, err := s.reviews.ListSuggestions(ctx, memberID, since)
	if err != nil {
		return reviewDomain.Feedback{}, err
	}
	out := reviewDomain.Feedback{Posts: make([]reviewDomain.PostFeedback, 0, len(notices)), Suggestions: suggestions}
	for _, notice := range notices {
		item := reviewDomain.PostFeedback{Notice: notice, PostStatus: s.postStatus(ctx, notice)}
		if draft, err := s.drafts.ByID(ctx, memberID, notice.DraftID); err == nil {
			item.FinalText = draft.Body
			item.Edited = draft.Body != notice.ProposedText
			if item.Edited {
				item.Diff = reviewDomain.WordDiff(notice.ProposedText, draft.Body)
			}
		}
		out.Posts = append(out.Posts, item)
	}
	return out, nil
}

// postStatus is "skipped", "scheduled" while the post is still waiting, or the schedule's final status.
func (s *ListFeedback) postStatus(ctx context.Context, notice reviewDomain.Notice) string {
	if notice.Status == reviewDomain.NoticeSkipped {
		return string(reviewDomain.NoticeSkipped)
	}
	if notice.ScheduleID == "" {
		return "draft"
	}
	job, err := s.jobs.ByID(ctx, notice.MemberID, notice.ScheduleID)
	if err != nil {
		return "unknown"
	}
	switch job.Status {
	case scheduledDomain.Pending, scheduledDomain.Retry, scheduledDomain.Processing:
		return "scheduled"
	default:
		return string(job.Status)
	}
}

func noticeText(header string, notice reviewDomain.Notice) string {
	head := header
	if notice.ScheduleID != "" {
		head += "\nGoes out: " + formatPostTime(notice.ScheduledAt)
	}
	if notice.Note != "" {
		head += "\nNote: " + notice.Note
	}
	return fitTelegram(head+"\n\n", notice.ProposedText, "\n\n(Shortened here; open Bloomary for the full text.)")
}

// fitTelegram appends body to head, cutting body (and adding suffix) so the message fits Telegram's limit.
func fitTelegram(head, body, suffix string) string {
	if utf16Len(head)+utf16Len(body) <= telegramMessageLimit {
		return head + body
	}
	room := telegramMessageLimit - utf16Len(head) - utf16Len(suffix)
	used := 0
	var cut strings.Builder
	for _, r := range body {
		size := utf16.RuneLen(r)
		if size < 0 {
			size = 1
		}
		if used+size > room {
			break
		}
		used += size
		cut.WriteRune(r)
	}
	return head + cut.String() + suffix
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// formatPostTime shows a post time as /planned does: in the member's post time zone (Cyprus).
func formatPostTime(t time.Time) string {
	return t.In(postTimeZone).Format("Mon 2 Jan, 15:04") + " (Cyprus)"
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
