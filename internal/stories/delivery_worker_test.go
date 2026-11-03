package stories

import (
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

// dueDelivery stores an approved delivery due now in its own publication without LinkedIn.
func (f *crosspostFixture) dueDelivery(t *testing.T, id string, destination crosspostDomain.Destination) {
	t.Helper()
	d := crosspostDomain.Delivery{ID: id, PublicationID: "pub-" + id, MemberID: xMember, DestinationID: destination.ID, Network: destination.Network,
		Text: "Hello", PublishAt: time.Now().Add(-time.Minute), Status: crosspostDomain.StatusScheduled}
	require.NoError(t, f.publications.Create(f.ctx, crosspostDomain.Publication{ID: "pub-" + id, MemberID: xMember, DraftID: "draft-" + id}, []crosspostDomain.Delivery{d}))
}

func (f *crosspostFixture) worker(t *testing.T, mode crosspostDomain.PublishMode, hooks []crosspostDomain.DeliveryFinished, publishers ...crosspostDomain.NetworkPublisher) *DeliveryWorker {
	t.Helper()
	guard, err := NewPublishGuard(f.pause, PublishGuardConfig{Mode: mode})
	require.NoError(t, err)
	byNetwork := map[crosspostDomain.Network]crosspostDomain.NetworkPublisher{}
	for _, p := range publishers {
		byNetwork[p.Network()] = p
	}
	worker, err := NewDeliveryWorker(f.repos, f.vault, guard, byNetwork, f.connectors, f.drafts, time.Minute, hooks)
	require.NoError(t, err)
	return worker
}

func (f *crosspostFixture) status(t *testing.T, id string) crosspostDomain.Delivery {
	t.Helper()
	return f.deliveries(t, "pub-"+id)[f.mustNetwork(t, id)]
}

func (f *crosspostFixture) mustNetwork(t *testing.T, id string) crosspostDomain.Network {
	t.Helper()
	var network crosspostDomain.Network
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT network FROM bloomary_deliveries WHERE id=?`, id).Scan(&network))
	return network
}

func TestDeliveryWorkerPublishesRetriesRateLimitsSendsToInboxAndMarksTheDraft(t *testing.T) {
	// Verifies: BE-054
	f := newCrosspostFixture(t)
	x, tiktok := f.destination(t, crosspostDomain.X), f.destination(t, crosspostDomain.TikTok)
	f.draft(t, "draft-x-1", "Hello")
	f.dueDelivery(t, "x-1", x)
	f.dueDelivery(t, "tt-1", tiktok)
	xPublisher := &fakePublisher{network: crosspostDomain.X, outcomes: []publishOutcome{
		{err: crosspostDomain.RateLimitedError{RetryAfter: 10 * time.Minute}},
		{result: crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished, ExternalPostID: "x-post-1", ExternalURL: "https://x.test/p/1"}},
	}}
	tiktokPublisher := &fakePublisher{network: crosspostDomain.TikTok, outcomes: []publishOutcome{{result: crosspostDomain.PublishResult{Status: crosspostDomain.StatusSentToInbox}}}}
	hook := &finishedHook{}
	worker := f.worker(t, crosspostDomain.ModeLive, []crosspostDomain.DeliveryFinished{hook}, xPublisher, tiktokPublisher)

	worker.RunDue(f.ctx)
	limited := f.status(t, "x-1")
	require.Equal(t, crosspostDomain.StatusScheduled, limited.Status, "rate-limited: back to scheduled")
	require.Equal(t, crosspostDomain.StatusSentToInbox, f.status(t, "tt-1").Status)
	require.Equal(t, "synthetic-token", tiktokPublisher.requests[0].Credentials.AccessToken)

	worker.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	worker.RunDue(f.ctx)
	published := f.status(t, "x-1")
	require.Equal(t, crosspostDomain.StatusPublished, published.Status)
	require.Equal(t, "https://x.test/p/1", published.ExternalURL)
	require.Equal(t, "X ✓ https://x.test/p/1", CrosspostStatusLine(published))
	require.ElementsMatch(t, []crosspostDomain.DeliveryStatus{crosspostDomain.StatusSentToInbox, crosspostDomain.StatusPublished}, hook.statuses)
	draft, err := f.drafts.ByID(f.ctx, xMember, "draft-x-1")
	require.NoError(t, err)
	require.Equal(t, "published", draft.Status, "no LinkedIn part: the worker marks the draft published")
	publication, err := f.publications.ByID(f.ctx, xMember, "pub-x-1")
	require.NoError(t, err)
	require.False(t, publication.ClosedAt.IsZero())
}

func TestDeliveryWorkerInterruptedNeedsReviewThenRetriedAndPublished(t *testing.T) {
	// Verifies: BE-055
	f := newCrosspostFixture(t)
	x := f.destination(t, crosspostDomain.X)
	f.dueDelivery(t, "x-1", x)
	_, err := f.db.ExecContext(f.ctx, `UPDATE bloomary_deliveries SET status='publishing' WHERE id='x-1'`)
	require.NoError(t, err)
	require.NoError(t, f.repos.Deliveries.MarkInterrupted(f.ctx))
	require.Equal(t, crosspostDomain.StatusNeedsReview, f.status(t, "x-1").Status)
	require.Equal(t, "X · may already be posted (Bloomary stopped mid-post)", CrosspostStatusLine(f.status(t, "x-1")))

	retry, err := NewRetryDeliveryStory(f.repos)
	require.NoError(t, err)
	_, err = retry.RetryDelivery(f.ctx, crosspostDomain.RetryDeliveryInput{MemberID: xMember, PublicationID: "pub-x-1", DeliveryID: "x-1", TelegramUserID: 42})
	require.NoError(t, err)
	publisher := &fakePublisher{network: crosspostDomain.X, outcomes: []publishOutcome{{result: crosspostDomain.PublishResult{Status: crosspostDomain.StatusPublished, ExternalPostID: "p"}}}}
	f.worker(t, crosspostDomain.ModeLive, nil, publisher).RunDue(f.ctx)
	require.Equal(t, crosspostDomain.StatusPublished, f.status(t, "x-1").Status)
}

func TestDeliveryWorkerSimulatesInDryRunAndSkipsPausedNetworks(t *testing.T) {
	// Verifies: BE-056
	f := newCrosspostFixture(t)
	x, facebook := f.destination(t, crosspostDomain.X), f.destination(t, crosspostDomain.FacebookPage)
	f.dueDelivery(t, "x-1", x)
	f.dueDelivery(t, "fb-1", facebook)
	require.NoError(t, f.pause.SetPaused(f.ctx, crosspostDomain.FacebookPage, true, "ops", time.Now()))
	hook := &finishedHook{}
	publisher := &fakePublisher{network: crosspostDomain.X}

	f.worker(t, crosspostDomain.ModeDryRun, []crosspostDomain.DeliveryFinished{hook}, publisher).RunDue(f.ctx)

	require.Equal(t, crosspostDomain.StatusSimulated, f.status(t, "x-1").Status)
	require.Equal(t, "X · [stage · not posted]", CrosspostStatusLine(f.status(t, "x-1")))
	require.Empty(t, publisher.requests, "dry_run never calls the publisher")
	require.Empty(t, hook.statuses, "no hooks for a simulated delivery")
	require.Equal(t, crosspostDomain.StatusScheduled, f.status(t, "fb-1").Status, "a paused network is not claimed")
}
