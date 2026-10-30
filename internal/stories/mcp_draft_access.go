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

// allowedTechNames look like domains but are product or library names, in any letter case ("ASP.NET",
// "Microsoft.net/Java", "socket.io"). Their dot becomes a space before the domain check.
var allowedTechNames = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}.@/-])(socket\.io|(?:asp|ado|vb|microsoft)\.net)($|[^\p{L}\p{N}.-])`)

// dotLookalikes are full-stop look-alikes that NFKC leaves alone (U+3002 "。", which U+FF61 and U+FE12 fold to, and
// others); they count as a dot in a domain.
var dotLookalikes = strings.NewReplacer("\u3002", ".", "\u2E3C", ".", "\u06D4", ".", "\u0701", ".", "\u0702", ".",
	"\uA4F8", ".", "\uA60E", ".", "\u00B7", ".", "\u2027", ".", "\u2219", ".", "\u22C5", ".", "\u2E31", ".", "\u30FB", ".")

// sentenceTLDs are TLDs that are also short words that start a sentence ("Cyprus.It was", "here.So what",
// "Berlin.De facto"). Only these may be read as a new sentence; "Kat.Net is", "Youtu.Be sure" are links.
var sentenceTLDs = map[string]bool{"it": true, "so": true, "in": true, "to": true, "no": true, "at": true, "us": true, "de": true, "one": true, "today": true}

// hasDraftLink reports whether a post contains a link: a scheme or www., or a domain with a known TLD in any letter
// case. One exception reads a match as two sentences: a single word, a full stop, then a capitalised sentence word
// from sentenceTLDs, a space and a lowercase letter or digit ("I moved to Cyprus.It was hard"). A multi-label name
// (Kat.Substack.Com), an e-mail domain (kat@Gmail.Com), a path, a query or any other ending is a link.
func hasDraftLink(body string) bool {
	text := dotLookalikes.Replace(norm.NFKC.String(body))
	for i := 0; i < 3; i++ { // adjacent names share a boundary character, so replace until stable
		next := allowedTechNames.ReplaceAllStringFunc(text, func(m string) string { return strings.Replace(m, ".", " ", 1) })
		if next == text {
			break
		}
		text = next
	}
	if draftSchemePattern.MatchString(text) {
		return true
	}
	for _, m := range draftDomainPattern.FindAllStringSubmatchIndex(text, -1) {
		if !twoSentences(text, m) {
			return true
		}
	}
	return false
}

// twoSentences applies the sentence exception to one draftDomainPattern match (indexes from FindAllStringSubmatchIndex).
func twoSentences(text string, m []int) bool {
	name, tld := text[m[0]:m[4]-1], text[m[4]:m[5]]
	if strings.Contains(name, ".") || !sentenceStart(tld) || !sentenceTLDs[strings.ToLower(tld)] {
		return false
	}
	if before, _ := utf8.DecodeLastRuneInString(text[:m[0]]); before == '@' {
		return false
	}
	space, size := utf8.DecodeRuneInString(text[m[6]:])
	if m[6] == m[7] || !unicode.IsSpace(space) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(text[m[6]+size:])
	return unicode.IsLower(next) || unicode.IsDigit(next)
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
