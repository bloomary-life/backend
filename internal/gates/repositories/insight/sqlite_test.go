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

func TestReserveVoiceCapsAttemptsAndSecondsAtomically(t *testing.T) {
	// Verifies: BE-021
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)

	ok, err := repo.ReserveVoice(ctx, "member-1", insightDomain.DailyVoiceSeconds-10, since, now)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ReserveVoice(ctx, "member-1", 11, since, now)
	require.NoError(t, err)
	require.False(t, ok, "the minutes cap counts audio already sent")
	ok, err = repo.ReserveVoice(ctx, "member-2", 11, since, now)
	require.NoError(t, err)
	require.True(t, ok, "caps are per member")

	for i := 1; i < insightDomain.DailyVoiceLimit; i++ {
		ok, err = repo.ReserveVoice(ctx, "member-3", 1, since, now)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err = repo.ReserveVoice(ctx, "member-3", 1, since, now)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ReserveVoice(ctx, "member-3", 1, since, now)
	require.NoError(t, err)
	require.False(t, ok, "attempt count is capped")

	require.NoError(t, repo.DeleteByMemberID(ctx, "member-3"))
	ok, err = repo.ReserveVoice(ctx, "member-3", 1, since, now)
	require.NoError(t, err)
	require.True(t, ok, "account deletion removes voice attempts")
}

func TestInsightKeepsTrendContext(t *testing.T) {
	// Verifies: BE-023
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	trend := &insightDomain.Trend{SourceURL: "https://www.anthropic.com/news/x", Summary: "Summary"}
	require.NoError(t, repo.Create(ctx, insightDomain.Insight{ID: "i-1", MemberID: "member-1", Text: "My take", Source: insightDomain.SourceText, CreatedAt: now, Trend: trend}))
	require.NoError(t, repo.Create(ctx, insightDomain.Insight{ID: "i-2", MemberID: "member-1", Text: "Plain idea", Source: insightDomain.SourceText, CreatedAt: now.Add(time.Second)}))

	list, err := repo.List(ctx, insightDomain.ListFilter{MemberID: "member-1", Since: now.Add(-time.Hour)})

	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Nil(t, list[0].Trend)
	require.Equal(t, trend, list[1].Trend)
}

func TestReserveVoiceKeepsAttemptsFor24HoursOnly(t *testing.T) {
	// Verifies: BE-021
	repo := newRepo(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-48 * time.Hour)
	ok, err := repo.ReserveVoice(ctx, "member-1", 30, start.Add(-24*time.Hour), start)
	require.NoError(t, err)
	require.True(t, ok)

	now := time.Now().UTC()
	ok, err = repo.ReserveVoice(ctx, "member-2", 5, now.Add(-24*time.Hour), now)
	require.NoError(t, err)
	require.True(t, ok)

	var rows int
	require.NoError(t, repo.db.QueryRow(`SELECT COUNT(*) FROM member_voice_attempts`).Scan(&rows))
	require.Equal(t, 1, rows, "attempts older than 24 hours are deleted")
}

func TestPruneVoiceAttemptsRemovesOldRowsWithoutNewVoiceNotes(t *testing.T) {
	// Verifies: BE-021
	repo := newRepo(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-30 * time.Hour)
	ok, err := repo.ReserveVoice(ctx, "member-1", 5, old.Add(-24*time.Hour), old)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, repo.PruneVoiceAttempts(ctx, time.Now().UTC().Add(-24*time.Hour)))

	var rows int
	require.NoError(t, repo.db.QueryRow(`SELECT COUNT(*) FROM member_voice_attempts`).Scan(&rows))
	require.Zero(t, rows)
}
