package v0

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	insightDomain "bloomary-api/internal/domain/insight"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpInsight struct {
	InsightID string `json:"insightId"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
	UsedAt    string `json:"usedAt,omitempty"`
	DraftID   string `json:"draftId,omitempty"`
	// Trend is set when the insight is the member's reply to a trend message: the story it answers.
	Trend *mcpTrend `json:"trend,omitempty"`
}

type mcpTrend struct {
	SourceURL string `json:"sourceUrl"`
	Summary   string `json:"summary"`
}

type mcpInsightList struct {
	Insights []mcpInsight `json:"insights"`
}

type mcpListInsightsInput struct {
	Since      string `json:"since,omitempty" jsonschema:"Only insights created at or after this RFC 3339 time"`
	UnusedOnly bool   `json:"unused_only,omitempty" jsonschema:"Skip insights already marked used"`
}

type mcpAttachImageInput struct {
	DraftID    string `json:"draft_id" jsonschema:"Unpublished draft to illustrate"`
	ImageURL   string `json:"image_url,omitempty" jsonschema:"Public https URL of a JPEG, PNG or GIF (max 5 MB); or send data_base64"`
	DataBase64 string `json:"data_base64,omitempty" jsonschema:"Standard base64 image bytes; or send image_url"`
	MimeType   string `json:"mime_type,omitempty" jsonschema:"image/jpeg, image/png or image/gif; checked against the bytes"`
}

type mcpScheduleDraftInput struct {
	DraftID   string `json:"draft_id" jsonschema:"Draft to publish on LinkedIn"`
	PublishAt string `json:"publish_at" jsonschema:"RFC 3339 time with offset, 5 minutes to 31 days ahead"`
	// FirstComment carries what may not go into the post (links, sources); it is sent to the member when the post publishes.
	FirstComment string `json:"first_comment,omitempty" jsonschema:"Optional plain text up to 1000 characters, links allowed (booking page, job board, sources). Sent to the member in Telegram with Copy and Open post when the post publishes, to paste as the first comment."`
}

type mcpSchedule struct {
	ScheduleID string `json:"scheduleId"`
	DraftID    string `json:"draftId"`
	Status     string `json:"status"`
	PublishAt  string `json:"publishAt"`
}

type mcpNotifyScheduledInput struct {
	DraftID string `json:"draft_id" jsonschema:"Draft scheduled with schedule_draft"`
	Note    string `json:"note,omitempty" jsonschema:"Plain-text note shown above the preview (max 1000 characters), e.g. sources"`
}

type mcpNotifyDraftInput struct {
	DraftID           string `json:"draft_id" jsonschema:"Unscheduled, unpublished draft"`
	Note              string `json:"note,omitempty" jsonschema:"Plain-text note shown above the preview (max 1000 characters), e.g. sources"`
	FirstComment      string `json:"first_comment,omitempty" jsonschema:"Optional text the member pastes as the first comment after publishing (links allowed: sources, booking page, job board; max 1000 characters, a Copy button up to 256). Stored with the preview; it replaces the earlier one. Empty: the draft's earlier first comment is shown again."`
	ClearFirstComment bool   `json:"clear_first_comment,omitempty" jsonschema:"true: send the preview without a first comment and remove the stored one, so later previews do not show it again. Do not combine with first_comment."`
}

type mcpNotice struct {
	NoticeID   string `json:"noticeId"`
	DraftID    string `json:"draftId"`
	ScheduleID string `json:"scheduleId,omitempty"`
	PublishAt  string `json:"publishAt,omitempty"`
}

type mcpSendSuggestionInput struct {
	Kind      string `json:"kind" jsonschema:"\"comment\" (a comment idea for a LinkedIn post) or \"trend\" (a news story; the member's reply to it becomes an insight with the story attached)"`
	TargetURL string `json:"target_url" jsonschema:"comment: https URL of the LinkedIn post (linkedin.com); trend: https URL of the story's source"`
	Text      string `json:"text" jsonschema:"comment: the comment, up to 256 characters gets a Copy button; trend: a two-line summary, up to 600 characters"`
	Context   string `json:"context,omitempty" jsonschema:"One line on why this post and comment"`
	Silent    bool   `json:"silent,omitempty" jsonschema:"Deliver without a notification sound (e.g. every trend after the first of the day)"`
}

// mcpSuggestionKindHeader, when a client sends it, is the only send_suggestion kind that connection may use.
const mcpSuggestionKindHeader = "X-Bloomary-Suggestion-Kind"

type mcpSuggestionSent struct {
	SuggestionID string `json:"suggestionId"`
	CopyButton   bool   `json:"copyButton"`
}

type mcpListFeedbackInput struct {
	Since string `json:"since,omitempty" jsonschema:"RFC 3339 time; default 30 days ago"`
}

type mcpPostFeedback struct {
	NoticeID     string `json:"noticeId"`
	DraftID      string `json:"draftId"`
	Status       string `json:"status"`
	Note         string `json:"note,omitempty"`
	PublishAt    string `json:"publishAt,omitempty"`
	NotifiedAt   string `json:"notifiedAt"`
	SkippedAt    string `json:"skippedAt,omitempty"`
	ProposedText string `json:"proposedText"`
	FinalText    string `json:"finalText,omitempty"`
	Diff         string `json:"diff,omitempty"`
	Edited       bool   `json:"edited"`
}

type mcpSuggestionFeedback struct {
	SuggestionID string `json:"suggestionId"`
	Kind         string `json:"kind"`
	TargetURL    string `json:"targetUrl"`
	Text         string `json:"text"`
	Context      string `json:"context,omitempty"`
	CreatedAt    string `json:"createdAt"`
	Rating       int    `json:"rating,omitempty"`
	Posted       bool   `json:"posted"`
	// Approved: the member tapped Post it, so the AI app may post the comment for them (then mark_suggestion_posted).
	Approved   bool   `json:"approved"`
	ApprovedAt string `json:"approvedAt,omitempty"`
}

type mcpMarkSuggestionPostedInput struct {
	SuggestionID string `json:"suggestion_id" jsonschema:"suggestionId of a comment suggestion from list_feedback"`
}

type mcpFeedback struct {
	Posts       []mcpPostFeedback       `json:"posts"`
	Suggestions []mcpSuggestionFeedback `json:"suggestions"`
}

type mcpListEditRequestsInput struct {
	UnappliedOnly bool `json:"unapplied_only,omitempty" jsonschema:"Skip edit requests already marked applied"`
}

type mcpMarkEditAppliedInput struct {
	EditID string `json:"edit_id" jsonschema:"Edit request ID from list_edit_requests"`
}

type mcpEditRequest struct {
	EditID    string `json:"editId"`
	DraftID   string `json:"draftId"`
	NoticeID  string `json:"noticeId"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
	AppliedAt string `json:"appliedAt,omitempty"`
	// FirstComment is the draft's current first comment (its newest preview's), so an edit can change or clear it.
	FirstComment string `json:"firstComment,omitempty"`
}

