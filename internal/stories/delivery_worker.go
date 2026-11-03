package stories

import (
	"context"
	"errors"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	"bloomary-api/internal/platform/logger"
)

// retryAfter are the waits after an error before anything was sent: 1 min, 5 min, 30 min, 2 h, then failed.
var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// refreshLockFor is how long one worker holds a connection's refresh lock; refreshAhead refreshes tokens that expire soon.
const (
	refreshLockFor = 2 * time.Minute
	refreshAhead   = 5 * time.Minute
)

// DeliveryWorker posts due deliveries to X, Facebook Page, Instagram, YouTube and TikTok (LinkedIn keeps its worker).
type DeliveryWorker struct {
	repos      CrosspostRepos
	vault      crosspostDomain.CredentialVault
	guard      crosspostDomain.PublishGuard
	publishers map[crosspostDomain.Network]crosspostDomain.NetworkPublisher
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
	drafts     draftDomain.Repository
	hooks      []crosspostDomain.DeliveryFinished
	now        func() time.Time
	interval   time.Duration
}

// NewDeliveryWorker: publishers and connectors stay empty until the network gates land (dry_run still simulates);
// hooks run after a delivery is published, sent to the inbox, failed, held or needs review (never when simulated).
func NewDeliveryWorker(repos CrosspostRepos, vault crosspostDomain.CredentialVault, guard crosspostDomain.PublishGuard,
	publishers map[crosspostDomain.Network]crosspostDomain.NetworkPublisher, connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector,
	drafts draftDomain.Repository, interval time.Duration, hooks []crosspostDomain.DeliveryFinished) (*DeliveryWorker, error) {
	err := requireDeps("delivery worker", map[string]any{"connections": repos.Connections, "destinations": repos.Destinations, "publications": repos.Publications,
		"deliveries": repos.Deliveries, "audit": repos.Audit, "vault": vault, "publish guard": guard, "publishers": publishers, "connectors": connectors, "drafts": drafts})
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &DeliveryWorker{repos: repos, vault: vault, guard: guard, publishers: publishers, connectors: connectors, drafts: drafts, hooks: hooks, now: time.Now, interval: interval}, nil
}

// RunWorker marks deliveries left in publishing as needs_review, then posts due deliveries every interval.
func (w *DeliveryWorker) RunWorker(ctx context.Context) {
	if err := w.repos.Deliveries.MarkInterrupted(ctx); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_mark_interrupted_failed")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.RunDue(ctx)
	}
}

// RunDue posts every due delivery on unpaused networks. The pause is read before claiming; with every network
// paused nothing is claimed until the next tick (no busy loop).
func (w *DeliveryWorker) RunDue(ctx context.Context) {
	var paused []crosspostDomain.Network
	for _, network := range crosspostDomain.CrosspostNetworks {
		stop, err := w.guard.Paused(ctx, network)
		if err != nil {
			logger.FromContext(ctx).WithError(err).Error("crosspost_pause_read_failed")
			return
		}
		if stop {
			paused = append(paused, network)
		}
	}
	if len(paused) == len(crosspostDomain.CrosspostNetworks) {
		return
	}
	for {
		d, err := w.repos.Deliveries.ClaimDue(ctx, w.now().UTC(), paused)
		if err != nil {
			if !isNotFound(err) {
				logger.FromContext(ctx).WithError(err).Error("crosspost_claim_failed")
			}
			return
		}
		w.deliver(ctx, d)
	}
}

