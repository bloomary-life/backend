package scheduledpost

import (
	"context"
	"database/sql"
	"testing"
	"time"

	domain "bloomary-api/internal/domain/scheduledpost"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestCancelRemovesDraftFromActiveSchedules(t *testing.T) {
	// Verifies: BE-002
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	post, err := repo.Schedule(ctx, domain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)

	require.NoError(t, repo.Cancel(ctx, "member-1", "draft-1"))

	active, err := repo.ListActive(ctx, "member-1")
	require.NoError(t, err)
	require.Empty(t, active)
	cancelled, err := repo.ByID(ctx, "member-1", post.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Cancelled, cancelled.Status)
}

func TestApprovalRequiredPostIsNotClaimedUntilApproved(t *testing.T) {
	// Verifies: BE-013
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	post, err := repo.Schedule(ctx, domain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: time.Now().Add(time.Minute), NeedsApproval: true})
	require.NoError(t, err)
	require.True(t, post.AwaitingApproval)
	later := time.Now().Add(time.Hour)

	_, err = repo.ClaimDue(ctx, later)
	require.ErrorIs(t, err, sql.ErrNoRows)

	require.NoError(t, repo.Approve(ctx, "member-1", post.ID))
	require.NoError(t, repo.HoldForApproval(ctx, "member-1", "draft-1"))
	_, err = repo.ClaimDue(ctx, later)
	require.ErrorIs(t, err, sql.ErrNoRows, "an MCP change holds the approved post again")

	require.NoError(t, repo.Approve(ctx, "member-1", post.ID))
	claimed, err := repo.ClaimDue(ctx, later)
	require.NoError(t, err)
	require.Equal(t, post.ID, claimed.ID)
	require.False(t, claimed.AwaitingApproval)
}
