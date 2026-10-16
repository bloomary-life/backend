package stories

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	appleiapMocks "bloomary-api/internal/mocks/domain/appleiap"
	"bloomary-api/internal/platform/identity"
)

func TestAppleTransactionSyncAppliesVerifiedTransaction(t *testing.T) {
	now := time.Now().UTC()
	verifier := appleiapMocks.NewMockTransactionVerifier(t)
	repo := appleiapMocks.NewMockEntitlementRepository(t)
	transaction := verifiedTransaction("member-1", now)
	verifier.On("VerifyTransaction", mock.Anything, "signed-jws", mock.AnythingOfType("time.Time")).
		Return(transaction, nil)
	repo.On("Apply", mock.Anything, mock.MatchedBy(func(update appleiapDomain.EntitlementUpdate) bool {
		return update.MemberID == "member-1" &&
			update.AppAccountToken == transaction.AppAccountToken &&
			update.OriginalTransactionID == "original-1" &&
			update.Status == appleiapDomain.StatusActive
	})).Return(true, nil)
	story, err := NewAppleTransactionSync(verifier, repo)
	require.NoError(t, err)

	result, err := story.SyncTransaction(context.Background(), appleiapDomain.SyncTransactionInput{
		MemberID:          "member-1",
		SignedTransaction: "signed-jws",
	})

	require.NoError(t, err)
	require.True(t, appleHasAccess(result.Entitlement, now))
}

func verifiedTransaction(memberID string, now time.Time) appleiapDomain.VerifiedTransaction {
	return appleiapDomain.VerifiedTransaction{
		AppAccountToken:       identity.AppAccountTokenFor("linkedin",memberID),
		OriginalTransactionID: "original-1",
		TransactionID:         "transaction-1",
		ProductID:             "bloomary.pro.monthly",
		AccessUntil:           now.Add(30 * 24 * time.Hour),
		Status:                appleiapDomain.StatusActive,
		Environment:           "Sandbox",
		EventOccurredAt:       now,
	}
}
