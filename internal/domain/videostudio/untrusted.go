package videostudio

import (
	"regexp"
	"strings"
	"unicode"
)

// Everything the producer submits is untrusted (security.md §3 row 6): invisible and direction-control characters
// are removed; URLs and @mentions are listed separately for Kat; caption URLs stay only when they are in Kat's idea.

var (
	urlPattern     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"]+`)
	mentionPattern = regexp.MustCompile(`(?:^|[^\w@])(@[A-Za-z0-9_.]{2,})`)
)

// CleanText removes format characters (zero-width, bidi embeddings, overrides and isolates, BOM) and other control
// characters except new lines and tabs.
func CleanText(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.Is(unicode.Cf, r), unicode.IsControl(r):
			return -1
		}
		return r
	}, text)
}

// LinksAndMentions lists every URL and @mention in the texts once, in order.
func LinksAndMentions(texts ...string) []string {
	var found []string
	seen := map[string]bool{}
	add := func(item string) {
		item = strings.TrimRight(item, ".,;:!?)")
		if item != "" && !seen[item] {
			seen[item] = true
			found = append(found, item)
		}
	}
	for _, text := range texts {
		for _, link := range urlPattern.FindAllString(text, -1) {
			add(link)
		}
		for _, match := range mentionPattern.FindAllStringSubmatch(text, -1) {
			add(match[1])
		}
	}
	return found
}

// KeepIdeaURLs drops every URL from a caption that isn't in Kat's idea.
func KeepIdeaURLs(caption, idea string) string {
	cleaned := urlPattern.ReplaceAllStringFunc(caption, func(link string) string {
		if strings.Contains(idea, strings.TrimRight(link, ".,;:!?)")) {
			return link
		}
		return ""
	})
	if cleaned == caption {
		return caption
	}
	lines := strings.Split(cleaned, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// CleanScenario cleans every text field of a submitted scenario; captions keep only the idea's URLs.
func CleanScenario(s Scenario, idea string) Scenario {
	s.Title, s.Hook, s.Script = CleanText(s.Title), CleanText(s.Hook), CleanText(s.Script)
	s.VoiceOverText, s.ShotList, s.SelfReview = CleanText(s.VoiceOverText), CleanText(s.ShotList), CleanText(s.SelfReview)
	s.YouTube = YouTubeText{Title: CleanText(s.YouTube.Title), Description: KeepIdeaURLs(CleanText(s.YouTube.Description), idea), Chapters: CleanText(s.YouTube.Chapters)}
	captions := make(map[string]string, len(s.Captions))
	for network, caption := range s.Captions {
		captions[CleanText(network)] = KeepIdeaURLs(CleanText(caption), idea)
	}
	s.Captions = captions
	unfixed := make([]string, 0, len(s.Unfixed))
	for _, line := range s.Unfixed {
		if line = strings.TrimSpace(CleanText(line)); line != "" {
			unfixed = append(unfixed, line)
		}
	}
	s.Unfixed = unfixed
	if s.PaidShots < 0 {
		s.PaidShots = 0
	}
	return s
}
