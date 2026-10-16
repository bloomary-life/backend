package stories

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

var ErrMCPDraftNotFound = errors.New("MCP draft not found")
var ErrMCPDraftExists = errors.New("MCP draft ID already exists")
var ErrMCPDraftNotEditable = errors.New("MCP can update unpublished drafts only")
var ErrMCPRequiresPaid = errors.New("active paid plan required for MCP")

func requireMCPPaid(ctx context.Context, access entitlementDomain.AccessStory, memberID string) (entitlementDomain.Entitlement, error) {
	if memberID == "" {
		return entitlementDomain.Entitlement{}, ErrMCPRequiresPaid
	}
	entitlement, err := access.Resolve(ctx, memberID, time.Now())
	if err != nil {
		return entitlementDomain.Entitlement{}, fmt.Errorf("resolve MCP entitlement: %w", err)
	}
	if entitlement.Tier != entitlementDomain.TierPro && entitlement.Tier != entitlementDomain.TierTrial {
		return entitlementDomain.Entitlement{}, ErrMCPRequiresPaid
	}
	return entitlement, nil
}

func validateMCPDraft(draftID, body string) error {
	if draftID == "" {
		return errors.New("draft ID is required")
	}
	if utf8.RuneCountInString(body) > MaxDraftBodyRunes {
		return draftDomain.ErrDraftBodyTooLong
	}
	return nil
}
