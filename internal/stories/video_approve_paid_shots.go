package stories

import (
	"context"
	"fmt"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

type approvePaidShotsStory struct {
	deps StudioDeps
}

func NewApprovePaidShotsStory(deps StudioDeps) (videostudioDomain.ApprovePaidShotsStory, error) {
	if err := requireDeps("approve paid shots story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs}); err != nil {
		return nil, err
	}
	return &approvePaidShotsStory{deps: deps}, nil
}

// ApprovePaidShots answers the question asked only above the per-video cap: "Go" makes the voice-over and the video
// with the scenario's shots; "Keep to <cap>" asks the producer to revise the scenario within the cap.
func (s *approvePaidShotsStory) ApprovePaidShots(ctx context.Context, input videostudioDomain.ApprovePaidShotsInput) (videostudioDomain.VideoRequest, error) {
	request, err := s.deps.Requests.ByID(ctx, input.MemberID, input.RequestID)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if request.Status != videostudioDomain.StatusNeedsPaidGo {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if !input.KeepToCap {
		request.State.PaidShotsGo = true
		if err := request.MoveTo(videostudioDomain.StatusVoicing); err != nil {
			return videostudioDomain.VideoRequest{}, err
		}
		return saveRequest(ctx, s.deps.Requests, request)
	}
	if err := request.MoveTo(videostudioDomain.StatusWritingScenario); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	change := fmt.Sprintf("Use at most %d paid AI shots.", s.deps.Limits.PaidShotsPerVideo)
	in := jobInput{Idea: request.Idea, Change: change, Scenario: toProducerScenario(request.Scenario)}
	if err := enqueueJob(ctx, s.deps.Jobs, saved, videostudioDomain.JobReviseScenario, encodeJobInput(in)); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	return saved, nil
}
