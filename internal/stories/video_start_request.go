package stories

import (
	"context"
	"strings"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

type startVideoRequestStory struct {
	deps StudioDeps
}

func NewStartVideoRequestStory(deps StudioDeps) (videostudioDomain.StartVideoRequestStory, error) {
	if err := requireDeps("start video request story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "allowance": deps.Allowance}); err != nil {
		return nil, err
	}
	return &startVideoRequestStory{deps: deps}, nil
}

// StartVideoRequest takes Kat's reply to the 🎬 prompt (within 24 hours) as the idea and queues write_scenario.
func (s *startVideoRequestStory) StartVideoRequest(ctx context.Context, input videostudioDomain.StartVideoRequestInput) (videostudioDomain.VideoRequest, error) {
	if allowed, _ := s.deps.Allowance.Allowed(ctx, input.MemberID); !allowed {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrNotAllowed
	}
	request, err := s.deps.Requests.ByMessage(ctx, input.ChatID, input.ReplyTo)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	now := time.Now().UTC()
	if request.MemberID != input.MemberID || request.Messages.IdeaPrompt != input.ReplyTo || now.Sub(request.CreatedAt) > videostudioDomain.IdeaPromptTTL {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrNotFound
	}
	idea := truncateRunes(videostudioDomain.CleanText(input.Idea), maxIdeaRunes)
	if strings.TrimSpace(idea) == "" {
		return videostudioDomain.VideoRequest{}, videostudioDomain.ErrInvalidTransition
	}
	if err := request.MoveTo(videostudioDomain.StatusWritingScenario); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if err := checkDailyLimit(ctx, s.deps, input.MemberID, now); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	request.Idea = idea
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	if err := enqueueJob(ctx, s.deps.Jobs, saved, videostudioDomain.JobWriteScenario, encodeJobInput(jobInput{Idea: idea})); err != nil {
		return videostudioDomain.VideoRequest{}, err
	}
	return saved, nil
}
