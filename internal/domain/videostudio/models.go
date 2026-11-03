package videostudio

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("videostudio: not found")
	// ErrJobCancelled: Kat cancelled the job; the producer's heartbeat gets 409.
	ErrJobCancelled = errors.New("videostudio: job cancelled")
	// ErrLeaseLost: the lease token is wrong or expired.
	ErrLeaseLost       = errors.New("videostudio: lease lost")
	ErrVersionConflict = errors.New("videostudio: request changed since it was read")
	// ErrInvalidTransition: the request can't move to that status from where it is (e.g. a stale button).
	ErrInvalidTransition = errors.New("videostudio: invalid status change")
	// ErrMasterChanged: the master's SHA-256 differs from the one recorded at /render; nothing is approved.
	ErrMasterChanged = errors.New("videostudio: the video changed after it was checked")
	// ErrRenderSubmitted: the job's render was already submitted; a second /render is refused.
	ErrRenderSubmitted = errors.New("videostudio: render already submitted")
	// ErrNotAllowed: the member isn't in the studio allowlist.
	ErrNotAllowed = errors.New("videostudio: the video studio isn't open for this account")
)

// BadUploadError: an upload failed the backend's checks (size, probe, hash); its objects were deleted.
type BadUploadError struct{ Reason string }

func (e BadUploadError) Error() string { return "videostudio: upload refused: " + e.Reason }

// DailyLimitError: the member started Limits.JobsPerDay jobs in the last 24 hours; NextAt is when one frees up.
type DailyLimitError struct {
	NextAt time.Time
	Limit  int
}

func (e DailyLimitError) Error() string { return "videostudio: daily job limit reached" }

// Limits are the cost guard (security.md §3 row 7): time per job, jobs per member per rolling 24 hours (change
// requests included), paid AI shots per video that need no go.
type Limits struct {
	JobTime           time.Duration
	JobsPerDay        int
	PaidShotsPerVideo int
}

// DefaultLimits: 60 minutes, 10 jobs, 5 paid shots (BLOOMARY_STUDIO_PAID_SHOTS_PER_VIDEO).
var DefaultLimits = Limits{JobTime: time.Hour, JobsPerDay: 10, PaidShotsPerVideo: 5}

// Lease is how long a claim holds a job before another claim may take it.
const Lease = 30 * time.Minute

// IdeaPromptTTL: a reply to the 🎬 prompt is accepted for 24 hours.
const IdeaPromptTTL = 24 * time.Hour

type RequestStatus string

const (
	StatusWaitingForIdea     RequestStatus = "waiting_for_idea"
	StatusWaitingForProducer RequestStatus = "waiting_for_producer"
	StatusWritingScenario    RequestStatus = "writing_scenario"
	StatusNeedsPaidGo        RequestStatus = "needs_paid_go"
	StatusVoicing            RequestStatus = "voicing"
	StatusRendering          RequestStatus = "rendering"
	StatusVideoReady         RequestStatus = "video_ready"
	StatusApproved           RequestStatus = "approved"
	StatusCancelled          RequestStatus = "cancelled"
	StatusDiscarded          RequestStatus = "discarded"
	StatusFailed             RequestStatus = "failed"
)

// VideoRequest is Kat's idea moving through scenario → video → approved draft.
// The draft ID at approval = the request ID (CreateIfAbsent), so a second press makes no second draft.
type VideoRequest struct {
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ID           string
	MemberID     string
	Idea         string
	VoiceOverKey string
	Status       RequestStatus
	Scenario     Scenario
	Render       Render
	Messages     StudioMessages
	// PublicationID is the publication Prepare made at the video approval.
	PublicationID string
	State         StudioState
	Version       int
}

// StudioState is what the studio remembers between messages: Kat's AI label choice, her network choices before the
// text approval, the paid-shot answer and the last failure.
type StudioState struct {
	// Choices: delivery ID → ChoiceNow or ChoiceSkip (absent = best time), from "Choose networks".
	Choices      map[string]string
	FailedKind   JobKind
	Failure      string
	AILabelOff   bool
	Choosing     bool
	OfflineShown bool
	PaidShotsGo  bool
}

const (
	ChoiceNow  = "now"
	ChoiceSkip = "skip"
)

