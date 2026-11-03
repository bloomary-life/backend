package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

type retryDeliveryStory struct {
	repos CrosspostRepos
}

func NewRetryDeliveryStory(repos CrosspostRepos) (crosspostDomain.RetryDeliveryStory, error) {
	err := requireDeps("retry delivery story", map[string]any{"publications": repos.Publications, "deliveries": repos.Deliveries, "audit": repos.Audit})
	if err != nil {
		return nil, err
	}
	return &retryDeliveryStory{repos: repos}, nil
}

// RetryDelivery: after the member says so ("I checked · Post again", Retry), a failed, needs_review or
// needs_reconnect delivery is scheduled now with a fresh try count.
func (s *retryDeliveryStory) RetryDelivery(ctx context.Context, input crosspostDomain.RetryDeliveryInput) (crosspostDomain.Delivery, error) {
	_, d, err := ownedDelivery(ctx, s.repos, input.MemberID, input.PublicationID, input.DeliveryID)
	if err != nil {
		return crosspostDomain.Delivery{}, err
	}
	if d.Status != crosspostDomain.StatusFailed && d.Status != crosspostDomain.StatusNeedsReview && d.Status != crosspostDomain.StatusNeedsReconnect {
		return crosspostDomain.Delivery{}, crosspostDomain.ErrInvalidTransition
	}
	if err := d.MoveTo(crosspostDomain.StatusScheduled); err != nil {
		return crosspostDomain.Delivery{}, err
	}
	d.NextAttemptAt, d.Attempts, d.LastErrorCode = time.Now().UTC(), 0, ""
	if err := s.repos.Deliveries.Save(ctx, d); err != nil {
		return crosspostDomain.Delivery{}, err
	}
	d.Version++
	appendAudit(ctx, s.repos.Audit, deliveryAudit(crosspostDomain.AuditRetry, d, input.TelegramUserID))
	return d, nil
}
