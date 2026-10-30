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
	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Create(ctx context.Context, in insightDomain.Insight) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO member_insights(id,member_id,body,source,created_at) VALUES(?,?,?,?,?)`,
		in.ID, in.MemberID, in.Text, string(in.Source), in.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert insight member_id=%q: %w", in.MemberID, err)
	}
	return nil
}

func (r *SQLiteRepository) List(ctx context.Context, filter insightDomain.ListFilter) ([]insightDomain.Insight, error) {
	query := `SELECT id,member_id,body,source,created_at,COALESCE(used_at,''),COALESCE(draft_id,'') FROM member_insights WHERE member_id=? AND created_at>=?`
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
		RETURNING id,member_id,body,source,created_at,COALESCE(used_at,''),COALESCE(draft_id,'')`,
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

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM member_insights WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete insights member_id=%q: %w", memberID, err)
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (insightDomain.Insight, error) {
	var in insightDomain.Insight
	var source, createdAt, usedAt string
	if err := row.Scan(&in.ID, &in.MemberID, &in.Text, &source, &createdAt, &usedAt, &in.DraftID); err != nil {
		return insightDomain.Insight{}, err
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
