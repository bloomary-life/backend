package crosspost

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Counting is how a network counts text length.
type Counting string

const (
	CountRunes     Counting = "runes"
	CountXWeighted Counting = "x_weighted"
	CountUTF16     Counting = "utf16"
	CountBytes     Counting = "bytes"
)

// TextLimit is a length limit and how it is counted; Limit 0 = no limit known.
type TextLimit struct {
	Counting Counting
	Limit    int
}

type Transfer string

const (
	TransferPull Transfer = "pull" // the network fetches a signed URL
	TransferPush Transfer = "push" // Bloomary uploads the bytes
)

// MediaRules limit one kind of media. Zero values are not checked (no sourced number).
type MediaRules struct {
	ContentTypes []string
	MaxBytes     int64
	MinSeconds   float64
	MaxSeconds   float64
	AspectMin    float64 // width / height
	AspectMax    float64
	MaxWidth     int
	MaxHeight    int
}

// ContentRules hold one network's limits (platforms.md Table 2). Image or Video nil = that kind isn't accepted.
type ContentRules struct {
	Image          *MediaRules
	Video          *MediaRules
	Name           string // as members read it: "X", "YouTube"
	ForbiddenChars string // in text and title
	MediaTransfer  Transfer
	TextLimit      TextLimit
	TitleLimit     TextLimit
	FoldAt         int // warning only; 0 until checked on live posts
	MaxHashtags    int
	// ReelsMaxSeconds: a longer video still posts, as a Page video, with a warning.
	ReelsMaxSeconds float64
	MediaRequired   bool
}

// Rules per network. Values: platforms.md Table 2 (docs checked 2026-11-03). Numbers marked UNVERIFIED there are left at zero.
var Rules = map[Network]ContentRules{
	LinkedIn: {
		Name:          "LinkedIn",
		TextLimit:     TextLimit{Counting: CountRunes, Limit: 3000},
		Image:         &MediaRules{}, // video posts skip LinkedIn (no Community Management API)
		MediaTransfer: TransferPush,
	},
	X: {
		Name:      "X",
		TextLimit: TextLimit{Counting: CountXWeighted, Limit: 280},
		Image:     &MediaRules{},
		// 32×32 – 1280×1024 for non-Premium, yet 720×1280 recommended: dimensions are left to the variant until tested.
		Video:         &MediaRules{},
		MediaTransfer: TransferPush,
	},
	FacebookPage: {
		Name:            "Facebook",
		Image:           &MediaRules{},
		Video:           &MediaRules{MinSeconds: 3},
		ReelsMaxSeconds: 90,
		MediaTransfer:   TransferPull,
	},
	Instagram: {
		Name:        "Instagram",
		TextLimit:   TextLimit{Counting: CountRunes, Limit: 2200},
		MaxHashtags: 30,
		Image: &MediaRules{
			ContentTypes: []string{"image/jpeg"}, MaxBytes: 8 << 20,
			AspectMin: 4.0 / 5.0, AspectMax: 1.91, MaxWidth: 1440,
		},
		Video:         &MediaRules{MinSeconds: 3, MaxSeconds: 15 * 60, MaxBytes: 300 << 20},
		MediaRequired: true,
		MediaTransfer: TransferPull,
	},
	YouTube: {
		Name:           "YouTube",
		TextLimit:      TextLimit{Counting: CountBytes, Limit: 5000},
		TitleLimit:     TextLimit{Counting: CountRunes, Limit: 100},
		ForbiddenChars: "<>",
		Video:          &MediaRules{},
		MediaRequired:  true,
		MediaTransfer:  TransferPush,
	},
	TikTok: {
		Name:          "TikTok",
		TextLimit:     TextLimit{Counting: CountUTF16, Limit: 2200},
		Video:         &MediaRules{MaxSeconds: 10 * 60},
		MediaRequired: true,
		MediaTransfer: TransferPush,
	},
}

// ContentToCheck is one network's post before or after its media is frozen.
type ContentToCheck struct {
	Media    *MediaInfo
	Text     string
	Title    string
	Hashtags []string
}

// ComposeText is the text a network receives: the text, then the hashtags on their own line.
func ComposeText(text string, hashtags []string) string {
	if len(hashtags) == 0 {
		return text
	}
	tags := make([]string, 0, len(hashtags))
	for _, tag := range hashtags {
		tags = append(tags, "#"+strings.TrimPrefix(tag, "#"))
	}
	return strings.TrimRight(text, "\n ") + "\n\n" + strings.Join(tags, " ")
}

