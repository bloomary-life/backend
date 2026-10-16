package stories

import (
	"context"
	"testing"
)

type billingRepositoryStub struct{}

func (billingRepositoryStub) CustomerIDByMemberID(context.Context, string) (string, error) {
	return "ctm_123", nil
}

type portalGateStub struct{}

func (portalGateStub) CreateCustomerPortal(context.Context, string) (string, error) {
	return "https://customer-portal.paddle.com/session", nil
}

func TestPaddleBillingPortalURL(t *testing.T) {
	story, err := NewPaddleBilling(billingRepositoryStub{}, portalGateStub{})
	if err != nil {
		t.Fatal(err)
	}
	url, err := story.PortalURL(context.Background(), "member-123")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://customer-portal.paddle.com/session" {
		t.Fatalf("url = %q", url)
	}
}
