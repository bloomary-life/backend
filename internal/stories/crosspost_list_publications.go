package stories

import (
	"context"
	"fmt"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

// recentlyClosed: the Scheduled tab and /planned keep a finished publication for 7 days, so its outcome shows.
const recentlyClosed = 7 * 24 * time.Hour

type listPublicationsStory struct {
	repos     CrosspostRepos
	lister    crosspostDomain.PublicationLister
	scheduled scheduledDomain.Repository
}

func NewListPublicationsStory(repos CrosspostRepos, lister crosspostDomain.PublicationLister, scheduled scheduledDomain.Repository) (crosspostDomain.ListPublicationsStory, error) {
	err := requireDeps("list publications story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "linkedin intents": repos.LinkedIn,
		"publication lister": lister, "scheduled posts": scheduled})
	if err != nil {
		return nil, err
	}
	return &listPublicationsStory{repos: repos, lister: lister, scheduled: scheduled}, nil
}

// ListPublications lists the member's approved publications, open or closed in the last 7 days, newest first, each
// with its deliveries and LinkedIn part.
func (s *listPublicationsStory) ListPublications(ctx context.Context, memberID string) ([]crosspostDomain.PublicationView, error) {
	publications, err := s.lister.ListApproved(ctx, memberID, time.Now().UTC().Add(-recentlyClosed))
	if err != nil {
		return nil, err
	}
	views := make([]crosspostDomain.PublicationView, 0, len(publications))
	for _, p := range publications {
		view, err := publicationView(ctx, s.repos, s.scheduled, memberID, p.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// PublicationSummaryLine is the Scheduled tab's row: "4 networks · next: X Wed 4 Nov, 13:00 (UTC+2)" (ux.md web 8).
func PublicationSummaryLine(view crosspostDomain.PublicationView) string {
	count := len(view.Deliveries)
	if view.LinkedIn != nil {
		count++
	}
	line := fmt.Sprintf("%d networks", count)
	if count == 1 {
		line = "1 network"
	}
	if next, ok := nextDelivery(view); ok {
		line += " · next: " + networkName(next.Network) + " " + formatPostTime(next.PublishAt)
	}
	return line
}

// nextDelivery is the earliest delivery that hasn't gone out yet.
func nextDelivery(view crosspostDomain.PublicationView) (crosspostDomain.Delivery, bool) {
	for _, d := range view.Deliveries { // ordered by publish time
		if d.Status == crosspostDomain.StatusScheduled {
			return d, true
		}
	}
	return crosspostDomain.Delivery{}, false
}
