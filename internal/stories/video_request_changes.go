package stories

import (
	"context"
	"strings"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

type requestChangesStory struct {
	deps StudioDeps
}

func NewRequestChangesStory(deps StudioDeps) (videostudioDomain.RequestChangesStory, error) {
	if err := requireDeps("request changes story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "publications": deps.Publications}); err != nil {
		return nil, err
	}
	return &requestChangesStory{deps: deps}, nil
}

// RequestChanges: a reply to the video (video_ready) revises the scenario and makes the video again; a reply to the
// text and times (approved video, text not approved yet) rewrites the captions. Both count toward the daily limit.
func (s *requestChangesStory) RequestChanges(ctx context.Context, input videostudioDomain.RequestChangesInput) (videostudioDomain.VideoRequest, error) {
	request, err := s.deps.Requests.ByID(ctx, input.MemberID, input.RequestID)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	change := truncateRunes(videostudioDomain.CleanText(input.Change), maxChangeRunes)
	if strings.TrimSpace(change) == "" {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	kind := videostudioDomain.JobReviseScenario
	switch request.Status {
	case videostudioDomain.StatusVideoReady:
		if err := request.MoveTo(videostudioDomain.StatusWritingScenario); err != nil {
			return videostudioDomain.VideoRequest{}, err
		}
	case videostudioDomain.StatusApproved:
		approved, err := textApproved(ctx, s.deps.Publications, request)
		if err != nil {
			return videostudioDomain.VideoRequest{}, err
		}
		if approved {
			return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
		}
		kind = videostudioDomain.JobReviseCaptions
	default:
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if err := checkDailyLimit(ctx, s.deps, input.MemberID, time.Now().UTC()); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	request.State.PaidShotsGo, request.State.Failure = false, ""
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	in := jobInput{Idea: request.Idea, Change: change, Scenario: toProducerScenario(request.Scenario)}
	if err := enqueueJob(ctx, s.deps.Jobs, saved, kind, encodeJobInput(in)); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	return saved, nil
}
