package stories

import (
	"context"
	"fmt"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

type ListMCPDrafts struct {
	access entitlementDomain.AccessStory
	drafts draftDomain.Repository
}

func NewListMCPDrafts(access entitlementDomain.AccessStory, drafts draftDomain.Repository) (*ListMCPDrafts, error) {
	if access == nil || drafts == nil {
		return nil, fmt.Errorf("list MCP drafts: access and repository are required")
	}
	return &ListMCPDrafts{access: access, drafts: drafts}, nil
}

func (s *ListMCPDrafts) List(ctx context.Context, memberID string) ([]draftDomain.Draft, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return nil, err
	}
	return s.drafts.ListByMemberID(ctx, memberID)
}
