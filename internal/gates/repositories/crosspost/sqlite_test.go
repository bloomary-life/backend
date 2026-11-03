package crosspost

import (
	"context"
	"database/sql"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftRepo "bloomary-api/internal/gates/repositories/draft"
	"bloomary-api/internal/gates/repositories/migrations"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type repos struct {
	db           *sql.DB
	connections  *ConnectionRepository
	destinations *DestinationRepository
	publications *PublicationRepository
	deliveries   *DeliveryRepository
	pause        *PauseRepository
	audit        *AuditRepository
}

func newRepos(t *testing.T) repos {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	drafts, err := draftRepo.NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, drafts.Init(ctx))
	require.NoError(t, migrations.Up(ctx, db))
	r := repos{db: db}
	r.connections, err = NewConnectionRepository(db)
	require.NoError(t, err)
	r.destinations, err = NewDestinationRepository(db)
	require.NoError(t, err)
	r.publications, err = NewPublicationRepository(db)
	require.NoError(t, err)
	r.deliveries, err = NewDeliveryRepository(db)
	require.NoError(t, err)
	r.pause, err = NewPauseRepository(db)
	require.NoError(t, err)
	r.audit, err = NewAuditRepository(db)
	require.NoError(t, err)
	return r
}

func (r repos) seedDestinations(t *testing.T, memberID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.connections.Save(ctx, crosspostDomain.Connection{
		ID: "conn-" + memberID, MemberID: memberID, Provider: crosspostDomain.ProviderMeta, AppOwner: crosspostDomain.AppOwnerBloomary,
		AccountID: "acct-1", Health: crosspostDomain.HealthConnected, Sealed: crosspostDomain.SealedCredentials{KeyID: "k1", SchemaVersion: 1, Data: []byte{1}},
	}, 0))
	for _, network := range []crosspostDomain.Network{crosspostDomain.X, crosspostDomain.FacebookPage} {
		require.NoError(t, r.destinations.Save(ctx, crosspostDomain.Destination{
			ID: "dest-" + string(network) + "-" + memberID, MemberID: memberID, ConnectionID: "conn-" + memberID, Network: network,
			AccountID: "acct-" + string(network), AccountName: "Synthetic", AudienceZone: "Europe/Nicosia", Enabled: true,
		}))
	}
}

func proposed(memberID, publicationID string, network crosspostDomain.Network, at time.Time) crosspostDomain.Delivery {
	return crosspostDomain.Delivery{
		ID: "del-" + string(network) + "-" + publicationID, PublicationID: publicationID, MemberID: memberID,
		DestinationID: "dest-" + string(network) + "-" + memberID, Network: network, Text: "Synthetic post", Hashtags: []string{"one"},
		PublishAt: at, Status: crosspostDomain.StatusProposed,
	}
}

