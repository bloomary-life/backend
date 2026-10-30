package review

import (
	"context"
	"database/sql"
	"testing"
	"time"

	reviewDomain "bloomary-api/internal/domain/review"

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

func TestNoticeIsSkippedOnce(t *testing.T) {
	// Verifies: BE-013
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	notice := reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ProposedText: "Post", Status: reviewDomain.NoticeSent, ScheduledAt: now.Add(time.Hour), CreatedAt: now}
	require.NoError(t, repo.CreateNotice(ctx, notice))

	skipped, err := repo.SkipNotice(ctx, "member-1", "n-1", now)
	require.NoError(t, err)
	require.Equal(t, reviewDomain.NoticeSkipped, skipped.Status)
	require.NotNil(t, skipped.SkippedAt)
	_, err = repo.SkipNotice(ctx, "member-1", "n-1", now)
	require.ErrorIs(t, err, reviewDomain.ErrNotFound)

	list, err := repo.ListNotices(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, list, 1)
	count, err := repo.CountNoticesSince(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestSuggestionStoresRatingAndPostedFlag(t *testing.T) {
	// Verifies: BE-012
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateSuggestion(ctx, reviewDomain.Suggestion{ID: "s-1", MemberID: "member-1", Kind: "comment", TargetURL: "https://www.linkedin.com/feed/update/1", Text: "Good point", CreatedAt: now}))

	require.NoError(t, repo.RateSuggestion(ctx, "member-1", "s-1", 4, now))
	require.NoError(t, repo.MarkSuggestionPosted(ctx, "member-1", "s-1", now))

	list, err := repo.ListSuggestions(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 4, list[0].Rating)
	require.NotNil(t, list[0].PostedAt)
	count, err := repo.CountSuggestionsSince(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
