package stories

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"

	"golang.org/x/text/unicode/norm"
)

var ErrMCPDraftNotFound = errors.New("MCP draft not found")
var ErrMCPDraftExists = errors.New("MCP draft ID already exists")
var ErrMCPDraftNotEditable = errors.New("MCP can update unpublished drafts only")
var ErrMCPRequiresPaid = errors.New("active paid plan required for MCP")
var ErrMCPDraftHasLink = errors.New("links are not allowed in posts: remove every URL (http://, https://, www. or a domain) and send the text again")

// draftSchemePattern finds a link written with a scheme anywhere (also "https ://", "hxxps://", "//host.") or www.
// It runs after NFKC folds fullwidth letters and dot look-alikes to ASCII.
var draftSchemePattern = regexp.MustCompile(`(?i)h[tx]{2}ps?\s*:\s*//|(^|[^\p{L}\p{N}])//[\p{L}\p{N}-]+\.|\bwww\.`)

// draftDomainPattern finds a domain with a known TLD, with or without a path (lumen.observer, youtu.be/x,
// kat.substack.com, kat.net). The label before the TLD needs a letter, so a version ("2.0.app") is not a domain.
// Group 2 is the TLD, group 3 what follows it.
var draftDomainPattern = regexp.MustCompile(`(?i)([\p{L}\p{N}-]+\.)*[\p{L}\p{N}-]*\p{L}[\p{L}\p{N}-]*\.(` + draftLinkTLDs + `)($|[^\p{L}\p{N}-])`)

// draftLinkTLDs are top-level domains common in links people share (gTLDs, link shorteners, EU ccTLDs).
const draftLinkTLDs = "com|org|io|ai|dev|app|co|eu|uk|cy|me|ly|gl|gle|gg|gy|in|to|so|xyz|info|link|page|site|tech|life|observer|" +
	"be|ee|de|fr|ru|us|nl|es|it|ch|at|se|no|fi|dk|pl|pt|gr|ie|cz|ro|hu|bg|hr|sk|si|lt|lv|ua|il|tr|ca|au|nz|jp|cn|kr|br|mx|ar|za|" +
	"tv|fm|sh|cc|ws|im|vc|gd|tt|biz|blog|news|online|store|shop|club|work|jobs|careers|email|agency|studio|space|website|live|world|" +
	"today|one|pro|net"

// allowedTechNames look like domains but are product or library names.
var allowedTechNames = strings.NewReplacer("socket.io", "socket io", "Socket.io", "Socket io", "Socket.IO", "Socket IO",
	"ASP.NET", "ASP NET", "ADO.NET", "ADO NET", "VB.NET", "VB NET", "Microsoft.NET", "Microsoft NET", "Microsoft.net", "Microsoft net")

// hasDraftLink reports whether a post contains a link. A capitalised word right after a full stop with no space
// ("Cyprus.It was hard", "here.So what") is a new sentence, not a TLD, unless a path follows it.
func hasDraftLink(body string) bool {
	text := allowedTechNames.Replace(norm.NFKC.String(body))
	if draftSchemePattern.MatchString(text) {
		return true
	}
	for _, m := range draftDomainPattern.FindAllStringSubmatchIndex(text, -1) {
		tld, after := text[m[4]:m[5]], text[m[6]:m[7]]
		if !sentenceStart(tld) || after == "/" {
			return true
		}
	}
	return false
}

// sentenceStart is true for a capitalised word ("It", "So"), the way a sentence starts; "COM" and "com" are not.
func sentenceStart(word string) bool {
	first, size := utf8.DecodeRuneInString(word)
	rest := word[size:]
	return unicode.IsUpper(first) && rest != "" && rest == strings.ToLower(rest)
}

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
	if hasDraftLink(body) {
		return ErrMCPDraftHasLink
	}
	return nil
}