type mcpEditRequestList struct {
	EditRequests []mcpEditRequest `json:"editRequests"`
}

type mcpMarkInsightUsedInput struct {
	InsightID string `json:"insight_id" jsonschema:"Insight ID from list_insights"`
	DraftID   string `json:"draft_id" jsonschema:"Draft written from this insight"`
}

// addMCPContentTools registers the content-engine tools: insights, images, schedules, notices, suggestions.
func (s *service) addMCPContentTools(server *mcp.Server) {
	mcp.AddTool(server, mcpOAuthTool("list_insights", "List ideas the member sent to the Bloomary Telegram bot (text or transcribed voice), newest first.", "insights:read"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpListInsightsInput) (*mcp.CallToolResult, mcpInsightList, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "insights:read")
			if err != nil {
				return nil, mcpInsightList{}, err
			}
			filter := insightDomain.ListFilter{MemberID: memberID, UnusedOnly: input.UnusedOnly}
			if strings.TrimSpace(input.Since) != "" {
				filter.Since, err = time.Parse(time.RFC3339, strings.TrimSpace(input.Since))
				if err != nil {
					return nil, mcpInsightList{}, errors.New("since must be an RFC 3339 time")
				}
			}
			insights, err := s.listInsightsStory.List(ctx, filter)
			if err != nil {
				return nil, mcpInsightList{}, err
			}
			out := mcpInsightList{Insights: make([]mcpInsight, 0, len(insights))}
			for _, in := range insights {
				out.Insights = append(out.Insights, toMCPInsight(in))
			}
			return nil, out, nil
		})
	mcp.AddTool(server, mcpOAuthTool("mark_insight_used", "Mark an insight as used by a draft, so unused_only lists skip it.", "insights:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpMarkInsightUsedInput) (*mcp.CallToolResult, mcpInsight, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "insights:write")
			if err != nil {
				return nil, mcpInsight{}, err
			}
			in, err := s.markInsightUsedStory.MarkUsed(ctx, memberID, input.InsightID, draftDomain.NormalizeID(input.DraftID))
			if err != nil {
				return nil, mcpInsight{}, err
			}
			return nil, toMCPInsight(in), nil
		})
	mcp.AddTool(server, mcpOAuthTool("attach_image", "Attach one image to an unpublished draft, replacing its current image. LinkedIn posts it with the text.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpAttachImageInput) (*mcp.CallToolResult, mcpDraft, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpDraft{}, err
			}
			if s.attachImageStory == nil {
				return nil, mcpDraft{}, errors.New("draft image storage is not configured")
			}
			draft, err := s.attachImageStory.Attach(ctx, draftDomain.AttachImageInput{MemberID: memberID, DraftID: draftDomain.NormalizeID(input.DraftID),
				ImageURL: input.ImageURL, DataBase64: input.DataBase64, MimeType: input.MimeType})
			if err != nil {
				return nil, mcpDraft{}, err
			}
			return nil, toMCPDraft(draft), nil
		})
	s.addMCPReviewTools(server)
}

