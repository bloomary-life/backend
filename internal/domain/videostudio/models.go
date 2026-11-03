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
)

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
	Version      int
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
	Bytes        int64
	Seconds      float64
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
}
