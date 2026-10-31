package v0

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	insightDomain "bloomary-api/internal/domain/insight"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	insightMocks "bloomary-api/internal/mocks/domain/insight"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"
	"bloomary-api/internal/stories"

	entitlementDomain "bloomary-api/internal/domain/entitlement"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mcpToolResult struct {
	Result struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

func newMCPTestService(t *testing.T, svc *service) *service {
	t.Helper()
	svc.config.MCPTokenSigningKey = "test-signing-key"
	svc.config.MCPResourceURL = "https://api.bloomary.life/mcp"
	svc.config.MCPIssuerURL = "https://api.bloomary.life"
	svc.mcpHandler = svc.newMCPHandler()
	return svc
}

// callMCPTool sends one tools/call through the real MCP HTTP transport with a signed member token.
func callMCPTool(t *testing.T, svc *service, scopes []string, tool string, args any) mcpToolResult {
	t.Helper()
	return callMCPToolWithHeaders(t, svc, scopes, tool, args, nil)
}

// callMCPToolWithHeaders is callMCPTool with extra request headers.
func callMCPToolWithHeaders(t *testing.T, svc *service, scopes []string, tool string, args any, headers map[string]string) mcpToolResult {
	t.Helper()
	token, err := svc.signMCPToken(mcpTokenClaims{
		Issuer: svc.config.MCPIssuerURL, Subject: "member-1", Audience: svc.config.MCPResourceURL,
		IssuedAt: time.Now().Unix(), Expires: time.Now().Add(time.Minute).Unix(), Scope: scopes,
	})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	svc.mcpHandler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var result mcpToolResult
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result), response.Body.String())
	return result
}

func paidAccess(t *testing.T) *entitlementMocks.MockAccessStory {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro, IsPro: true}, nil).Maybe()
	return access
}

func TestMCPListInsightsReturnsUnusedInsights(t *testing.T) {
	// Verifies: BE-008
	insights := insightMocks.NewMockRepository(t)
	insights.EXPECT().List(mock.Anything, mock.MatchedBy(func(filter insightDomain.ListFilter) bool {
		return filter.MemberID == "member-1" && filter.UnusedOnly && filter.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	})).Return([]insightDomain.Insight{{ID: "i-1", MemberID: "member-1", Text: "Hiring needs proof", Source: insightDomain.SourceVoice, CreatedAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}}, nil)
	list, err := stories.NewListMCPInsights(paidAccess(t), insights)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{listInsightsStory: list})

	result := callMCPTool(t, svc, []string{"insights:read"}, "list_insights", map[string]any{"since": "2026-10-01T00:00:00Z", "unused_only": true})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpInsightList
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Len(t, out.Insights, 1)
	require.Equal(t, mcpInsight{InsightID: "i-1", Text: "Hiring needs proof", Source: "voice", CreatedAt: "2026-10-02T09:00:00Z"}, out.Insights[0])
}

func TestMCPMarkInsightUsedLinksDraft(t *testing.T) {
	// Verifies: BE-008
	used := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	insights := insightMocks.NewMockRepository(t)
	insights.EXPECT().MarkUsed(mock.Anything, "member-1", "i-1", "draft-1", mock.Anything).
		Return(insightDomain.Insight{ID: "i-1", Text: "Idea", Source: insightDomain.SourceText, CreatedAt: used, UsedAt: &used, DraftID: "draft-1"}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil)
	mark, err := stories.NewMarkMCPInsightUsed(paidAccess(t), insights, drafts)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{markInsightUsedStory: mark})

	result := callMCPTool(t, svc, []string{"insights:write"}, "mark_insight_used", map[string]any{"insight_id": "i-1", "draft_id": "DRAFT-1"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpInsight
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, "draft-1", out.DraftID)
	require.Equal(t, "2026-10-03T09:00:00Z", out.UsedAt)
}

func draftDomainDraft(id string) draftDomain.Draft {
	return draftDomain.Draft{DraftID: id, Body: "Body", Status: "draft"}
}

func TestMCPAttachImageStoresBase64Image(t *testing.T) {
	// Verifies: BE-011
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil).Once()
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().MaxUploadBytes().Return(5 << 20)
	objects.EXPECT().LinkedObjectKey("member-1", "draft-1", mock.Anything).Return("linked-key")
	objects.EXPECT().Put(mock.Anything, "linked-key", "image/png", png, mock.Anything).Return(nil)
	drafts.EXPECT().SetImage(mock.Anything, "member-1", "draft-1", mock.Anything).Return("", nil)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").
		Return(draftDomain.Draft{DraftID: "draft-1", Status: "draft", Image: &draftDomain.DraftImage{ImageID: "img-1", MimeType: "image/png", SizeBytes: 16}}, nil).Once()
	attach, err := stories.NewAttachMCPImage(paidAccess(t), drafts, objects, draftMocks.NewMockRemoteImageFetcher(t))
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{attachImageStory: attach})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "attach_image", map[string]any{"draft_id": "draft-1", "data_base64": base64.StdEncoding.EncodeToString(png)})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpDraft
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, &mcpDraftImage{ImageID: "img-1", MimeType: "image/png", SizeBytes: 16}, out.Image)
}

