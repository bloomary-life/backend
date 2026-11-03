package crosspost

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNextPostingTimeWithoutWindowsIsNow(t *testing.T) {
	// Verifies: BE-034
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	for _, network := range []Network{X, FacebookPage, Instagram, YouTube, TikTok} {
		require.Equal(t, now, NextPostingTime(network, now, time.UTC, nil))
	}
}

func TestNextPostingTimeInAudienceZone(t *testing.T) {
	// Verifies: BE-034
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	PostingWindows[X] = NetworkWindows{Windows: []PostingWindow{
		{Weekday: time.Tuesday, StartHour: 12, EndHour: 18},
		{Weekday: time.Wednesday, StartHour: 12, EndHour: 18},
	}}
	t.Cleanup(func() { delete(PostingWindows, X) })

	// Tue 3 Nov 2026, 09:00 in New York: today's window opens at 12:00 there.
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, newYork)
	require.Equal(t, time.Date(2026, 11, 3, 12, 0, 0, 0, newYork), NextPostingTime(X, now, newYork, nil))

	// Inside the open window: now.
	inside := time.Date(2026, 11, 3, 13, 30, 0, 0, newYork)
	require.Equal(t, inside, NextPostingTime(X, inside, newYork, nil))

	// Tuesday already has a post: Wednesday's window.
	taken := []time.Time{time.Date(2026, 11, 3, 17, 0, 0, 0, newYork).UTC()}
	require.Equal(t, time.Date(2026, 11, 4, 12, 0, 0, 0, newYork), NextPostingTime(X, now, newYork, taken))

	// Every window this week taken: the next window's start anyway.
	taken = append(taken, time.Date(2026, 11, 4, 12, 0, 0, 0, newYork), time.Date(2026, 11, 10, 12, 0, 0, 0, newYork))
	require.Equal(t, time.Date(2026, 11, 4, 12, 0, 0, 0, newYork), NextPostingTime(X, now, newYork, taken))
}
