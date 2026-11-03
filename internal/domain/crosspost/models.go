package crosspost

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Network is where a post appears.
type Network string

const (
	LinkedIn     Network = "linkedin"
	X            Network = "x"
	FacebookPage Network = "facebook_page"
	Instagram    Network = "instagram"
	YouTube      Network = "youtube"
	TikTok       Network = "tiktok"
)

// Provider is whose login a connection uses.
type Provider string

const (
	ProviderX      Provider = "x"
	ProviderMeta   Provider = "meta" // Facebook Pages + Instagram
	ProviderGoogle Provider = "google"
	ProviderTikTok Provider = "tiktok"
)

// AppOwner is whose developer app a connection uses.
type AppOwner string

const (
	AppOwnerBloomary AppOwner = "bloomary"
	AppOwnerMember   AppOwner = "member"
)

type ConnectionHealth string

const (
	HealthConnected      ConnectionHealth = "connected"
	HealthNeedsReconnect ConnectionHealth = "needs_reconnect"
	HealthOff            ConnectionHealth = "off"
)

// Credentials are network tokens in the clear. They never leave the backend: every way of printing,
// logging or encoding them shows "[redacted]". Only the vault reads the fields to seal them.
type Credentials struct {
	ExpiresAt    time.Time
	AccessToken  string
	RefreshToken string
	Scope        string
}

const redacted = "[redacted]"

func (Credentials) String() string               { return redacted }
func (Credentials) GoString() string             { return redacted }
func (Credentials) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Credentials) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Credentials) LogValue() slog.Value         { return slog.StringValue(redacted) }

// Format makes %v, %+v, %#v and %s print "[redacted]".
func (Credentials) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// SealedCredentials is a vault record: the data key wrapped by keyring key KeyID, then the sealed payload.
type SealedCredentials struct {
	KeyID         string
	SchemaVersion int
	Data          []byte
}

// SealedProgress is a network step saved mid-publish (Instagram container ID, YouTube upload session), sealed by the vault.
type SealedProgress SealedCredentials

// VaultBinding ties a sealed record to its owner. The vault adds the key ID and schema version to it as associated data.
type VaultBinding struct {
	RecordID string
	MemberID string
	Provider Provider
}

type Connection struct {
	ExpiresAt time.Time
	ID        string
	MemberID  string
	Provider  Provider
	AppOwner  AppOwner
	AccountID string // a reconnect must return the same account
	Health    ConnectionHealth
	Sealed    SealedCredentials
	Version   int
}

// DestinationCandidate is FinishConnect output, before sealing.
type DestinationCandidate struct {
	Network     Network
	AccountID   string
	AccountName string
	Token       Credentials // e.g. a Facebook Page token
}

type Destination struct {
	ID           string
	MemberID     string
	ConnectionID string
	Network      Network
	AccountID    string // format checked per network by its gate
	AccountName  string
	AudienceZone string // IANA zone; default the member's zone
	Sealed       SealedCredentials
	Enabled      bool
}

type Publication struct {
	CreatedAt          time.Time
	ApprovedAt         time.Time
	ClosedAt           time.Time // set when the last delivery is final
	ID                 string
	MemberID           string
	DraftID            string
	LinkedInScheduleID string // empty when LinkedIn isn't included
}

type DeliveryStatus string

const (
	StatusProposed       DeliveryStatus = "proposed"
	StatusSkipped        DeliveryStatus = "skipped"
	StatusScheduled      DeliveryStatus = "scheduled"
	StatusPublishing     DeliveryStatus = "publishing"
	StatusPublished      DeliveryStatus = "published"
	StatusSentToInbox    DeliveryStatus = "sent_to_inbox"
	StatusNeedsReview    DeliveryStatus = "needs_review"
	StatusNeedsReconnect DeliveryStatus = "needs_reconnect"
	StatusFailed         DeliveryStatus = "failed"
	StatusCancelled      DeliveryStatus = "cancelled"
	StatusSimulated      DeliveryStatus = "simulated"
)

// FinalStatuses never change again; a publication closes when all its deliveries are in one.
var FinalStatuses = []DeliveryStatus{StatusSkipped, StatusPublished, StatusSentToInbox, StatusFailed, StatusCancelled, StatusSimulated}

func (s DeliveryStatus) Final() bool {
	for _, final := range FinalStatuses {
		if s == final {
			return true
		}
	}
	return false
}

// YouTubeChoices are chosen by the member; there is no default.
type YouTubeChoices struct {
	MadeForKids *bool
	Privacy     string // public, unlisted, private; empty until chosen
}

// Delivery is the post on one non-LinkedIn destination: its own aggregate with frozen text, media and time.
type Delivery struct {
	PublishAt      time.Time
	NextAttemptAt  time.Time
	YouTube        YouTubeChoices
	ID             string
	PublicationID  string
	MemberID       string
	DestinationID  string
	Network        Network
	Text           string
	Title          string
	MediaKey       string // frozen copy for this network, backend-hashed
	MediaSHA256    string
	CoverKey       string // frozen copy of the video's cover image, when it has one
	Status         DeliveryStatus
	SkipReason     string
	ExternalPostID string
	ExternalURL    string
	LastErrorCode  string // category + network code only
	Hashtags       []string
	Warnings       []string
	Progress       SealedProgress
	Attempts       int
	Version        int
	AILabel        bool // computed per network, confirmed by the member
}

