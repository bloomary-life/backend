package v0

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestMCPScheduleDraftHoldsPostForApproval(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().Schedule(mock.Anything, scheduledDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: publishAt, NeedsApproval: true}).
		Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt, AwaitingApproval: true}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().SupersedePending(mock.Anything, "member-1", "draft-1", mock.Anything).Return(nil)
	schedule, err := stories.NewScheduleMCPDraft(schedules, reviews)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{scheduleMCPDraftStory: schedule})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "schedule_draft", map[string]any{"draft_id": "draft-1", "publish_at": "2026-11-03T11:00:00+02:00"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpSchedule
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, mcpSchedule{ScheduleID: "job-1", DraftID: "draft-1", Status: "pending", PublishAt: "2026-11-03T09:00:00Z", AwaitingApproval: true}, out)
}

func TestMCPRequestApprovalSendsTelegramPreview(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomainDraft("draft-1"), nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt}}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountApprovalsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().SupersedePending(mock.Anything, "member-1", "draft-1", mock.Anything).Return(nil)
	reviews.EXPECT().CreateApproval(mock.Anything, mock.Anything).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.Anything, (*telegramDomain.Photo)(nil)).Return(1, nil)
	request, err := stories.NewRequestApproval(paidAccess(t), drafts, jobs, reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{requestApprovalStory: request})

	result := callMCPTool(t, svc, []string{"drafts:write"}, "request_approval", map[string]any{"draft_id": "draft-1"})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpApproval
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, "pending", out.Status)
	require.Equal(t, "job-1", out.ScheduleID)
	require.Equal(t, "2026-11-03T09:00:00Z", out.PublishAt)
}

func TestMCPSendSuggestionReportsCopyButton(t *testing.T) {
	// Verifies: BE-012
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.Anything).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.Anything, (*telegramDomain.Photo)(nil)).Return(1, nil)
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

func TestMCPListFeedbackReturnsDiffAndRatings(t *testing.T) {
	// Verifies: BE-014
	created := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().ListApprovals(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Approval{
		{ID: "a-1", DraftID: "draft-1", Status: reviewDomain.ApprovalApproved, ProposedText: "Hiring is hard", ScheduledAt: created, CreatedAt: created, DecidedAt: &created},
	}, nil)
	reviews.EXPECT().ListSuggestions(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Suggestion{{ID: "s-1", Kind: "comment", TargetURL: "https://x.test/p", Text: "Nice", Rating: 4, CreatedAt: created, PostedAt: &created}}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{Body: "Hiring is slow"}, nil)
	list, err := stories.NewListFeedback(paidAccess(t), reviews, drafts)
	require.NoError(t, err)
	svc := newMCPTestService(t, &service{listFeedbackStory: list})

	result := callMCPTool(t, svc, []string{"insights:read"}, "list_feedback", map[string]any{})

	require.False(t, result.Result.IsError, result.Result.Content)
	var out mcpFeedback
	require.NoError(t, json.Unmarshal(result.Result.StructuredContent, &out))
	require.Equal(t, "Hiring is [-hard-]{+slow+}", out.Approvals[0].Diff)
	require.Equal(t, "approved", out.Approvals[0].Status)
	require.Equal(t, mcpSuggestionFeedback{SuggestionID: "s-1", Kind: "comment", TargetURL: "https://x.test/p", Text: "Nice", CreatedAt: "2026-10-30T09:00:00Z", Rating: 4, Posted: true}, out.Suggestions[0])
}
