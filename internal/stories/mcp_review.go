package stories

import (
	"context"
	"crypto/rand"
	"database/sql"
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

// Telegram counts message length in UTF-16 code units; a message holds at most 4096.
const telegramMessageLimit = 4096

// ScheduleMCPDraft schedules through the regular schedule story, but always held for approval.
type ScheduleMCPDraft struct {
	schedules scheduledDomain.Story
	reviews   reviewDomain.Repository
}

func NewScheduleMCPDraft(schedules scheduledDomain.Story, reviews reviewDomain.Repository) (*ScheduleMCPDraft, error) {
	if schedules == nil || reviews == nil {
		return nil, fmt.Errorf("schedule MCP draft: schedule story and review repository are required")
	}
	return &ScheduleMCPDraft{schedules: schedules, reviews: reviews}, nil
}

func (s *ScheduleMCPDraft) Schedule(ctx context.Context, memberID, draftID string, publishAt time.Time) (scheduledDomain.Post, error) {
	post, err := s.schedules.Schedule(ctx, scheduledDomain.CreateInput{MemberID: memberID, DraftID: draftID, ScheduledAt: publishAt.UTC(), NeedsApproval: true})
	if err != nil {
		return scheduledDomain.Post{}, err
	}
	// A new time makes earlier previews of this draft stale.
	return post, s.reviews.SupersedePending(ctx, memberID, draftID, time.Now().UTC())
}

// MCPDraftChangeHold holds a draft's schedule again whenever the AI app changes its text or image.
type MCPDraftChangeHold struct {
	jobs    scheduledDomain.Repository
	reviews reviewDomain.Repository
}

func NewMCPDraftChangeHold(jobs scheduledDomain.Repository, reviews reviewDomain.Repository) (*MCPDraftChangeHold, error) {
	if jobs == nil || reviews == nil {
		return nil, fmt.Errorf("MCP draft change hold: schedule and review repositories are required")
	}
	return &MCPDraftChangeHold{jobs: jobs, reviews: reviews}, nil
}

func (s *MCPDraftChangeHold) HoldForApproval(ctx context.Context, memberID, draftID string) error {
	if err := s.jobs.HoldForApproval(ctx, memberID, draftID); err != nil {
		return err
	}
	return s.reviews.SupersedePending(ctx, memberID, draftID, time.Now().UTC())
}

// HoldOnMCPUpdate wraps update_draft so an approved schedule never posts text the member did not see.
type HoldOnMCPUpdate struct {
	inner draftDomain.UpdateMCPDraftStory
	hold  reviewDomain.DraftChangeHold
}

func NewHoldOnMCPUpdate(inner draftDomain.UpdateMCPDraftStory, hold reviewDomain.DraftChangeHold) (*HoldOnMCPUpdate, error) {
	if inner == nil || hold == nil {
		return nil, fmt.Errorf("hold on MCP update: story and hold are required")
	}
	return &HoldOnMCPUpdate{inner: inner, hold: hold}, nil
}

func (s *HoldOnMCPUpdate) Update(ctx context.Context, memberID, draftID, body string) (draftDomain.Draft, error) {
	draft, err := s.inner.Update(ctx, memberID, draftID, body)
	if err != nil {
		return draft, err
	}
	return draft, s.hold.HoldForApproval(ctx, memberID, draftID)
}

// HoldOnMCPAttach wraps attach_image the same way as HoldOnMCPUpdate.
type HoldOnMCPAttach struct {
	inner draftDomain.AttachMCPImageStory
	hold  reviewDomain.DraftChangeHold
}

func NewHoldOnMCPAttach(inner draftDomain.AttachMCPImageStory, hold reviewDomain.DraftChangeHold) (*HoldOnMCPAttach, error) {
	if inner == nil || hold == nil {
		return nil, fmt.Errorf("hold on MCP attach: story and hold are required")
	}
	return &HoldOnMCPAttach{inner: inner, hold: hold}, nil
}

func (s *HoldOnMCPAttach) Attach(ctx context.Context, in draftDomain.AttachImageInput) (draftDomain.Draft, error) {
	draft, err := s.inner.Attach(ctx, in)
	if err != nil {
		return draft, err
	}
	return draft, s.hold.HoldForApproval(ctx, in.MemberID, in.DraftID)
}

// RequestApproval sends the member a Telegram preview of a held schedule with Approve, Skip and Edit.
type RequestApproval struct {
	access      entitlementDomain.AccessStory
	drafts      draftDomain.Repository
	jobs        scheduledDomain.Repository
	reviews     reviewDomain.Repository
	objects     draftDomain.ObjectStore
	notifier    telegramDomain.Notifier
	platformURL string
}

// NewRequestApproval builds the story; objects may be nil when draft images are not configured.
func NewRequestApproval(access entitlementDomain.AccessStory, drafts draftDomain.Repository, jobs scheduledDomain.Repository, reviews reviewDomain.Repository,
	objects draftDomain.ObjectStore, notifier telegramDomain.Notifier, platformURL string) (*RequestApproval, error) {
	if access == nil || drafts == nil || jobs == nil || reviews == nil || notifier == nil {
		return nil, fmt.Errorf("request approval: access, drafts, schedules, reviews and notifier are required")
	}
	return &RequestApproval{access: access, drafts: drafts, jobs: jobs, reviews: reviews, objects: objects, notifier: notifier, platformURL: strings.TrimRight(platformURL, "/")}, nil
}

func (s *RequestApproval) Request(ctx context.Context, memberID, draftID, note string) (reviewDomain.Approval, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Approval{}, err
	}
	if utf8.RuneCountInString(note) > reviewDomain.MaxNoteRunes {
		return reviewDomain.Approval{}, fmt.Errorf("note is longer than %d characters", reviewDomain.MaxNoteRunes)
	}
	draft, err := s.drafts.ByID(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Approval{}, ErrMCPDraftNotFound
	}
	job, err := s.heldSchedule(ctx, memberID, draftID)
	if err != nil {
		return reviewDomain.Approval{}, err
	}
	now := time.Now().UTC()
	count, err := s.reviews.CountApprovalsSince(ctx, memberID, now.Add(-24*time.Hour))
	if err != nil {
		return reviewDomain.Approval{}, err
	}
	if count >= reviewDomain.DailyApprovalLimit {
		return reviewDomain.Approval{}, reviewDomain.ErrDailyLimit
	}
	if err := s.reviews.SupersedePending(ctx, memberID, draftID, now); err != nil {
		return reviewDomain.Approval{}, err
	}
	id, err := randomHex(16)
	if err != nil {
		return reviewDomain.Approval{}, err
	}
	approval := reviewDomain.Approval{ID: id, MemberID: memberID, DraftID: draftID, ScheduleID: job.ID, ScheduledAt: job.ScheduledAt,
		ProposedText: draft.Body, Note: strings.TrimSpace(note), Status: reviewDomain.ApprovalPending, CreatedAt: now}
	if err := s.reviews.CreateApproval(ctx, approval); err != nil {
		return reviewDomain.Approval{}, err
	}
	var photo *telegramDomain.Photo
	if draft.Image != nil && s.objects != nil {
		data, err := s.objects.Read(ctx, draft.Image.ObjectKey)
		if err != nil {
			return reviewDomain.Approval{}, fmt.Errorf("read draft image: %w", err)
		}
		photo = &telegramDomain.Photo{Data: data, MimeType: draft.Image.MimeType, Caption: "Image for the post below"}
	}
	message := telegramDomain.OutgoingMessage{Text: approvalText("Approve this LinkedIn post?", approval), Buttons: [][]telegramDomain.Button{
		{{Text: "Approve", CallbackData: "ap:" + id + ":y"}, {Text: "Skip", CallbackData: "ap:" + id + ":n"}},
		{{Text: "Edit in Bloomary", URL: s.platformURL + "/editor?draft=" + url.QueryEscape(draftID)}},
	}}
	if _, err := s.notifier.Notify(ctx, memberID, message, photo); err != nil {
		_, _ = s.reviews.DecideApproval(ctx, memberID, id, reviewDomain.ApprovalSuperseded, now)
		if errors.Is(err, telegramDomain.ErrNotLinked) {
			return reviewDomain.Approval{}, reviewDomain.ErrNotLinked
		}
		return reviewDomain.Approval{}, fmt.Errorf("send Telegram preview: %w", err)
	}
	return approval, nil
}

