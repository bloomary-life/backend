package crosspost

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeliveryValidTransitions(t *testing.T) {
	// Verifies: BE-033
	for from, targets := range deliveryTransitions {
		for _, to := range targets {
			d := Delivery{Status: from}
			require.NoError(t, d.MoveTo(to), "%s → %s", from, to)
			require.Equal(t, to, d.Status)
		}
	}
	require.True(t, Delivery{Status: StatusScheduled}.Editable())
	for _, status := range FinalStatuses {
		require.True(t, status.Final())
	}
}

func TestParsePublishModeDefaultsToDryRun(t *testing.T) {
	// Verifies: BE-040
	require.Equal(t, ModeLive, ParsePublishMode("live"))
	require.Equal(t, ModeLiveAllowlist, ParsePublishMode("live_allowlist"))
	require.Equal(t, ModeDryRun, ParsePublishMode(""))
	require.Equal(t, ModeDryRun, ParsePublishMode("LIVE!"))
}

func TestCredentialsAreRedactedEverywhere(t *testing.T) {
	// Verifies: BE-035
	credentials := Credentials{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Unix(0, 0)}
	holder := struct{ C Credentials }{credentials}
	encoded, err := json.Marshal(holder)
	require.NoError(t, err)
	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "credentials", credentials)
	for _, out := range []string{
		fmt.Sprint(credentials), fmt.Sprintf("%v %+v %#v %s", credentials, credentials, credentials, credentials),
		fmt.Sprintf("%#v", holder), string(encoded), logged.String(),
	} {
		require.NotContains(t, out, "synthetic")
		require.Contains(t, out, "[redacted]")
	}
}

func TestXAILine(t *testing.T) {
	// Verifies: BE-067
	require.Equal(t, "Made with AI voice.", XAILine(AIFlags{Voice: true}))
	require.Equal(t, "Made with AI voice and visuals.", XAILine(AIFlags{Voice: true, RealisticShots: true}))
	require.Equal(t, "Made with AI voice and visuals.", XAILine(AIFlags{Voice: true, Music: true}))
	require.Empty(t, XAILine(AIFlags{}))
}