func TestMCPScheduleDraftSchedulesPost(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().Schedule(mock.Anything, scheduledDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: publishAt}).
		Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt}, nil)
	svc := newMCPTestService(t, &service{scheduledPostStory: schedules})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "schedule_draft", map[string]any{"draft_id": "draft-1", "publish_at": "2026-11-03T11:00:00+02:00"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpSchedule
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, mcpSchedule{ScheduleID: "job-1", DraftID: "draft-1", Status: "pending", PublishAt: "2026-11-03T09:00:00Z"}, out)
}

func TestMCPNotifyScheduledSendsTelegramPreview(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt}}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.Anything).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return(nil, nil).Maybe()
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(1), int64(0)).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.Anything, (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 1}, nil)
	notify, err := stories.NewNotifyScheduled(paidAccess(t), drafts, jobs, reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{notifyScheduledStory: notify})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "notify_scheduled", map[string]any{"draft_id": "draft-1", "note": "image: scenario"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpNotice
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, "job-1", out.ScheduleID)
	require.Equal(t, "2026-11-03T09:00:00Z", out.PublishAt)
	require.Len(t, out.NoticeID, 32)
}

func TestMCPSendSuggestionReportsCopyButton(t *testing.T) {
	// Verifies: BE-012
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.Anything).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.Anything, (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 1}, nil)
	send, err := stories.NewSendSuggestion(paidAccess(t), reviews, notifier)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{sendSuggestionStory: send})

	result := callMCPTool(t, svc, []string{"insights:write"}, "send_suggestion", map[string]any{"kind": "comment", "target_url": "https://www.linkedin.com/feed/update/1", "text": "Short comment"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpSuggestionSent
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.True(t, out.CopyButton)
	require.Len(t, out.SuggestionID, 32)
}

func TestMCPListFeedbackReturnsOutcomesDiffAndRatings(t *testing.T) {
	// Verifies: BE-014
	created := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", Status: reviewDomain.NoticeSent, ProposedText: "Hiring is hard", ScheduledAt: created, CreatedAt: created},
	}, nil)
	reviews.EXPECT().ListSuggestions(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Suggestion{{ID: "s-1", Kind: "comment", TargetURL: "https://x.test/p", Text: "Nice", Rating: 4, CreatedAt: created, PostedAt: &created}}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{Body: "Hiring is slow"}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{Status: scheduledDomain.Published}, nil)
	list, err := stories.NewListFeedback(paidAccess(t), reviews, drafts, jobs)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{listFeedbackStory: list})

	result := callMCPTool(t, svc, []string{"insights:read"}, "list_feedback", map[string]any{})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpFeedback
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, mcpPostFeedback{NoticeID: "n-1", DraftID: "draft-1", Status: "published", PublishAt: "2026-10-30T09:00:00Z", NotifiedAt: "2026-10-30T09:00:00Z",
		ProposedText: "Hiring is hard", FinalText: "Hiring is slow", Diff: "Hiring is [-hard-]{+slow+}", Edited: true}, out.Posts[0])
	require.Equal(t, mcpSuggestionFeedback{SuggestionID: "s-1", Kind: "comment", TargetURL: "https://x.test/p", Text: "Nice", CreatedAt: "2026-10-30T09:00:00Z", Rating: 4, Posted: true}, out.Suggestions[0])
}

