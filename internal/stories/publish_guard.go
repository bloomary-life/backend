package stories

import (
	"context"
	"fmt"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

// PublishGuardConfig comes from BLOOMARY_PUBLISHING_PAUSED, BLOOMARY_PUBLISH_MODE and BLOOMARY_LIVE_DESTINATION_IDS.
type PublishGuardConfig struct {
	Mode               crosspostDomain.PublishMode
	LiveDestinationIDs []string
	EnvPaused          bool
}

type publishGuard struct {
	pause     crosspostDomain.PauseStore
	live      map[string]bool
	mode      crosspostDomain.PublishMode
	envPaused bool
}

// NewPublishGuard: the pause switch is the DB row (per network or "all"), with the env flag as a fallback that
// pauses everything. Publishing is real in live mode; in live_allowlist mode only for the listed destinations
// (an empty list posts nothing for real); dry_run (the default) simulates everything.
func NewPublishGuard(pause crosspostDomain.PauseStore, config PublishGuardConfig) (crosspostDomain.PublishGuard, error) {
	if pause == nil {
		return nil, fmt.Errorf("publish guard: pause store is required")
	}
	live := make(map[string]bool, len(config.LiveDestinationIDs))
	for _, id := range config.LiveDestinationIDs {
		if id != "" {
			live[id] = true
		}
	}
	return &publishGuard{pause: pause, live: live, mode: crosspostDomain.ParsePublishMode(string(config.Mode)), envPaused: config.EnvPaused}, nil
}

func (g *publishGuard) Paused(ctx context.Context, network crosspostDomain.Network) (bool, error) {
	if g.envPaused {
		return true, nil
	}
	return g.pause.Paused(ctx, network)
}

func (g *publishGuard) Decide(_ context.Context, delivery crosspostDomain.Delivery) (crosspostDomain.GuardDecision, error) {
	switch {
	case g.mode == crosspostDomain.ModeLive:
		return crosspostDomain.DecisionPublish, nil
	case g.mode == crosspostDomain.ModeLiveAllowlist && g.live[delivery.DestinationID]:
		return crosspostDomain.DecisionPublish, nil
	default:
		return crosspostDomain.DecisionSimulate, nil
	}
}
