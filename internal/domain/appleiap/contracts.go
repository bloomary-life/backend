package appleiap

import (
	"context"
	"time"
)

type EntitlementRepository interface {
	ByMemberID(ctx context.Context, memberID string, now time.Time) (Entitlement, error)
	// LatestEntitlement returns the raw entitlement record for a member without
	// applying any access filter. The bool return indicates whether a record exists.
	// The caller is responsible for evaluating HasAccess on the returned Entitlement.
	LatestEntitlement(ctx context.Context, memberID string) (Entitlement, bool, error)
	ByOriginalTransactionID(ctx context.Context, originalTransactionID string) (EntitlementRecord, error)
	Apply(ctx context.Context, update EntitlementUpdate) (bool, error)
}

type TransactionVerifier interface {
	VerifyTransaction(ctx context.Context, signedTransaction string, now time.Time) (VerifiedTransaction, error)
}

type NotificationVerifier interface {
	VerifyNotification(ctx context.Context, signedPayload string, now time.Time) (VerifiedNotification, error)
}

type TransactionSyncStory interface {
	SyncTransaction(ctx context.Context, input SyncTransactionInput) (SyncResult, error)
}

type NotificationStory interface {
	ProcessNotification(ctx context.Context, input NotificationInput) error
}