func (s *service) addMCPReviewTools(server *mcp.Server) {
	mcp.AddTool(server, mcpOAuthTool("schedule_draft", "Schedule a draft for LinkedIn. It publishes at publish_at unless the member taps Skip; call notify_scheduled next. Put links and sources in first_comment, never in the post.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpScheduleDraftInput) (*mcp.CallToolResult, mcpSchedule, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpSchedule{}, err
			}
			publishAt, err := time.Parse(time.RFC3339, strings.TrimSpace(input.PublishAt))
			if err != nil {
				return nil, mcpSchedule{}, errors.New("publish_at must be an RFC 3339 time with offset")
			}
			firstComment := strings.TrimSpace(input.FirstComment)
			if firstComment != "" && s.firstCommentStory == nil {
				return nil, mcpSchedule{}, errors.New("the Bloomary Telegram bot is not configured, so first_comment cannot be delivered")
			}
			if utf8.RuneCountInString(firstComment) > reviewDomain.MaxFirstCommentRunes {
				return nil, mcpSchedule{}, fmt.Errorf("first_comment must be at most %d characters", reviewDomain.MaxFirstCommentRunes)
			}
			draftID := draftDomain.NormalizeID(input.DraftID)
			post, err := s.scheduledPostStory.Schedule(ctx, scheduledDomain.CreateInput{MemberID: memberID, DraftID: draftID, ScheduledAt: publishAt.UTC()})
			if err != nil {
				return nil, mcpSchedule{}, err
			}
			if firstComment != "" {
				if err := s.firstCommentStory.Save(ctx, memberID, draftID, post.ID, firstComment); err != nil {
					return nil, mcpSchedule{}, fmt.Errorf("the post is scheduled, but its first comment was not saved: %w", err)
				}
			} else if s.firstCommentStory != nil {
				if err := s.firstCommentStory.Clear(ctx, memberID, draftID); err != nil {
					return nil, mcpSchedule{}, fmt.Errorf("the post is scheduled, but its old first comment was not removed: %w", err)
				}
			}
			return nil, toMCPSchedule(post), nil
		})
	mcp.AddTool(server, mcpOAuthTool("notify_scheduled", "Send the member a Telegram message for a scheduled draft: text, image, time, an Edit link and a Skip button that cancels the schedule.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpNotifyScheduledInput) (*mcp.CallToolResult, mcpNotice, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpNotice{}, err
			}
			if s.notifyScheduledStory == nil {
				return nil, mcpNotice{}, errors.New("the Bloomary Telegram bot is not configured")
			}
			notice, err := s.notifyScheduledStory.Notify(ctx, memberID, draftDomain.NormalizeID(input.DraftID), input.Note)
			if err != nil {
				return nil, mcpNotice{}, err
			}
			return nil, mcpNotice{NoticeID: notice.ID, DraftID: notice.DraftID, ScheduleID: notice.ScheduleID,
				PublishAt: notice.ScheduledAt.UTC().Format(time.RFC3339)}, nil
		})
	mcp.AddTool(server, mcpOAuthTool("list_edit_requests", "List the member's replies (text or transcribed voice) to post previews in Telegram: how to change that draft, oldest first. Rewrite the draft, call update_draft, then notify_scheduled (scheduled post) or notify_draft (unscheduled draft), then mark_edit_applied.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpListEditRequestsInput) (*mcp.CallToolResult, mcpEditRequestList, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpEditRequestList{}, err
			}
			edits, err := s.editRequestsStory.List(ctx, memberID, input.UnappliedOnly)
			if err != nil {
				return nil, mcpEditRequestList{}, err
			}
			out := mcpEditRequestList{EditRequests: make([]mcpEditRequest, 0, len(edits))}
			for _, e := range edits {
				out.EditRequests = append(out.EditRequests, toMCPEditRequest(e))
			}
			return nil, out, nil
		})
	mcp.AddTool(server, mcpOAuthTool("mark_edit_applied", "Mark an edit request applied after the draft was rewritten (or after telling the member it was unclear), so unapplied_only lists skip it.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpMarkEditAppliedInput) (*mcp.CallToolResult, mcpEditRequest, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpEditRequest{}, err
			}
			edit, err := s.editRequestsStory.MarkApplied(ctx, memberID, input.EditID)
			if err != nil {
				return nil, mcpEditRequest{}, err
			}
			return nil, toMCPEditRequest(edit), nil
		})
	mcp.AddTool(server, mcpOAuthTool("notify_draft", "Send the member a Telegram preview of an unscheduled draft (text, image, optional first comment with Copy) with How to edit, Discard and an Edit link. Replies to it become edit requests.", "drafts:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpNotifyDraftInput) (*mcp.CallToolResult, mcpNotice, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "drafts:write")
			if err != nil {
				return nil, mcpNotice{}, err
			}
			if s.notifyScheduledStory == nil {
				return nil, mcpNotice{}, errors.New("the Bloomary Telegram bot is not configured")
			}
			notice, err := s.notifyScheduledStory.NotifyDraft(ctx, memberID, draftDomain.NormalizeID(input.DraftID), input.Note, input.FirstComment, input.ClearFirstComment)
			if err != nil {
				return nil, mcpNotice{}, err
			}
			return nil, mcpNotice{NoticeID: notice.ID, DraftID: notice.DraftID}, nil
		})
	mcp.AddTool(server, mcpOAuthTool("send_suggestion", "Send the member a comment idea (kind comment: Open post, Copy, rating 1-5 and Posted; the member posts it themselves) or a news story (kind trend: Open source; a reply to it becomes an insight with the story in list_insights).", "insights:write"),
		func(ctx context.Context, req *mcp.CallToolRequest, input mcpSendSuggestionInput) (*mcp.CallToolResult, mcpSuggestionSent, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "insights:write")
			if err != nil {
				return nil, mcpSuggestionSent{}, err
			}
			if s.sendSuggestionStory == nil {
				return nil, mcpSuggestionSent{}, errors.New("the Bloomary Telegram bot is not configured")
			}
			kind := strings.TrimSpace(input.Kind)
			// A client may pin the one kind its session may send (the content engine's comments job: comment), so text
			// its model read cannot make it send the other kind.
			if req != nil && req.Extra != nil {
				if pinned := strings.TrimSpace(req.Extra.Header.Get(mcpSuggestionKindHeader)); pinned != "" && pinned != kind {
					return nil, mcpSuggestionSent{}, fmt.Errorf("this connection may send kind %q only", pinned)
				}
			}
			suggestion, err := s.sendSuggestionStory.Send(ctx, reviewDomain.SuggestionInput{MemberID: memberID, Kind: kind, Silent: input.Silent,
				TargetURL: input.TargetURL, Text: input.Text, Context: input.Context})
			if err != nil {
				return nil, mcpSuggestionSent{}, err
			}
			return nil, mcpSuggestionSent{SuggestionID: suggestion.ID, CopyButton: len([]rune(suggestion.Text)) <= reviewDomain.CopyTextLimit}, nil
		})
	mcp.AddTool(server, mcpOAuthTool("mark_suggestion_posted", "Mark a comment suggestion posted after posting it on LinkedIn for the member (they approved it with Post it); its Telegram message shows ✅ Posted.", "insights:write"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpMarkSuggestionPostedInput) (*mcp.CallToolResult, mcpSuggestionFeedback, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "insights:write")
			if err != nil {
				return nil, mcpSuggestionFeedback{}, err
			}
			if s.sendSuggestionStory == nil {
				return nil, mcpSuggestionFeedback{}, errors.New("the Bloomary Telegram bot is not configured")
			}
			suggestion, err := s.sendSuggestionStory.MarkPosted(ctx, memberID, input.SuggestionID)
			if err != nil {
				return nil, mcpSuggestionFeedback{}, err
			}
			return nil, toMCPFeedback(reviewDomain.Feedback{Suggestions: []reviewDomain.Suggestion{suggestion}}).Suggestions[0], nil
		})
	mcp.AddTool(server, mcpOAuthTool("list_feedback", "List notified posts with their outcome (scheduled, published, skipped, failed) and the member's edits as a word diff, plus comment suggestion ratings, approved (Post it tapped) and posted flags.", "insights:read"),
		func(ctx context.Context, _ *mcp.CallToolRequest, input mcpListFeedbackInput) (*mcp.CallToolResult, mcpFeedback, error) {
			memberID, err := s.mcpMemberWithScope(ctx, "insights:read")
			if err != nil {
				return nil, mcpFeedback{}, err
			}
			var since time.Time
			if strings.TrimSpace(input.Since) != "" {
				since, err = time.Parse(time.RFC3339, strings.TrimSpace(input.Since))
				if err != nil {
					return nil, mcpFeedback{}, errors.New("since must be an RFC 3339 time")
				}
			}
			feedback, err := s.listFeedbackStory.List(ctx, memberID, since)
			if err != nil {
				return nil, mcpFeedback{}, err
			}
			return nil, toMCPFeedback(feedback), nil
		})
}