func (w *DeliveryWorker) deliver(ctx context.Context, d crosspostDomain.Delivery) {
	log := logger.FromContext(ctx)
	decision, err := w.guard.Decide(ctx, d)
	if err != nil {
		decision = crosspostDomain.DecisionSimulate
	}
	if decision == crosspostDomain.DecisionSimulate {
		w.finish(ctx, d, crosspostDomain.StatusSimulated, "")
		return
	}
	publisher, ok := w.publishers[d.Network]
	if !ok {
		w.finish(ctx, d, crosspostDomain.StatusFailed, errorCodeNotAvailable)
		return
	}
	request, retryIn, err := w.request(ctx, d)
	if errors.Is(err, crosspostDomain.ErrNeedsReconnect) {
		w.finish(ctx, d, crosspostDomain.StatusNeedsReconnect, errorCodeReconnect)
		return
	}
	if err != nil || retryIn > 0 {
		if err != nil {
			log.WithError(err).Error("crosspost_credentials_failed")
		}
		w.retry(ctx, d, err != nil, retryIn)
		return
	}
	request.SaveProgress = func(ctx context.Context, progress crosspostDomain.Credentials) error {
		return w.saveProgress(ctx, &d, progress)
	}
	result, err := publisher.Publish(ctx, request)
	if result.Progress.AccessToken != "" {
		_ = w.saveProgress(ctx, &d, result.Progress)
	}
	var limited crosspostDomain.RateLimitedError
	switch {
	case err == nil:
		d.ExternalPostID, d.ExternalURL, d.Progress = result.ExternalPostID, result.ExternalURL, crosspostDomain.SealedProgress{}
		status := result.Status
		if status != crosspostDomain.StatusSentToInbox {
			status = crosspostDomain.StatusPublished
		}
		w.finish(ctx, d, status, "")
	case errors.As(err, &limited):
		d.NextAttemptAt = w.now().UTC().Add(limited.RetryAfter)
		w.save(ctx, &d, crosspostDomain.StatusScheduled)
	case errors.Is(err, crosspostDomain.ErrNeedsReconnect):
		w.markConnection(ctx, d, crosspostDomain.HealthNeedsReconnect)
		w.finish(ctx, d, crosspostDomain.StatusNeedsReconnect, crosspostDomain.ErrorCode(err))
	case errors.Is(err, crosspostDomain.ErrContentRejected):
		w.finish(ctx, d, crosspostDomain.StatusFailed, crosspostDomain.ErrorCode(err))
	case errors.Is(err, crosspostDomain.ErrOutcomeUnknown):
		w.finish(ctx, d, crosspostDomain.StatusNeedsReview, crosspostDomain.ErrorCode(err))
	default:
		log.WithError(err).Error("crosspost_publish_failed")
		w.retry(ctx, d, true, 0)
	}
}

// request opens the credentials (the destination's own token where it has one, e.g. a Facebook Page token, else the
// connection's), refreshing an expiring connection token under the connection's lock. retryIn > 0 = another worker
// holds the refresh lock; try again then.
func (w *DeliveryWorker) request(ctx context.Context, d crosspostDomain.Delivery) (crosspostDomain.PublishRequest, time.Duration, error) {
	destination, err := w.repos.Destinations.ByID(ctx, d.MemberID, d.DestinationID)
	if err != nil {
		return crosspostDomain.PublishRequest{}, 0, err
	}
	connection, err := w.repos.Connections.ByID(ctx, d.MemberID, destination.ConnectionID)
	if err != nil {
		return crosspostDomain.PublishRequest{}, 0, err
	}
	if connection.Health == crosspostDomain.HealthNeedsReconnect {
		return crosspostDomain.PublishRequest{}, 0, crosspostDomain.ErrNeedsReconnect
	}
	credentials, err := w.vault.Open(ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: d.MemberID, Provider: connection.Provider}, connection.Sealed)
	if err != nil {
		return crosspostDomain.PublishRequest{}, 0, err
	}
	if !credentials.ExpiresAt.IsZero() && credentials.ExpiresAt.Before(w.now().Add(refreshAhead)) {
		credentials, err = w.refresh(ctx, connection, credentials)
		if err != nil || credentials.AccessToken == "" {
			return crosspostDomain.PublishRequest{}, time.Minute, err
		}
	}
	request := crosspostDomain.PublishRequest{Delivery: d, Destination: destination, Credentials: credentials, CoverKey: d.CoverKey}
	if len(destination.Sealed.Data) > 0 {
		token, err := w.vault.Open(ctx, crosspostDomain.VaultBinding{RecordID: destination.ID, MemberID: d.MemberID, Provider: connection.Provider}, destination.Sealed)
		if err != nil {
			return crosspostDomain.PublishRequest{}, 0, err
		}
		request.Credentials = token
	}
	if len(d.Progress.Data) > 0 {
		progress, err := w.vault.Open(ctx, w.binding(d.ID, d.MemberID, d.Network), crosspostDomain.SealedCredentials(d.Progress))
		if err != nil {
			return crosspostDomain.PublishRequest{}, 0, err
		}
		request.Progress = progress
	}
	return request, 0, nil
}

// refresh refreshes the connection's token once per connection (LockForRefresh); an empty result = locked elsewhere.
func (w *DeliveryWorker) refresh(ctx context.Context, connection crosspostDomain.Connection, credentials crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	connector, ok := w.connectors[connection.Provider]
	if !ok {
		return crosspostDomain.Credentials{}, crosspostDomain.ErrNotOpen
	}
	locked, err := w.repos.Connections.LockForRefresh(ctx, connection.ID, w.now().Add(refreshLockFor))
	if err != nil || !locked {
		return crosspostDomain.Credentials{}, err
	}
	fresh, err := connector.Refresh(ctx, credentials)
	if err != nil {
		if errors.Is(err, crosspostDomain.ErrNeedsReconnect) {
			connection.Health = crosspostDomain.HealthNeedsReconnect
			_ = w.repos.Connections.Save(ctx, connection, connection.Version)
		}
		return crosspostDomain.Credentials{}, err
	}
	sealed, err := w.vault.Seal(ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: connection.MemberID, Provider: connection.Provider}, fresh)
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	connection.Sealed, connection.ExpiresAt = sealed, fresh.ExpiresAt
	if err := w.repos.Connections.Save(ctx, connection, connection.Version); err != nil {
		return crosspostDomain.Credentials{}, err
	}
	return fresh, nil
}

