package paddle_entitlement

import "context"

type Repository interface {
	ByMemberID(ctx context.Context, memberID string) (Entitlement, error)
	Apply(ctx context.Context, update Update) (bool, error)
}

type BillingRepository interface {
	CustomerIDByMemberID(ctx context.Context, memberID string) (string, error)
}

type PortalGate interface {
	CreateCustomerPortal(ctx context.Context, customerID string) (string, error)
}

type BillingStory interface {
	PortalURL(ctx context.Context, memberID string) (string, error)
}

type WebhookStory interface {
	Process(ctx context.Context, event WebhookEvent) (WebhookOutcome, error)
}