func TestPublicationLifecycle(t *testing.T) {
	// Verifies: BE-037
	r := newRepos(t)
	ctx := context.Background()
	r.seedDestinations(t, "member-1")
	due := time.Now().Add(-time.Minute)
	pub := crosspostDomain.Publication{ID: "pub-1", MemberID: "member-1", DraftID: "draft-1"}
	require.NoError(t, r.publications.Create(ctx, pub, []crosspostDomain.Delivery{
		proposed("member-1", "pub-1", crosspostDomain.X, due),
		proposed("member-1", "pub-1", crosspostDomain.FacebookPage, due.Add(time.Second)),
	}))

	deliveries, err := r.deliveries.ByPublication(ctx, "member-1", "pub-1")
	require.NoError(t, err)
	require.Len(t, deliveries, 2)
	require.Equal(t, 1, deliveries[0].Version)
	require.Equal(t, []string{"one"}, deliveries[0].Hashtags)
	kids := false
	for i := range deliveries {
		require.NoError(t, deliveries[i].MoveTo(crosspostDomain.StatusScheduled))
		deliveries[i].YouTube = crosspostDomain.YouTubeChoices{Privacy: "public", MadeForKids: &kids}
	}
	pub.LinkedInScheduleID = "schedule-1"
	require.NoError(t, r.publications.ApproveAll(ctx, pub, deliveries))
	stored, err := r.publications.ByID(ctx, "member-1", "pub-1")
	require.NoError(t, err)
	require.False(t, stored.ApprovedAt.IsZero())
	require.Equal(t, "schedule-1", stored.LinkedInScheduleID)

	// X is paused: only the Facebook delivery is claimed.
	claimed, err := r.deliveries.ClaimDue(ctx, time.Now(), []crosspostDomain.Network{crosspostDomain.X})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.FacebookPage, claimed.Network)
	require.Equal(t, crosspostDomain.StatusPublishing, claimed.Status)
	require.Equal(t, 3, claimed.Version)
	require.Equal(t, "public", claimed.YouTube.Privacy)
	require.False(t, *claimed.YouTube.MadeForKids)

	require.NoError(t, claimed.MoveTo(crosspostDomain.StatusPublished))
	claimed.ExternalPostID, claimed.ExternalURL = "post-1", "https://example.com/post-1"
	require.NoError(t, r.deliveries.Save(ctx, claimed))

	xDelivery, err := r.deliveries.ClaimDue(ctx, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.X, xDelivery.Network)
	require.NoError(t, xDelivery.MoveTo(crosspostDomain.StatusSimulated))
	require.NoError(t, r.deliveries.Save(ctx, xDelivery))

	// Every delivery is final: the publication is closed and the draft can get a new one.
	stored, err = r.publications.ByID(ctx, "member-1", "pub-1")
	require.NoError(t, err)
	require.False(t, stored.ClosedAt.IsZero())
	require.NoError(t, r.publications.Create(ctx, crosspostDomain.Publication{ID: "pub-2", MemberID: "member-1", DraftID: "draft-1"}, nil))

	taken, err := r.deliveries.TakenTimes(ctx, "member-1", crosspostDomain.X, due.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, taken, 1)
	require.WithinDuration(t, due, taken[0], time.Millisecond)
}

func TestMarkInterruptedMovesPublishingToNeedsReview(t *testing.T) {
	// Verifies: BE-037
	r := newRepos(t)
	ctx := context.Background()
	r.seedDestinations(t, "member-1")
	d := proposed("member-1", "pub-1", crosspostDomain.X, time.Now().Add(-time.Minute))
	d.Status = crosspostDomain.StatusScheduled
	require.NoError(t, r.publications.Create(ctx, crosspostDomain.Publication{ID: "pub-1", MemberID: "member-1", DraftID: "draft-1"}, []crosspostDomain.Delivery{d}))
	_, err := r.deliveries.ClaimDue(ctx, time.Now(), nil)
	require.NoError(t, err)

	require.NoError(t, r.deliveries.MarkInterrupted(ctx))

	deliveries, err := r.deliveries.ByPublication(ctx, "member-1", "pub-1")
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusNeedsReview, deliveries[0].Status)
	require.Equal(t, "interrupted", deliveries[0].LastErrorCode)
}

func TestConnectionsAndDestinations(t *testing.T) {
	// Verifies: BE-037
	r := newRepos(t)
	ctx := context.Background()
	r.seedDestinations(t, "member-1")

	connection, err := r.connections.ByID(ctx, "member-1", "conn-member-1")
	require.NoError(t, err)
	require.Equal(t, 1, connection.Version)
	connection.Health = crosspostDomain.HealthNeedsReconnect
	require.NoError(t, r.connections.Save(ctx, connection, connection.Version))
	connections, err := r.connections.ListByMember(ctx, "member-1")
	require.NoError(t, err)
	require.Len(t, connections, 1)
	require.Equal(t, 2, connections[0].Version)
	require.Equal(t, crosspostDomain.HealthNeedsReconnect, connections[0].Health)
	require.Equal(t, []byte{1}, connections[0].Sealed.Data)

	locked, err := r.connections.LockForRefresh(ctx, "conn-member-1", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, locked)

	destination, err := r.destinations.ByID(ctx, "member-1", "dest-x-member-1")
	require.NoError(t, err)
	destination.AudienceZone, destination.Enabled = "America/New_York", false
	require.NoError(t, r.destinations.Save(ctx, destination))
	destinations, err := r.destinations.ListByMember(ctx, "member-1")
	require.NoError(t, err)
	require.Len(t, destinations, 2)
	require.Equal(t, "America/New_York", destinations[0].AudienceZone)
	require.False(t, destinations[0].Enabled)
}