// ConfirmedAI is the AI content as Kat confirmed it at the video approval ("No AI label" turns all off).
func (r VideoRequest) ConfirmedAI() AIContent {
	if r.State.AILabelOff {
		return AIContent{}
	}
	return r.Scenario.AIContent
}

// requestTransitions lists every allowed status change; a change to the same status is allowed (idempotent saves).
var requestTransitions = map[RequestStatus][]RequestStatus{
	StatusWaitingForIdea:  {StatusWritingScenario, StatusDiscarded},
	StatusWritingScenario: {StatusNeedsPaidGo, StatusVoicing, StatusCancelled, StatusFailed, StatusDiscarded},
	StatusNeedsPaidGo:     {StatusVoicing, StatusWritingScenario, StatusDiscarded},
	StatusVoicing:         {StatusRendering, StatusCancelled, StatusFailed, StatusDiscarded},
	StatusRendering:       {StatusVideoReady, StatusCancelled, StatusFailed, StatusDiscarded},
	StatusVideoReady:      {StatusApproved, StatusWritingScenario, StatusDiscarded},
	StatusApproved:        {StatusDiscarded},
	StatusCancelled:       {StatusDiscarded},
	StatusFailed:          {StatusWritingScenario, StatusVoicing, StatusRendering, StatusDiscarded},
}

// MoveTo changes the request's status when the change is allowed.
func (r *VideoRequest) MoveTo(status RequestStatus) error {
	if r.Status == status {
		return nil
	}
	for _, allowed := range requestTransitions[r.Status] {
		if allowed == status {
			r.Status = status
			return nil
		}
	}
	return ErrInvalidTransition
}

// Working reports whether the producer or the voice-over job is busy with the request.
func (r VideoRequest) Working() bool {
	return r.Status == StatusWritingScenario || r.Status == StatusVoicing || r.Status == StatusRendering
}

// AIContent is declared by the producer and confirmed by Kat at the video approval.
type AIContent struct {
	Voice          bool
	RealisticShots bool
	Music          bool
}

type YouTubeText struct {
	Title       string
	Description string
	Chapters    string
}

type Scenario struct {
	Captions      map[string]string // network name → caption + hashtags; input to Prepare only
	Title         string
	Hook          string
	Script        string
	VoiceOverText string
	ShotList      string
	SelfReview    string
	YouTube       YouTubeText
	Unfixed       []string
	PaidShots     int
	AIContent     AIContent
}

// NeedsPaidGo: paid shots within the per-video cap are Kat's standing go; more need her answer.
func (s Scenario) NeedsPaidGo(capPerVideo int) bool {
	return s.PaidShots > capPerVideo
}

// Render is the producer's upload, hashed and probed by the backend.
type Render struct {
	MasterKey    string
	MasterSHA256 string // recorded at /render, checked at approval
	CaptionsKey  string
	CoverKey     string
	MusicLicence string
	// CaptionsSHA256 and CoverSHA256 are recorded at /render too and checked at approval.
	CaptionsSHA256 string
	CoverSHA256    string
	Bytes          int64
	Seconds        float64
	Width          int
	Height         int
	// JobID is the render job whose /render was accepted (a second one is refused).
	JobID string
}

// StudioMessages are the Telegram messages of one request, so a reply finds its request.
type StudioMessages struct {
	ChatID       int64
	IdeaPrompt   int64
	Status       int64
	Video        int64
	TextAndTimes []int64
}

type JobKind string

const (
	JobWriteScenario  JobKind = "write_scenario"
	JobReviseScenario JobKind = "revise_scenario"
	JobRenderVideo    JobKind = "render_video"
	JobReviseCaptions JobKind = "revise_captions"
)

type ProducerJob struct {
	LeaseUntil time.Time
	ID         string
	RequestID  string
	MemberID   string
	Kind       JobKind
	Input      string
	LeaseToken string // returned once by Claim; stored only as its SHA-256
	Cancelled  bool
	// ClaimedAt is the first claim: the job's time limit runs from it.
	ClaimedAt time.Time
	CreatedAt time.Time
}

// StartsWork reports whether the job counts toward the daily limit: a new idea or a change request; the render that
// follows a scenario is part of the same request.
func (k JobKind) StartsWork() bool { return k != JobRenderVideo }
