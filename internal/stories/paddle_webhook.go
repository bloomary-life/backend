package stories

import (
	"context"
	"fmt"
	"time"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
)

type paddleWebhookStory struct {
	entitlements paddleDomain.Repository
}

func NewPaddleWebhook(
	entitlements paddleDomain.Repository,
) (paddleDomain.WebhookStory, error) {
	if entitlements == nil {
		return nil, fmt.Errorf("paddle webhook story: entitlement repo is required")
	}
	return &paddleWebhookStory{entitlements: entitlements}, nil
}

func (s *paddleWebhookStory) Process(
	ctx context.Context, event paddleDomain.WebhookEvent,
) (paddleDomain.WebhookOutcome, error) {
	if !isPaddleSubscriptionEvent(event.EventType) {
		return paddleDomain.OutcomeIgnored, nil
	}
	if event.MemberID == "" {
		return paddleDomain.OutcomeMissingMember, nil
	}
	if event.SubscriptionID == "" {
		return "", fmt.Errorf("subscription id is required")
	}

	occurredAt, err := time.Parse(time.RFC3339, event.OccurredAt)
	if err != nil {
		return "", fmt.Errorf("parse occurred_at: %w", err)
	}

	update := paddleDomain.Update{
		MemberID:         event.MemberID,
		SubscriptionID:   event.SubscriptionID,
		PaddleCustomerID: event.CustomerID,
		EventOccurredAt:  occurredAt,
	}

	switch event.EventType {
	case paddleDomain.EventSubscriptionCanceled:
		// ProUntil stays zero: revokes access.
	case paddleDomain.EventSubscriptionCreated, paddleDomain.EventSubscriptionUpdated:
		if event.Status != "active" && event.Status != "trialing" {
			return paddleDomain.OutcomeIgnored, nil
		}
		proUntil, err := time.Parse(time.RFC3339, event.EndsAt)
		if err != nil {
			return "", fmt.Errorf("parse current_billing_period.ends_at: %w", err)
		}
		update.ProUntil = proUntil
	default:
		return paddleDomain.OutcomeIgnored, nil
	}

	applied, err := s.entitlements.Apply(ctx, update)
	if err != nil {
		return "", err
	}
	if !applied {
		return paddleDomain.OutcomeStale, nil
	}
	return paddleDomain.OutcomeApplied, nil
}

func isPaddleSubscriptionEvent(eventType string) bool {
	return eventType == paddleDomain.EventSubscriptionCreated ||
		eventType == paddleDomain.EventSubscriptionUpdated ||
		eventType == paddleDomain.EventSubscriptionCanceled
}
