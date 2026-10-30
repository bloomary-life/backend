package metrics

import (
	"context"
	"database/sql"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var _ accountDomain.MemberDataDeleter = (*PostPublishRepository)(nil)

func TestPostPublishEventsAreDeletedWithTheAccount(t *testing.T) {
	// Verifies: BE-015
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	repo, err := NewPostPublishRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, repo.InitTable(ctx))
	for _, member := range []string{"member-1", "member-1", "member-2"} {
		require.NoError(t, repo.RecordPostPublished(ctx, member, time.Now()))
	}

	require.NoError(t, repo.DeleteByMemberID(ctx, "member-1"))

	var mine, others int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM metric_post_publishes WHERE member_id='member-1'`).Scan(&mine))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM metric_post_publishes WHERE member_id='member-2'`).Scan(&others))
	require.Zero(t, mine)
	require.Equal(t, 1, others)
}
