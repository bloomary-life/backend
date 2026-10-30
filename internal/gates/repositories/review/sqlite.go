package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	reviewDomain "bloomary-api/internal/domain/review"
)

const (
	approvalColumns   = `id,member_id,draft_id,schedule_id,proposed_text,note,status,scheduled_at,created_at,COALESCE(decided_at,'')`
	suggestionColumns = `id,member_id,kind,target_url,body,context,rating,created_at,COALESCE(rated_at,''),COALESCE(posted_at,'')`
)

type SQLiteRepository struct{ db *sql.DB }

var _ reviewDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("review sqlite repository: db is required")
	}

	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS draft_approvals (
			id TEXT PRIMARY KEY, member_id TEXT NOT NULL, draft_id TEXT NOT NULL, schedule_id TEXT NOT NULL,
			proposed_text TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			scheduled_at TEXT NOT NULL, created_at TEXT NOT NULL, decided_at TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_draft_approvals_member_created ON draft_approvals(member_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS comment_suggestions (
			id TEXT PRIMARY KEY, member_id TEXT NOT NULL, kind TEXT NOT NULL, target_url TEXT NOT NULL,
			body TEXT NOT NULL, context TEXT NOT NULL DEFAULT '', rating INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, rated_at TEXT, posted_at TEXT)`,
		`CREATE INDEX IF NOT EXISTS idx_comment_suggestions_member_created ON comment_suggestions(member_id, created_at)`,
	} {
		if _, err := db.Exec(query); err != nil {
			return nil, fmt.Errorf("create review tables: %w", err)
		}
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) CreateApproval(ctx context.Context, a reviewDomain.Approval) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO draft_approvals(id,member_id,draft_id,schedule_id,proposed_text,note,status,scheduled_at,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.MemberID, a.DraftID, a.ScheduleID, a.ProposedText, a.Note, string(a.Status), formatTime(a.ScheduledAt), formatTime(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert draft approval member_id=%q: %w", a.MemberID, err)
	}

	return nil
}

func (r *SQLiteRepository) ApprovalByID(ctx context.Context, memberID, id string) (reviewDomain.Approval, error) {
	return scanApproval(r.db.QueryRowContext(ctx, `SELECT `+approvalColumns+` FROM draft_approvals WHERE member_id=? AND id=?`, memberID, id))
}

func (r *SQLiteRepository) DecideApproval(ctx context.Context, memberID, id string, status reviewDomain.ApprovalStatus, at time.Time) (reviewDomain.Approval, error) {
	return scanApproval(r.db.QueryRowContext(ctx, `UPDATE draft_approvals SET status=?,decided_at=? WHERE member_id=? AND id=? AND status='pending' RETURNING `+approvalColumns,
		string(status), formatTime(at), memberID, id))
}

func (r *SQLiteRepository) SupersedePending(ctx context.Context, memberID, draftID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE draft_approvals SET status='superseded',decided_at=? WHERE member_id=? AND draft_id=? AND status='pending'`, formatTime(at), memberID, draftID)
	if err != nil {
		return fmt.Errorf("supersede draft approvals member_id=%q: %w", memberID, err)
	}

	return nil
}

func (r *SQLiteRepository) ListApprovals(ctx context.Context, memberID string, since time.Time) ([]reviewDomain.Approval, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+approvalColumns+` FROM draft_approvals WHERE member_id=? AND created_at>=? ORDER BY created_at DESC LIMIT 200`, memberID, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("list draft approvals member_id=%q: %w", memberID, err)
	}
	defer rows.Close()
	out := make([]reviewDomain.Approval, 0)
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, a)
	}

	return out, rows.Err()
}

func (r *SQLiteRepository) CountApprovalsSince(ctx context.Context, memberID string, since time.Time) (int, error) {
	return r.count(ctx, `SELECT COUNT(*) FROM draft_approvals WHERE member_id=? AND created_at>=?`, memberID, since)
}

