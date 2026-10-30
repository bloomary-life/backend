package v0

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	insightDomain "bloomary-api/internal/domain/insight"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	insightMocks "bloomary-api/internal/mocks/domain/insight"
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
