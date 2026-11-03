package videostudio

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

// Two approvals (Kat 2026-11-03): the finished video, then the text and times. No scenario approval.

type StartVideoRequestInput struct {
	MemberID string
	Idea     string
	ChatID   int64
	ReplyTo  int64 // the idea prompt the member replied to
}

type StartVideoRequestStory interface {
	StartVideoRequest(ctx context.Context, input StartVideoRequestInput) (VideoRequest, error)
}

type RequestChangesInput struct {
	MemberID  string
	RequestID string
	Change    string
}

type RequestChangesStory interface {
	// RequestChanges: a reply to the video revises the scenario and re-renders; a reply to the text revises the captions.
	RequestChanges(ctx context.Context, input RequestChangesInput) (VideoRequest, error)
}

type ApprovePaidShotsInput struct {
	MemberID  string
	RequestID string
	KeepToCap bool // "Keep to 5" instead of "Go: 8 paid shots"
}

type ApprovePaidShotsStory interface {
	// ApprovePaidShots is asked only when the scenario's paid shots are above the per-video cap.
	ApprovePaidShots(ctx context.Context, input ApprovePaidShotsInput) (VideoRequest, error)
}

type MakeVoiceOverStory interface {
	// MakeVoiceOver (worker) speaks the scenario's voice-over text, then enqueues the render job.
	MakeVoiceOver(ctx context.Context, requestID string) error
}

type ApproveVideoInput struct {
	MemberID       string
	RequestID      string
	TelegramUserID int64
	AIContent      AIContent // as Kat confirmed or changed it
}

type ApproveVideoOutput struct {
	Request     VideoRequest
	Publication crosspostDomain.PublicationView
}

type ApproveVideoStory interface {
	// ApproveVideo checks the master's SHA-256, copies it to approved/, creates the draft (ID = request ID) and runs Prepare.
	ApproveVideo(ctx context.Context, input ApproveVideoInput) (ApproveVideoOutput, error)
}

type RetryVideoRequestStory interface {
	// RetryVideoRequest runs the failed step again (Retry after "… failed" or the time limit).
	RetryVideoRequest(ctx context.Context, memberID, requestID string) (VideoRequest, error)
}

type CancelVideoRequestStory interface {
	CancelVideoRequest(ctx context.Context, memberID, requestID string) (VideoRequest, error)
}

type DiscardVideoRequestStory interface {
	DiscardVideoRequest(ctx context.Context, memberID, requestID string) (VideoRequest, error)
}

// Producer side.

type ClaimedJob struct {
	Job      ProducerJob
	InputURL string // presigned GET for the voice-over, when the job has one
}

type ClaimProducerJobStory interface {
	// ClaimProducerJob returns the oldest due job with a 30-minute lease, or ErrNotFound.
	ClaimProducerJob(ctx context.Context) (ClaimedJob, error)
}

type HeartbeatProducerJobStory interface {
	// HeartbeatProducerJob extends the lease; ErrJobCancelled when Kat cancelled or the time limit stopped it.
	HeartbeatProducerJob(ctx context.Context, jobID, leaseToken string) error
}

// UploadSlot is one presigned PUT for a file of the job.
type UploadSlot struct {
	ExpiresAt   time.Time
	Name        string // master, captions, cover
	Key         string
	URL         string
	ContentType string
}

type PrepareUploadsStory interface {
	// PrepareUploads returns presigned PUTs (short TTL, content type fixed, keys scoped to the job) for a render job.
	PrepareUploads(ctx context.Context, jobID, leaseToken string) ([]UploadSlot, error)
}

type SubmitScenarioInput struct {
	JobID      string
	LeaseToken string
	Scenario   Scenario
}

type SubmitScenarioStory interface {
	SubmitScenario(ctx context.Context, input SubmitScenarioInput) error
}

// SubmitRenderInput: the files are the job's upload slots (master, captions, cover), never other keys.
type SubmitRenderInput struct {
	JobID        string
	LeaseToken   string
	MusicLicence string
}

type SubmitRenderStory interface {
	// SubmitRender hashes and probes each upload itself and refuses a second render for the job.
	SubmitRender(ctx context.Context, input SubmitRenderInput) error
}

type ReportProducerFailureInput struct {
	JobID      string
	LeaseToken string
	Reason     string
}

type ReportProducerFailureStory interface {
	ReportProducerFailure(ctx context.Context, input ReportProducerFailureInput) error
}
