package review

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound      = errors.New("review item not found")
	ErrDailyLimit    = errors.New("daily Telegram message limit for this member reached")
	ErrNotScheduled  = errors.New("schedule the draft with schedule_draft before requesting approval")
	ErrNotLinked     = errors.New("the member has not linked Telegram yet (Bloomary → Settings → Telegram)")
	ErrUnknownKind   = errors.New(`kind must be "comment"`)
	ErrInvalidTarget = errors.New("target_url must be an http or https URL")
)

const (
	DailyApprovalLimit   = 30
	DailySuggestionLimit = 50
	MaxSuggestionRunes   = 3000
	MaxContextRunes      = 1000
	MaxNoteRunes         = 500
	// CopyTextLimit is the Bot API limit for a CopyTextButton (1-256 characters).
	CopyTextLimit = 256
	// FeedbackWindow is how far back list_feedback looks when no since is given.
	FeedbackWindow = 30 * 24 * time.Hour
)

type ApprovalStatus string

const (
	ApprovalPending    ApprovalStatus = "pending"
	ApprovalApproved   ApprovalStatus = "approved"
	ApprovalSkipped    ApprovalStatus = "skipped"
	ApprovalSuperseded ApprovalStatus = "superseded"
)

// Approval is one Telegram preview of an MCP-scheduled draft; ProposedText is the draft text the AI app wrote.
type Approval struct {
	CreatedAt    time.Time
	ScheduledAt  time.Time
	DecidedAt    *time.Time
	ID           string
	MemberID     string
	DraftID      string
	ScheduleID   string
	ProposedText string
	Note         string
	Status       ApprovalStatus
}

// Suggestion is a comment idea sent to the member's Telegram, with the member's rating and posted flag.
type Suggestion struct {
	CreatedAt time.Time
	RatedAt   *time.Time
	PostedAt  *time.Time
	ID        string
	MemberID  string
	Kind      string
	TargetURL string
	Text      string
	Context   string
	Rating    int
}

type SuggestionInput struct {
	MemberID  string
	Kind      string
	TargetURL string
	Text      string
	Context   string
}

// ApprovalFeedback adds the text as it is now; Diff marks the member's edits as [-removed-]{+added+}.
type ApprovalFeedback struct {
	Approval
	FinalText string
	Diff      string
	Edited    bool
}

type Feedback struct {
	Approvals   []ApprovalFeedback
	Suggestions []Suggestion
}

const maxDiffWords = 2000

// WordDiff compares two texts word by word (longest common subsequence) and returns
// the final words with removals as [-old-] and additions as {+new+}. Texts longer than
// maxDiffWords words return an empty diff.
func WordDiff(before, after string) string {
	a, b := strings.Fields(before), strings.Fields(after)
	if len(a) > maxDiffWords || len(b) > maxDiffWords {
		return ""
	}

	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}

	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var out []string
	var removed, added []string
	flush := func() {
		change := ""
		if len(removed) > 0 {
			change += "[-" + strings.Join(removed, " ") + "-]"
			removed = nil
		}

		if len(added) > 0 {
			change += "{+" + strings.Join(added, " ") + "+}"
			added = nil
		}

		if change != "" {
			out = append(out, change)
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			flush()
			out = append(out, a[i])
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			added = append(added, b[j])
			j++
		default:
			removed = append(removed, a[i])
			i++
		}
	}

	flush()
	return strings.Join(out, " ")
}
