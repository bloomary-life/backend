package stories

import (
	"context"
	"time"
	"unicode/utf8"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

// Producer side of the studio: the producer on Kat's Mac claims jobs, heartbeats, uploads and submits
// (video-studio.md Table 1). Every call after the claim carries the lease token.

type claimProducerJobStory struct {
	deps StudioDeps
	now  func() time.Time
}

func NewClaimProducerJobStory(deps StudioDeps) (videostudioDomain.ClaimProducerJobStory, error) {
	if err := requireDeps("claim producer job story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "media": deps.Media, "notifier": deps.Notifier, "audit": deps.Audit}); err != nil {
		return nil, err
	}
	return &claimProducerJobStory{deps: deps, now: time.Now}, nil
}

// ClaimProducerJob records the producer as seen and leases the oldest waiting job for 30 minutes, with a presigned GET
// for the voice-over when the job renders; ErrNotFound when nothing waits. A request shown as "waiting for the Mac"
// gets its message back.
func (s *claimProducerJobStory) ClaimProducerJob(ctx context.Context) (videostudioDomain.ClaimedJob, error) {
	job, err := s.deps.Jobs.Claim(ctx, s.now().UTC(), videostudioDomain.Lease)
	if err != nil {
		return videostudioDomain.ClaimedJob{}, err
	}
	appendAudit(ctx, s.deps.Audit, crosspostDomain.AuditRecord{At: s.now().UTC(), MemberID: job.MemberID, Action: crosspostDomain.AuditClaim, RecordIDs: []string{job.RequestID, job.ID}})
	claimed := videostudioDomain.ClaimedJob{Job: job}
	request, err := s.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
	if err != nil {
		return claimed, nil
	}
	if job.Kind == videostudioDomain.JobRenderVideo && request.VoiceOverKey != "" {
		if claimed.InputURL, _, err = s.deps.Media.DownloadURL(ctx, request.VoiceOverKey); err != nil {
			return videostudioDomain.ClaimedJob{}, err
		}
	}
	if request.State.OfflineShown {
		request.State.OfflineShown = false
		if saved, err := saveRequest(ctx, s.deps.Requests, request); err == nil {
			notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventOnline)
		}
	}
	return claimed, nil
}

type heartbeatProducerJobStory struct {
	deps StudioDeps
	stop *overTimeStopper
	now  func() time.Time
}

func NewHeartbeatProducerJobStory(deps StudioDeps) (videostudioDomain.HeartbeatProducerJobStory, error) {
	if err := requireDeps("heartbeat producer job story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "notifier": deps.Notifier}); err != nil {
		return nil, err
	}
	return &heartbeatProducerJobStory{deps: deps, stop: &overTimeStopper{deps: deps}, now: time.Now}, nil
}

// HeartbeatProducerJob extends the lease; a job past its time limit (60 minutes from the first claim) is stopped and
// Kat is told, and the producer gets ErrJobCancelled (409) as after a Cancel.
func (s *heartbeatProducerJobStory) HeartbeatProducerJob(ctx context.Context, jobID, leaseToken string) error {
	job, err := s.deps.Jobs.Leased(ctx, jobID, leaseToken)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if !job.ClaimedAt.IsZero() && now.Sub(job.ClaimedAt) > s.deps.Limits.JobTime {
		if err := s.stop.Stop(ctx, job); err != nil {
			return err
		}
		return videostudioDomain.ErrJobCancelled
	}
	return s.deps.Jobs.Heartbeat(ctx, jobID, leaseToken, now.Add(videostudioDomain.Lease))
}

// overTimeStopper stops a job past the time limit: the request's jobs are cancelled and it waits for Retry or Discard.
type overTimeStopper struct {
	deps StudioDeps
}

