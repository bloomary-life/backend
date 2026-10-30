package stories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"

	"golang.org/x/text/unicode/norm"
)

var ErrMCPDraftNotFound = errors.New("MCP draft not found")
var ErrMCPDraftExists = errors.New("MCP draft ID already exists")
var ErrMCPDraftNotEditable = errors.New("MCP can update unpublished drafts only")
var ErrMCPRequiresPaid = errors.New("active paid plan required for MCP")
var ErrMCPDraftHasLink = errors.New("links are not allowed in posts: remove every URL (http://, https://, www. or a domain with a path) and send the text again")

// draftLinkPattern finds a link in a post (after NFKC folds fullwidth letters and dot look-alikes to ASCII):
// a scheme anywhere (also "https ://", "hxxps://", "//host."), www., or a domain with a known TLD, with or
// without a path (lumen.observer, youtu.be/x, kat.substack.com). ".net" and ".js" are left out of the TLDs, so
// tech names such as "Microsoft.net/Java" and "Node.js" stay allowed.
var draftLinkPattern = regexp.MustCompile(`(?i)h[tx]{2}ps?\s*:\s*//|(^|[^\p{L}\p{N}])//[\p{L}\p{N}-]+\.|\bwww\.|[\p{L}\p{N}-]+(\.[\p{L}\p{N}-]+)*\.(` + draftLinkTLDs + `)($|[^\p{L}\p{N}-])`)

// draftLinkTLDs are top-level domains common in links people share (gTLDs, link shorteners, EU ccTLDs).
const draftLinkTLDs = "com|org|io|ai|dev|app|co|eu|uk|cy|me|ly|gl|gle|gg|gy|in|to|so|xyz|info|link|page|site|tech|life|observer|" +
	"be|ee|de|fr|ru|us|nl|es|it|ch|at|se|no|fi|dk|pl|pt|gr|ie|cz|ro|hu|bg|hr|sk|si|lt|lv|ua|il|tr|ca|au|nz|jp|cn|kr|br|mx|ar|za|" +
	"tv|fm|sh|cc|ws|im|vc|gd|tt|biz|blog|news|online|store|shop|club|work|jobs|careers|email|agency|studio|space|website|live|world|" +
	"today|one|pro"

// allowedTechNames look like domains but are product or library names.
var allowedTechNames = strings.NewReplacer("socket.io", "socket io", "Socket.io", "Socket io", "Socket.IO", "Socket IO", "ASP.NET", "ASP NET")

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
	if draftLinkPattern.MatchString(allowedTechNames.Replace(norm.NFKC.String(body))) {
		return ErrMCPDraftHasLink
	}
	return nil
}
