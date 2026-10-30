package scheduledpost

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	domain "bloomary-api/internal/domain/scheduledpost"
)

type SQLiteRepository struct{ db *sql.DB }

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("scheduled post sqlite repository: db is required")
	}
	r := &SQLiteRepository{db: db}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS scheduled_posts (
		id TEXT PRIMARY KEY, member_id TEXT NOT NULL, draft_id TEXT NOT NULL,
		scheduled_at TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, next_attempt_at TEXT,
		linkedin_post_id TEXT, error TEXT, retries INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		return nil, fmt.Errorf("create scheduled_posts: %w", err)
	}
	_, _ = db.Exec(`UPDATE scheduled_posts SET status='needs_review',error='Worker stopped while processing; verify LinkedIn before retrying' WHERE status='processing'`)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_scheduled_posts_due ON scheduled_posts(status, scheduled_at) WHERE status='pending'`)
	if err != nil {
		return nil, fmt.Errorf("index scheduled_posts: %w", err)
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_scheduled_posts_retry ON scheduled_posts(status, next_attempt_at) WHERE status='retry'`)
	if err != nil {
		return nil, fmt.Errorf("index scheduled post retries: %w", err)
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_schedule_per_draft ON scheduled_posts(member_id,draft_id) WHERE status IN ('pending','processing','retry','needs_review')`)
	if err != nil {
		return nil, fmt.Errorf("index active scheduled posts: %w", err)
	}
	return r, nil
}

func (r *SQLiteRepository) Schedule(ctx context.Context, in domain.CreateInput) (domain.Post, error) {
	now := time.Now().UTC()
	updated, err := scan(r.db.QueryRowContext(ctx, `UPDATE scheduled_posts SET scheduled_at=?,status='pending',retries=0,next_attempt_at=NULL,error=NULL,updated_at=? WHERE member_id=? AND draft_id=? AND (status IN ('pending','retry') OR (status='needs_review' AND ?)) RETURNING id,member_id,draft_id,scheduled_at,status,COALESCE(linkedin_post_id,''),COALESCE(error,'')`, in.ScheduledAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), in.MemberID, in.DraftID, in.ReviewConfirmed))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Post{}, err
	}
	var activeStatus string
	err = r.db.QueryRowContext(ctx, `SELECT status FROM scheduled_posts WHERE member_id=? AND draft_id=? AND status IN ('processing','needs_review') LIMIT 1`, in.MemberID, in.DraftID).Scan(&activeStatus)
	if err == nil {
		return domain.Post{}, fmt.Errorf("schedule cannot be changed while post is %s", activeStatus)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Post{}, err
	}
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return domain.Post{}, err
	}
	id := hex.EncodeToString(randomID)
	_, err = r.db.ExecContext(ctx, `INSERT INTO scheduled_posts(id,member_id,draft_id,scheduled_at,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, in.MemberID, in.DraftID, in.ScheduledAt.UTC().Format(time.RFC3339Nano), domain.Pending, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return domain.Post{}, err
	}
	return domain.Post{ID: id, MemberID: in.MemberID, DraftID: in.DraftID, ScheduledAt: in.ScheduledAt.UTC(), Status: domain.Pending}, nil
}

func (r *SQLiteRepository) ListActive(ctx context.Context, memberID string) ([]domain.Post, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,member_id,draft_id,scheduled_at,status,COALESCE(linkedin_post_id,''),COALESCE(error,'') FROM scheduled_posts WHERE member_id=? AND status IN ('pending','processing','retry','needs_review') ORDER BY scheduled_at`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts := make([]domain.Post, 0)
	for rows.Next() {
		post, err := scan(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func (r *SQLiteRepository) ByID(ctx context.Context, memberID, id string) (domain.Post, error) {
	return scan(r.db.QueryRowContext(ctx, `SELECT id,member_id,draft_id,scheduled_at,status,COALESCE(linkedin_post_id,''),COALESCE(error,'') FROM scheduled_posts WHERE member_id=? AND id=?`, memberID, id))
}

func (r *SQLiteRepository) ClaimDue(ctx context.Context, now time.Time) (domain.Post, error) {
	row := r.db.QueryRowContext(ctx, `UPDATE scheduled_posts SET status='processing',updated_at=? WHERE id=(SELECT id FROM scheduled_posts WHERE (status='pending' AND scheduled_at<=?) OR (status='retry' AND next_attempt_at<=?) ORDER BY scheduled_at LIMIT 1) RETURNING id,member_id,draft_id,scheduled_at,status,COALESCE(linkedin_post_id,''),COALESCE(error,'')`, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	return scan(row)
}

func (r *SQLiteRepository) Update(ctx context.Context, id string, status domain.Status, postID, message string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduled_posts SET status=?,linkedin_post_id=CASE WHEN ?='' THEN linkedin_post_id ELSE ? END,error=?,updated_at=? WHERE id=?`, status, postID, postID, message, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (r *SQLiteRepository) Retry(ctx context.Context, id string, next time.Time, message string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduled_posts SET status='retry',retries=retries+1,next_attempt_at=?,error=?,updated_at=? WHERE id=? AND retries<3`, next.UTC().Format(time.RFC3339Nano), message, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// Cancel leaves processing jobs alone: the worker may already be posting them.
func (r *SQLiteRepository) Cancel(ctx context.Context, memberID, draftID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduled_posts SET status='cancelled',updated_at=? WHERE member_id=? AND draft_id=? AND status IN ('pending','retry','needs_review')`, time.Now().UTC().Format(time.RFC3339Nano), memberID, draftID)
	return err
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (domain.Post, error) {
	var p domain.Post
	var scheduled string
	var status string
	err := row.Scan(&p.ID, &p.MemberID, &p.DraftID, &scheduled, &status, &p.LinkedInPostID, &p.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Post{}, sql.ErrNoRows
	}
	if err != nil {
		return domain.Post{}, err
	}
	p.ScheduledAt, err = time.Parse(time.RFC3339Nano, scheduled)
	p.Status = domain.Status(status)
	return p, err
}
