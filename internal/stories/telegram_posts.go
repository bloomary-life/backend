package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
)

const (
	previewPrefix  = "pv:"
	editSaved      = "Got it — I'll rewrite the post and send you the new preview."
	editTooLate    = "Too late: this post is no longer scheduled or editable (published, skipped or cancelled)."
	editLimit      = "Daily edit limit reached. Edit the post in Bloomary instead."
	editHint       = "Reply to the post preview with a voice note or text: say what to change."
	plannedNone    = "No scheduled posts."
	plannedHeader  = "Scheduled posts (Cyprus time):"
	plannedRunes   = 60
	plannedMaxRows = 20
)

// postTimeZone is where the member reads post times in the bot (Kat, Cyprus); falls back to UTC.
var postTimeZone = loadZone("Europe/Nicosia")

func loadZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return zone
}

// EditReplies turns a member's reply to a post preview into an edit request for that draft.
type EditReplies struct {
	reviews reviewDomain.Repository
	jobs    scheduledDomain.Repository
	drafts  draftDomain.Repository
}

var _ telegramDomain.ReplyHandler = (*EditReplies)(nil)

func NewEditReplies(reviews reviewDomain.Repository, jobs scheduledDomain.Repository, drafts draftDomain.Repository) (*EditReplies, error) {
	if reviews == nil || jobs == nil || drafts == nil {
		return nil, fmt.Errorf("edit replies: reviews, schedules and drafts are required")
	}
	return &EditReplies{reviews: reviews, jobs: jobs, drafts: drafts}, nil
}

// Claim owns replies to the member's own previews; it refuses once the post is no longer scheduled or the daily limit is hit.
func (s *EditReplies) Claim(ctx context.Context, link telegramDomain.Link, replyToMessageID int64) (telegramDomain.ReplyClaim, bool, error) {
	notice, err := s.reviews.NoticeByMessage(ctx, link.MemberID, replyToMessageID)
	if errors.Is(err, reviewDomain.ErrNotFound) {
		return telegramDomain.ReplyClaim{}, false, nil
	}
	if err != nil {
		return telegramDomain.ReplyClaim{}, false, err
	}
	claim := telegramDomain.ReplyClaim{ID: notice.ID}
	if !s.editable(ctx, link.MemberID, notice) {
		claim.Refusal = editTooLate
		return claim, true, nil
	}
	count, err := s.reviews.CountEditRequestsSince(ctx, link.MemberID, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		return telegramDomain.ReplyClaim{}, false, err
	}
	if count >= reviewDomain.DailyEditLimit {
		claim.Refusal = editLimit
	}
	return claim, true, nil
}

// editable: a scheduled post until it starts going out or is skipped; a draft preview while the draft is unpublished.
func (s *EditReplies) editable(ctx context.Context, memberID string, notice reviewDomain.Notice) bool {
	if notice.Status == reviewDomain.NoticeSkipped {
		return false
	}
	if notice.ScheduleID == "" {
		draft, err := s.drafts.ByID(ctx, memberID, notice.DraftID)
		return err == nil && draft.Status == "draft"
	}
	job, err := s.jobs.ByID(ctx, memberID, notice.ScheduleID)
	return err == nil && stillScheduled(job.Status)
}