// Check returns problems (the delivery is skipped) and warnings (kept and shown) as lines from ux.md Table 2.
func (r ContentRules) Check(c ContentToCheck) (problems, warnings []string) {
	text := ComposeText(c.Text, c.Hashtags)
	if n := Count(text, r.TextLimit.Counting); r.TextLimit.Limit > 0 && n > r.TextLimit.Limit {
		if r.TitleLimit.Limit > 0 {
			problems = append(problems, fmt.Sprintf("Too long for the %s description (%d/%d). Edit the description.", r.Name, n, r.TextLimit.Limit))
		} else {
			problems = append(problems, fmt.Sprintf("Too long for %s (%d/%d). Edit the %s text.", r.Name, n, r.TextLimit.Limit, r.Name))
		}
	}
	if n := Count(c.Title, r.TitleLimit.Counting); r.TitleLimit.Limit > 0 && n > r.TitleLimit.Limit {
		problems = append(problems, fmt.Sprintf("Too long for the %s title (%d/%d). Edit the title.", r.Name, n, r.TitleLimit.Limit))
	}
	if r.ForbiddenChars != "" {
		if strings.ContainsAny(c.Title, r.ForbiddenChars) {
			problems = append(problems, fmt.Sprintf("The %s title can't contain %s.", r.Name, orList(r.ForbiddenChars)))
		}
		if strings.ContainsAny(text, r.ForbiddenChars) {
			problems = append(problems, fmt.Sprintf("The %s description can't contain %s.", r.Name, orList(r.ForbiddenChars)))
		}
	}
	if r.MaxHashtags > 0 && len(c.Hashtags) > r.MaxHashtags {
		problems = append(problems, fmt.Sprintf("Too many hashtags for %s (%d/%d).", r.Name, len(c.Hashtags), r.MaxHashtags))
	}
	mediaProblems, mediaWarnings := r.checkMedia(c.Media)
	return append(problems, mediaProblems...), append(warnings, mediaWarnings...)
}

func (r ContentRules) checkMedia(media *MediaInfo) (problems, warnings []string) {
	skipped := r.Name + " · skipped: "
	if media == nil {
		if r.MediaRequired {
			problems = append(problems, skipped+r.needsMedia())
		}
		return problems, warnings
	}
	rules := r.Image
	if media.Kind == MediaVideo {
		rules = r.Video
	}
	if rules == nil {
		if media.Kind == MediaVideo {
			return append(problems, skipped+"can't post a video"), warnings
		}
		return append(problems, skipped+r.needsMedia()), warnings
	}
	if len(rules.ContentTypes) > 0 && !contains(rules.ContentTypes, media.ContentType) {
		problems = append(problems, skipped+"needs "+strings.Join(rules.ContentTypes, " or "))
	}
	if rules.MaxBytes > 0 && media.Bytes > rules.MaxBytes {
		problems = append(problems, fmt.Sprintf("%sthe file is over %d MB", skipped, rules.MaxBytes>>20))
	}
	if rules.MinSeconds > 0 && media.Seconds < rules.MinSeconds {
		problems = append(problems, fmt.Sprintf("%sthe video is shorter than %g s", skipped, rules.MinSeconds))
	}
	if rules.MaxSeconds > 0 && media.Seconds > rules.MaxSeconds {
		problems = append(problems, fmt.Sprintf("%sthe video is longer than %g s", skipped, rules.MaxSeconds))
	}
	if media.Height > 0 && (rules.AspectMin > 0 || rules.AspectMax > 0) {
		aspect := float64(media.Width) / float64(media.Height)
		if (rules.AspectMin > 0 && aspect < rules.AspectMin) || (rules.AspectMax > 0 && aspect > rules.AspectMax) {
			problems = append(problems, skipped+"the picture's shape doesn't fit")
		}
	}
	if rules.MaxWidth > 0 && media.Width > rules.MaxWidth {
		problems = append(problems, fmt.Sprintf("%sthe picture is wider than %d px", skipped, rules.MaxWidth))
	}
	if rules.MaxHeight > 0 && media.Height > rules.MaxHeight {
		problems = append(problems, fmt.Sprintf("%sthe picture is taller than %d px", skipped, rules.MaxHeight))
	}
	if media.Kind == MediaVideo && r.ReelsMaxSeconds > 0 && media.Seconds > r.ReelsMaxSeconds {
		warnings = append(warnings, fmt.Sprintf("%s · posted as a Page video (Reels must be %g s or shorter)", r.Name, r.ReelsMaxSeconds))
	}
	return problems, warnings
}

func (r ContentRules) needsMedia() string {
	if r.Image != nil && r.Video != nil {
		return "needs an image or video"
	}
	if r.Video != nil {
		return "needs a video"
	}
	return "needs an image"
}

