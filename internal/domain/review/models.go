package review

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound      = errors.New("review item not found")
	ErrDailyLimit    = errors.New("daily Telegram message limit for this member reached")
	ErrNotScheduled  = errors.New("schedule the draft with schedule_draft before calling notify_scheduled")
	ErrNotLinked     = errors.New("the member has not linked Telegram yet (Bloomary → Settings → Telegram)")
	ErrUnknownKind   = errors.New(`kind must be "comment"`)
	ErrInvalidTarget = errors.New("target_url must be an https URL of a LinkedIn post (linkedin.com)")
	ErrEditApplied   = errors.New("this edit request is already applied")
)

const (
	DailyNoticeLimit     = 30
	DailySuggestionLimit = 50
	MaxSuggestionRunes   = 3000
	MaxContextRunes      = 1000
	MaxNoteRunes         = 1000
	// DailyEditLimit caps edit requests (replies to post previews) per member per 24 hours; it stays below
	// DailyNoticeLimit so the previews that answer edits leave room for new posts' previews.
	DailyEditLimit = 20
	// DailyPreviewLimit caps previews the member asks for in the bot (Planned posts → Preview), counted
	// apart from DailyNoticeLimit so browsing never blocks the AI app's previews.
	DailyPreviewLimit = 30
	// MaxEditRunes caps one stored edit request.
	MaxEditRunes = 4096
	// CopyTextLimit is the Bot API limit for a CopyTextButton (1-256 characters).
	CopyTextLimit = 256
	// FeedbackWindow is how far back list_feedback looks when no since is given.
	FeedbackWindow = 30 * 24 * time.Hour
)

type NoticeStatus string

const (
	NoticeSent    NoticeStatus = "sent"
	NoticeSkipped NoticeStatus = "skipped"
)

// Notice is one Telegram preview of an MCP-scheduled post; ProposedText is the draft text the AI app wrote.
// The post publishes at ScheduledAt unless the member taps Skip.
type Notice struct {
	CreatedAt    time.Time
	ScheduledAt  time.Time
	SkippedAt    *time.Time
	ID           string
	MemberID     string
	DraftID      string
	ScheduleID   string
	ProposedText string
	Note         string
	Status       NoticeStatus
	// MessageID is the Telegram message of the preview; a reply to it becomes an edit request.
	MessageID int64
	// PhotoMessageID is the image sent just before the preview (0 without image); replies to it count too.
	PhotoMessageID int64
	// Requested marks previews the member asked for in the bot; they have their own daily limit.
	Requested bool
}

// EditRequest is the member's reply (text or transcribed voice) to a post preview: how to change the draft.
// The member's AI app rewrites the draft, sends a new preview and marks the request applied.
type EditRequest struct {
	CreatedAt time.Time
	AppliedAt *time.Time
	ID        string
	MemberID  string
	DraftID   string
	NoticeID  string
	Text      string
	Source    string
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

// PostFeedback adds what happened to the post (scheduled, published, skipped, failed, ...) and the
// draft text as it is now; Diff marks the member's edits as [-removed-]{+added+}.
type PostFeedback struct {
	Notice
	PostStatus string
	FinalText  string
	Diff       string
	Edited     bool
}

type Feedback struct {
	Posts       []PostFeedback
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
