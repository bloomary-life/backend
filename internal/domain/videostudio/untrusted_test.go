package videostudio

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScenarioTextIsCleanedAndCaptionURLsComeOnlyFromTheIdea(t *testing.T) {
	// Verifies: BE-107
	idea := "Talk about https://bloomary.life/guide please"
	scenario := CleanScenario(Scenario{
		Title:    "Ti​tle‮",
		Captions: map[string]string{"x": "Read https://bloomary.life/guide and https://evil.example/x now @someone"},
		YouTube:  YouTubeText{Description: "More at https://evil.example/y"},
		Unfixed:  []string{" ⁦ ", "music too loud"},
	}, idea)

	require.Equal(t, "Title", scenario.Title)
	require.Equal(t, "Read https://bloomary.life/guide and now @someone", scenario.Captions["x"])
	require.Equal(t, "More at", scenario.YouTube.Description)
	require.Equal(t, []string{"music too loud"}, scenario.Unfixed)
	require.Equal(t, []string{"https://bloomary.life/guide", "@someone"}, LinksAndMentions(scenario.Captions["x"]))
}

func TestVideoRequestMovesOnlyAlongItsSteps(t *testing.T) {
	// Verifies: BE-110
	request := VideoRequest{Status: StatusWaitingForIdea}
	for _, next := range []RequestStatus{StatusWritingScenario, StatusVoicing, StatusRendering, StatusVideoReady, StatusApproved, StatusDiscarded} {
		require.NoError(t, request.MoveTo(next), next)
	}
	cancelled := VideoRequest{Status: StatusRendering}
	require.True(t, cancelled.Working())
	require.NoError(t, cancelled.MoveTo(StatusCancelled))
	require.Equal(t, AIContent{}, VideoRequest{Scenario: Scenario{AIContent: AIContent{Voice: true}}, State: StudioState{AILabelOff: true}}.ConfirmedAI())
	require.False(t, JobRenderVideo.StartsWork())
	require.True(t, JobReviseCaptions.StartsWork())
}
