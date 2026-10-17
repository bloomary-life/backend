package daily

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	dailyDomain "bloomary-api/internal/domain/daily"
)

type SQLiteRepository struct {
	db *sql.DB
}

var _ dailyDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("daily sqlite repository: db is required")
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(
		ctx, `
	CREATE TABLE IF NOT EXISTS linkedin_daily_requests (
	  member_id TEXT NOT NULL,
	  day TEXT NOT NULL,
	  count INTEGER NOT NULL,
	  posted INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY(member_id, day)
	)`,
	)
	if err != nil {
		return fmt.Errorf("init daily sqlite table: %w", err)
	}

	if err := r.ensurePostedColumn(ctx); err != nil {
		return err
	}

	return nil
}

func (r *SQLiteRepository) ensurePostedColumn(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `ALTER TABLE linkedin_daily_requests ADD COLUMN posted INTEGER NOT NULL DEFAULT 1`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add linkedin daily request posted column: %w", err)
	}

	return nil
}

func (r *SQLiteRepository) ConsumeQuota(ctx context.Context, memberID string, now time.Time, limit int) (bool, error) {
	return r.Increment(ctx, memberID, now, limit)
}

func (r *SQLiteRepository) Increment(ctx context.Context, memberID string, now time.Time, limit int) (bool, error) {
	if memberID == "" || limit <= 0 {
		return true, nil
	}

	result, err := r.db.ExecContext(
		ctx, `
	INSERT INTO linkedin_daily_requests (member_id, day, count, posted)
	VALUES (?, ?, 1, 0)
	ON CONFLICT(member_id, day) DO UPDATE SET
	  count = count + 1
	WHERE linkedin_daily_requests.count < ?`,
		memberID,
		now.UTC().Format("2006-01-02"),
		limit,
	)
	if err != nil {
		return false, fmt.Errorf("increment linkedin daily request sqlite counter member_id=%q: %w", memberID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read linkedin daily request sqlite rows affected member_id=%q: %w", memberID, err)
	}

	return affected > 0, nil
}

func (r *SQLiteRepository) MarkPosted(ctx context.Context, memberID string, now time.Time) error {
	if memberID == "" {
		return nil
	}

	_, err := r.db.ExecContext(
		ctx, `
	INSERT INTO linkedin_daily_requests (member_id, day, count, posted)
	VALUES (?, ?, 0, 1)
	ON CONFLICT(member_id, day) DO UPDATE SET posted = 1`,
		memberID,
		now.UTC().Format("2006-01-02"),
	)
	if err != nil {
		return fmt.Errorf("mark linkedin daily request posted member_id=%q: %w", memberID, err)
	}

	return nil
}

func (r *SQLiteRepository) TotalPosts(ctx context.Context, memberID string) (int, error) {
	var total int
	err := r.db.QueryRowContext(
		ctx, `SELECT COALESCE(SUM(posted), 0) FROM linkedin_daily_requests WHERE member_id = ?`, memberID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count total posts member_id=%q: %w", memberID, err)
	}
	return total, nil
}

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM linkedin_daily_requests WHERE member_id = ?`, memberID)
	if err != nil {
		return fmt.Errorf("delete linkedin daily request sqlite rows member_id=%q: %w", memberID, err)
	}

	return nil
}
