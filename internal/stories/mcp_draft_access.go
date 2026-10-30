package stories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
)

var ErrMCPDraftNotFound = errors.New("MCP draft not found")
var ErrMCPDraftExists = errors.New("MCP draft ID already exists")
var ErrMCPDraftNotEditable = errors.New("MCP can update unpublished drafts only")
var ErrMCPRequiresPaid = errors.New("active paid plan required for MCP")
var ErrMCPDraftHasLink = errors.New("links are not allowed in posts: remove every URL (http://, https://, www. or a domain with a path) and send the text again")

// draftLinkPattern finds a URL in a post: a scheme, www., or a bare domain with a path (lumen.observer/call).
// Bare domains need a known TLD, so "Node.js/Go" stays allowed.
var draftLinkPattern = regexp.MustCompile(`(?i)\bhttps?://|\bwww\.|\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|org|net|io|ai|dev|app|co|eu|uk|cy|me|ly|gl|gg|in|to|so|xyz|info|link|page|site|tech|life|observer|de|fr|ru|us)/\S`)

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
	if draftLinkPattern.MatchString(body) {
		return ErrMCPDraftHasLink
	}
	return nil
}