// deliveryTransitions lists every allowed status change (publish-flow.md §3–4).
var deliveryTransitions = map[DeliveryStatus][]DeliveryStatus{
	StatusProposed:       {StatusProposed, StatusScheduled, StatusSkipped, StatusCancelled},
	StatusScheduled:      {StatusScheduled, StatusPublishing, StatusCancelled},
	StatusPublishing:     {StatusPublished, StatusSentToInbox, StatusScheduled, StatusNeedsReview, StatusNeedsReconnect, StatusFailed, StatusSimulated},
	StatusNeedsReview:    {StatusScheduled, StatusCancelled},
	StatusNeedsReconnect: {StatusScheduled, StatusCancelled},
	StatusFailed:         {StatusScheduled},
}

// MoveTo changes the delivery's status when the change is allowed.
func (d *Delivery) MoveTo(status DeliveryStatus) error {
	for _, allowed := range deliveryTransitions[d.Status] {
		if allowed == status {
			d.Status = status
			return nil
		}
	}
	return ErrInvalidTransition
}

// Editable reports whether the member can still change the delivery (it has not started).
func (d Delivery) Editable() bool {
	return d.Status == StatusProposed || d.Status == StatusScheduled
}

type MediaKind string

const (
	MediaImage MediaKind = "image"
	MediaVideo MediaKind = "video"
)

// MediaInfo is what MediaWorker.Probe reports about a stored file.
type MediaInfo struct {
	Kind        MediaKind
	ContentType string
	Codec       string
	Bytes       int64
	Seconds     float64
	Width       int
	Height      int
}

// VariantSpec asks MediaWorker for a network's copy of a file (X 720×1280, Telegram preview, image fit).
type VariantSpec struct {
	ContentType string
	Width       int // fit inside Width×Height, aspect kept
	Height      int
	MaxBytes    int64
	AspectMin   float64 // width/height; when set, the picture is padded into [AspectMin, AspectMax] (Instagram 0.8–1.91)
	AspectMax   float64
}

type MediaRef struct {
	Key    string
	SHA256 string
	Info   MediaInfo
}

type PublishRequest struct {
	Delivery    Delivery
	Destination Destination
	Credentials Credentials
	Progress    Credentials // opened SealedProgress; empty on the first attempt
	CoverKey    string      // optional video cover image (Instagram cover_url)
	// SaveProgress, when set, seals and stores a network step before the next one (Instagram container, YouTube
	// upload session, Facebook Reel video ID). An error stops the publish before the next request goes out.
	SaveProgress func(ctx context.Context, progress Credentials) error
}

type PublishResult struct {
	Status         DeliveryStatus // published or sent_to_inbox
	ExternalPostID string
	ExternalURL    string
	Progress       Credentials // step to save before the next one
}

type ConnectRequest struct {
	MemberID    string
	AppOwner    AppOwner
	RedirectURI string
	State       string
}

type ConnectStart struct {
	AuthorizationURL string
	CodeVerifier     string // PKCE, kept on the server with the state
}

type ConnectCallback struct {
	Code         string
	State        string
	CodeVerifier string
	RedirectURI  string
}

type GuardDecision string

const (
	DecisionPublish  GuardDecision = "publish"
	DecisionSimulate GuardDecision = "simulate"
)

// PublishMode: dry_run (default when unset or invalid), live_allowlist (stage: real only for the listed
// destinations; an empty list = nothing real) or live (prod: everything real).
type PublishMode string

const (
	ModeDryRun        PublishMode = "dry_run"
	ModeLiveAllowlist PublishMode = "live_allowlist"
	ModeLive          PublishMode = "live"
)

// ParsePublishMode returns live_allowlist or live only for exactly those words; anything else is dry_run.
func ParsePublishMode(value string) PublishMode {
	switch mode := PublishMode(value); mode {
	case ModeLiveAllowlist, ModeLive:
		return mode
	default:
		return ModeDryRun
	}
}

// AllNetworks is the pause row that stops every network at once.
const AllNetworks Network = "all"

type AuditAction string

const (
	AuditConnect    AuditAction = "connect"
	AuditDisconnect AuditAction = "disconnect"
	AuditApprove    AuditAction = "approve"
	AuditEdit       AuditAction = "edit"
	AuditCancel     AuditAction = "cancel"
	AuditPublish    AuditAction = "publish"
	AuditFail       AuditAction = "fail"
	AuditRetry      AuditAction = "retry"
	AuditClaim      AuditAction = "producer_claim"
	AuditPause      AuditAction = "pause"
)

// AuditRecord is one append-only audit row. Text is stored only as its SHA-256.
type AuditRecord struct {
	At             time.Time
	MemberID       string
	Action         AuditAction
	RecordIDs      []string
	MediaSHA256    string
	TextSHA256     string
	NetworkPostID  string
	TelegramUserID int64
}
