package stories

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	appleiapMocks "bloomary-api/internal/mocks/domain/appleiap"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/identity"
)

func TestAppleNotificationsAppliesRenewal(t *testing.T) {
	now := time.Now().UTC()
	verifier := appleiapMocks.NewMockNotificationVerifier(t)
	repo := appleiapMocks.NewMockEntitlementRepository(t)
	notification := verifiedNotification("DID_RENEW", "", verifiedTransaction("member-1", now))
	verifier.On("VerifyNotification", mock.Anything, "signed-payload", mock.AnythingOfType("time.Time")).
		Return(notification, nil)
	repo.On("ByOriginalTransactionID", mock.Anything, "original-1").
		Return(entitlementRecord("member-1", notification.Transaction), nil)
	repo.On("Apply", mock.Anything, mock.MatchedBy(func(update appleiapDomain.EntitlementUpdate) bool {
		return update.MemberID == "member-1" && update.Status == appleiapDomain.StatusActive
	})).Return(true, nil)
	story, err := NewAppleNotifications(verifier, repo)
	require.NoError(t, err)

	err = story.ProcessNotification(context.Background(), appleiapDomain.NotificationInput{SignedPayload: "signed-payload"})

	require.NoError(t, err)
}

func TestAppleNotificationsRefundRevokesAccess(t *testing.T) {
	now := time.Now().UTC()
	verifier := appleiapMocks.NewMockNotificationVerifier(t)
	repo := appleiapMocks.NewMockEntitlementRepository(t)
	notification := verifiedNotification("REFUND", "", verifiedTransaction("member-1", now))
	verifier.On("VerifyNotification", mock.Anything, "signed-payload", mock.AnythingOfType("time.Time")).
		Return(notification, nil)
	repo.On("ByOriginalTransactionID", mock.Anything, "original-1").
		Return(entitlementRecord("member-1", notification.Transaction), nil)
	repo.On("Apply", mock.Anything, mock.MatchedBy(func(update appleiapDomain.EntitlementUpdate) bool {
		return update.Status == appleiapDomain.StatusRefunded
	})).Return(true, nil)
	story, err := NewAppleNotifications(verifier, repo)
	require.NoError(t, err)

	err = story.ProcessNotification(context.Background(), appleiapDomain.NotificationInput{SignedPayload: "signed-payload"})

	require.NoError(t, err)
}

func TestAppleNotificationsRejectsUnknownTransactionChain(t *testing.T) {
	now := time.Now().UTC()
	verifier := appleiapMocks.NewMockNotificationVerifier(t)
	repo := appleiapMocks.NewMockEntitlementRepository(t)
	notification := verifiedNotification("DID_RENEW", "", verifiedTransaction("member-1", now))
	verifier.On("VerifyNotification", mock.Anything, "signed-payload", mock.AnythingOfType("time.Time")).
		Return(notification, nil)
	repo.On("ByOriginalTransactionID", mock.Anything, "original-1").
		Return(appleiapDomain.EntitlementRecord{}, nil)
	story, err := NewAppleNotifications(verifier, repo)
	require.NoError(t, err)

	err = story.ProcessNotification(context.Background(), appleiapDomain.NotificationInput{SignedPayload: "signed-payload"})

	require.Error(t, err)
	require.True(t, perror.Is(err, perror.ErrCodeRejected))
	repo.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func verifiedNotification(
	notificationType string, subtype string, transaction appleiapDomain.VerifiedTransaction,
) appleiapDomain.VerifiedNotification {
	return appleiapDomain.VerifiedNotification{
		NotificationType: notificationType,
		Subtype:          subtype,
		Transaction:      transaction,
	}
}

func entitlementRecord(
	memberID string, transaction appleiapDomain.VerifiedTransaction,
) appleiapDomain.EntitlementRecord {
	return appleiapDomain.EntitlementRecord{
		MemberID:              memberID,
		AccountID:             identity.AppAccountTokenFor("linkedin", memberID),
		AppAccountToken:       transaction.AppAccountToken,
		OriginalTransactionID: transaction.OriginalTransactionID,
	}
}
