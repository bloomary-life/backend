package stories

import (
	"context"
	"fmt"
	"time"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	perror "bloomary-api/internal/platform/error"
)

type appleNotificationsStory struct {
	verifier     appleiapDomain.NotificationVerifier
	entitlements appleiapDomain.EntitlementRepository
}

func NewAppleNotifications(
	verifier appleiapDomain.NotificationVerifier,
	entitlements appleiapDomain.EntitlementRepository,
) (appleiapDomain.NotificationStory, error) {
	if verifier == nil {
		return nil, fmt.Errorf("apple notifications story: verifier is required")
	}
	if entitlements == nil {
		return nil, fmt.Errorf("apple notifications story: entitlement repo is required")
	}
	return &appleNotificationsStory{verifier: verifier, entitlements: entitlements}, nil
}

func (s *appleNotificationsStory) ProcessNotification(
	ctx context.Context, input appleiapDomain.NotificationInput,
) error {
	if input.SignedPayload == "" {
		return perror.New(perror.ErrCodeRejected, nil, "signed payload is required")
	}

	now := time.Now().UTC()
	notification, err := s.verifier.VerifyNotification(ctx, input.SignedPayload, now)
	if err != nil {
		return err
	}

	transaction := notification.Transaction
	record, err := s.entitlements.ByOriginalTransactionID(ctx, transaction.OriginalTransactionID)
	if err != nil {
		return err
	}
	if record.MemberID == "" {
		return perror.New(perror.ErrCodeRejected, nil, "unknown apple transaction")
	}
	if transaction.AppAccountToken != record.AppAccountToken {
		return perror.New(perror.ErrCodeRejected, nil, "apple app account token mismatch")
	}

	transaction.Status = notificationStatus(notification)
	_, err = s.entitlements.Apply(ctx, appleiapDomain.EntitlementUpdate{
		MemberID:              record.MemberID,
		AccountID:             record.AccountID,
		AppAccountToken:       transaction.AppAccountToken,
		OriginalTransactionID: transaction.OriginalTransactionID,
		TransactionID:         transaction.TransactionID,
		ProductID:             transaction.ProductID,
		AccessUntil:           transaction.AccessUntil,
		Status:                transaction.Status,
		Environment:           transaction.Environment,
		EventOccurredAt:       transaction.EventOccurredAt,
	})
	return err
}

func notificationStatus(notification appleiapDomain.VerifiedNotification) appleiapDomain.Status {
	switch notification.NotificationType {
	case "REFUND":
		return appleiapDomain.StatusRefunded
	case "REVOKE":
		return appleiapDomain.StatusRevoked
	case "EXPIRED", "GRACE_PERIOD_EXPIRED":
		return appleiapDomain.StatusExpired
	case "DID_FAIL_TO_RENEW":
		if notification.Subtype == "GRACE_PERIOD" {
			return appleiapDomain.StatusGracePeriod
		}
		return appleiapDomain.StatusBillingRetry
	case "DID_RENEW", "SUBSCRIBED", "DID_RECOVER":
		return appleiapDomain.StatusActive
	default:
		return notification.Transaction.Status
	}
}
