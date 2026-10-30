package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	insightDomain "bloomary-api/internal/domain/insight"
)

type ListMCPInsights struct {
	access   entitlementDomain.AccessStory
	insights insightDomain.Repository
}

func NewListMCPInsights(access entitlementDomain.AccessStory, insights insightDomain.Repository) (*ListMCPInsights, error) {
	if access == nil || insights == nil {
		return nil, fmt.Errorf("list MCP insights: access and repository are required")
	}
	return &ListMCPInsights{access: access, insights: insights}, nil
}

func (s *ListMCPInsights) List(ctx context.Context, filter insightDomain.ListFilter) ([]insightDomain.Insight, error) {
	if _, err := requireMCPPaid(ctx, s.access, filter.MemberID); err != nil {
		return nil, err
	}
	return s.insights.List(ctx, filter)
}

type MarkMCPInsightUsed struct {
	access   entitlementDomain.AccessStory
	insights insightDomain.Repository
	drafts   draftDomain.Repository
}

func NewMarkMCPInsightUsed(access entitlementDomain.AccessStory, insights insightDomain.Repository, drafts draftDomain.Repository) (*MarkMCPInsightUsed, error) {
	if access == nil || insights == nil || drafts == nil {
		return nil, fmt.Errorf("mark MCP insight used: access, insight and draft repositories are required")
	}
	return &MarkMCPInsightUsed{access: access, insights: insights, drafts: drafts}, nil
}

// MarkUsed links an insight to the member's own draft, so the next list can skip it.
func (s *MarkMCPInsightUsed) MarkUsed(ctx context.Context, memberID, insightID, draftID string) (insightDomain.Insight, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return insightDomain.Insight{}, err
	}
	insightID = strings.TrimSpace(insightID)
	if insightID == "" || draftID == "" {
		return insightDomain.Insight{}, errors.New("insight_id and draft_id are required")
	}
	if _, err := s.drafts.ByID(ctx, memberID, draftID); err != nil {
		return insightDomain.Insight{}, ErrMCPDraftNotFound
	}
	return s.insights.MarkUsed(ctx, memberID, insightID, draftID, time.Now().UTC())
}
