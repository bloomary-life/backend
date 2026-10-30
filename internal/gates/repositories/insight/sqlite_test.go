package insight

import (
	"context"
	"database/sql"
	"testing"
	"time"

	insightDomain "bloomary-api/internal/domain/insight"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	return repo
}

func TestInsightListsUnusedAndMarksUsed(t *testing.T) {
	// Verifies: BE-008
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.Create(ctx, insightDomain.Insight{ID: "i-1", MemberID: "member-1", Text: "Hiring needs proof", Source: insightDomain.SourceText, CreatedAt: now.Add(-time.Hour)}))
	require.NoError(t, repo.Create(ctx, insightDomain.Insight{ID: "i-2", MemberID: "member-1", Text: "Voice idea", Source: insightDomain.SourceVoice, CreatedAt: now}))
	require.NoError(t, repo.Create(ctx, insightDomain.Insight{ID: "i-3", MemberID: "member-2", Text: "Other member", Source: insightDomain.SourceText, CreatedAt: now}))

	used, err := repo.MarkUsed(ctx, "member-1", "i-1", "draft-1", now)
	require.NoError(t, err)
	require.Equal(t, "draft-1", used.DraftID)
	require.NotNil(t, used.UsedAt)

	unused, err := repo.List(ctx, insightDomain.ListFilter{MemberID: "member-1", UnusedOnly: true})
	require.NoError(t, err)
	require.Len(t, unused, 1)
	require.Equal(t, "i-2", unused[0].ID)
	require.Equal(t, insightDomain.SourceVoice, unused[0].Source)

	all, err := repo.List(ctx, insightDomain.ListFilter{MemberID: "member-1", Since: now.Add(-2 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, all, 2)

	voice, err := repo.CountSince(ctx, "member-1", insightDomain.SourceVoice, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, voice)
	total, err := repo.CountSince(ctx, "member-1", "", now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, total)
}
