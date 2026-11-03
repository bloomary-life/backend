package stories

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	"bloomary-api/internal/platform/logger"

	"github.com/google/uuid"
)

// CrosspostRepos are the crosspost persistence ports; each story constructor checks the ones it uses.
type CrosspostRepos struct {
	Connections  crosspostDomain.ConnectionRepository
	Destinations crosspostDomain.DestinationRepository
	Publications crosspostDomain.PublicationRepository
	Unapproved   crosspostDomain.UnapprovedCloser
	Deliveries   crosspostDomain.DeliveryRepository
	DeliveryList crosspostDomain.DeliveryLister
	Remover      crosspostDomain.ConnectionRemover
	LinkedIn     crosspostDomain.LinkedInIntentStore
	Audit        crosspostDomain.AuditLog
	States       crosspostDomain.ConnectStateStore
}

// DefaultAudienceZone is the member's zone until the account saves one (ux.md "Time").
const DefaultAudienceZone = "Europe/Nicosia"

func requireDeps(story string, deps map[string]any) error {
	for name, dep := range deps {
		if dep == nil {
			return fmt.Errorf("%s: %s is required", story, name)
		}
	}
	return nil
}

func newCrosspostID() string { return uuid.NewString() }

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// requirePlan: publishing needs Pro or Trial, as LinkedIn today.
func requirePlan(ctx context.Context, access entitlementDomain.AccessStory, memberID string, now time.Time) error {
	entitlement, err := access.Resolve(ctx, memberID, now)
	if err != nil {
		return fmt.Errorf("resolve entitlement: %w", err)
	}
	if entitlement.Tier != entitlementDomain.TierPro && entitlement.Tier != entitlementDomain.TierTrial {
		return crosspostDomain.ErrPlanRequired
	}
	return nil
}

func appendAudit(ctx context.Context, audit crosspostDomain.AuditLog, record crosspostDomain.AuditRecord) {
	if err := audit.Append(ctx, record); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_audit_append_failed")
	}
}

func deliveryAudit(action crosspostDomain.AuditAction, d crosspostDomain.Delivery, telegramUserID int64) crosspostDomain.AuditRecord {
	return crosspostDomain.AuditRecord{
		At: time.Now().UTC(), MemberID: d.MemberID, Action: action, RecordIDs: []string{d.PublicationID, d.ID},
		MediaSHA256: d.MediaSHA256, TextSHA256: sha256Hex(crosspostDomain.ComposeText(d.Text, d.Hashtags)),
		NetworkPostID: d.ExternalPostID, TelegramUserID: telegramUserID,
	}
}

// ownedDelivery loads one delivery of the member's publication.
func ownedDelivery(ctx context.Context, repos CrosspostRepos, memberID, publicationID, deliveryID string) (crosspostDomain.Publication, crosspostDomain.Delivery, error) {
	publication, err := repos.Publications.ByID(ctx, memberID, publicationID)
	if err != nil {
		return crosspostDomain.Publication{}, crosspostDomain.Delivery{}, err
	}
	deliveries, err := repos.Deliveries.ByPublication(ctx, memberID, publicationID)
	if err != nil {
		return crosspostDomain.Publication{}, crosspostDomain.Delivery{}, err
	}
	for _, d := range deliveries {
		if d.ID == deliveryID {
			return publication, d, nil
		}
	}
	return crosspostDomain.Publication{}, crosspostDomain.Delivery{}, crosspostDomain.ErrNotFound
}

func audienceZone(name string) *time.Location {
	if location, err := time.LoadLocation(name); err == nil && name != "" {
		return location
	}
	location, err := time.LoadLocation(DefaultAudienceZone)
	if err != nil {
		return time.UTC
	}
	return location
}

func networkName(network crosspostDomain.Network) string {
	if rules, ok := crosspostDomain.Rules[network]; ok {
		return rules.Name
	}
	return string(network)
}

// CrosspostStatusLine is the delivery's line on the web and in Telegram (ux.md Table 3; skip lines Table 2).
func CrosspostStatusLine(d crosspostDomain.Delivery) string {
	name := networkName(d.Network)
	switch d.Status {
	case crosspostDomain.StatusProposed, crosspostDomain.StatusScheduled:
		return name + " · " + formatPostTime(d.PublishAt)
	case crosspostDomain.StatusPublishing:
		return name + " · posting now"
	case crosspostDomain.StatusPublished:
		return strings.TrimSpace(name + " ✓ " + d.ExternalURL)
	case crosspostDomain.StatusSentToInbox:
		return name + " · in your " + name + " inbox, you post it"
	case crosspostDomain.StatusNeedsReview:
		return name + " · may already be posted (Bloomary stopped mid-post)"
	case crosspostDomain.StatusNeedsReconnect:
		return name + " · held: reconnect " + name
	case crosspostDomain.StatusFailed:
		return name + " · failed: " + failureReason(d)
	case crosspostDomain.StatusCancelled:
		return name + " · skipped"
	case crosspostDomain.StatusSkipped:
		if d.SkipReason != "" {
			return d.SkipReason
		}
		return name + " · skipped"
	case crosspostDomain.StatusSimulated:
		return name + " · [stage · not posted]"
	}
	return name
}

// LinkedInStatusLine is the LinkedIn part's line: its scheduling refusal, or the scheduled post's status.
func LinkedInStatusLine(part crosspostDomain.LinkedInPart) string {
	if part.SkipReason != "" {
		return "LinkedIn · skipped: " + part.SkipReason
	}
	if part.Post == nil {
		return "LinkedIn · " + formatPostTime(part.ProposedAt)
	}
	switch part.Post.Status {
	case "published":
		return "LinkedIn ✓"
	case "failed":
		return "LinkedIn · failed: " + part.Post.Error
	case "needs_review":
		return "LinkedIn · may already be posted (Bloomary stopped mid-post)"
	case "cancelled", "deleted_draft":
		return "LinkedIn · skipped"
	case "processing":
		return "LinkedIn · posting now"
	}
	return "LinkedIn · " + formatPostTime(part.Post.ScheduledAt)
}

// Delivery error codes (LastErrorCode: category only, never network text).
const (
	errorCodeSendFailed   = "send_failed"
	errorCodeNotAvailable = "network_not_available"
	errorCodeReconnect    = "needs_reconnect"
)

func failureReason(d crosspostDomain.Delivery) string {
	name := networkName(d.Network)
	switch {
	case strings.HasPrefix(d.LastErrorCode, "content_rejected"):
		return name + " refused the post"
	case d.LastErrorCode == errorCodeNotAvailable:
		return name + " isn't open yet"
	default:
		return name + " didn't answer after 5 tries"
	}
}

func isNotFound(err error) bool { return errors.Is(err, crosspostDomain.ErrNotFound) }

func isVersionConflict(err error) bool { return errors.Is(err, crosspostDomain.ErrVersionConflict) }