func toMCPSchedule(post scheduledDomain.Post) mcpSchedule {
	return mcpSchedule{ScheduleID: post.ID, DraftID: post.DraftID, Status: string(post.Status), PublishAt: post.ScheduledAt.UTC().Format(time.RFC3339)}
}

func toMCPFeedback(feedback reviewDomain.Feedback) mcpFeedback {
	out := mcpFeedback{Posts: make([]mcpPostFeedback, 0, len(feedback.Posts)), Suggestions: make([]mcpSuggestionFeedback, 0, len(feedback.Suggestions))}
	for _, p := range feedback.Posts {
		item := mcpPostFeedback{NoticeID: p.ID, DraftID: p.DraftID, Status: p.PostStatus, Note: p.Note,
			NotifiedAt:   p.CreatedAt.UTC().Format(time.RFC3339),
			ProposedText: p.ProposedText, FinalText: p.FinalText, Diff: p.Diff, Edited: p.Edited}
		if p.ScheduleID != "" {
			item.PublishAt = p.ScheduledAt.UTC().Format(time.RFC3339)
		}
		if p.SkippedAt != nil {
			item.SkippedAt = p.SkippedAt.UTC().Format(time.RFC3339)
		}
		out.Posts = append(out.Posts, item)
	}
	for _, sg := range feedback.Suggestions {
		item := mcpSuggestionFeedback{SuggestionID: sg.ID, Kind: sg.Kind, TargetURL: sg.TargetURL, Text: sg.Text,
			Context: sg.Context, CreatedAt: sg.CreatedAt.UTC().Format(time.RFC3339), Rating: sg.Rating, Posted: sg.PostedAt != nil, Approved: sg.ApprovedAt != nil}
		if sg.ApprovedAt != nil {
			item.ApprovedAt = sg.ApprovedAt.UTC().Format(time.RFC3339)
		}
		out.Suggestions = append(out.Suggestions, item)
	}
	return out
}

func toMCPEditRequest(e reviewDomain.EditRequest) mcpEditRequest {
	out := mcpEditRequest{EditID: e.ID, DraftID: e.DraftID, NoticeID: e.NoticeID, Text: e.Text, Source: e.Source, CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339), FirstComment: e.FirstComment}
	if e.AppliedAt != nil {
		out.AppliedAt = e.AppliedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func toMCPInsight(in insightDomain.Insight) mcpInsight {
	out := mcpInsight{InsightID: in.ID, Text: in.Text, Source: string(in.Source), CreatedAt: in.CreatedAt.UTC().Format(time.RFC3339), DraftID: in.DraftID}
	if in.UsedAt != nil {
		out.UsedAt = in.UsedAt.UTC().Format(time.RFC3339)
	}
	if in.Trend != nil {
		out.Trend = &mcpTrend{SourceURL: in.Trend.SourceURL, Summary: in.Trend.Summary}
	}
	return out
}