// saveProgress seals a network step (Instagram container, YouTube upload session) and stores it before the next
// request goes out, so a restart resumes instead of posting twice.
func (w *DeliveryWorker) saveProgress(ctx context.Context, d *crosspostDomain.Delivery, progress crosspostDomain.Credentials) error {
	sealed, err := w.vault.Seal(ctx, w.binding(d.ID, d.MemberID, d.Network), progress)
	if err != nil {
		return err
	}
	d.Progress = crosspostDomain.SealedProgress(sealed)
	if err := w.repos.Deliveries.Save(ctx, *d); err != nil {
		return err
	}
	d.Version++
	return nil
}

func (w *DeliveryWorker) binding(recordID, memberID string, network crosspostDomain.Network) crosspostDomain.VaultBinding {
	return crosspostDomain.VaultBinding{RecordID: recordID, MemberID: memberID, Provider: crosspostDomain.ProviderOf(network)}
}

// retry: an error before anything was sent tries again after 1 min, 5 min, 30 min, 2 h, then fails. A refresh lock
// held elsewhere waits without counting as a try.
func (w *DeliveryWorker) retry(ctx context.Context, d crosspostDomain.Delivery, counts bool, wait time.Duration) {
	if counts {
		d.Attempts++
		if d.Attempts > len(retryAfter) {
			w.finish(ctx, d, crosspostDomain.StatusFailed, errorCodeSendFailed)
			return
		}
		wait = retryAfter[d.Attempts-1]
	}
	d.NextAttemptAt = w.now().UTC().Add(wait)
	w.save(ctx, &d, crosspostDomain.StatusScheduled)
}

func (w *DeliveryWorker) save(ctx context.Context, d *crosspostDomain.Delivery, status crosspostDomain.DeliveryStatus) bool {
	if err := d.MoveTo(status); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_delivery_transition_failed")
		return false
	}
	if err := w.repos.Deliveries.Save(ctx, *d); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_delivery_save_failed")
		return false
	}
	return true
}

// finish saves an outcome, writes its audit row, runs the hooks (not for simulated) and marks the draft published on
// a publication without LinkedIn (LinkedIn's worker does that itself).
func (w *DeliveryWorker) finish(ctx context.Context, d crosspostDomain.Delivery, status crosspostDomain.DeliveryStatus, errorCode string) {
	d.LastErrorCode = errorCode
	if !w.save(ctx, &d, status) {
		return
	}
	d.Version++
	switch status {
	case crosspostDomain.StatusSimulated:
		return
	case crosspostDomain.StatusPublished, crosspostDomain.StatusSentToInbox:
		appendAudit(ctx, w.repos.Audit, deliveryAudit(crosspostDomain.AuditPublish, d, 0))
		w.markDraftPublished(ctx, d)
	default:
		appendAudit(ctx, w.repos.Audit, deliveryAudit(crosspostDomain.AuditFail, d, 0))
	}
	for _, hook := range w.hooks {
		hook.DeliveryFinished(ctx, d)
	}
}

func (w *DeliveryWorker) markDraftPublished(ctx context.Context, d crosspostDomain.Delivery) {
	if d.Status != crosspostDomain.StatusPublished {
		return
	}
	publication, err := w.repos.Publications.ByID(ctx, d.MemberID, d.PublicationID)
	if err != nil || publication.LinkedInScheduleID != "" {
		return
	}
	if err := w.drafts.MarkPublished(ctx, d.MemberID, publication.DraftID); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_mark_draft_published_failed")
	}
}

func (w *DeliveryWorker) markConnection(ctx context.Context, d crosspostDomain.Delivery, health crosspostDomain.ConnectionHealth) {
	destination, err := w.repos.Destinations.ByID(ctx, d.MemberID, d.DestinationID)
	if err != nil {
		return
	}
	connection, err := w.repos.Connections.ByID(ctx, d.MemberID, destination.ConnectionID)
	if err != nil || connection.Health == health {
		return
	}
	connection.Health = health
	if err := w.repos.Connections.Save(ctx, connection, connection.Version); err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_connection_health_save_failed")
	}
}
