package crosspost

import (
	"context"
	"time"
)

// NetworkPublisher posts one delivery to one network (strategy: one gate package per network).
type NetworkPublisher interface {
	Network() Network
	Publish(ctx context.Context, request PublishRequest) (PublishResult, error) // published | sent_to_inbox, + Progress
}

type ProviderConnector interface {
	Provider() Provider
	StartConnect(ctx context.Context, request ConnectRequest) (ConnectStart, error)
	FinishConnect(ctx context.Context, callback ConnectCallback) (Credentials, []DestinationCandidate, error)
	Check(ctx context.Context, credentials Credentials) error
	Refresh(ctx context.Context, credentials Credentials) (Credentials, error)
	Revoke(ctx context.Context, credentials Credentials) error
}

// CredentialVault seals credentials; associated data = record ID + member ID + provider + key ID + schema version.
type CredentialVault interface {
	Seal(ctx context.Context, binding VaultBinding, credentials Credentials) (SealedCredentials, error)
	Open(ctx context.Context, binding VaultBinding, sealed SealedCredentials) (Credentials, error)
}

// PublishGuard: pause switch (DB, env fallback) and stage dry-run allowlist. Asked before claiming
// (crosspost worker and the scheduledpost worker) and by Post now.
type PublishGuard interface {
	Paused(ctx context.Context, network Network) (bool, error)
	Decide(ctx context.Context, delivery Delivery) (GuardDecision, error) // publish | simulate
}

// MediaWorker runs ffprobe/ffmpeg as a sandboxed child process (security.md §2).
type MediaWorker interface {
	Probe(ctx context.Context, key string) (MediaInfo, error)                    // seconds, size, codec, dimensions
	Variant(ctx context.Context, key string, spec VariantSpec) (MediaRef, error) // X 720×1280, Telegram preview, image fit
}

type ConnectionRepository interface {
	// Save inserts when expectedVersion is 0, else updates only the row still at expectedVersion.
	Save(ctx context.Context, connection Connection, expectedVersion int) error
	ByID(ctx context.Context, memberID, id string) (Connection, error)
	ListByMember(ctx context.Context, memberID string) ([]Connection, error)
	LockForRefresh(ctx context.Context, id string, until time.Time) (bool, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type DestinationRepository interface {
	Save(ctx context.Context, destination Destination) error
	ByID(ctx context.Context, memberID, id string) (Destination, error)
	ListByMember(ctx context.Context, memberID string) ([]Destination, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type PublicationRepository interface {
	// Create fails with ErrOpenPublication when the draft has an open publication:
	// unique index (member_id, draft_id) WHERE closed_at IS NULL.
	Create(ctx context.Context, publication Publication, deliveries []Delivery) error
	// ApproveAll saves the publication and all its deliveries in one transaction.
	ApproveAll(ctx context.Context, publication Publication, deliveries []Delivery) error
	ByID(ctx context.Context, memberID, id string) (Publication, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type DeliveryRepository interface {
	Save(ctx context.Context, delivery Delivery) error // WHERE version = delivery.Version; closes the publication when the last one is final
	ClaimDue(ctx context.Context, now time.Time, paused []Network) (Delivery, error)
	ByPublication(ctx context.Context, memberID, publicationID string) ([]Delivery, error)
	TakenTimes(ctx context.Context, memberID string, network Network, from time.Time) ([]time.Time, error)
	MarkInterrupted(ctx context.Context) error // at start: publishing → needs_review
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// PauseStore holds the pause switch (bloomary_publish_pause), set by the ops bot. Network AllNetworks pauses everything.
type PauseStore interface {
	Paused(ctx context.Context, network Network) (bool, error)
	SetPaused(ctx context.Context, network Network, paused bool, changedBy string, at time.Time) error
}

// AuditLog is the append-only bloomary_crosspost_audit table (security.md §6).
type AuditLog interface {
	Append(ctx context.Context, record AuditRecord) error
	// StripMember replaces the member ID with its keyed HMAC and removes post IDs and text hashes (account deletion).
	StripMember(ctx context.Context, memberID, memberHMAC string) error
	// PurgeBefore deletes rows older than cutoff (the yearly purge).
	PurgeBefore(ctx context.Context, cutoff time.Time) error
}
