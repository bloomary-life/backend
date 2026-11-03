package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
)

const (
	previewPrefix   = "pv:"
	editSavedTail   = "When your AI app rewrites the post, the new preview comes here."
	editSaved       = "Got it. " + editSavedTail
	editTooLate     = "This post can't be changed any more: it was published, skipped, discarded or deleted."
	editLimit       = "Daily edit limit reached. Edit the post in Bloomary instead."
	editHowTo       = "Reply to this message with a voice note or text and say what to change."
	plannedNone     = "No scheduled posts and no drafts previewed in the last 7 days. All drafts: Bloomary → Drafts."
	plannedHeader   = "Scheduled posts:"
	plannedNetworks = "Posts to other networks:"
	draftsHeader    = "Drafts (not scheduled):"
	draftsWindow    = 7 * 24 * time.Hour
	plannedRunes    = 60
	plannedMaxRows  = 20
)

// postTimeZone is the zone the bot shows post times in (Cyprus for every member until members can set their own); falls back to UTC.
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
	// Message IDs are per chat: a preview sent before the current link may share its ID with a message in a new chat.
	if notice.CreatedAt.Before(link.LinkedAt) {
		return telegramDomain.ReplyClaim{}, false, nil
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
	if source == "voice" {
		return "Got it: “" + snippet(text) + "”\n\n" + editSavedTail, nil
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

// PlannedPosts answers /planned (and the 📅 My posts button) with the member's scheduled posts and the drafts
// previewed in the last 7 days that are still unpublished, each with a Preview button.
type PlannedPosts struct {
	jobs    scheduledDomain.Repository
	drafts  draftDomain.Repository
	notify  *NotifyScheduled
	bot     telegramDomain.Gate
	command string
	// publications lists posts to other networks (nil while cross-posting is off).
	publications crosspostDomain.ListPublicationsStory
}

var (
	_ telegramDomain.CommandHandler  = (*PlannedPosts)(nil)
	_ telegramDomain.CallbackHandler = (*PlannedPosts)(nil)
)

// NewPlannedPosts: publications is nil while cross-posting is off (then /planned lists LinkedIn only, as before).
func NewPlannedPosts(jobs scheduledDomain.Repository, drafts draftDomain.Repository, notify *NotifyScheduled, bot telegramDomain.Gate,
	publications crosspostDomain.ListPublicationsStory) (*PlannedPosts, error) {
	if jobs == nil || drafts == nil || notify == nil || bot == nil {
		return nil, fmt.Errorf("planned posts: schedules, drafts, notify story and bot gate are required")
	}
	return &PlannedPosts{jobs: jobs, drafts: drafts, notify: notify, bot: bot, command: "/planned", publications: publications}, nil
}

func (s *PlannedPosts) Command() string { return s.command }

func (s *PlannedPosts) HandleCommand(ctx context.Context, link telegramDomain.Link, chatID int64) error {
	jobs, err := s.jobs.ListActive(ctx, link.MemberID)
	if err != nil {
		return err
	}
	var lines []string
	var buttons [][]telegramDomain.Button
	publicationLines, inPublication, err := s.publicationRows(ctx, link.MemberID, &buttons)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if !stillScheduled(job.Status) || len(buttons) == plannedMaxRows || inPublication[job.ID] {
			continue
		}
		first := "(draft not found)"
		if draft, err := s.drafts.ByID(ctx, link.MemberID, job.DraftID); err == nil {
			first = firstLine(draft.Body)
		}
		when := formatPostTime(job.ScheduledAt)
		lines = append(lines, fmt.Sprintf("%d. %s — %s", len(buttons)+1, when, first))
		buttons = append(buttons, []telegramDomain.Button{{Text: fmt.Sprintf("Preview %d · %s", len(buttons)+1, job.ScheduledAt.In(postTimeZone).Format("Mon 2 Jan, 15:04")), CallbackData: previewPrefix + job.ID}})
	}
	var sections []string
	if len(lines) > 0 {
		sections = append(sections, plannedHeader+"\n"+strings.Join(lines, "\n"))
	}
	if len(publicationLines) > 0 {
		sections = append(sections, plannedNetworks+"\n"+strings.Join(publicationLines, "\n"))
	}
	draftLines, err := s.recentDrafts(ctx, link.MemberID, &buttons)
	if err != nil {
		return err
	}
	if len(draftLines) > 0 {
		sections = append(sections, draftsHeader+"\n"+strings.Join(draftLines, "\n"))
	}
	if len(sections) == 0 {
		_, err = s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: plannedNone,
			Buttons: [][]telegramDomain.Button{{{Text: "Open Drafts", URL: s.notify.platformURL + "/drafts"}}}})
		return err
	}
	_, err = s.bot.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: chatID, Text: strings.Join(sections, "\n\n"), Buttons: buttons})
	return err
}