func (s *overTimeStopper) Stop(ctx context.Context, job videostudioDomain.ProducerJob) error {
	if err := s.deps.Jobs.Cancel(ctx, job.RequestID); err != nil {
		return err
	}
	request, err := s.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
	if err != nil {
		return err
	}
	request.State.FailedKind, request.State.FailedInput, request.State.Failure = job.Kind, job.Input, "time limit"
	if job.Kind != videostudioDomain.JobReviseCaptions {
		if err := request.MoveTo(videostudioDomain.StatusFailed); err != nil {
			return nil // already discarded or cancelled
		}
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return err
	}
	notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventTimeLimit)
	return nil
}

type prepareUploadsStory struct {
	deps StudioDeps
}

func NewPrepareUploadsStory(deps StudioDeps) (videostudioDomain.PrepareUploadsStory, error) {
	if err := requireDeps("prepare uploads story", map[string]any{"jobs": deps.Jobs, "media": deps.Media}); err != nil {
		return nil, err
	}
	return &prepareUploadsStory{deps: deps}, nil
}

// renderFiles are the files a render job uploads: name, content type, the most bytes the backend accepts, required.
var renderFiles = []struct {
	name, contentType string
	maxBytes          int64
	required          bool
}{
	{"master", "video/mp4", 50 << 20, true},
	{"captions", "application/x-subrip", 1 << 20, false},
	{"cover", "image/jpeg", 5 << 20, true},
}

func renderKey(media videostudioDomain.MediaStore, job videostudioDomain.ProducerJob, name string) string {
	return media.Key(job.MemberID, job.RequestID, job.ID+"-"+name)
}

// PrepareUploads gives a render job one presigned PUT per file, its key scoped to the job and its content type fixed.
func (s *prepareUploadsStory) PrepareUploads(ctx context.Context, jobID, leaseToken string) ([]videostudioDomain.UploadSlot, error) {
	job, err := s.deps.Jobs.Leased(ctx, jobID, leaseToken)
	if err != nil {
		return nil, err
	}
	if job.Kind != videostudioDomain.JobRenderVideo {
		return nil, videostudioDomain.ErrInvalidTransition
	}
	slots := make([]videostudioDomain.UploadSlot, 0, len(renderFiles))
	for _, file := range renderFiles {
		key := renderKey(s.deps.Media, job, file.name)
		link, expires, err := s.deps.Media.UploadURL(ctx, key, file.contentType)
		if err != nil {
			return nil, err
		}
		slots = append(slots, videostudioDomain.UploadSlot{Name: file.name, Key: key, URL: link, ContentType: file.contentType, ExpiresAt: expires})
	}
	return slots, nil
}

type submitScenarioStory struct {
	deps StudioDeps
	edit crosspostDomain.EditDeliveryStory
}