func orList(chars string) string {
	parts := strings.Split(chars, "")
	return strings.Join(parts, " or ")
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// Count measures text the way a network does. Text is NFC-normalised first (X counts NFC).
func Count(text string, counting Counting) int {
	text = norm.NFC.String(text)
	switch counting {
	case CountXWeighted:
		return xWeightedLength(text)
	case CountUTF16:
		return len(utf16.Encode([]rune(text)))
	case CountBytes:
		return len(text)
	default:
		return utf8.RuneCountInString(text)
	}
}

// Propose is the first text for a network: plain text for every network but LinkedIn (Unicode bold/italic from
// the editor turned back into plain letters); X = the first paragraph when it fits 280 weighted characters, else
// the first sentence; YouTube title from the draft title. Hashtags start empty.
func Propose(network Network, d DraftContent) (text, title string) {
	if network == LinkedIn {
		return d.Text, ""
	}
	plain := PlainText(d.Text)
	switch network {
	case X:
		paragraph := strings.TrimSpace(strings.SplitN(strings.TrimSpace(plain), "\n\n", 2)[0])
		if Count(paragraph, CountXWeighted) <= Rules[X].TextLimit.Limit {
			return paragraph, ""
		}
		return firstSentence(paragraph), ""
	case YouTube:
		return plain, PlainText(d.Title)
	default:
		return plain, ""
	}
}

// DraftContent is what Propose reads from a draft.
type DraftContent struct {
	Text  string
	Title string
}

// PlainText turns the editor's Unicode bold, italic and monospace letters back into plain letters and drops its
// underline and strike-through marks, as the web editor's "clear formatting" does.
func PlainText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == 0x0332 || r == 0x0336:
			continue
		case (r >= 0x1D400 && r <= 0x1D7FF) || r == 0x210E:
			b.WriteString(norm.NFKC.String(string(r)))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstSentence(text string) string {
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			next := i + utf8.RuneLen(r)
			if next == len(text) || text[next] == ' ' || text[next] == '\n' {
				return text[:next]
			}
		}
	}
	return text
}

// X weighted counting, per https://docs.x.com/fundamentals/counting-characters and twitter-text config v3:
// scale 100, default weight 200, weight 100 in the ranges below, every URL 23, every emoji sequence 2.
var xLightRanges = [][2]rune{{0, 4351}, {8192, 8205}, {8208, 8223}, {8242, 8247}}

const xURLLength = 23

func xWeightedLength(text string) int {
	runes := []rune(text)
	weight := 0
	for i := 0; i < len(runes); {
		if n := urlLength(runes[i:]); n > 0 && (i == 0 || !isURLRune(runes[i-1])) {
			weight += xURLLength * 100
			i += n
			continue
		}
		if n := emojiLength(runes[i:]); n > 0 {
			weight += 200
			i += n
			continue
		}
		weight += xRuneWeight(runes[i])
		i++
	}
	return (weight + 99) / 100
}

func xRuneWeight(r rune) int {
	for _, rng := range xLightRanges {
		if r >= rng[0] && r <= rng[1] {
			return 100
		}
	}
	return 200
}

// urlLength finds an http(s):// or www. URL at the start of runes. Bare domains ("example.com") are not detected.
func urlLength(runes []rune) int {
	head := strings.ToLower(string(runes[:min(len(runes), 8)]))
	start := 0
	switch {
	case strings.HasPrefix(head, "https://"):
		start = 8
	case strings.HasPrefix(head, "http://"):
		start = 7
	case strings.HasPrefix(head, "www."):
		start = 4
	default:
		return 0
	}
	end := start
	for end < len(runes) && isURLRune(runes[end]) {
		end++
	}
	for end > start && strings.ContainsRune(".,;:!?)'\"", runes[end-1]) {
		end--
	}
	if end == start {
		return 0
	}
	return end
}

func isURLRune(r rune) bool {
	return r > ' ' && r < 0x7F && !strings.ContainsRune(`"<>`, r)
}

// emojiLength returns how many runes the emoji sequence at the start of runes takes (0 = not an emoji).
func emojiLength(runes []rune) int {
	first := runes[0]
	switch {
	case isRegionalIndicator(first):
		if len(runes) > 1 && isRegionalIndicator(runes[1]) {
			return 2
		}
		return 1
	case (first >= '0' && first <= '9') || first == '#' || first == '*':
		if len(runes) > 2 && runes[1] == 0xFE0F && runes[2] == 0x20E3 {
			return 3
		}
		if len(runes) > 1 && runes[1] == 0x20E3 {
			return 2
		}
		return 0
	case isTextDefaultEmoji(first):
		if len(runes) < 2 || runes[1] != 0xFE0F {
			return 0
		}
	case !isPictograph(first):
		return 0
	}
	n := 1
	for n < len(runes) {
		r := runes[n]
		switch {
		case r == 0xFE0F || r == 0xFE0E || r == 0x20E3 || (r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0xE0020 && r <= 0xE007F):
			n++
		case r == 0x200D && n+1 < len(runes) && isPictograph(runes[n+1]):
			n += 2
		default:
			return n
		}
	}
	return n
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isPictograph(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || (r >= 0x2300 && r <= 0x23FF) ||
		(r >= 0x2B00 && r <= 0x2BFF) || r == 0x3030 || r == 0x303D || r == 0x3297 || r == 0x3299
}

// isTextDefaultEmoji: symbols (© ® ™ ‼ ⁉ ℹ arrows) that are emoji only with the emoji variation selector U+FE0F.
func isTextDefaultEmoji(r rune) bool {
	return r == 0x00A9 || r == 0x00AE || r == 0x203C || r == 0x2049 || r == 0x2122 || r == 0x2139 || (r >= 0x2194 && r <= 0x21AA)
}