func (s *RequestApproval) heldSchedule(ctx context.Context, memberID, draftID string) (scheduledDomain.Post, error) {
	jobs, err := s.jobs.ListActive(ctx, memberID)
	if err != nil {
		return scheduledDomain.Post{}, err
	}
	for _, job := range jobs {
		if job.DraftID == draftID && job.Status == scheduledDomain.Pending {
			return job, nil
		}
	}
	return scheduledDomain.Post{}, reviewDomain.ErrNotScheduled
}

// ApprovalCallbacks handles Approve ("ap:<id>:y") and Skip ("ap:<id>:n") under a preview.
type ApprovalCallbacks struct {
	reviews reviewDomain.Repository
	jobs    scheduledDomain.Repository
	bot     telegramDomain.Gate
}

var _ telegramDomain.CallbackHandler = (*ApprovalCallbacks)(nil)

func NewApprovalCallbacks(reviews reviewDomain.Repository, jobs scheduledDomain.Repository, bot telegramDomain.Gate) (*ApprovalCallbacks, error) {
	if reviews == nil || jobs == nil || bot == nil {
		return nil, fmt.Errorf("approval callbacks: reviews, schedules and bot gate are required")
	}
	return &ApprovalCallbacks{reviews: reviews, jobs: jobs, bot: bot}, nil
}

