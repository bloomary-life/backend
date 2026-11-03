package stories

import (
	"context"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

type pauseRows map[crosspostDomain.Network]bool

func (p pauseRows) Paused(_ context.Context, network crosspostDomain.Network) (bool, error) {
	return p[network] || p[crosspostDomain.AllNetworks], nil
}

func (p pauseRows) SetPaused(_ context.Context, network crosspostDomain.Network, paused bool, _ string, _ time.Time) error {
	p[network] = paused
	return nil
}

func TestPublishGuardPauseFromDBAndEnv(t *testing.T) {
	// Verifies: BE-040
	ctx := context.Background()
	rows := pauseRows{crosspostDomain.X: true}
	guard, err := NewPublishGuard(rows, PublishGuardConfig{})
	require.NoError(t, err)
	paused, err := guard.Paused(ctx, crosspostDomain.X)
	require.NoError(t, err)
	require.True(t, paused)
	paused, err = guard.Paused(ctx, crosspostDomain.LinkedIn)
	require.NoError(t, err)
	require.False(t, paused)

	envGuard, err := NewPublishGuard(pauseRows{}, PublishGuardConfig{EnvPaused: true})
	require.NoError(t, err)
	paused, err = envGuard.Paused(ctx, crosspostDomain.LinkedIn)
	require.NoError(t, err)
	require.True(t, paused)
}

func TestPublishGuardDecide(t *testing.T) {
	// Verifies: BE-040
	ctx := context.Background()
	delivery := crosspostDomain.Delivery{DestinationID: "dest-test"}

	unset, err := NewPublishGuard(pauseRows{}, PublishGuardConfig{})
	require.NoError(t, err)
	decision, err := unset.Decide(ctx, delivery)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.DecisionSimulate, decision)

	live, err := NewPublishGuard(pauseRows{}, PublishGuardConfig{Mode: crosspostDomain.ModeLive, LiveDestinationIDs: []string{"dest-other"}})
	require.NoError(t, err)
	decision, err = live.Decide(ctx, delivery)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.DecisionPublish, decision, "live posts every destination; the list is not used")

	emptyAllowlist, err := NewPublishGuard(pauseRows{}, PublishGuardConfig{Mode: crosspostDomain.ModeLiveAllowlist})
	require.NoError(t, err)
	decision, err = emptyAllowlist.Decide(ctx, delivery)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.DecisionSimulate, decision, "an empty allowlist posts nothing for real")

	allowlisted, err := NewPublishGuard(pauseRows{}, PublishGuardConfig{Mode: crosspostDomain.ModeLiveAllowlist, LiveDestinationIDs: []string{"dest-test"}})
	require.NoError(t, err)
	decision, err = allowlisted.Decide(ctx, delivery)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.DecisionPublish, decision)
	decision, err = allowlisted.Decide(ctx, crosspostDomain.Delivery{DestinationID: "dest-other"})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.DecisionSimulate, decision)
}