func TestDeleteByMemberIDRemovesCrosspostRows(t *testing.T) {
	// Verifies: BE-037
	r := newRepos(t)
	ctx := context.Background()
	r.seedDestinations(t, "member-1")
	r.seedDestinations(t, "member-2")
	for _, member := range []string{"member-1", "member-2"} {
		require.NoError(t, r.publications.Create(ctx, crosspostDomain.Publication{ID: "pub-" + member, MemberID: member, DraftID: "draft-1"},
			[]crosspostDomain.Delivery{proposed(member, "pub-"+member, crosspostDomain.X, time.Now())}))
	}

	for _, deleter := range []interface {
		DeleteByMemberID(context.Context, string) error
	}{r.deliveries, r.publications, r.destinations, r.connections} {
		require.NoError(t, deleter.DeleteByMemberID(ctx, "member-1"))
	}

	for _, table := range []string{"bloomary_connections", "bloomary_destinations", "bloomary_publications", "bloomary_deliveries"} {
		var left, other int
		require.NoError(t, r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE member_id='member-1'`).Scan(&left))
		require.NoError(t, r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE member_id='member-2'`).Scan(&other))
		require.Zero(t, left, table)
		require.NotZero(t, other, table)
	}
}

func TestAuditStripAndYearlyPurge(t *testing.T) {
	// Verifies: BE-038
	r := newRepos(t)
	ctx := context.Background()
	require.NoError(t, r.audit.Append(ctx, crosspostDomain.AuditRecord{
		MemberID: "member-1", Action: crosspostDomain.AuditPublish, RecordIDs: []string{"pub-1", "del-1"},
		MediaSHA256: "media-hash", TextSHA256: "text-hash", NetworkPostID: "post-1", TelegramUserID: 42,
	}))
	require.NoError(t, r.audit.Append(ctx, crosspostDomain.AuditRecord{
		MemberID: "member-2", Action: crosspostDomain.AuditApprove, At: time.Now().AddDate(-1, 0, -2),
	}))

	require.NoError(t, r.audit.StripMember(ctx, "member-1", "hmac-of-member-1"))
	var member, records, media, text, post string
	var telegram int64
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT member_id,record_ids,media_sha256,text_sha256,network_post_id,telegram_user_id FROM bloomary_crosspost_audit WHERE action='publish'`).
		Scan(&member, &records, &media, &text, &post, &telegram))
	require.Equal(t, []any{"hmac-of-member-1", "pub-1,del-1", "media-hash", "", "", int64(0)}, []any{member, records, media, text, post, telegram})

	require.NoError(t, r.audit.PurgeBefore(ctx, time.Now().AddDate(-1, 0, -1)))
	var count int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bloomary_crosspost_audit`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPauseSwitchPerNetworkAndAll(t *testing.T) {
	// Verifies: BE-040
	r := newRepos(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, r.pause.SetPaused(ctx, crosspostDomain.X, true, "ops-bot", now))
	paused, err := r.pause.Paused(ctx, crosspostDomain.X)
	require.NoError(t, err)
	require.True(t, paused)
	paused, err = r.pause.Paused(ctx, crosspostDomain.LinkedIn)
	require.NoError(t, err)
	require.False(t, paused)

	require.NoError(t, r.pause.SetPaused(ctx, crosspostDomain.AllNetworks, true, "ops-bot", now))
	paused, err = r.pause.Paused(ctx, crosspostDomain.LinkedIn)
	require.NoError(t, err)
	require.True(t, paused)

	require.NoError(t, r.pause.SetPaused(ctx, crosspostDomain.AllNetworks, false, "ops-bot", now))
	require.NoError(t, r.pause.SetPaused(ctx, crosspostDomain.X, false, "ops-bot", now))
	paused, err = r.pause.Paused(ctx, crosspostDomain.X)
	require.NoError(t, err)
	require.False(t, paused)
}
