package crosspost

import (
	"context"
	"time"

	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
)

// One interface per use case (domain.md Table 3).

type StartConnectInput struct {
	MemberID string
	Provider Provider
	AppOwner AppOwner
}

type StartConnectStory interface {
	StartConnect(ctx context.Context, input StartConnectInput) (ConnectStart, error)
}

type FinishConnectInput struct {
	MemberID string
	Provider Provider
	Callback ConnectCallback
}

// FinishConnectOutput offers the destinations the login gives; the member ticks them with SetDestinationStory.
type FinishConnectOutput struct {
	Connection   Connection
	Destinations []Destination
}

type FinishConnectStory interface {
	// FinishConnect seals the credentials; a reconnect must return the same AccountID.
	FinishConnect(ctx context.Context, input FinishConnectInput) (FinishConnectOutput, error)
}

type TestConnectionStory interface {
	TestConnection(ctx context.Context, memberID, connectionID string) (ConnectionHealth, error)
}

type RemoveConnectionStory interface {
	// RemoveConnection revokes, deletes its destinations' data (YouTube within 7 days) and cancels their unstarted deliveries.
	RemoveConnection(ctx context.Context, memberID, connectionID string) error
}

type SetDestinationInput struct {
	MemberID      string
	DestinationID string
	AudienceZone  string
	Enabled       bool
}

type SetDestinationStory interface {
	SetDestination(ctx context.Context, input SetDestinationInput) (Destination, error)
}

type PrepareInput struct {
	MemberID        string
	DraftID         string
	DestinationIDs  []string
	IncludeLinkedIn bool
}

// LinkedInPart is the publication's LinkedIn post: a normal scheduled post, read from scheduledpost when shown.
type LinkedInPart struct {
	ProposedAt time.Time
	Post       *scheduledDomain.Post
	SkipReason string
}

type PublicationView struct {
	LinkedIn    *LinkedInPart // nil when LinkedIn isn't included
	Publication Publication
	Deliveries  []Delivery
}

type PreparePublicationStory interface {
	// Prepare makes a publication with proposed deliveries and a proposed LinkedIn time. Nothing posts.
	Prepare(ctx context.Context, input PrepareInput) (PublicationView, error)
}

// EditDeliveryInput changes one delivery before it starts; nil fields stay as they are.
type EditDeliveryInput struct {
	Text          *string
	Title         *string
	Hashtags      *[]string
	PublishAt     *time.Time
	YouTube       *YouTubeChoices
	AILabel       *bool
	Skip          *bool
	MemberID      string
	PublicationID string
	DeliveryID    string
	Version       int // the version the member edited; an older one is refused (ErrVersionConflict)
}

type EditDeliveryStory interface {
	EditDelivery(ctx context.Context, input EditDeliveryInput) (Delivery, error)
}

type ApproveInput struct {
	MemberID       string
	PublicationID  string
	TelegramUserID int64 // recorded in the audit row when approved in Telegram
	AllNow         bool  // "Approve: all now"; otherwise each delivery keeps its time
}

type ApprovePublicationStory interface {
	// Approve freezes media per delivery, re-checks rules, re-proposes past times, schedules LinkedIn and runs ApproveAll.
	Approve(ctx context.Context, input ApproveInput) (PublicationView, error)
}

type CancelDeliveryInput struct {
	MemberID      string
	PublicationID string
	DeliveryID    string // empty = all unstarted deliveries
}

type CancelDeliveryStory interface {
	CancelDelivery(ctx context.Context, input CancelDeliveryInput) error
}

type RetryDeliveryInput struct {
	MemberID       string
	PublicationID  string
	DeliveryID     string
	TelegramUserID int64
}

type RetryDeliveryStory interface {
	// RetryDelivery moves a failed or needs_review delivery to scheduled now, after the member says so.
	RetryDelivery(ctx context.Context, input RetryDeliveryInput) (Delivery, error)
}

type PublicationStatusStory interface {
	PublicationStatus(ctx context.Context, memberID, publicationID string) (PublicationView, error)
}

type DeleteCrosspostDataStory interface {
	// DeleteCrosspostData runs on account deletion, before the draft step: pause the member, revoke tokens,
	// delete R2 media, delete rows, strip audit rows.
	DeleteCrosspostData(ctx context.Context, memberID string) error
}

// DeliveryFinished is a hook run after a delivery reaches a final status (Telegram status, iOS push, metrics, outcome record).
type DeliveryFinished interface {
	DeliveryFinished(ctx context.Context, delivery Delivery)
}