// publicationRows lists open publications to other networks: "3. Wed 4 Nov — “first line” · X 13:00, YouTube 16:00"
// with a Status button; LinkedIn posts that belong to one are left out of the LinkedIn list (inPublication).
func (s *PlannedPosts) publicationRows(ctx context.Context, memberID string, buttons *[][]telegramDomain.Button) ([]string, map[string]bool, error) {
	inPublication := map[string]bool{}
	if s.publications == nil {
		return nil, inPublication, nil
	}
	views, err := s.publications.ListPublications(ctx, memberID)
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	for _, view := range views {
		if !view.Publication.ClosedAt.IsZero() || len(*buttons) == plannedMaxRows {
			continue
		}
		inPublication[view.Publication.LinkedInScheduleID] = true
		var times []string
		day := ""
		for _, d := range view.Deliveries {
			if d.Status != crosspostDomain.StatusScheduled {
				continue
			}
			local := d.PublishAt.In(postTimeZone)
			if day == "" {
				day = local.Format("Mon 2 Jan")
			}
			times = append(times, networkName(d.Network)+" "+local.Format("15:04"))
		}
		if len(times) == 0 {
			continue
		}
		first := "(draft not found)"
		if draft, err := s.drafts.ByID(ctx, memberID, view.Publication.DraftID); err == nil {
			first = firstLine(draft.Body)
		}
		n := len(*buttons) + 1
		lines = append(lines, fmt.Sprintf("%d. %s — “%s” · %s", n, day, first, strings.Join(times, ", ")))
		*buttons = append(*buttons, []telegramDomain.Button{{Text: fmt.Sprintf("Status %d", n), CallbackData: statusPrefix + "st:" + view.Publication.ID}})
	}
	delete(inPublication, "")
	return lines, inPublication, nil
}

// recentDrafts lists the newest live draft preview per draft from the last 7 days whose draft is still unpublished.
func (s *PlannedPosts) recentDrafts(ctx context.Context, memberID string, buttons *[][]telegramDomain.Button) ([]string, error) {
	notices, err := s.notify.reviews.ListNotices(ctx, memberID, time.Now().UTC().Add(-draftsWindow))
	if err != nil {
		return nil, err
	}
	var lines []string
	seen := map[string]bool{}
	for _, notice := range notices {
		if notice.ScheduleID != "" || notice.Status != reviewDomain.NoticeSent || seen[notice.DraftID] || len(*buttons) == plannedMaxRows {
			continue
		}
		seen[notice.DraftID] = true
		draft, err := s.drafts.ByID(ctx, memberID, notice.DraftID)
		if err != nil || draft.Status != "draft" {
			continue
		}
		n := len(*buttons) + 1
		lines = append(lines, fmt.Sprintf("%d. %s", n, firstLine(draft.Body)))
		*buttons = append(*buttons, []telegramDomain.Button{{Text: fmt.Sprintf("Preview %d · draft", n), CallbackData: previewPrefix + "d:" + notice.ID}})
	}
	return lines, nil
}

func (s *PlannedPosts) Prefix() string { return previewPrefix }

// HandleCallback sends the preview for "pv:<schedule id>" or a draft's "pv:d:<notice id>"; "pv:e" (How to edit) explains how to reply.
func (s *PlannedPosts) HandleCallback(ctx context.Context, link telegramDomain.Link, callback telegramDomain.Callback) (string, error) {
	id := strings.TrimPrefix(callback.Data, previewPrefix)
	if id == "e" {
		return editHowTo, nil
	}
	var err error
	if noticeID, isDraft := strings.CutPrefix(id, "d:"); isDraft {
		_, err = s.notify.PreviewDraft(ctx, link.MemberID, noticeID)
	} else {
		_, err = s.notify.Preview(ctx, link.MemberID, id)
	}
	switch {
	case errors.Is(err, reviewDomain.ErrNotScheduled), errors.Is(err, ErrMCPDraftNotFound):
		return "This post is no longer scheduled or the draft was published.", nil
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
