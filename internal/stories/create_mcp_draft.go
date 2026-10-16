package stories

import (
	"context"
	"fmt"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

type CreateMCPDraft struct {
	access entitlementDomain.AccessStory
	drafts draftDomain.Repository
}

func NewCreateMCPDraft(access entitlementDomain.AccessStory, drafts draftDomain.Repository) (*CreateMCPDraft, error) {
	if access == nil || drafts == nil {
		return nil, fmt.Errorf("create MCP draft: access and repository are required")
	}
	return &CreateMCPDraft{access: access, drafts: drafts}, nil
}

func (s *CreateMCPDraft) Create(ctx context.Context, memberID, draftID, body string) (draftDomain.Draft, error) {
	entitlement, err := requireMCPPaid(ctx, s.access, memberID)
	if err != nil {
		return draftDomain.Draft{}, err
	}
	if err := validateMCPDraft(draftID, body); err != nil {
		return draftDomain.Draft{}, err
	}
	limit := entitlement.DraftLimit
	if limit <= 0 {
		limit = entitlementDomain.ProDraftLimit
	}
	draft, created, err := s.drafts.CreateIfAbsent(ctx, memberID, draftID, body, limit, time.Now().UTC())
	if err != nil {
		return draftDomain.Draft{}, err
	}
	if created || (draft.Status == "draft" && draft.Body == body) {
		return draft, nil
	}
	return draftDomain.Draft{}, ErrMCPDraftExists
}
