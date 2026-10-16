package stories

import (
	"context"
	"fmt"
	"time"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	"bloomary-api/internal/platform/identity"
	perror "bloomary-api/internal/platform/error"
)

type appleTransactionSyncStory struct {
	verifier     appleiapDomain.TransactionVerifier
	entitlements appleiapDomain.EntitlementRepository
}

func NewAppleTransactionSync(
	verifier appleiapDomain.TransactionVerifier,
	entitlements appleiapDomain.EntitlementRepository,
) (appleiapDomain.TransactionSyncStory, error) {
	if verifier == nil {
		return nil, fmt.Errorf("apple transaction sync story: verifier is required")
	}
	if entitlements == nil {
		return nil, fmt.Errorf("apple transaction sync story: entitlement repo is required")
	}
	return &appleTransactionSyncStory{verifier: verifier, entitlements: entitlements}, nil
}

func (s *appleTransactionSyncStory) SyncTransaction(
	ctx context.Context, input appleiapDomain.SyncTransactionInput,
) (appleiapDomain.SyncResult, error) {
	if input.MemberID == "" {
		return appleiapDomain.SyncResult{}, perror.New(perror.ErrCodeRejected, nil, "member id is required")
	}
	if input.SignedTransaction == "" {
		return appleiapDomain.SyncResult{}, perror.New(perror.ErrCodeRejected, nil, "signed transaction is required")
	}

	now := time.Now().UTC()
	transaction, err := s.verifier.VerifyTransaction(ctx, input.SignedTransaction, now)
	if err != nil {
		return appleiapDomain.SyncResult{}, err
	}

	accountToken := identity.AppAccountTokenFor("linkedin", input.MemberID)
	if transaction.AppAccountToken != accountToken {
		return appleiapDomain.SyncResult{}, perror.New(
			perror.ErrCodeRejected, nil, "apple app account token mismatch",
		)
	}

	entitlement := appleiapDomain.Entitlement{AccessUntil: transaction.AccessUntil, Status: transaction.Status}
	_, err = s.entitlements.Apply(ctx, appleiapDomain.EntitlementUpdate{
		MemberID:              input.MemberID,
		AccountID:             accountToken,
		AppAccountToken:       transaction.AppAccountToken,
		OriginalTransactionID: transaction.OriginalTransactionID,
		TransactionID:         transaction.TransactionID,
		ProductID:             transaction.ProductID,
		AccessUntil:           transaction.AccessUntil,
		Status:                transaction.Status,
		Environment:           transaction.Environment,
		EventOccurredAt:       transaction.EventOccurredAt,
	})
	if err != nil {
		return appleiapDomain.SyncResult{}, err
	}

	return appleiapDomain.SyncResult{
		Entitlement: entitlement,
		IsPro:       appleHasAccess(entitlement, now),
	}, nil
}