func (r *SQLiteRepository) CreateSuggestion(ctx context.Context, s reviewDomain.Suggestion) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO comment_suggestions(id,member_id,kind,target_url,body,context,created_at) VALUES(?,?,?,?,?,?,?)`,
		s.ID, s.MemberID, s.Kind, s.TargetURL, s.Text, s.Context, formatTime(s.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert comment suggestion member_id=%q: %w", s.MemberID, err)
	}

	return nil
}

func (r *SQLiteRepository) RateSuggestion(ctx context.Context, memberID, id string, rating int, at time.Time) error {
	return r.updateOne(ctx, `UPDATE comment_suggestions SET rating=?,rated_at=? WHERE member_id=? AND id=?`, rating, formatTime(at), memberID, id)
}

func (r *SQLiteRepository) MarkSuggestionPosted(ctx context.Context, memberID, id string, at time.Time) error {
	return r.updateOne(ctx, `UPDATE comment_suggestions SET posted_at=? WHERE member_id=? AND id=?`, formatTime(at), memberID, id)
}

func (r *SQLiteRepository) ListSuggestions(ctx context.Context, memberID string, since time.Time) ([]reviewDomain.Suggestion, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+suggestionColumns+` FROM comment_suggestions WHERE member_id=? AND created_at>=? ORDER BY created_at DESC LIMIT 500`, memberID, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("list comment suggestions member_id=%q: %w", memberID, err)
	}
	defer rows.Close()
	out := make([]reviewDomain.Suggestion, 0)
	for rows.Next() {
		s, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, s)
	}

	return out, rows.Err()
}

func (r *SQLiteRepository) CountSuggestionsSince(ctx context.Context, memberID string, since time.Time) (int, error) {
	return r.count(ctx, `SELECT COUNT(*) FROM comment_suggestions WHERE member_id=? AND created_at>=?`, memberID, since)
}

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	for _, query := range []string{`DELETE FROM draft_approvals WHERE member_id=?`, `DELETE FROM comment_suggestions WHERE member_id=?`} {
		if _, err := r.db.ExecContext(ctx, query, memberID); err != nil {
			return fmt.Errorf("delete review data member_id=%q: %w", memberID, err)
		}
	}

	return nil
}

func (r *SQLiteRepository) count(ctx context.Context, query, memberID string, since time.Time) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, query, memberID, formatTime(since)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count review rows member_id=%q: %w", memberID, err)
	}

	return n, nil
}

func (r *SQLiteRepository) updateOne(ctx context.Context, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return reviewDomain.ErrNotFound
	}

	return err
}

type scanner interface{ Scan(...any) error }

func scanApproval(row scanner) (reviewDomain.Approval, error) {
	var a reviewDomain.Approval
	var status, scheduledAt, createdAt, decidedAt string
	err := row.Scan(&a.ID, &a.MemberID, &a.DraftID, &a.ScheduleID, &a.ProposedText, &a.Note, &status, &scheduledAt, &createdAt, &decidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return reviewDomain.Approval{}, reviewDomain.ErrNotFound
	}

	if err != nil {
		return reviewDomain.Approval{}, err
	}

	a.Status = reviewDomain.ApprovalStatus(status)
	if a.ScheduledAt, err = time.Parse(time.RFC3339Nano, scheduledAt); err != nil {
		return reviewDomain.Approval{}, err
	}

	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return reviewDomain.Approval{}, err
	}

	a.DecidedAt, err = optionalTime(decidedAt)
	return a, err
}

func scanSuggestion(row scanner) (reviewDomain.Suggestion, error) {
	var s reviewDomain.Suggestion
	var createdAt, ratedAt, postedAt string
	err := row.Scan(&s.ID, &s.MemberID, &s.Kind, &s.TargetURL, &s.Text, &s.Context, &s.Rating, &createdAt, &ratedAt, &postedAt)
	if err != nil {
		return reviewDomain.Suggestion{}, err
	}

	if s.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return reviewDomain.Suggestion{}, err
	}

	if s.RatedAt, err = optionalTime(ratedAt); err != nil {
		return reviewDomain.Suggestion{}, err
	}

	s.PostedAt, err = optionalTime(postedAt)
	return s, err
}

func optionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
