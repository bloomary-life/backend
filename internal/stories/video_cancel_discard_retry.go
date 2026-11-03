package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

type cancelVideoRequestStory struct {
	deps StudioDeps
}

func NewCancelVideoRequestStory(deps StudioDeps) (videostudioDomain.CancelVideoRequestStory, error) {
	if err := requireDeps("cancel video request story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs}); err != nil {
		return nil, err
	}
	return &cancelVideoRequestStory{deps: deps}, nil
}

// CancelVideoRequest stops the scenario, voice-over or render under way; the producer's next heartbeat gets 409.
func (s *cancelVideoRequestStory) CancelVideoRequest(ctx context.Context, memberID, requestID string) (videostudioDomain.VideoRequest, error) {
	request, err := s.deps.Requests.ByID(ctx, memberID, requestID)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if request.Status == videostudioDomain.StatusCancelled {
		return request, nil
	}
	if !request.Working() {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if err := s.deps.Jobs.Cancel(ctx, request.ID); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if err := request.MoveTo(videostudioDomain.StatusCancelled); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	return saveRequest(ctx, s.deps.Requests, request)
}

type discardVideoRequestStory struct {
	deps   StudioDeps
	cancel crosspostDomain.CancelDeliveryStory
}

func NewDiscardVideoRequestStory(deps StudioDeps, cancel crosspostDomain.CancelDeliveryStory) (videostudioDomain.DiscardVideoRequestStory, error) {
	if err := requireDeps("discard video request story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "publications": deps.Publications, "cancel delivery": cancel}); err != nil {
		return nil, err
	}
	return &discardVideoRequestStory{deps: deps, cancel: cancel}, nil
}

// DiscardVideoRequest drops the request at any step before the text is approved: running jobs stop, an unapproved
// publication's proposed posts are cancelled; the draft (if any) stays in Bloomary → Drafts. Nothing goes public.
func (s *discardVideoRequestStory) DiscardVideoRequest(ctx context.Context, memberID, requestID string) (videostudioDomain.VideoRequest, error) {
	request, err := s.deps.Requests.ByID(ctx, memberID, requestID)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if request.Status == videostudioDomain.StatusDiscarded {
		return request, nil
	}
	approved, err := textApproved(ctx, s.deps.Publications, request)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if approved {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if err := request.MoveTo(videostudioDomain.StatusDiscarded); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if err := s.deps.Jobs.Cancel(ctx, request.ID); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if request.PublicationID != "" {
		if err := s.cancel.CancelDelivery(ctx, crosspostDomain.CancelDeliveryInput{MemberID: memberID, PublicationID: request.PublicationID}); err != nil {
			return videostudioDomain.VideoRequest{}, err
		}
	}
	return saveRequest(ctx, s.deps.Requests, request)
}

type retryVideoRequestStory struct {
	deps StudioDeps
}

func NewRetryVideoRequestStory(deps StudioDeps) (videostudioDomain.RetryVideoRequestStory, error) {
	if err := requireDeps("retry video request story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs}); err != nil {
		return nil, err
	}
	return &retryVideoRequestStory{deps: deps}, nil
}

// retryStatus is where a failed step goes back to.
var retryStatus = map[videostudioDomain.JobKind]videostudioDomain.RequestStatus{
	videostudioDomain.JobWriteScenario:  videostudioDomain.StatusWritingScenario,
	videostudioDomain.JobReviseScenario: videostudioDomain.StatusWritingScenario,
	videostudioDomain.StepVoiceOver:     videostudioDomain.StatusVoicing,
	videostudioDomain.JobRenderVideo:    videostudioDomain.StatusRendering,
	videostudioDomain.JobReviseCaptions: videostudioDomain.StatusApproved,
}

// RetryVideoRequest runs the failed step again with the same input (Retry after "… failed" or the time limit); a
// producer job counts toward the daily limit again.
func (s *retryVideoRequestStory) RetryVideoRequest(ctx context.Context, memberID, requestID string) (videostudioDomain.VideoRequest, error) {
	request, err := s.deps.Requests.ByID(ctx, memberID, requestID)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	kind := request.State.FailedKind
	next, ok := retryStatus[kind]
	if !ok || request.State.Failure == "" {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if err := request.MoveTo(next); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if kind != videostudioDomain.StepVoiceOver && kind.StartsWork() {
		if err := checkDailyLimit(ctx, s.deps, memberID, time.Now().UTC()); err != nil {
			return videostudioDomain.VideoRequest{}, err
		}
	}
	input := request.State.FailedInput
	request.State.FailedKind, request.State.FailedInput, request.State.Failure = "", "", ""
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil || kind == videostudioDomain.StepVoiceOver {
		return saved, err
	}
	return saved, enqueueJob(ctx, s.deps.Jobs, saved, kind, input)
}
