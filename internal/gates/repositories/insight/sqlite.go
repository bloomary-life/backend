package insight

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	insightDomain "bloomary-api/internal/domain/insight"
)

type SQLiteRepository struct{ db *sql.DB }

var _ insightDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("insight sqlite repository: db is required")
	}

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS member_insights (
		id TEXT PRIMARY KEY, member_id TEXT NOT NULL, body TEXT NOT NULL, source TEXT NOT NULL,
		created_at TEXT NOT NULL, used_at TEXT, draft_id TEXT)`)
	if err != nil {
		return nil, fmt.Errorf("create member_insights: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_member_insights_member_created ON member_insights(member_id, created_at)`)
	if err != nil {
		return nil, fmt.Errorf("index member_insights: %w", err)
	}

	for _, column := range []string{"trend_source_url", "trend_summary"} {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('member_insights') WHERE name=?`, column).Scan(&exists); err != nil {
			return nil, fmt.Errorf("read member_insights columns: %w", err)
		}
		if exists == 0 {
			if _, err := db.Exec(`ALTER TABLE member_insights ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return nil, fmt.Errorf("add member_insights.%s: %w", column, err)
			}
		}
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS member_voice_attempts (member_id TEXT NOT NULL, seconds INTEGER NOT NULL, created_at TEXT NOT NULL)`)
	if err != nil {
		return nil, fmt.Errorf("create member_voice_attempts: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_member_voice_attempts_member_created ON member_voice_attempts(member_id, created_at)`)
	if err != nil {
		return nil, fmt.Errorf("index member_voice_attempts: %w", err)
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Create(ctx context.Context, in insightDomain.Insight) error {
	var trend insightDomain.Trend
	if in.Trend != nil {
		trend = *in.Trend
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO member_insights(id,member_id,body,source,created_at,trend_source_url,trend_summary) VALUES(?,?,?,?,?,?,?)`,
		in.ID, in.MemberID, in.Text, string(in.Source), in.CreatedAt.UTC().Format(time.RFC3339Nano), trend.SourceURL, trend.Summary)
	if err != nil {
		return fmt.Errorf("insert insight member_id=%q: %w", in.MemberID, err)
	}

	return nil
}

func (r *SQLiteRepository) List(ctx context.Context, filter insightDomain.ListFilter) ([]insightDomain.Insight, error) {
	query := `SELECT id,member_id,body,source,created_at,COALESCE(used_at,''),COALESCE(draft_id,''),trend_source_url,trend_summary FROM member_insights WHERE member_id=? AND created_at>=?`
	if filter.UnusedOnly {
		query += ` AND used_at IS NULL`
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = insightDomain.DefaultListLimit
	}

	rows, err := r.db.QueryContext(ctx, query+` ORDER BY created_at DESC LIMIT ?`,
		filter.MemberID, filter.Since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("list insights member_id=%q: %w", filter.MemberID, err)
	}
	defer rows.Close()
	out := make([]insightDomain.Insight, 0)
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, in)
	}

	return out, rows.Err()
}

func (r *SQLiteRepository) MarkUsed(ctx context.Context, memberID, insightID, draftID string, at time.Time) (insightDomain.Insight, error) {
	in, err := scan(r.db.QueryRowContext(ctx, `UPDATE member_insights SET used_at=?,draft_id=? WHERE member_id=? AND id=?
		RETURNING id,member_id,body,source,created_at,COALESCE(used_at,''),COALESCE(draft_id,''),trend_source_url,trend_summary`,
		at.UTC().Format(time.RFC3339Nano), draftID, memberID, insightID))
	if errors.Is(err, sql.ErrNoRows) {
		return insightDomain.Insight{}, insightDomain.ErrNotFound
	}

	return in, err
}

func (r *SQLiteRepository) CountSince(ctx context.Context, memberID string, source insightDomain.Source, since time.Time) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_insights WHERE member_id=? AND created_at>=? AND (?='' OR source=?)`,
		memberID, since.UTC().Format(time.RFC3339Nano), string(source), string(source)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count insights member_id=%q: %w", memberID, err)
	}

	return count, nil
}

// ReserveVoice is one INSERT ... SELECT, so parallel updates cannot pass the limit together.
func (r *SQLiteRepository) ReserveVoice(ctx context.Context, memberID string, seconds int, since, at time.Time) (bool, error) {
	from := since.UTC().Format(time.RFC3339Nano)
	// Attempts older than the window are no longer needed for the cap (privacy policy: kept 24 hours).
	if _, err := r.db.ExecContext(ctx, `DELETE FROM member_voice_attempts WHERE created_at<?`, from); err != nil {
		return false, fmt.Errorf("prune voice attempts: %w", err)
	}

	result, err := r.db.ExecContext(ctx, `INSERT INTO member_voice_attempts(member_id,seconds,created_at)
		SELECT ?,?,? WHERE (SELECT COUNT(*) FROM member_voice_attempts WHERE member_id=? AND created_at>=?) < ?
		AND (SELECT COALESCE(SUM(seconds),0) FROM member_voice_attempts WHERE member_id=? AND created_at>=?) + ? <= ?`,
		memberID, seconds, at.UTC().Format(time.RFC3339Nano), memberID, from, insightDomain.DailyVoiceLimit, memberID, from, seconds, insightDomain.DailyVoiceSeconds)
	if err != nil {
		return false, fmt.Errorf("reserve voice member_id=%q: %w", memberID, err)
	}

	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM member_insights WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete insights member_id=%q: %w", memberID, err)
	}

	if _, err := r.db.ExecContext(ctx, `DELETE FROM member_voice_attempts WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete voice attempts member_id=%q: %w", memberID, err)
	}

	return nil
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (insightDomain.Insight, error) {
	var in insightDomain.Insight
	var source, createdAt, usedAt, trendURL, trendSummary string
	if err := row.Scan(&in.ID, &in.MemberID, &in.Text, &source, &createdAt, &usedAt, &in.DraftID, &trendURL, &trendSummary); err != nil {
		return insightDomain.Insight{}, err
	}

	if trendURL != "" {
		in.Trend = &insightDomain.Trend{SourceURL: trendURL, Summary: trendSummary}
	}

	in.Source = insightDomain.Source(source)
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return insightDomain.Insight{}, fmt.Errorf("parse insight created_at id=%q: %w", in.ID, err)
	}

	in.CreatedAt = parsed
	if strings.TrimSpace(usedAt) != "" {
		used, err := time.Parse(time.RFC3339Nano, usedAt)
		if err != nil {
			return insightDomain.Insight{}, fmt.Errorf("parse insight used_at id=%q: %w", in.ID, err)
		}

		in.UsedAt = &used
	}

	return in, nil
}
