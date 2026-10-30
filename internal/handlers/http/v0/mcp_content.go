package v0

import (
	"context"
	"errors"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	insightDomain "bloomary-api/internal/domain/insight"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpInsight struct {
	InsightID string `json:"insightId"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
	UsedAt    string `json:"usedAt,omitempty"`
	DraftID   string `json:"draftId,omitempty"`
}

type mcpInsightList struct {
	Insights []mcpInsight `json:"insights"`
}

type mcpListInsightsInput struct {
	Since      string `json:"since,omitempty" jsonschema:"Only insights created at or after this RFC 3339 time"`
	UnusedOnly bool   `json:"unused_only,omitempty" jsonschema:"Skip insights already marked used"`
}

type mcpMarkInsightUsedInput struct {
	InsightID string `json:"insight_id" jsonschema:"Insight ID from list_insights"`
	DraftID   string `json:"draft_id" jsonschema:"Draft written from this insight"`
}

// addMCPContentTools registers the content-engine tools: insights, images, schedules, approvals, suggestions.
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
}

func toMCPInsight(in insightDomain.Insight) mcpInsight {
	out := mcpInsight{InsightID: in.ID, Text: in.Text, Source: string(in.Source), CreatedAt: in.CreatedAt.UTC().Format(time.RFC3339), DraftID: in.DraftID}
	if in.UsedAt != nil {
		out.UsedAt = in.UsedAt.UTC().Format(time.RFC3339)
	}
	return out
}
