package paddle_entitlement

import "time"

const (
	EventSubscriptionCreated  = "subscription.created"
	EventSubscriptionUpdated  = "subscription.updated"
	EventSubscriptionCanceled = "subscription.canceled"
)

// Entitlement is the Paddle-specific entitlement value object.
// It only carries raw access data; the story decides whether access is granted.
type Entitlement struct {
	AccessUntil time.Time
}

// Update is a Paddle subscription event translated into a persistence-friendly
// record.
type Update struct {
	MemberID         string
	SubscriptionID   string
	PaddleCustomerID string
	ProUntil         time.Time
	EventOccurredAt  time.Time
}

// WebhookEvent is a parsed Paddle subscription event. The HTTP handler is
// responsible for signature verification and JSON decoding; this type carries
// only the fields the story needs for the entitlement decision.
type WebhookEvent struct {
	EventType      string
	EventID        string
	SubscriptionID string
	MemberID       string
	CustomerID     string
	Status         string
	OccurredAt     string
	EndsAt         string
}

// WebhookOutcome is the result of processing a Paddle webhook event.
type WebhookOutcome string

const (
	OutcomeApplied       WebhookOutcome = "applied"
	OutcomeStale         WebhookOutcome = "stale"
	OutcomeIgnored       WebhookOutcome = "ignored"
	OutcomeMissingMember WebhookOutcome = "missing_member_id"
)