func (s *ApprovalCallbacks) Prefix() string { return "ap:" }

func (s *ApprovalCallbacks) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(callback.Data, ":")
	if len(parts) != 3 || (parts[2] != "y" && parts[2] != "n") {
		return "This button is no longer active.", nil
	}
	approval, err := s.reviews.ApprovalByID(ctx, link.MemberID, parts[1])
	if err != nil {
		return "This preview is no longer active.", nil
	}
	switch approval.Status {
	case reviewDomain.ApprovalPending:
	case reviewDomain.ApprovalSuperseded:
		return "The draft changed after this preview. Use the newest preview.", nil
	default:
		return "Already " + string(approval.Status) + ".", nil
	}
	now := time.Now().UTC()
	if parts[2] == "n" {
		if err := s.jobs.Cancel(ctx, link.MemberID, approval.DraftID); err != nil {
			return "", err
		}
		return s.decide(ctx, link, callback, approval, reviewDomain.ApprovalSkipped, "Skipped: the schedule is cancelled, the draft stays in Bloomary.")
	}
	job, err := s.jobs.ByID(ctx, link.MemberID, approval.ScheduleID)
	if err != nil || job.Status != scheduledDomain.Pending {
		return "This schedule is no longer active. Open Bloomary to schedule it again.", nil
	}
	if !job.ScheduledAt.After(now) {
		return "The scheduled time has passed. Open Bloomary to pick a new time.", nil
	}
	if err := s.jobs.Approve(ctx, link.MemberID, approval.ScheduleID); errors.Is(err, sql.ErrNoRows) {
		return "This schedule is no longer active. Open Bloomary to schedule it again.", nil
	} else if err != nil {
		return "", err
	}
	return s.decide(ctx, link, callback, approval, reviewDomain.ApprovalApproved, "Approved: posts "+formatPostTime(job.ScheduledAt)+".")
}

