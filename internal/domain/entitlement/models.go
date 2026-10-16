package entitlement

import "time"

const (
	FreeDraftLimit = 5
	ProDraftLimit  = 50

	SubscriptionSourcePaddle = "paddle"
	SubscriptionSourceApple  = "apple"
	SubscriptionSourceTrial  = "trial"

	// "Free" is not one access state. A member whose trial ran out and a member
	// whose paid subscription ended both land in TierLapsed, but the read-only
	// copy has to tell them apart, so the state is computed here once instead of
	// being re-derived differently by each client.
	TierTrial  = "trial"
	TierPro    = "pro"
	TierLapsed = "lapsed"
)

// Entitlement is the unified access decision produced by the access story.
// It does not know which provider granted access; it only records whether the
// user has access and until when.
type Entitlement struct {
	AccessUntil        time.Time
	IsPro              bool
	SubscriptionSource string
	Tier               string
	DraftLimit         int
	PostCount          int
}

// HasDraftAccess reports whether the server-side draft archive is fully open to
// this member. A lapsed member's archive is write-once: pull stays open, a new
// draft (and its image) may be added under the free limit, but nothing stored
// can be edited or deleted. Draft image downloads stay reachable so the
// read-only archive can still show its images.
func (e Entitlement) HasDraftAccess() bool {
	return e.Tier == TierPro || e.Tier == TierTrial
}
