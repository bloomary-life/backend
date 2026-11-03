package stories

import (
	"context"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	"bloomary-api/internal/platform/logger"
)

// Retention periods: market/docs/legal/ropa.md "Cross-posting retention" Table 1 (approved by Kat 2026-11-03).
const (
	publicationRetention = 365 * 24 * time.Hour // row 2: 1 year after the last delivery
	auditRetention       = 365 * 24 * time.Hour // row 6: 1 year
	mediaRetention       = 90 * 24 * time.Hour  // row 4: 90 days; kept while a delivery is unfinished
)

// RetentionJob runs daily: it deletes the frozen R2 media of publications closed more than 90 days ago (every
// delivery final), publications (with their deliveries) closed more than a year ago, and audit rows older than a
// year. Connections and destinations stay until removed or account deletion (row 1). Video requests (row 3) and
// studio masters belong to the video studio's own purge.
type RetentionJob struct {
	publications crosspostDomain.RetentionStore
	audit        crosspostDomain.AuditLog
	objects      draftDomain.ObjectStore
	now          func() time.Time
}

// NewRetentionJob: objects is nil while R2 isn't configured (then no media purge).
func NewRetentionJob(publications crosspostDomain.RetentionStore, audit crosspostDomain.AuditLog, objects draftDomain.ObjectStore) (*RetentionJob, error) {
	if err := requireDeps("retention job", map[string]any{"publications": publications, "audit": audit}); err != nil {
		return nil, err
	}
	return &RetentionJob{publications: publications, audit: audit, objects: objects, now: time.Now}, nil
}

func (j *RetentionJob) Run(ctx context.Context) {
	runDaily(ctx, j.PurgeOnce)
}

// PurgeOnce deletes what the retention table no longer keeps.
func (j *RetentionJob) PurgeOnce(ctx context.Context) {
	now := j.now().UTC()
	if j.objects != nil {
		j.purgeMedia(ctx, now.Add(-mediaRetention))
	}
	deleted, err := j.publications.DeleteClosedBefore(ctx, now.Add(-publicationRetention))
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_retention_publications_failed")
	}
	if err := j.audit.PurgeBefore(ctx, now.Add(-auditRetention)); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_retention_audit_failed")
	}
	logger.FromContext(ctx).WithFields(logger.Fields{"publications_deleted": deleted}).Info("crosspost_retention_done")
}

func (j *RetentionJob) purgeMedia(ctx context.Context, cutoff time.Time) {
	keys, err := j.publications.MediaClosedBefore(ctx, cutoff)
	if err == nil {
		err = deleteObjectKeys(ctx, j.objects, keys)
	}
	if err == nil {
		err = j.publications.ClearMediaClosedBefore(ctx, cutoff)
	}
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_retention_media_failed")
	}
}
