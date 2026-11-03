package stories

import (
	"context"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

type cancelDeliveryStory struct {
	repos     CrosspostRepos
	scheduled scheduledDomain.Story
}

func NewCancelDeliveryStory(repos CrosspostRepos, scheduled scheduledDomain.Story) (crosspostDomain.CancelDeliveryStory, error) {
	err := requireDeps("cancel delivery story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "audit": repos.Audit, "scheduled post story": scheduled})
	if err != nil {
		return nil, err
	}
	return &cancelDeliveryStory{repos: repos, scheduled: scheduled}, nil
}

// CancelDelivery cancels one unstarted delivery, or with no delivery ID all of them and the LinkedIn post
// (today's Unschedule). Started deliveries are left as they are.
func (s *cancelDeliveryStory) CancelDelivery(ctx context.Context, input crosspostDomain.CancelDeliveryInput) error {
	publication, err := s.repos.Publications.ByID(ctx, input.MemberID, input.PublicationID)
	if err != nil {
		return err
	}
	deliveries, err := s.repos.Deliveries.ByPublication(ctx, input.MemberID, input.PublicationID)
	if err != nil {
		return err
	}
	found := input.DeliveryID == ""
	for _, d := range deliveries {
		if input.DeliveryID != "" && d.ID != input.DeliveryID {
			continue
		}
		found = true
		if !unstarted(d.Status) {
			if input.DeliveryID != "" {
				return crosspostDomain.ErrNotEditable
			}
			continue
		}
		if err := d.MoveTo(crosspostDomain.StatusCancelled); err != nil {
			continue
		}
		if err := s.repos.Deliveries.Save(ctx, d); err != nil {
			return err
		}
		appendAudit(ctx, s.repos.Audit, deliveryAudit(crosspostDomain.AuditCancel, d, 0))
	}
	if !found {
		return crosspostDomain.ErrNotFound
	}
	if input.DeliveryID == "" && publication.LinkedInScheduleID != "" {
		return s.scheduled.Cancel(ctx, input.MemberID, publication.DraftID)
	}
	return nil
}