func (s *ApprovalCallbacks) decide(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback, approval reviewDomain.Approval, status reviewDomain.ApprovalStatus, result string) (string, error) {
	if _, err := s.reviews.DecideApproval(ctx, link.MemberID, approval.ID, status, time.Now().UTC()); err != nil {
		return "", err
	}
	if err := s.bot.EditMessageText(ctx, callback.ChatID, callback.MessageID, approvalText(result, approval)); err != nil {
		logger.FromContext(ctx).WithError(err).Info("telegram_edit_approval_failed")
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
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" || target.User != nil {
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
	body := "Comment idea"
	if postContext != "" {
		body += "\n" + postContext
	}
	message := telegramDomain.OutgoingMessage{Text: fitTelegram(body+"\n\n", text, ""), Buttons: [][]telegramDomain.Button{
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

// SuggestionCallbacks stores a rating ("sg:<id>:1".."5") or the posted flag ("sg:<id>:p").
type SuggestionCallbacks struct {
	reviews reviewDomain.Repository
}

var _ telegramDomain.CallbackHandler = (*SuggestionCallbacks)(nil)

func NewSuggestionCallbacks(reviews reviewDomain.Repository) (*SuggestionCallbacks, error) {
	if reviews == nil {
		return nil, fmt.Errorf("suggestion callbacks: review repository is required")
	}
	return &SuggestionCallbacks{reviews: reviews}, nil
}

func (s *SuggestionCallbacks) Prefix() string { return "sg:" }

func (s *SuggestionCallbacks) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	parts := strings.Split(callback.Data, ":")
	if len(parts) != 3 {
		return "This button is no longer active.", nil
	}
	now := time.Now().UTC()
	if parts[2] == "p" {
		if err := s.reviews.MarkSuggestionPosted(ctx, link.MemberID, parts[1], now); err != nil {
			return "This suggestion is no longer active.", nil
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

// ListFeedback returns approvals with the member's edits and suggestion ratings, so the AI app can learn.
type ListFeedback struct {
	access  entitlementDomain.AccessStory
	reviews reviewDomain.Repository
	drafts  draftDomain.Repository
}

func NewListFeedback(access entitlementDomain.AccessStory, reviews reviewDomain.Repository, drafts draftDomain.Repository) (*ListFeedback, error) {
	if access == nil || reviews == nil || drafts == nil {
		return nil, fmt.Errorf("list feedback: access, reviews and drafts are required")
	}
	return &ListFeedback{access: access, reviews: reviews, drafts: drafts}, nil
}

func (s *ListFeedback) List(ctx context.Context, memberID string, since time.Time) (reviewDomain.Feedback, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.Feedback{}, err
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-reviewDomain.FeedbackWindow)
	}
	approvals, err := s.reviews.ListApprovals(ctx, memberID, since)
	if err != nil {
		return reviewDomain.Feedback{}, err
	}
	suggestions, err := s.reviews.ListSuggestions(ctx, memberID, since)
	if err != nil {
		return reviewDomain.Feedback{}, err
	}
	out := reviewDomain.Feedback{Approvals: make([]reviewDomain.ApprovalFeedback, 0, len(approvals)), Suggestions: suggestions}
	for _, approval := range approvals {
		item := reviewDomain.ApprovalFeedback{Approval: approval}
		if approval.Status == reviewDomain.ApprovalApproved {
			if draft, err := s.drafts.ByID(ctx, memberID, approval.DraftID); err == nil {
				item.FinalText = draft.Body
				item.Edited = draft.Body != approval.ProposedText
				if item.Edited {
					item.Diff = reviewDomain.WordDiff(approval.ProposedText, draft.Body)
				}
			}
		}
		out.Approvals = append(out.Approvals, item)
	}
	return out, nil
}

func approvalText(header string, approval reviewDomain.Approval) string {
	head := header + "\nScheduled: " + formatPostTime(approval.ScheduledAt)
	if approval.Note != "" {
		head += "\nNote: " + approval.Note
	}
	return fitTelegram(head+"\n\n", approval.ProposedText, "\n\n(Shortened here; open Bloomary for the full text.)")
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

func formatPostTime(t time.Time) string {
	return t.UTC().Format("Mon 2 Jan 2006, 15:04") + " UTC"
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
