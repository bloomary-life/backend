package appleiap

import "time"

type Status string

const (
	StatusActive       Status = "active"
	StatusGracePeriod  Status = "grace_period"
	StatusBillingRetry Status = "billing_retry"
	StatusExpired      Status = "expired"
	StatusRevoked      Status = "revoked"
	StatusRefunded     Status = "refunded"
)

type Entitlement struct {
	AccessUntil time.Time
	Status      Status
}

type EntitlementUpdate struct {
	MemberID              string
	AccountID             string
	AppAccountToken       string
	OriginalTransactionID string
	TransactionID         string
	ProductID             string
	AccessUntil           time.Time
	Status                Status
	Environment           string
	EventOccurredAt       time.Time
}

type EntitlementRecord struct {
	MemberID              string
	AccountID             string
	AppAccountToken       string
	OriginalTransactionID string
}

type VerifiedTransaction struct {
	AppAccountToken       string
	OriginalTransactionID string
	TransactionID         string
	ProductID             string
	AccessUntil           time.Time
	Status                Status
	Environment           string
	EventOccurredAt       time.Time
}

type VerifiedNotification struct {
	NotificationType string
	Subtype          string
	Transaction      VerifiedTransaction
}

type SyncTransactionInput struct {
	MemberID          string
	SignedTransaction string
}

type NotificationInput struct {
	SignedPayload string
}

// SyncResult is returned by the transaction sync story with the access
// decision already computed using the story's own clock (not the handler's).
type SyncResult struct {
	Entitlement Entitlement
	IsPro       bool
}
