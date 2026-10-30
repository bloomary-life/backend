package stories

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
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
	return s.send(ctx, memberID, draft, job, note, "", false)
}

// NotifyDraft sends the preview of an unscheduled, unpublished draft (draft-only mode): no time and no Skip.
func (s *NotifyScheduled) NotifyDraft(ctx context.Context, memberID, draftID, note, firstComment string) (reviewDomain.Notice, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Notice{}, err
	}
	if utf8.RuneCountInString(note) > reviewDomain.MaxNoteRunes {
		return reviewDomain.Notice{}, fmt.Errorf("note is longer than %d characters", reviewDomain.MaxNoteRunes)
	}
	if firstComment = strings.TrimSpace(firstComment); utf8.RuneCountInString(firstComment) > reviewDomain.MaxFirstCommentRunes {
		return reviewDomain.Notice{}, fmt.Errorf("first_comment is longer than %d characters", reviewDomain.MaxFirstCommentRunes)
	}
	draft, err := s.drafts.ByID(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	if draft.Status != "draft" {
		return reviewDomain.Notice{}, ErrMCPDraftNotEditable
	}
	return s.send(ctx, memberID, draft, scheduledDomain.Post{}, note, firstComment, false)
}

// PreviewDraft sends again the draft of one of the member's draft previews on request in the bot (My posts → Preview).
// A preview replaced by a newer one still works (a second tap on the same button); a discarded one does not.
func (s *NotifyScheduled) PreviewDraft(ctx context.Context, memberID, noticeID string) (reviewDomain.Notice, error) {
	notice, err := s.reviews.NoticeByID(ctx, memberID, noticeID)
	if err != nil || notice.Status == reviewDomain.NoticeSkipped {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	draft, err := s.drafts.ByID(ctx, memberID, notice.DraftID)
	if err != nil || draft.Status != "draft" {
		return reviewDomain.Notice{}, ErrMCPDraftNotFound
	}
	return s.send(ctx, memberID, draft, scheduledDomain.Post{}, "", "", true)
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
	return s.send(ctx, memberID, draft, job, "", "", true)
}

// send stores a notice and sends the preview; a reply to the preview message (or its image) becomes an edit request.
// requested previews (the member's Preview taps) count against their own daily limit, not the AI app's.
func (s *NotifyScheduled) send(ctx context.Context, memberID string, draft draftDomain.Draft, job scheduledDomain.Post, note, firstComment string, requested bool) (reviewDomain.Notice, error) {
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
	note = strings.TrimSpace(note)
	if job.ID == "" && (firstComment == "" || note == "") {
		firstComment, note = s.earlierDraftPreview(ctx, memberID, draft.DraftID, now, firstComment, note)
	}
	notice := reviewDomain.Notice{ID: id, MemberID: memberID, DraftID: draft.DraftID, ScheduleID: job.ID, ScheduledAt: job.ScheduledAt,
		ProposedText: draft.Body, Note: note, FirstComment: firstComment, Status: reviewDomain.NoticeSent, CreatedAt: now, Requested: requested}
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
	text, buttons := noticeText(header, notice), [][]telegramDomain.Button{first}
	if firstComment != "" {
		text = noticeTextWithTail(header, notice, firstCommentBlock+firstComment)
		if utf8.RuneCountInString(firstComment) <= reviewDomain.CopyTextLimit {
			buttons = append(buttons, []telegramDomain.Button{{Text: "Copy first comment", CopyText: firstComment}})
		}
	}
	buttons = append(buttons, []telegramDomain.Button{{Text: "Edit in Bloomary", URL: s.platformURL + "/editor?draft=" + url.QueryEscape(draft.DraftID)}})
	message := telegramDomain.OutgoingMessage{Text: text, Buttons: buttons}
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
	s.replaceOlder(ctx, notice)
	return notice, nil
}

// earlierDraftPreview fills an empty first comment and note from the draft's newest earlier preview that has them, so the
// links Kat pastes under the post never vanish when an edit or a My posts tap replaces the preview that showed them.
func (s *NotifyScheduled) earlierDraftPreview(ctx context.Context, memberID, draftID string, now time.Time, firstComment, note string) (string, string) {
	notices, err := s.reviews.ListNotices(ctx, memberID, now.Add(-reviewDomain.FeedbackWindow))
	if err != nil {
		logger.FromContext(ctx).WithError(err).Info("telegram_list_previews_failed")
		return firstComment, note
	}
	for _, older := range notices {
		if older.DraftID != draftID || older.ScheduleID != "" {
			continue
		}
		if firstComment == "" {
			firstComment = older.FirstComment
		}
		if note == "" {
			note = older.Note
		}
	}
	return firstComment, note
}

const replacedPreview = "Replaced by a newer preview below."

// replaceOlder turns the draft's earlier previews into "Replaced by a newer preview below." without buttons,
// so only the newest one can be skipped or discarded. Failures are logged: the new preview already went out.
func (s *NotifyScheduled) replaceOlder(ctx context.Context, current reviewDomain.Notice) {
	notices, err := s.reviews.ListNotices(ctx, current.MemberID, current.CreatedAt.Add(-reviewDomain.FeedbackWindow))
	if err != nil {
		logger.FromContext(ctx).WithError(err).Info("telegram_list_previews_failed")
		return
	}
	for _, older := range notices {
		if older.ID == current.ID || older.DraftID != current.DraftID || older.Status != reviewDomain.NoticeSent || older.MessageID == 0 {
			continue
		}
		if err := s.reviews.ReplaceNotice(ctx, current.MemberID, older.ID); err != nil {
			continue
		}
		if err := s.notifier.Replace(ctx, current.MemberID, older.MessageID, replacedPreview); err != nil {
			logger.FromContext(ctx).WithError(err).Info("telegram_replace_preview_failed")
		}
	}
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
	result := "Discarded. No more edits from here; the draft is still in Bloomary → Drafts."
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
		if err := s.reviews.DeleteFirstComment(ctx, link.MemberID, notice.DraftID); err != nil {
			logger.FromContext(ctx).WithError(err).Info("first_comment_delete_failed")
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
	if in.Kind != reviewDomain.KindComment && in.Kind != reviewDomain.KindTrend {
		return reviewDomain.Suggestion{}, reviewDomain.ErrUnknownKind
	}
	// Comments target LinkedIn posts only; a trend links to its source, which can be any https site.
	target, err := url.Parse(strings.TrimSpace(in.TargetURL))
	if in.Kind == reviewDomain.KindTrend && (err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || net.ParseIP(target.Hostname()) != nil) {
		return reviewDomain.Suggestion{}, reviewDomain.ErrInvalidSource
	}
	if in.Kind == reviewDomain.KindComment && (err != nil || target.Scheme != "https" || (target.Host != "www.linkedin.com" && target.Host != "linkedin.com") || target.User != nil) {
		return reviewDomain.Suggestion{}, reviewDomain.ErrInvalidTarget
	}
	maxText := reviewDomain.MaxSuggestionRunes
	if in.Kind == reviewDomain.KindTrend {
		maxText = reviewDomain.MaxTrendRunes
	}
	text, postContext := strings.TrimSpace(in.Text), strings.TrimSpace(in.Context)
	if text == "" || utf8.RuneCountInString(text) > maxText || utf8.RuneCountInString(postContext) > reviewDomain.MaxContextRunes {
		return reviewDomain.Suggestion{}, fmt.Errorf("text must be 1-%d characters and context at most %d", maxText, reviewDomain.MaxContextRunes)
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
	if in.Kind == reviewDomain.KindTrend {
		return s.sendTrend(ctx, suggestion, in.Silent)
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
	message := telegramDomain.OutgoingMessage{Text: body, Silent: in.Silent, Buttons: [][]telegramDomain.Button{
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

const trendFooter = "\n\nReply to this message (voice or text) with your take and it becomes a post draft."

// sendTrend sends a story with an Open source button; the message ID is kept so a reply becomes an insight with the story.
func (s *SendSuggestion) sendTrend(ctx context.Context, suggestion reviewDomain.Suggestion, silent bool) (reviewDomain.Suggestion, error) {
	message := telegramDomain.OutgoingMessage{Text: fitTelegram("Story for today\n", suggestion.Text, "") + trendFooter,
		Buttons: [][]telegramDomain.Button{{{Text: "Open source", URL: suggestion.TargetURL}}}, Silent: silent}
	sent, err := s.notifier.Notify(ctx, suggestion.MemberID, message, nil)
	if err != nil {
		if errors.Is(err, telegramDomain.ErrNotLinked) {
			return reviewDomain.Suggestion{}, reviewDomain.ErrNotLinked
		}
		return reviewDomain.Suggestion{}, fmt.Errorf("send Telegram trend: %w", err)
	}
	if err := s.reviews.SetSuggestionMessage(ctx, suggestion.MemberID, suggestion.ID, sent.MessageID); err != nil {
		return reviewDomain.Suggestion{}, err
	}
	suggestion.MessageID = sent.MessageID
	return suggestion, nil
}

const suggestionRateLine = "\n\nRate this comment idea 1 (unusable) to 5 (good as is). Tap Posted once it's on LinkedIn."

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

// postStatus is "skipped", "replaced" (an older preview of a draft), "scheduled" while the post is still waiting,
// or the schedule's final status.
func (s *ListFeedback) postStatus(ctx context.Context, notice reviewDomain.Notice) string {
	if notice.Status == reviewDomain.NoticeSkipped || notice.Status == reviewDomain.NoticeReplaced {
		return string(notice.Status)
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

const firstCommentBlock = "\n\nFirst comment (paste under the post after you publish):\n"

// noticeTextWithTail is noticeText with tail (the first comment) kept whole after the post; the post is shortened instead.
func noticeTextWithTail(header string, notice reviewDomain.Notice, tail string) string {
	return noticeHead(header, notice, telegramMessageLimit-utf16Len(tail)) + tail
}

func noticeText(header string, notice reviewDomain.Notice) string {
	return noticeHead(header, notice, telegramMessageLimit)
}

func noticeHead(header string, notice reviewDomain.Notice, limit int) string {
	head := header
	if notice.ScheduleID != "" {
		head += "\nGoes out: " + formatPostTime(notice.ScheduledAt)
	}
	if notice.Note != "" {
		head += "\nNote: " + notice.Note
	}
	return fitTelegramLimit(head+"\n\n", notice.ProposedText, "\n\n(Shortened here; open Bloomary for the full text.)", limit)
}

// fitTelegram appends body to head, cutting body (and adding suffix) so the message fits Telegram's limit.
func fitTelegram(head, body, suffix string) string {
	return fitTelegramLimit(head, body, suffix, telegramMessageLimit)
}

func fitTelegramLimit(head, body, suffix string, limit int) string {
	if utf16Len(head)+utf16Len(body) <= limit {
		return head + body
	}
	room := limit - utf16Len(head) - utf16Len(suffix)
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
// formatPostTime shows a post time in postTimeZone with its UTC offset ("Tue 3 Nov, 10:00 (UTC+2)"), so a member
// in another zone can read it.
func formatPostTime(t time.Time) string {
	local := t.In(postTimeZone)
	_, offset := local.Zone()
	label := fmt.Sprintf("UTC%+d", offset/3600)
	if offset%3600 != 0 {
		label = "UTC" + local.Format("-07:00")
	}
	return local.Format("Mon 2 Jan, 15:04") + " (" + label + ")"
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
