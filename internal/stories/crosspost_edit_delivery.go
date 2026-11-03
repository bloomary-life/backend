package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type editDeliveryStory struct {
	repos CrosspostRepos
}

func NewEditDeliveryStory(repos CrosspostRepos) (crosspostDomain.EditDeliveryStory, error) {
	err := requireDeps("edit delivery story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "audit": repos.Audit})
	if err != nil {
		return nil, err
	}
	return &editDeliveryStory{repos: repos}, nil
}

// EditDelivery changes one delivery before it starts. An edit based on an older version is refused
// (ErrVersionConflict: "This post changed. Reload to see the newest version."). Skip is final.
func (s *editDeliveryStory) EditDelivery(ctx context.Context, input crosspostDomain.EditDeliveryInput) (crosspostDomain.Delivery, error) {
	_, d, err := ownedDelivery(ctx, s.repos, input.MemberID, input.PublicationID, input.DeliveryID)
	if err != nil {
		return crosspostDomain.Delivery{}, err
	}
	if input.Version != d.Version {
		return crosspostDomain.Delivery{}, crosspostDomain.ErrVersionConflict
	}
	if !d.Editable() {
		return crosspostDomain.Delivery{}, crosspostDomain.ErrNotEditable
	}
	if input.Text != nil {
		d.Text = *input.Text
	}
	if input.Title != nil {
		d.Title = *input.Title
	}
	if input.Hashtags != nil {
		d.Hashtags = append([]string{}, (*input.Hashtags)...)
	}
	if input.PublishAt != nil {
		if input.PublishAt.Before(time.Now().Add(-scheduleGrace)) {
			return crosspostDomain.Delivery{}, crosspostDomain.ErrInvalidTransition
		}
		d.PublishAt = input.PublishAt.UTC()
	}
	if input.YouTube != nil {
		d.YouTube = *input.YouTube
	}
	if input.AILabel != nil {
		d.AILabel = *input.AILabel
	}
	if input.Skip != nil && *input.Skip {
		next := crosspostDomain.StatusSkipped
		if d.Status == crosspostDomain.StatusScheduled {
			next = crosspostDomain.StatusCancelled
		}
		if err := d.MoveTo(next); err != nil {
			return crosspostDomain.Delivery{}, err
		}
	}
	if err := s.repos.Deliveries.Save(ctx, d); err != nil {
		return crosspostDomain.Delivery{}, err
	}
	d.Version++
	appendAudit(ctx, s.repos.Audit, deliveryAudit(crosspostDomain.AuditEdit, d, 0))
	return d, nil
}
