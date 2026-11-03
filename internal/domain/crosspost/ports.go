package crosspost

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Ports and words the crosspost stories need beyond contracts.go.

var (
	// ErrNotOpen: no connector or publisher for the provider or network yet ("Not open yet. We'll tell you when it is.").
	ErrNotOpen = errors.New("crosspost: not open yet")
	// ErrConnectNotAllowed: the member is not in BLOOMARY_CONNECT_MEMBER_IDS.
	ErrConnectNotAllowed = errors.New("crosspost: connecting networks is not open for this account")
	// ErrStateInvalid: the login's state is unknown, used, expired or another member's.
	ErrStateInvalid = errors.New("crosspost: login expired, start again")
	// ErrNotYourDestination: a destination the member doesn't own (403).
	ErrNotYourDestination = errors.New("crosspost: destination not found in this account")
	ErrPlanRequired       = errors.New("crosspost: publishing needs an active plan")
	// ErrYouTubeChoices: privacy and made-for-kids are chosen by the member before approval.
	ErrYouTubeChoices = errors.New("YouTube needs 2 choices first.")
	ErrNotEditable    = errors.New("crosspost: this post has started and can't be changed")
	ErrBadZone        = errors.New("crosspost: unknown time zone")
)

// DifferentAccountError: a reconnect returned another account; nothing was replaced.
type DifferentAccountError struct {
	Network Network
	Name    string // the account the connection keeps
}

func (e DifferentAccountError) Error() string {
	name := Rules[e.Network].Name
	return "That's a different " + name + " account. Nothing changed. Sign in as " + e.Name + ", or remove this one and add the new one."
}

// BlockingProblemsError: approval waits until these lines are fixed ("Fix 1 problem first: …").
type BlockingProblemsError struct{ Lines []string }

func (e BlockingProblemsError) Error() string {
	if len(e.Lines) == 1 {
		return "Fix 1 problem first: " + e.Lines[0]
	}
	return "Fix these problems first: " + strings.Join(e.Lines, " ")
}

// ConnectState is a started provider login: kept on the server, single-use, 10 minutes.
type ConnectState struct {
	CreatedAt    time.Time
	MemberID     string
	Provider     Provider
	AppOwner     AppOwner
	CodeVerifier string
	RedirectURI  string
}

const ConnectStateTTL = 10 * time.Minute

type ConnectStateStore interface {
	Put(ctx context.Context, state string, value ConnectState) error
	// Take returns the state once; ErrStateInvalid when unknown, used or older than ConnectStateTTL.
	Take(ctx context.Context, state string, now time.Time) (ConnectState, error)
}

// DeliveryLister lists every delivery of a member (connection removal, account deletion, reconnect).
type DeliveryLister interface {
	ListByMember(ctx context.Context, memberID string) ([]Delivery, error)
}

// ConnectionRemover deletes one connection and its destinations.
type ConnectionRemover interface {
	DeleteConnection(ctx context.Context, memberID, connectionID string) error
}

// LinkedInIntent is a publication's LinkedIn part before and after approval: its proposed time and the
// scheduling refusal, if any. The LinkedIn post itself lives in scheduledpost.
type LinkedInIntent struct {
	ProposedAt time.Time
	SkipReason string
}

type LinkedInIntentStore interface {
	SaveIntent(ctx context.Context, memberID, publicationID string, intent LinkedInIntent) error
	// Intent returns ErrNotFound when the publication has no LinkedIn part.
	Intent(ctx context.Context, memberID, publicationID string) (LinkedInIntent, error)
	DeleteByMemberID(ctx context.Context, memberID string) error
}

// RetentionStore deletes what the retention table (market/docs/legal/ropa.md) no longer keeps.
type RetentionStore interface {
	// DeleteClosedBefore deletes publications closed before cutoff with their deliveries and LinkedIn intents.
	DeleteClosedBefore(ctx context.Context, cutoff time.Time) (int, error)
	// MediaClosedBefore lists the frozen media keys of deliveries whose publication closed before cutoff;
	// ClearMediaClosedBefore then blanks them once the objects are deleted.
	MediaClosedBefore(ctx context.Context, cutoff time.Time) ([]string, error)
	ClearMediaClosedBefore(ctx context.Context, cutoff time.Time) error
}

// DeliveryCounter counts finished deliveries by network and status (metrics).
type DeliveryCounter interface {
	RecordDelivery(network Network, status DeliveryStatus)
}

// AIFlags are the draft's AI content flags.
type AIFlags struct {
	Voice          bool
	RealisticShots bool
	Music          bool
}

// AILabel is whether a network's AI label is on for these flags (platforms.md Table 4). YouTube: not for a synthetic
// voice alone; TikTok: voice or realistic shots; Instagram, Facebook and X: any of the three.
func AILabel(network Network, flags AIFlags) bool {
	switch network {
	case YouTube:
		return flags.RealisticShots || flags.Music
	case TikTok:
		return flags.Voice || flags.RealisticShots
	case LinkedIn:
		return false
	default:
		return flags.Voice || flags.RealisticShots || flags.Music
	}
}

// BlockingProblems are the text, title and hashtag problems the member must fix before approval (media problems skip
// the delivery instead, at Prepare and Approve).
func BlockingProblems(d Delivery) []string {
	problems, _ := Rules[d.Network].Check(ContentToCheck{Text: d.Text, Title: d.Title, Hashtags: d.Hashtags})
	var blocking []string
	for _, line := range problems {
		if !strings.Contains(line, " · skipped: ") {
			blocking = append(blocking, line)
		}
	}
	return blocking
}

// SkipLines splits Check problems into skip lines (media) and blocking lines (text).
func SkipLines(problems []string) (skip, blocking []string) {
	for _, line := range problems {
		if strings.Contains(line, " · skipped: ") {
			skip = append(skip, line)
		} else {
			blocking = append(blocking, line)
		}
	}
	return skip, blocking
}

// ProviderOf is the login a network's destinations come from.
func ProviderOf(network Network) Provider {
	switch network {
	case X:
		return ProviderX
	case FacebookPage, Instagram:
		return ProviderMeta
	case YouTube:
		return ProviderGoogle
	case TikTok:
		return ProviderTikTok
	}
	return ""
}

// CrosspostNetworks are the networks the crosspost worker posts to (LinkedIn keeps its own worker).
var CrosspostNetworks = []Network{X, FacebookPage, Instagram, YouTube, TikTok}

// NetworksView is Settings → Networks: the member's connections and destinations, and the providers open to connect.
type NetworksView struct {
	Connections  []Connection
	Destinations []Destination
	Open         []Provider
}

type ListNetworksStory interface {
	ListNetworks(ctx context.Context, memberID string) (NetworksView, error)
}
