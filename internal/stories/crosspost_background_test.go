package stories

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	crosspostDomain "bloomary-api/internal/domain/crosspost"
	deletionlogRepo "bloomary-api/internal/gates/repositories/deletionlog"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	dailyMocks "bloomary-api/internal/mocks/domain/daily"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestConnectionCheckerRefreshesExpiringTokensAndRetentionPurgesOldRecords(t *testing.T) {
	// Verifies: BE-057
	f := newCrosspostFixture(t)
	x := f.destination(t, crosspostDomain.X)
	connection, err := f.connections.ByID(f.ctx, xMember, x.ConnectionID)
	require.NoError(t, err)
	binding := crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: xMember, Provider: crosspostDomain.ProviderX}
	connection.Sealed, err = f.vault.Seal(f.ctx, binding, crosspostDomain.Credentials{AccessToken: "old", ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	require.NoError(t, f.connections.Save(f.ctx, connection, connection.Version))

	checker, err := NewConnectionChecker(f.connections, f.connections, f.vault, f.connectors)
	require.NoError(t, err)
	checker.CheckAll(f.ctx)
	require.Equal(t, 1, f.connector.refreshed)
	refreshed, err := f.connections.ByID(f.ctx, xMember, x.ConnectionID)
	require.NoError(t, err)
	opened, err := f.vault.Open(f.ctx, binding, refreshed.Sealed)
	require.NoError(t, err)
	require.Equal(t, "old-refreshed", opened.AccessToken)

	// A publication closed 400 days ago: media gone (after 90 days), then rows (after a year); an audit row too.
	f.objects.objects["frozen-old"] = []byte("x")
	old := crosspostDomain.Delivery{ID: "old-1", PublicationID: "pub-old", MemberID: xMember, DestinationID: x.ID, Network: crosspostDomain.X,
		MediaKey: "frozen-old", PublishAt: time.Now(), Status: crosspostDomain.StatusPublished}
	require.NoError(t, f.publications.Create(f.ctx, crosspostDomain.Publication{ID: "pub-old", MemberID: xMember, DraftID: "d"}, []crosspostDomain.Delivery{old}))
	long := time.Now().Add(-400 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	_, err = f.db.ExecContext(f.ctx, `UPDATE bloomary_publications SET closed_at=? WHERE id='pub-old'`, long)
	require.NoError(t, err)
	require.NoError(t, f.repos.Audit.Append(f.ctx, crosspostDomain.AuditRecord{At: time.Now().Add(-400 * 24 * time.Hour), MemberID: xMember, Action: crosspostDomain.AuditPublish}))

	job, err := NewRetentionJob(f.publications, f.repos.Audit, f.objects)
	require.NoError(t, err)
	job.PurgeOnce(f.ctx)
	require.NotContains(t, f.objects.objects, "frozen-old")
	_, err = f.publications.ByID(f.ctx, xMember, "pub-old")
	require.ErrorIs(t, err, crosspostDomain.ErrNotFound)
	var audits int
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM bloomary_crosspost_audit`).Scan(&audits))
	require.Zero(t, audits)
}

func TestDeleteCrosspostDataRunsBeforeDraftsRevokesAndDeletesMediaRowsAndStripsAudit(t *testing.T) {
	// Verifies: BE-058
	f := newCrosspostFixture(t)
	x := f.destination(t, crosspostDomain.X)
	f.draft(t, "draft-1", "Hello")
	f.objects.objects["frozen-1"] = []byte("x")
	f.objects.objects["video-master"] = []byte("v")
	_, err := f.db.ExecContext(f.ctx, `INSERT INTO bloomary_draft_videos(member_id,draft_id,master_key,sha256,bytes,seconds,width,height,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		xMember, "draft-1", "video-master", "sha", 10, 12.5, 1080, 1920, "2026-11-03T00:00:00.000Z")
	require.NoError(t, err)
	d := crosspostDomain.Delivery{ID: "d-1", PublicationID: "pub-1", MemberID: xMember, DestinationID: x.ID, Network: crosspostDomain.X, MediaKey: "frozen-1",
		PublishAt: time.Now(), Status: crosspostDomain.StatusScheduled}
	require.NoError(t, f.publications.Create(f.ctx, crosspostDomain.Publication{ID: "pub-1", MemberID: xMember, DraftID: "draft-1"}, []crosspostDomain.Delivery{d}))
	require.NoError(t, f.repos.Audit.Append(f.ctx, crosspostDomain.AuditRecord{MemberID: xMember, Action: crosspostDomain.AuditPublish, NetworkPostID: "post-1", TextSHA256: "t"}))
	deletion, err := NewDeleteCrosspostDataStory(f.repos, f.vault, f.connectors, f.drafts, f.objects, []byte("synthetic-audit-key"))
	require.NoError(t, err)

	require.NoError(t, deletion.DeleteCrosspostData(f.ctx, xMember))

	require.Equal(t, 1, f.connector.revoked)
	require.NotContains(t, f.objects.objects, "frozen-1")
	require.NotContains(t, f.objects.objects, "video-master")
	for _, table := range []string{"bloomary_deliveries", "bloomary_publications", "bloomary_destinations", "bloomary_connections", "bloomary_draft_videos"} {
		var n int
		require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM `+table+` WHERE member_id=?`, xMember).Scan(&n))
		require.Zero(t, n, table)
	}
	var member, postID string
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT member_id,network_post_id FROM bloomary_crosspost_audit`).Scan(&member, &postID))
	require.NotEqual(t, xMember, member)
	require.Empty(t, postID)
}

// memberStep is an account-deletion step that records when it ran.
type memberStep func()

func (s memberStep) DeleteByMemberID(context.Context, string) error {
	s()
	return nil
}

func TestAccountDeletionRunsCrosspostStepBeforeDraftsAndWritesTheDeletionLogForReplay(t *testing.T) {
	// Verifies: BE-059
	f := newCrosspostFixture(t)
	f.draft(t, "draft-1", "Hello")
	var draftsLeftWhenCrosspostRan int
	before := memberStep(func() {
		require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM synced_drafts WHERE member_id=?`, xMember).Scan(&draftsLeftWhenCrosspostRan))
	})
	daily := dailyMocks.NewMockRepository(t)
	daily.EXPECT().DeleteByMemberID(mock.Anything, xMember).Return(nil).Twice()
	accounts := accountMocks.NewMockRepository(t)
	accounts.EXPECT().Delete(mock.Anything, xMember).Return(nil).Twice()
	story, err := NewAccountDeletionStoryWithSteps(daily, f.drafts, accounts, []accountDomain.MemberDataDeleter{before}, nil)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "bloomary.db.deletion-log")
	log, err := deletionlogRepo.NewFile(path)
	require.NoError(t, err)
	logged, err := NewLoggedAccountDeletion(story, log)
	require.NoError(t, err)

	backupTime := time.Now().Add(-time.Second)
	require.NoError(t, logged.DeleteAccount(f.ctx, accountDomain.DeleteAccountInput{MemberID: xMember}))
	require.Equal(t, 1, draftsLeftWhenCrosspostRan, "the crosspost step runs before the draft step")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z member-x\n$`, string(raw))

	replayed, err := ReplayDeletions(f.ctx, log, story, backupTime)
	require.NoError(t, err)
	require.Equal(t, []string{xMember}, replayed)
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, again, "a replay does not append to the log")
}
