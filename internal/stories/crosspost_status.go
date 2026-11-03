package stories

import (
	"context"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

type publicationStatusStory struct {
	repos     CrosspostRepos
	scheduled scheduledDomain.Repository
}

func NewPublicationStatusStory(repos CrosspostRepos, scheduled scheduledDomain.Repository) (crosspostDomain.PublicationStatusStory, error) {
	err := requireDeps("publication status story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "linkedin intents": repos.LinkedIn, "scheduled posts": scheduled})
	if err != nil {
		return nil, err
	}
	return &publicationStatusStory{repos: repos, scheduled: scheduled}, nil
}

// PublicationStatus returns the publication, its deliveries and its LinkedIn part; the LinkedIn post's status is read
// from scheduledpost, never copied.
func (s *publicationStatusStory) PublicationStatus(ctx context.Context, memberID, publicationID string) (crosspostDomain.PublicationView, error) {
	return publicationView(ctx, s.repos, s.scheduled, memberID, publicationID)
}

func publicationView(ctx context.Context, repos CrosspostRepos, scheduled scheduledDomain.Repository, memberID, publicationID string) (crosspostDomain.PublicationView, error) {
	publication, err := repos.Publications.ByID(ctx, memberID, publicationID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	deliveries, err := repos.Deliveries.ByPublication(ctx, memberID, publicationID)
	if err != nil {
		return crosspostDomain.PublicationView{}, err
	}
	view := crosspostDomain.PublicationView{Publication: publication, Deliveries: deliveries}
	intent, err := repos.LinkedIn.Intent(ctx, memberID, publicationID)
	if err != nil && !isNotFound(err) {
		return crosspostDomain.PublicationView{}, err
	}
	if err == nil {
		view.LinkedIn = &crosspostDomain.LinkedInPart{ProposedAt: intent.ProposedAt, SkipReason: intent.SkipReason}
		if publication.LinkedInScheduleID != "" {
			if post, err := scheduled.ByID(ctx, memberID, publication.LinkedInScheduleID); err == nil {
				view.LinkedIn.Post = &post
			}
		}
	}
	return view, nil
}
