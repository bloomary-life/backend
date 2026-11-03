package migrations

import (
	"context"
	"database/sql"
	"testing"

	draftRepo "bloomary-api/internal/gates/repositories/draft"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestUpCreatesTablesAfterInlineBaselineAndIsRepeatable(t *testing.T) {
	// Verifies: BE-036
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()
	drafts, err := draftRepo.NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, drafts.Init(ctx))

	require.NoError(t, Up(ctx, db))
	require.NoError(t, Up(ctx, db))

	for _, table := range []string{
		"bloomary_connections", "bloomary_destinations", "bloomary_publications", "bloomary_deliveries",
		"bloomary_crosspost_audit", "bloomary_publish_pause", "bloomary_video_requests", "bloomary_producer_jobs",
		"bloomary_producer_seen", "bloomary_draft_videos", TableName,
	} {
		var name string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name), table)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO synced_drafts(member_id,draft_id,body,title,ai_voice,ai_realistic_shots,ai_music) VALUES('m','d','body','Title',1,0,1)`)
	require.NoError(t, err)
}
