package stories

import (
	"context"
	"fmt"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
)

type paddleBillingStory struct {
	repository paddleDomain.BillingRepository
	portal     paddleDomain.PortalGate
}

func NewPaddleBilling(repository paddleDomain.BillingRepository, portal paddleDomain.PortalGate) (paddleDomain.BillingStory, error) {
	if repository == nil {
		return nil, fmt.Errorf("paddle billing repository is required")
	}
	if portal == nil {
		return nil, fmt.Errorf("paddle portal gate is required")
	}
	return &paddleBillingStory{repository: repository, portal: portal}, nil
}

func (s *paddleBillingStory) PortalURL(ctx context.Context, memberID string) (string, error) {
	customerID, err := s.repository.CustomerIDByMemberID(ctx, memberID)
	if err != nil {
		return "", err
	}
	if customerID == "" {
		return "", nil
	}
	return s.portal.CreateCustomerPortal(ctx, customerID)
}