func TestMCPListEditRequestsAndMarkEditApplied(t *testing.T) {
	// Verifies: BE-017
	created := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().ListEditRequests(mock.Anything, "member-1", true).Return([]reviewDomain.EditRequest{
		{ID: "e-1", MemberID: "member-1", DraftID: "draft-1", NoticeID: "n-1", Text: "Shorter", Source: "voice", CreatedAt: created, FirstComment: "Book: https://lumen.observer"},
	}, nil)
	reviews.EXPECT().MarkEditApplied(mock.Anything, "member-1", "e-1", mock.Anything).
		Return(reviewDomain.EditRequest{ID: "e-1", DraftID: "draft-1", NoticeID: "n-1", Text: "Shorter", Source: "voice", CreatedAt: created, AppliedAt: &created}, nil)
	edits, err := stories.NewEditRequests(paidAccess(t), reviews)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{editRequestsStory: edits})

	listed := callMCPTool(t, svc, []string{"drafts:write"}, "list_edit_requests", map[string]any{"unapplied_only": true})
	marked := callMCPTool(t, svc, []string{"drafts:write"}, "mark_edit_applied", map[string]any{"edit_id": "e-1"})

	require.False(t, listed.Result.IsError, listed.Result.Content)
	var list mcpEditRequestList
	require.NoError(t, json.Unmarshal(listed.Result.StructuredContent, &list))
	require.Equal(t, []mcpEditRequest{{EditID: "e-1", DraftID: "draft-1", NoticeID: "n-1", Text: "Shorter", Source: "voice", CreatedAt: "2026-10-30T09:00:00Z", FirstComment: "Book: https://lumen.observer"}}, list.EditRequests)
	require.False(t, marked.Result.IsError, marked.Result.Content)
	var out mcpEditRequest
	require.NoError(t, json.Unmarshal(marked.Result.StructuredContent, &out))
	require.Equal(t, "2026-10-30T09:00:00Z", out.AppliedAt)
}

func TestMCPNotifyDraftClearFirstCommentDropsTheStoredOne(t *testing.T) {
	// Verifies: BE-019
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "n-0", DraftID: "draft-1", Status: reviewDomain.NoticeSent, FirstComment: "Book: https://lumen.observer/call/katerina"}}, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool { return n.FirstComment == "" })).Return(nil)
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(4), int64(0)).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return !strings.Contains(m.Text, "lumen.observer")
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 4}, nil)
	notify, err := stories.NewNotifyScheduled(paidAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{notifyScheduledStory: notify})

	both := callMCPTool(t, svc, []string{"drafts:write"}, "notify_draft", map[string]any{"draft_id": "draft-1", "first_comment": "x", "clear_first_comment": true})
	result := callMCPTool(t, svc, []string{"drafts:write"}, "notify_draft", map[string]any{"draft_id": "draft-1", "note": "Edited: removed the booking link", "clear_first_comment": true})

	require.True(t, both.Result.IsError, "first_comment together with clear_first_comment accepted")
	require.False(t, result.Result.IsError, result.Result.Content)
}

func TestMCPNotifyDraftSendsUnscheduledPreviewWithoutSkip(t *testing.T) {
	// Verifies: BE-019
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool { return n.ScheduleID == "" && n.DraftID == "draft-1" })).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return(nil, nil).Maybe()
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(4), int64(0)).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "LinkedIn post draft below, not scheduled.") && !strings.Contains(m.Text, "Goes out") && len(m.Buttons) == 0
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 3}, nil).Once()
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return len(m.Buttons) == 2 && m.Buttons[0][0].Text == "📅 Schedule (next slot)" && m.Buttons[1][0].Text == "Discard"
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 4}, nil).Once()
	notify, err := stories.NewNotifyScheduled(paidAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{notifyScheduledStory: notify})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "notify_draft", map[string]any{"draft_id": "draft-1"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpNotice
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, "draft-1", out.DraftID)
	require.Empty(t, out.ScheduleID)
}

func TestMCPCreateAndUpdateDraftRejectLinks(t *testing.T) {
	// Verifies: BE-020
	create, err := stories.NewCreateMCPDraft(paidAccess(t), draftMocks.NewMockRepository(t))
	require.NoError(t, err)
	update, err := stories.NewUpdateMCPDraft(paidAccess(t), draftMocks.NewMockRepository(t))
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{createMCPDraftStory: create, updateMCPDraftStory: update})

	for _, body := range []string{"Book a call: https://lumen.observer/call/katerina", "See www.example.com", "Book at lumen.observer/call/katerina"} {
		for _, tool := range []string{"create_draft", "update_draft"} {
			result := callMCPTool(t, svc, []string{"drafts:write"}, tool, map[string]any{"draft_id": "draft-1", "body": body})
			require.True(t, result.Result.IsError, tool+": "+body)
			require.Contains(t, result.Result.Content[0].Text, "links are not allowed", tool)
		}
	}
}

func TestMCPCreateDraftAllowsTextWithoutLinks(t *testing.T) {
	// Verifies: BE-020
	body := "I moved from Node.js/Go to Rust. Message me if you want one."
	repository := draftMocks.NewMockRepository(t)
	repository.EXPECT().CreateIfAbsent(mock.Anything, "member-1", "draft-1", body, mock.Anything, mock.Anything).
		Return(draftDomain.Draft{DraftID: "draft-1", Body: body, Status: "draft"}, true, nil)
	create, err := stories.NewCreateMCPDraft(paidAccess(t), repository)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{createMCPDraftStory: create})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "create_draft", map[string]any{"draft_id": "draft-1", "body": body})

	require.False(t, result.Result.IsError, result.Result.Content)
}