func (s *EditReplies) Save(ctx context.Context, link telegramDomain.Link, claim telegramDomain.ReplyClaim, text, source string) (string, error) {
	notice, err := s.reviews.NoticeByID(ctx, link.MemberID, claim.ID)
	if err != nil {
		return editTooLate, nil
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > reviewDomain.MaxEditRunes {
		text = string([]rune(text)[:reviewDomain.MaxEditRunes])
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	err = s.reviews.CreateEditRequest(ctx, reviewDomain.EditRequest{ID: id, MemberID: link.MemberID, DraftID: notice.DraftID, NoticeID: notice.ID,
		Text: text, Source: source, CreatedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	return editSaved, nil
}

// EditRequests lets the member's AI app list edit requests and mark them applied (MCP).
type EditRequests struct {
	access  entitlementDomain.AccessStory
	reviews reviewDomain.Repository
}

var _ reviewDomain.EditRequestsStory = (*EditRequests)(nil)

func NewEditRequests(access entitlementDomain.AccessStory, reviews reviewDomain.Repository) (*EditRequests, error) {
	if access == nil || reviews == nil {
		return nil, fmt.Errorf("edit requests: access and reviews are required")
	}
	return &EditRequests{access: access, reviews: reviews}, nil
}

func (s *EditRequests) List(ctx context.Context, memberID string, unappliedOnly bool) ([]reviewDomain.EditRequest, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return nil, err
	}
	return s.reviews.ListEditRequests(ctx, memberID, unappliedOnly)
}

func (s *EditRequests) MarkApplied(ctx context.Context, memberID, id string) (reviewDomain.EditRequest, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return reviewDomain.EditRequest{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return reviewDomain.EditRequest{}, errors.New("edit_id is required")
	}
	return s.reviews.MarkEditApplied(ctx, memberID, id, time.Now().UTC())
}

// PlannedPosts answers /planned (and its keyboard button) with the member's scheduled posts, each with a Preview button.
type PlannedPosts struct {
	jobs    scheduledDomain.Repository
	drafts  draftDomain.Repository
	notify  *NotifyScheduled
	bot     telegramDomain.Gate
	command string
}

var (
	_ telegramDomain.CommandHandler  = (*PlannedPosts)(nil)
	_ telegramDomain.CallbackHandler = (*PlannedPosts)(nil)
)

func NewPlannedPosts(jobs scheduledDomain.Repository, drafts draftDomain.Repository, notify *NotifyScheduled, bot telegramDomain.Gate) (*PlannedPosts, error) {
	if jobs == nil || drafts == nil || notify == nil || bot == nil {
		return nil, fmt.Errorf("planned posts: schedules, drafts, notify story and bot gate are required")
	}
	return &PlannedPosts{jobs: jobs, drafts: drafts, notify: notify, bot: bot, command: "/planned"}, nil
}

func (s *PlannedPosts) Command() string { return s.command }

func (s *PlannedPosts) HandleCommand(ctx context.Context, link telegramDomain.Link, chatID int64) error {
	jobs, err := s.jobs.ListActive(ctx, link.MemberID)
	if err != nil {
		return err
	}
	var lines []string
	var buttons [][]telegramDomain.Button
	for _, job := range jobs {
		if !stillScheduled(job.Status) || len(buttons) == plannedMaxRows {
			continue
		}
		first := "(draft not found)"
		if draft, err := s.drafts.ByID(ctx, link.MemberID, job.DraftID); err == nil {
			first = firstLine(draft.Body)
		}
		when := job.ScheduledAt.In(postTimeZone).Format("Mon 2 Jan, 15:04")
		lines = append(lines, fmt.Sprintf("%d. %s — %s", len(buttons)+1, when, first))
		buttons = append(buttons, []telegramDomain.Button{{Text: fmt.Sprintf("Preview %d · %s", len(buttons)+1, when), CallbackData: previewPrefix + job.ID}})
	}
	if len(lines) == 0 {
		_, err = s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: plannedNone, Keyboard: botKeyboard})
		return err
	}
	_, err = s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: plannedHeader + "\n\n" + strings.Join(lines, "\n"), Buttons: buttons})
	return err
}

func (s *PlannedPosts) Prefix() string { return previewPrefix }

// HandleCallback sends the preview for "pv:<schedule id>"; "pv:e" (Edit by voice) explains how to reply.
func (s *PlannedPosts) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	id := strings.TrimPrefix(callback.Data, previewPrefix)
	if id == "e" {
		return editHint, nil
	}
	_, err := s.notify.Preview(ctx, link.MemberID, id)
	switch {
	case errors.Is(err, reviewDomain.ErrNotScheduled), errors.Is(err, ErrMCPDraftNotFound):
		return "This post is no longer scheduled.", nil
	case errors.Is(err, reviewDomain.ErrDailyLimit):
		return "Daily preview limit reached. Open Bloomary to see the post.", nil
	case err != nil:
		return "", err
	}
	return "", nil
}

func firstLine(body string) string {
	line := strings.TrimSpace(body)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if utf8.RuneCountInString(line) > plannedRunes {
		line = string([]rune(line)[:plannedRunes]) + "…"
	}
	return line
}
