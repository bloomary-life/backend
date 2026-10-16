package stories

import (
	"context"
	"database/sql"
	"fmt"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

type GetMCPDraft struct {
	access entitlementDomain.AccessStory
	drafts draftDomain.Repository
}

func NewGetMCPDraft(access entitlementDomain.AccessStory, drafts draftDomain.Repository) (*GetMCPDraft, error) {
	if access == nil || drafts == nil {
		return nil, fmt.Errorf("get MCP draft: access and repository are required")
	}
	return &GetMCPDraft{access: access, drafts: drafts}, nil
}

func (s *GetMCPDraft) Get(ctx context.Context, memberID, draftID string) (draftDomain.Draft, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return draftDomain.Draft{}, err
	}
	draft, err := s.drafts.ByID(ctx, memberID, draftID)
	if err == sql.ErrNoRows {
		return draftDomain.Draft{}, ErrMCPDraftNotFound
	}
	return draft, err
}
