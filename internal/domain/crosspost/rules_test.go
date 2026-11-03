package crosspost

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountMatchesSharedCases(t *testing.T) {
	// Verifies: BE-031
	raw, err := os.ReadFile("testdata/text_counting.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name     string   `json:"name"`
			Counting Counting `json:"counting"`
			Text     string   `json:"text"`
			Length   int      `json:"length"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, c := range fixture.Cases {
		require.Equal(t, c.Length, Count(c.Text, c.Counting), c.Name)
	}
}

func TestRulesRowsMatchPlatformLimits(t *testing.T) {
	// Verifies: BE-031
	require.Equal(t, TextLimit{Counting: CountRunes, Limit: 3000}, Rules[LinkedIn].TextLimit)
	require.Nil(t, Rules[LinkedIn].Video)
	require.Equal(t, TextLimit{Counting: CountXWeighted, Limit: 280}, Rules[X].TextLimit)
	require.Equal(t, TransferPull, Rules[FacebookPage].MediaTransfer)
	require.Equal(t, 90.0, Rules[FacebookPage].ReelsMaxSeconds)
	require.Equal(t, TextLimit{Counting: CountRunes, Limit: 2200}, Rules[Instagram].TextLimit)
	require.Equal(t, 30, Rules[Instagram].MaxHashtags)
	require.Equal(t, []string{"image/jpeg"}, Rules[Instagram].Image.ContentTypes)
	require.Equal(t, int64(8<<20), Rules[Instagram].Image.MaxBytes)
	require.Equal(t, 1440, Rules[Instagram].Image.MaxWidth)
	require.Equal(t, 900.0, Rules[Instagram].Video.MaxSeconds)
	require.Equal(t, int64(300<<20), Rules[Instagram].Video.MaxBytes)
	require.Equal(t, TextLimit{Counting: CountBytes, Limit: 5000}, Rules[YouTube].TextLimit)
	require.Equal(t, TextLimit{Counting: CountRunes, Limit: 100}, Rules[YouTube].TitleLimit)
	require.Nil(t, Rules[YouTube].Image)
	require.Equal(t, TextLimit{Counting: CountUTF16, Limit: 2200}, Rules[TikTok].TextLimit)
	require.Equal(t, 600.0, Rules[TikTok].Video.MaxSeconds)
	require.True(t, Rules[TikTok].MediaRequired)
}

func TestCheckAcceptsContentWithinEveryNetworkLimit(t *testing.T) {
	// Verifies: BE-031
	image := &MediaInfo{Kind: MediaImage, ContentType: "image/jpeg", Bytes: 1 << 20, Width: 1080, Height: 1350}
	video := &MediaInfo{Kind: MediaVideo, ContentType: "video/mp4", Bytes: 20 << 20, Seconds: 45, Width: 1080, Height: 1920}
	for network, media := range map[Network]*MediaInfo{LinkedIn: image, X: video, FacebookPage: video, Instagram: image, YouTube: video, TikTok: video} {
		problems, warnings := Rules[network].Check(ContentToCheck{Text: "A short post.", Title: "A title", Hashtags: []string{"writing"}, Media: media})
		require.Empty(t, problems, network)
		require.Empty(t, warnings, network)
	}
}

func TestCheckReportsSkipLines(t *testing.T) {
	// Verifies: BE-031
	problems, _ := Rules[X].Check(ContentToCheck{Text: strings.Repeat("a", 312)})
	require.Equal(t, []string{"Too long for X (312/280). Edit the X text."}, problems)

	problems, _ = Rules[YouTube].Check(ContentToCheck{Title: strings.Repeat("t", 112), Text: "a <b>", Media: &MediaInfo{Kind: MediaVideo}})
	require.Equal(t, []string{"Too long for the YouTube title (112/100). Edit the title.", "The YouTube description can't contain < or >."}, problems)

	problems, _ = Rules[Instagram].Check(ContentToCheck{Text: "no media"})
	require.Equal(t, []string{"Instagram · skipped: needs an image or video"}, problems)
	problems, _ = Rules[TikTok].Check(ContentToCheck{Text: "no media"})
	require.Equal(t, []string{"TikTok · skipped: needs a video"}, problems)
	problems, _ = Rules[LinkedIn].Check(ContentToCheck{Text: "video", Media: &MediaInfo{Kind: MediaVideo}})
	require.Equal(t, []string{"LinkedIn · skipped: can't post a video"}, problems)

	problems, _ = Rules[Instagram].Check(ContentToCheck{Media: &MediaInfo{Kind: MediaImage, ContentType: "image/png", Width: 1920, Height: 1080}})
	require.Equal(t, []string{"Instagram · skipped: needs image/jpeg", "Instagram · skipped: the picture is wider than 1440 px"}, problems)

	_, warnings := Rules[FacebookPage].Check(ContentToCheck{Media: &MediaInfo{Kind: MediaVideo, Seconds: 120}})
	require.Equal(t, []string{"Facebook · posted as a Page video (Reels must be 90 s or shorter)"}, warnings)
}

func TestComposeTextPutsHashtagsOnTheirOwnLine(t *testing.T) {
	// Verifies: BE-031
	require.Equal(t, "Post\n\n#one #two", ComposeText("Post\n", []string{"one", "#two"}))
	require.Equal(t, "Post", ComposeText("Post", nil))
}

func TestProposePlainTextPerNetwork(t *testing.T) {
	// Verifies: BE-032
	bold := "\U0001D5D5\U0001D5FC\U0001D5F9\U0001D5F1" // sans-serif bold "Bold"
	italicH := "ℎ"                                     // italic small h
	draft := DraftContent{Title: "My \U0001D400 title", Text: bold + " u̲n̶ " + italicH + "\n\nSecond paragraph."}

	text, title := Propose(LinkedIn, draft)
	require.Equal(t, draft.Text, text)
	require.Empty(t, title)

	text, _ = Propose(FacebookPage, draft)
	require.Equal(t, "Bold un h\n\nSecond paragraph.", text)

	text, _ = Propose(X, draft)
	require.Equal(t, "Bold un h", text)

	long := strings.Repeat("word ", 40) + "end. " + strings.Repeat("more ", 40)
	text, _ = Propose(X, DraftContent{Text: long})
	require.Equal(t, strings.Repeat("word ", 40)+"end.", text)

	text, title = Propose(YouTube, draft)
	require.Equal(t, "Bold un h\n\nSecond paragraph.", text)
	require.Equal(t, "My A title", title)
}