func TestMCPSendSuggestionAcceptsLinkedInPostsOnly(t *testing.T) {
	// Verifies: BE-012
	send, err := stories.NewSendSuggestion(paidAccess(t), reviewMocks.NewMockRepository(t), telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{sendSuggestionStory: send})

	for _, target := range []string{"https://evil.example/v?d=secret", "http://www.linkedin.com/posts/x"} {
		result := callMCPTool(t, svc, []string{"insights:write"}, "send_suggestion", map[string]any{"kind": "comment", "target_url": target, "text": "Short comment"})
		require.True(t, result.Result.IsError, target)
	}
}

func TestMCPSendSuggestionSendsTrendFromAnyHTTPSSource(t *testing.T) {
	// Verifies: BE-023
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.Anything).Return(nil)
	reviews.EXPECT().SetSuggestionMessage(mock.Anything, "member-1", mock.Anything, int64(5)).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Buttons[0][0] == telegramDomain.Button{Text: "Open source", URL: "https://www.sec.gov/x"}
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 5}, nil)
	send, err := stories.NewSendSuggestion(paidAccess(t), reviews, notifier)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{sendSuggestionStory: send})

	result := callMCPTool(t, svc, []string{"insights:write"}, "send_suggestion", map[string]any{"kind": "trend", "target_url": "https://www.sec.gov/x", "text": "Tesla filed its 10-Q.\nDeliveries fell."})

	require.False(t, result.Result.IsError, result.Result.Content)
}

func TestMCPListInsightsIncludesTrendContext(t *testing.T) {
	// Verifies: BE-023
	insights := insightMocks.NewMockRepository(t)
	insights.EXPECT().List(mock.Anything, mock.Anything).Return([]insightDomain.Insight{{ID: "i-1", Text: "My take", Source: insightDomain.SourceVoice,
		CreatedAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), Trend: &insightDomain.Trend{SourceURL: "https://www.anthropic.com/news/x", Summary: "Summary"}}}, nil)
	list, err := stories.NewListMCPInsights(paidAccess(t), insights)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{listInsightsStory: list})

	result := callMCPTool(t, svc, []string{"insights:read"}, "list_insights", map[string]any{})

	require.False(t, result.Result.IsError, result.Result.Content)
	require.Contains(t, string(result.Result.StructuredContent), `"trend":{"sourceUrl":"https://www.anthropic.com/news/x","summary":"Summary"}`)
}

type fakeFirstComments struct{ saved map[string]string }

func (f *fakeFirstComments) Save(_ context.Context, memberID, draftID, scheduleID, text string) error {
	f.saved[memberID+"/"+draftID] = scheduleID + ":" + text
	return nil
}

func (f *fakeFirstComments) Clear(_ context.Context, memberID, draftID string) error {
	delete(f.saved, memberID+"/"+draftID)
	return nil
}

func TestMCPScheduleDraftStoresFirstComment(t *testing.T) {
	// Verifies: BE-024
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().Schedule(mock.Anything, mock.Anything).Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)}, nil)
	comments := &fakeFirstComments{saved: map[string]string{}}
	svc := newMCPTestService(t, &service{scheduledPostStory: schedules, firstCommentStory: comments})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "schedule_draft", map[string]any{"draft_id": "draft-1", "publish_at": "2026-11-03T10:00:00+02:00",
		"first_comment": "Book a call: https://lumen.observer/call/katerina"})

	require.False(t, result.Result.IsError, result.Result.Content)
	require.Equal(t, "job-1:Book a call: https://lumen.observer/call/katerina", comments.saved["member-1/draft-1"])

	comments.saved["member-1/draft-1"] = "job-1:old"
	result = callMCPTool(t, svc, []string{"drafts:write"}, "schedule_draft", map[string]any{"draft_id": "draft-1", "publish_at": "2026-11-03T10:00:00+02:00"})
	require.False(t, result.Result.IsError, result.Result.Content)
	require.NotContains(t, comments.saved, "member-1/draft-1", "scheduling again without first_comment clears the old one")
}

func TestMCPSendSuggestionKeepsToTheKindItsConnectionPinned(t *testing.T) {
	// Verifies: BE-023
	send, err := stories.NewSendSuggestion(paidAccess(t), reviewMocks.NewMockRepository(t), telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{sendSuggestionStory: send})

	result := callMCPToolWithHeaders(t, svc, []string{"insights:write"}, "send_suggestion",
		map[string]any{"kind": "trend", "target_url": "https://xn--nthropic-abc.com/x", "text": "Story"}, map[string]string{mcpSuggestionKindHeader: "comment"})

	require.True(t, result.Result.IsError)
	require.Contains(t, result.Result.Content[0].Text, `may send kind "comment" only`)
}