func NewSubmitScenarioStory(deps StudioDeps, edit crosspostDomain.EditDeliveryStory) (videostudioDomain.SubmitScenarioStory, error) {
	err := requireDeps("submit scenario story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "notifier": deps.Notifier, "publications": deps.Publications, "edit delivery": edit})
	if err != nil {
		return nil, err
	}
	return &submitScenarioStory{deps: deps, edit: edit}, nil
}

// SubmitScenario stores the cleaned scenario (untrusted text: invisible and direction-control characters removed,
// caption URLs only from the idea). A scenario within the paid-shot cap goes straight to the voice-over ("Scenario
// ready"); above it Kat is asked. A revise_captions result updates the deliveries Kat hasn't edited.
func (s *submitScenarioStory) SubmitScenario(ctx context.Context, input videostudioDomain.SubmitScenarioInput) error {
	job, err := s.deps.Jobs.Leased(ctx, input.JobID, input.LeaseToken)
	if err != nil {
		return err
	}
	if job.Kind == videostudioDomain.JobRenderVideo {
		return videostudioDomain.ErrInvalidTransition
	}
	request, err := s.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
	if err != nil {
		return err
	}
	scenario := videostudioDomain.CleanScenario(input.Scenario, request.Idea)
	if scenario.Title == "" || scenario.VoiceOverText == "" || len(scenario.Captions) == 0 {
		return videostudioDomain.ErrScenarioIncomplete
	}
	if job.Kind == videostudioDomain.JobReviseCaptions {
		return s.captions(ctx, job, input.LeaseToken, request, scenario)
	}
	event := videostudioDomain.EventScenarioReady
	next := videostudioDomain.StatusVoicing
	if scenario.NeedsPaidGo(s.deps.Limits.PaidShotsPerVideo) && !request.State.PaidShotsGo {
		event, next = videostudioDomain.EventNeedsPaidGo, videostudioDomain.StatusNeedsPaidGo
	}
	request.Scenario = scenario
	if err := request.MoveTo(next); err != nil {
		return err
	}
	if err := s.deps.Jobs.Complete(ctx, job.ID, input.LeaseToken); err != nil {
		return err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return err
	}
	notifyStudio(ctx, s.deps.Notifier, saved, event)
	return nil
}

func (s *submitScenarioStory) captions(ctx context.Context, job videostudioDomain.ProducerJob, leaseToken string, request videostudioDomain.VideoRequest, scenario videostudioDomain.Scenario) error {
	view, err := s.deps.Publications.PublicationStatus(ctx, request.MemberID, request.PublicationID)
	if err != nil {
		return err
	}
	if !view.Publication.ApprovedAt.IsZero() {
		return videostudioDomain.ErrInvalidTransition
	}
	previous := request.Scenario
	request.Scenario.Captions, request.Scenario.YouTube = scenario.Captions, scenario.YouTube
	if err := applyCaptions(ctx, s.edit, view, request.Scenario, &previous, aiFlags(request.ConfirmedAI())); err != nil {
		return err
	}
	if err := s.deps.Jobs.Complete(ctx, job.ID, leaseToken); err != nil {
		return err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return err
	}
	notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventCaptionsUpdated)
	return nil
}

type submitRenderStory struct {
	deps  StudioDeps
	probe crosspostDomain.MediaWorker
}

func NewSubmitRenderStory(deps StudioDeps, probe crosspostDomain.MediaWorker) (videostudioDomain.SubmitRenderStory, error) {
	err := requireDeps("submit render story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "media": deps.Media, "notifier": deps.Notifier, "media worker": probe})
	if err != nil {
		return nil, err
	}
	return &submitRenderStory{deps: deps, probe: probe}, nil
}

// SubmitRender checks the job's uploads itself: each file's size (HEAD), then it streams and hashes each one and
// probes the master (1080×1920 H.264, at most 50 MB, so Telegram can show the master itself) and the cover. A
// failed check deletes the uploads. The SHA-256 of every file is kept for the approval; a second call is refused.
func (s *submitRenderStory) SubmitRender(ctx context.Context, input videostudioDomain.SubmitRenderInput) error {
	job, err := s.deps.Jobs.Leased(ctx, input.JobID, input.LeaseToken)
	if err != nil {
		return err
	}
	if job.Kind != videostudioDomain.JobRenderVideo {
		return videostudioDomain.ErrInvalidTransition
	}
	request, err := s.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
	if err != nil {
		return err
	}
	if request.Render.JobID == job.ID || request.Status != videostudioDomain.StatusRendering {
		return videostudioDomain.ErrRenderSubmitted
	}
	render, err := s.check(ctx, job)
	if err != nil {
		for _, file := range renderFiles {
			_ = s.deps.Media.Delete(ctx, renderKey(s.deps.Media, job, file.name))
		}
		return err
	}
	render.MusicLicence = truncateRunes(videostudioDomain.CleanText(input.MusicLicence), maxFailureRunes)
	previous := request.Render
	request.Render = render
	if err := request.MoveTo(videostudioDomain.StatusVideoReady); err != nil {
		return err
	}
	if err := s.deps.Jobs.Complete(ctx, job.ID, input.LeaseToken); err != nil {
		return err
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return err
	}
	for _, key := range []string{previous.MasterKey, previous.CaptionsKey, previous.CoverKey} {
		if key != "" {
			_ = s.deps.Media.Delete(ctx, key)
		}
	}
	notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventVideoReady)
	return nil
}

