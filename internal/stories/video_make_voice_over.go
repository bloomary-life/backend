package stories

import (
	"context"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/platform/logger"
)

type makeVoiceOverStory struct {
	deps  StudioDeps
	voice videostudioDomain.VoiceOver
}

func NewMakeVoiceOverStory(deps StudioDeps, voice videostudioDomain.VoiceOver) (videostudioDomain.MakeVoiceOverStory, error) {
	if err := requireDeps("make voice-over story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "media": deps.Media, "notifier": deps.Notifier, "voice-over": voice}); err != nil {
		return nil, err
	}
	return &makeVoiceOverStory{deps: deps, voice: voice}, nil
}

// MakeVoiceOver (the studio worker, never the Telegram handler) speaks the scenario's voice-over text with Gemini TTS,
// then queues render_video with the audio's key; a failure tells Kat with Retry.
func (s *makeVoiceOverStory) MakeVoiceOver(ctx context.Context, memberID, requestID string) error {
	request, err := s.deps.Requests.ByID(ctx, memberID, requestID)
	if err != nil {
		return err
	}
	if request.Status != videostudioDomain.StatusVoicing {
		return nil
	}
	key, err := s.voice.Speak(ctx, request.Scenario.VoiceOverText)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("studio_voice_over_failed")
		request.State.FailedKind, request.State.Failure = videostudioDomain.StepVoiceOver, "the voice-over service didn't answer"
		if moveErr := request.MoveTo(videostudioDomain.StatusFailed); moveErr != nil {
			return moveErr
		}
		saved, saveErr := saveRequest(ctx, s.deps.Requests, request)
		if saveErr != nil {
			return saveErr
		}
		notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventFailed)
		return nil
	}
	previous := request.VoiceOverKey
	request.VoiceOverKey = key
	if err := request.MoveTo(videostudioDomain.StatusRendering); err != nil {
		return err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		// Cancelled or discarded meanwhile: the new audio isn't used.
		_ = s.deps.Media.Delete(ctx, key)
		return err
	}
	if previous != "" && previous != key {
		_ = s.deps.Media.Delete(ctx, previous)
	}
	upTo := s.deps.Limits.PaidShotsPerVideo
	if request.State.PaidShotsGo {
		upTo = request.Scenario.PaidShots
	}
	in := jobInput{Idea: request.Idea, Scenario: toProducerScenario(request.Scenario), PaidShotsUpTo: upTo}
	return enqueueJob(ctx, s.deps.Jobs, saved, videostudioDomain.JobRenderVideo, encodeJobInput(in))
}
