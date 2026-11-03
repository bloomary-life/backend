package v0

import (
	"net/http"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type textLimitJSON struct {
	Counting string `json:"counting"`
	Limit    int    `json:"limit"`
}

type mediaRulesJSON struct {
	ContentTypes []string `json:"contentTypes"`
	MaxBytes     int64    `json:"maxBytes"`
	MinSeconds   float64  `json:"minSeconds"`
	MaxSeconds   float64  `json:"maxSeconds"`
	AspectMin    float64  `json:"aspectMin"`
	AspectMax    float64  `json:"aspectMax"`
	MaxWidth     int      `json:"maxWidth"`
	MaxHeight    int      `json:"maxHeight"`
}

type contentRulesJSON struct {
	Image           *mediaRulesJSON `json:"image"`
	Video           *mediaRulesJSON `json:"video"`
	Name            string          `json:"name"`
	ForbiddenChars  string          `json:"forbiddenChars"`
	MediaTransfer   string          `json:"mediaTransfer"`
	TextLimit       textLimitJSON   `json:"textLimit"`
	TitleLimit      textLimitJSON   `json:"titleLimit"`
	FoldAt          int             `json:"foldAt"`
	MaxHashtags     int             `json:"maxHashtags"`
	ReelsMaxSeconds float64         `json:"reelsMaxSeconds"`
	MediaRequired   bool            `json:"mediaRequired"`
}

func toMediaRules(rules *crosspostDomain.MediaRules) *mediaRulesJSON {
	if rules == nil {
		return nil
	}
	return &mediaRulesJSON{ContentTypes: nonNilStrings(rules.ContentTypes), MaxBytes: rules.MaxBytes, MinSeconds: rules.MinSeconds, MaxSeconds: rules.MaxSeconds,
		AspectMin: rules.AspectMin, AspectMax: rules.AspectMax, MaxWidth: rules.MaxWidth, MaxHeight: rules.MaxHeight}
}

// ContentRules: GET /api/content-rules serves crosspost.Rules, so the web counter uses the same limits and counting
// (cases: internal/domain/crosspost/testdata/text_counting.json). Zero = not checked.
func (s *service) ContentRules(w http.ResponseWriter, r *http.Request) {
	if s.acceptWebPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	networks := make(map[string]contentRulesJSON, len(crosspostDomain.Rules))
	for network, rules := range crosspostDomain.Rules {
		networks[string(network)] = contentRulesJSON{
			Image: toMediaRules(rules.Image), Video: toMediaRules(rules.Video), Name: rules.Name, ForbiddenChars: rules.ForbiddenChars,
			MediaTransfer: string(rules.MediaTransfer), TextLimit: textLimitJSON{Counting: string(rules.TextLimit.Counting), Limit: rules.TextLimit.Limit},
			TitleLimit: textLimitJSON{Counting: string(rules.TitleLimit.Counting), Limit: rules.TitleLimit.Limit},
			FoldAt:     rules.FoldAt, MaxHashtags: rules.MaxHashtags, ReelsMaxSeconds: rules.ReelsMaxSeconds, MediaRequired: rules.MediaRequired,
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, struct {
		Networks map[string]contentRulesJSON `json:"networks"`
	}{Networks: networks})
}
