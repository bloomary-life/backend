package stories

import (
	"context"
	"fmt"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	notificationDomain "bloomary-api/internal/domain/notification"
)

// CrosspostPushHook sends the iOS push LinkedIn sends today, after a delivery is published, failed or needs review.
type CrosspostPushHook struct {
	notifications notificationDomain.Service
}

var _ crosspostDomain.DeliveryFinished = (*CrosspostPushHook)(nil)

func NewCrosspostPushHook(notifications notificationDomain.Service) (*CrosspostPushHook, error) {
	if notifications == nil {
		return nil, fmt.Errorf("crosspost push hook: notifications are required")
	}
	return &CrosspostPushHook{notifications: notifications}, nil
}

func (h *CrosspostPushHook) DeliveryFinished(ctx context.Context, d crosspostDomain.Delivery) {
	switch d.Status {
	case crosspostDomain.StatusPublished, crosspostDomain.StatusFailed, crosspostDomain.StatusNeedsReview:
		h.notifications.Notify(ctx, d.MemberID, d.ID, string(d.Status))
	}
}

// CrosspostMetricsHook counts finished deliveries by network and status.
type CrosspostMetricsHook struct {
	counter crosspostDomain.DeliveryCounter
}

var _ crosspostDomain.DeliveryFinished = (*CrosspostMetricsHook)(nil)

func NewCrosspostMetricsHook(counter crosspostDomain.DeliveryCounter) (*CrosspostMetricsHook, error) {
	if counter == nil {
		return nil, fmt.Errorf("crosspost metrics hook: counter is required")
	}
	return &CrosspostMetricsHook{counter: counter}, nil
}

func (h *CrosspostMetricsHook) DeliveryFinished(_ context.Context, d crosspostDomain.Delivery) {
	h.counter.RecordDelivery(d.Network, d.Status)
}
