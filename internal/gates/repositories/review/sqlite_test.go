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

func TestApprovalIsDecidedOnceAndSupersededByNewPreview(t *testing.T) {
	// Verifies: BE-013
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	approval := reviewDomain.Approval{ID: "a-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ProposedText: "Post", Status: reviewDomain.ApprovalPending, ScheduledAt: now.Add(time.Hour), CreatedAt: now}
	require.NoError(t, repo.CreateApproval(ctx, approval))

	decided, err := repo.DecideApproval(ctx, "member-1", "a-1", reviewDomain.ApprovalApproved, now)
	require.NoError(t, err)
	require.Equal(t, reviewDomain.ApprovalApproved, decided.Status)
	require.NotNil(t, decided.DecidedAt)
	_, err = repo.DecideApproval(ctx, "member-1", "a-1", reviewDomain.ApprovalSkipped, now)
	require.ErrorIs(t, err, reviewDomain.ErrNotFound)

	approval.ID = "a-2"
	require.NoError(t, repo.CreateApproval(ctx, approval))
	require.NoError(t, repo.SupersedePending(ctx, "member-1", "draft-1", now))
	superseded, err := repo.ApprovalByID(ctx, "member-1", "a-2")
	require.NoError(t, err)
	require.Equal(t, reviewDomain.ApprovalSuperseded, superseded.Status)
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
