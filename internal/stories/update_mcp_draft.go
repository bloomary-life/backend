package stories

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

type UpdateMCPDraft struct {
	access entitlementDomain.AccessStory
	drafts draftDomain.Repository
}

func NewUpdateMCPDraft(access entitlementDomain.AccessStory, drafts draftDomain.Repository) (*UpdateMCPDraft, error) {
	if access == nil || drafts == nil {
		return nil, fmt.Errorf("update MCP draft: access and repository are required")
	}
	return &UpdateMCPDraft{access: access, drafts: drafts}, nil
}

func (s *UpdateMCPDraft) Update(ctx context.Context, memberID, draftID, body string) (draftDomain.Draft, error) {
	if _, err := requireMCPPaid(ctx, s.access, memberID); err != nil {
		return draftDomain.Draft{}, err
	}
	if err := validateMCPDraft(draftID, body); err != nil {
		return draftDomain.Draft{}, err
	}
	draft, err := s.drafts.UpdateUnpublished(ctx, memberID, draftID, body, time.Now().UTC())
	if err == sql.ErrNoRows {
		return draftDomain.Draft{}, ErrMCPDraftNotFound
	}
	if err == nil && draft.Status != "draft" {
		return draftDomain.Draft{}, ErrMCPDraftNotEditable
	}
	return draft, err
}