func (s *submitRenderStory) check(ctx context.Context, job videostudioDomain.ProducerJob) (videostudioDomain.Render, error) {
	render := videostudioDomain.Render{JobID: job.ID}
	for _, file := range renderFiles {
		key := renderKey(s.deps.Media, job, file.name)
		size, err := s.deps.Media.Size(ctx, key)
		if err != nil || size == 0 {
			if file.required {
				return render, videostudioDomain.BadUploadError{Reason: file.name + " is missing"}
			}
			continue
		}
		if size > file.maxBytes {
			return render, videostudioDomain.BadUploadError{Reason: file.name + " is too large"}
		}
		if file.name == "captions" {
			if err := s.cleanCaptions(ctx, key); err != nil {
				return render, err
			}
		}
		sum, err := s.deps.Media.SHA256(ctx, key)
		if err != nil {
			return render, err
		}
		switch file.name {
		case "master":
			info, err := s.probe.Probe(ctx, key)
			if err != nil {
				return render, videostudioDomain.BadUploadError{Reason: "master can't be read"}
			}
			if info.Kind != crosspostDomain.MediaVideo || info.Codec != "h264" || info.Width != 1080 || info.Height != 1920 || info.Seconds <= 0 {
				return render, videostudioDomain.BadUploadError{Reason: "master must be a 1080×1920 H.264 video"}
			}
			render.MasterKey, render.MasterSHA256, render.Bytes, render.Seconds, render.Width, render.Height = key, sum, size, info.Seconds, info.Width, info.Height
		case "cover":
			info, err := s.probe.Probe(ctx, key)
			if err != nil || info.Kind != crosspostDomain.MediaImage {
				return render, videostudioDomain.BadUploadError{Reason: "cover must be an image"}
			}
			render.CoverKey, render.CoverSHA256 = key, sum
		case "captions":
			render.CaptionsKey, render.CaptionsSHA256 = key, sum
		}
	}
	return render, nil
}

// cleanCaptions removes invisible and direction-control characters from the SRT (untrusted text) and stores it back.
func (s *submitRenderStory) cleanCaptions(ctx context.Context, key string) error {
	raw, err := s.deps.Media.Read(ctx, key)
	if err != nil {
		return err
	}
	cleaned := videostudioDomain.CleanText(string(raw))
	if !utf8.Valid(raw) {
		return videostudioDomain.BadUploadError{Reason: "captions must be UTF-8 text"}
	}
	if cleaned == string(raw) {
		return nil
	}
	return s.deps.Media.Put(ctx, key, "application/x-subrip", []byte(cleaned))
}

type reportProducerFailureStory struct {
	deps StudioDeps
}

func NewReportProducerFailureStory(deps StudioDeps) (videostudioDomain.ReportProducerFailureStory, error) {
	if err := requireDeps("report producer failure story", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "notifier": deps.Notifier}); err != nil {
		return nil, err
	}
	return &reportProducerFailureStory{deps: deps}, nil
}

// ReportProducerFailure ends the job; Kat gets "… failed: <reason>" with Retry · Discard.
func (s *reportProducerFailureStory) ReportProducerFailure(ctx context.Context, input videostudioDomain.ReportProducerFailureInput) error {
	job, err := s.deps.Jobs.Leased(ctx, input.JobID, input.LeaseToken)
	if err != nil {
		return err
	}
	reason := truncateRunes(videostudioDomain.CleanText(input.Reason), maxFailureRunes)
	if reason == "" {
		reason = "no reason given"
	}
	if err := s.deps.Jobs.Fail(ctx, job.ID, input.LeaseToken, reason); err != nil {
		return err
	}
	request, err := s.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
	if err != nil {
		return err
	}
	request.State.FailedKind, request.State.FailedInput, request.State.Failure = job.Kind, job.Input, reason
	if job.Kind != videostudioDomain.JobReviseCaptions {
		if err := request.MoveTo(videostudioDomain.StatusFailed); err != nil {
			return err
		}
	}
	saved, err := saveRequest(ctx, s.deps.Requests, request)
	if err != nil {
		return err
	}
	notifyStudio(ctx, s.deps.Notifier, saved, videostudioDomain.EventFailed)
	return nil
}
