package stories

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	paddleEntitlementMocks "bloomary-api/internal/mocks/domain/paddle_entitlement"
)

func TestPaddleWebhookAppliesActiveSubscription(t *testing.T) {
	now := time.Now().UTC()
	proUntil := now.Add(24 * time.Hour).Truncate(time.Second)
	repo := paddleEntitlementMocks.NewMockRepository(t)
	story, err := NewPaddleWebhook(repo)
	require.NoError(t, err)

	repo.On("Apply", mock.Anything, mock.MatchedBy(func(update paddleDomain.Update) bool {
		return update.MemberID == "member-1" &&
			update.SubscriptionID == "sub-1" &&
			update.PaddleCustomerID == "customer-1" &&
			update.ProUntil.Equal(proUntil) &&
			!update.EventOccurredAt.IsZero()
	})).Return(true, nil)

	outcome, err := story.Process(context.Background(), paddleDomain.WebhookEvent{
		EventType:      paddleDomain.EventSubscriptionCreated,
		EventID:        "evt-1",
		SubscriptionID: "sub-1",
		MemberID:       "member-1",
		CustomerID:     "customer-1",
		Status:         "active",
		OccurredAt:     now.Format(time.RFC3339),
		EndsAt:         proUntil.Format(time.RFC3339),
	})

	require.NoError(t, err)
	require.Equal(t, paddleDomain.OutcomeApplied, outcome)
}
