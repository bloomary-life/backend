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
	count, err := repo.CountNoticesSince(ctx, "member-1", now.Add(-time.Hour), false)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, repo.CreateNotice(ctx, reviewDomain.Notice{ID: "n-2", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ProposedText: "Post", Status: reviewDomain.NoticeSent, ScheduledAt: now.Add(time.Hour), CreatedAt: now, Requested: true}))
	count, err = repo.CountNoticesSince(ctx, "member-1", now.Add(-time.Hour), false)
	require.NoError(t, err)
	require.Equal(t, 1, count, "member-requested previews are counted apart")
	count, err = repo.CountNoticesSince(ctx, "member-1", now.Add(-time.Hour), true)
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
	posted, err := repo.MarkSuggestionPosted(ctx, "member-1", "s-1", now)
	require.NoError(t, err)
	require.Equal(t, "Good point", posted.Text)

	list, err := repo.ListSuggestions(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 4, list[0].Rating)
	require.NotNil(t, list[0].PostedAt)
	count, err := repo.CountSuggestionsSince(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestEditRequestIsFoundByPreviewMessageAndAppliedOnce(t *testing.T) {
	// Verifies: BE-017
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateNotice(ctx, reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ProposedText: "Post", Status: reviewDomain.NoticeSent, ScheduledAt: now.Add(time.Hour), CreatedAt: now}))
	require.NoError(t, repo.SetNoticeMessage(ctx, "member-1", "n-1", 55, 54))

	notice, err := repo.NoticeByMessage(ctx, "member-1", 55)
	require.NoError(t, err)
	require.Equal(t, "draft-1", notice.DraftID)
	require.Equal(t, int64(55), notice.MessageID)
	byPhoto, err := repo.NoticeByMessage(ctx, "member-1", 54)
	require.NoError(t, err)
	require.Equal(t, "n-1", byPhoto.ID)

	require.NoError(t, repo.CreateEditRequest(ctx, reviewDomain.EditRequest{ID: "e-1", MemberID: "member-1", DraftID: "draft-1", NoticeID: "n-1", Text: "Shorter, drop the last line", Source: "voice", CreatedAt: now}))
	count, err := repo.CountEditRequestsSince(ctx, "member-1", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, count)

	applied, err := repo.MarkEditApplied(ctx, "member-1", "e-1", now)
	require.NoError(t, err)
	require.NotNil(t, applied.AppliedAt)
	_, err = repo.MarkEditApplied(ctx, "member-1", "e-1", now)
	require.ErrorIs(t, err, reviewDomain.ErrEditApplied)

	open, err := repo.ListEditRequests(ctx, "member-1", true)
	require.NoError(t, err)
	require.Empty(t, open)
	all, err := repo.ListEditRequests(ctx, "member-1", false)
	require.NoError(t, err)
	require.Equal(t, "Shorter, drop the last line", all[0].Text)
}

func TestNewSQLiteRepositoryAddsMessageIDOnce(t *testing.T) {
	// Verifies: BE-017
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE scheduled_post_notices (id TEXT PRIMARY KEY, member_id TEXT NOT NULL, draft_id TEXT NOT NULL, schedule_id TEXT NOT NULL,
		proposed_text TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, scheduled_at TEXT NOT NULL, created_at TEXT NOT NULL, skipped_at TEXT)`)
	require.NoError(t, err)

	_, err = NewSQLiteRepository(db)
	require.NoError(t, err)
	_, err = NewSQLiteRepository(db)
	require.NoError(t, err)
	var columns int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('scheduled_post_notices') WHERE name='message_id'`).Scan(&columns))
	require.Equal(t, 1, columns)
}

func TestTrendSuggestionIsFoundByMessageAndFirstCommentIsReplaced(t *testing.T) {
	// Verifies: BE-023
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateSuggestion(ctx, reviewDomain.Suggestion{ID: "s-1", MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "https://www.anthropic.com/news/x", Text: "Summary", CreatedAt: now}))
	require.NoError(t, repo.SetSuggestionMessage(ctx, "member-1", "s-1", 70))

	byMessage, err := repo.SuggestionByMessage(ctx, "member-1", 70)
	require.NoError(t, err)
	require.Equal(t, "s-1", byMessage.ID)
	require.Equal(t, int64(70), byMessage.MessageID)
	byID, err := repo.SuggestionByID(ctx, "member-1", "s-1")
	require.NoError(t, err)
	require.Equal(t, reviewDomain.KindTrend, byID.Kind)

	require.NoError(t, repo.SaveFirstComment(ctx, "member-1", "draft-1", "job-1", "first", now))
	require.NoError(t, repo.SaveFirstComment(ctx, "member-1", "draft-1", "job-2", "second", now))
	comment, scheduleID, err := repo.FirstComment(ctx, "member-1", "draft-1")
	require.NoError(t, err)
	require.Equal(t, "second", comment)
	require.Equal(t, "job-2", scheduleID)
	require.NoError(t, repo.DeleteFirstComment(ctx, "member-1", "draft-1"))
	_, _, err = repo.FirstComment(ctx, "member-1", "draft-1")
	require.ErrorIs(t, err, reviewDomain.ErrNotFound)

	require.NoError(t, repo.SaveFirstComment(ctx, "member-1", "draft-1", "job-3", "third", now))
	require.NoError(t, repo.DeleteByMemberID(ctx, "member-1"))
	_, _, err = repo.FirstComment(ctx, "member-1", "draft-1")
	require.ErrorIs(t, err, reviewDomain.ErrNotFound)
}

func TestDraftPreviewKeepsItsFirstComment(t *testing.T) {
	// Verifies: BE-019
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateNotice(ctx, reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ProposedText: "Post",
		FirstComment: "Book: https://lumen.observer", Status: reviewDomain.NoticeSent, CreatedAt: now}))

	notice, err := repo.NoticeByID(ctx, "member-1", "n-1")

	require.NoError(t, err)
	require.Equal(t, "Book: https://lumen.observer", notice.FirstComment)
}

func TestEditRequestsCarryTheDraftsNewestFirstComment(t *testing.T) {
	// Verifies: BE-019
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, n := range []reviewDomain.Notice{
		{ID: "n-old", FirstComment: "Book: https://lumen.observer/call/katerina", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "n-new", FirstComment: "Sources: https://www.anthropic.com/news/x", CreatedAt: now.Add(-time.Hour)},
		{ID: "n-job", FirstComment: "scheduled", ScheduleID: "job-1", CreatedAt: now},
	} {
		n.MemberID, n.DraftID, n.ProposedText, n.Status = "member-1", "draft-1", "Post", reviewDomain.NoticeSent
		require.NoError(t, repo.CreateNotice(ctx, n))
	}
	require.NoError(t, repo.CreateEditRequest(ctx, reviewDomain.EditRequest{ID: "e-1", MemberID: "member-1", DraftID: "draft-1", NoticeID: "n-old", Text: "drop the booking link", Source: "text", CreatedAt: now}))
	require.NoError(t, repo.CreateEditRequest(ctx, reviewDomain.EditRequest{ID: "e-2", MemberID: "member-1", DraftID: "draft-2", NoticeID: "n-x", Text: "shorter", Source: "text", CreatedAt: now.Add(time.Second)}))

	open, err := repo.ListEditRequests(ctx, "member-1", true)

	require.NoError(t, err)
	require.Len(t, open, 2)
	require.Equal(t, "Sources: https://www.anthropic.com/news/x", open[0].FirstComment)
	require.Empty(t, open[1].FirstComment)
}
