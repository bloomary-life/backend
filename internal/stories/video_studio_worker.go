package stories

import (
	"context"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/platform/logger"
)

// Studio retention (market/docs/legal/ropa.md retention table): row 3 requests and scenarios 1 year; row 4 producer
// uploads and voice-overs 90 days after approval or discard (approved copies belong to the draft); row 8 jobs with
// their request.
const (
	studioRequestRetention = 365 * 24 * time.Hour
	studioMediaRetention   = 90 * 24 * time.Hour
	// producerOfflineAfter: a job waiting this long while the producer wasn't seen shows "Waiting: … offline".
	producerOfflineAfter = 3 * time.Minute
)

// StudioWorker is the studio's background job: voice-overs for requests in "voicing", stopping jobs past the time
// limit, telling Kat when the producer is offline, and (daily) the retention purge.
type StudioWorker struct {
	deps      StudioDeps
	voiceOver videostudioDomain.MakeVoiceOverStory
	stop      *overTimeStopper
	retention videostudioDomain.StudioRetention
	now       func() time.Time
	interval  time.Duration
}

func NewStudioWorker(deps StudioDeps, voiceOver videostudioDomain.MakeVoiceOverStory, retention videostudioDomain.StudioRetention, interval time.Duration) (*StudioWorker, error) {
	err := requireDeps("studio worker", map[string]any{"requests": deps.Requests, "jobs": deps.Jobs, "media": deps.Media, "notifier": deps.Notifier,
		"voice-over story": voiceOver, "retention": retention})
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &StudioWorker{deps: deps, voiceOver: voiceOver, stop: &overTimeStopper{deps: deps}, retention: retention, now: time.Now, interval: interval}, nil
}

func (w *StudioWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.RunOnce(ctx)
	}
}

// RunRetention runs the purge once a day.
func (w *StudioWorker) RunRetention(ctx context.Context) {
	runDaily(ctx, w.PurgeOnce)
}

// RunOnce makes waiting voice-overs, stops jobs past the time limit and shows the producer-offline line.
func (w *StudioWorker) RunOnce(ctx context.Context) {
	log := logger.FromContext(ctx)
	voicing, err := w.deps.Requests.ByStatus(ctx, videostudioDomain.StatusVoicing)
	if err != nil {
		log.WithError(err).Error("studio_voicing_list_failed")
	}
	for _, request := range voicing {
		if err := w.voiceOver.MakeVoiceOver(ctx, request.MemberID, request.ID); err != nil {
			log.WithError(err).Error("studio_voice_over_step_failed")
		}
	}
	now := w.now().UTC()
	over, err := w.deps.Jobs.OverTime(ctx, now.Add(-w.deps.Limits.JobTime))
	if err != nil {
		log.WithError(err).Error("studio_over_time_list_failed")
	}
	for _, job := range over {
		if err := w.stop.Stop(ctx, job); err != nil {
			log.WithError(err).Error("studio_time_limit_stop_failed")
		}
	}
	w.showOffline(ctx, now)
}

func (w *StudioWorker) showOffline(ctx context.Context, now time.Time) {
	waiting, err := w.deps.Jobs.WaitingSince(ctx, now.Add(-producerOfflineAfter))
	if err != nil || len(waiting) == 0 {
		return
	}
	seen, err := w.deps.Jobs.LastSeen(ctx)
	if err != nil || now.Sub(seen) < producerOfflineAfter {
		return
	}
	for _, job := range waiting {
		request, err := w.deps.Requests.ByID(ctx, job.MemberID, job.RequestID)
		if err != nil || request.State.OfflineShown {
			continue
		}
		request.State.OfflineShown = true
		saved, err := saveRequest(ctx, w.deps.Requests, request)
		if err != nil {
			continue
		}
		notifyStudio(ctx, w.deps.Notifier, saved, videostudioDomain.EventOffline)
	}
}

// PurgeOnce deletes producer uploads and voice-overs of requests finished more than 90 days ago, then requests (with
// their jobs) older than a year.
func (w *StudioWorker) PurgeOnce(ctx context.Context) {
	log := logger.FromContext(ctx)
	now := w.now().UTC()
	finished, err := w.retention.FinishedBefore(ctx, now.Add(-studioMediaRetention))
	if err != nil {
		log.WithError(err).Error("studio_retention_media_failed")
	}
	purged := 0
	for _, request := range finished {
		failed := false
		for _, key := range []string{request.VoiceOverKey, request.Render.MasterKey, request.Render.CaptionsKey, request.Render.CoverKey} {
			if key != "" && w.deps.Media.Delete(ctx, key) != nil {
				failed = true
			}
		}
		if failed {
			continue
		}
		request.VoiceOverKey, request.Render = "", videostudioDomain.Render{}
		if _, err := saveRequest(ctx, w.deps.Requests, request); err == nil {
			purged++
		}
	}
	deleted, err := w.retention.DeleteCreatedBefore(ctx, now.Add(-studioRequestRetention))
	if err != nil {
		log.WithError(err).Error("studio_retention_requests_failed")
	}
	log.WithFields(logger.Fields{"media_purged": purged, "requests_deleted": deleted}).Info("studio_retention_done")
}
