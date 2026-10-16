package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// PostPublishRepository records post publish events for metrics.
type PostPublishRepository struct {
	db *sql.DB
}

func NewPostPublishRepository(db *sql.DB) (*PostPublishRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("post publish repository: db is required")
	}

	return &PostPublishRepository{db: db}, nil
}

// InitTable creates the metric_post_publishes table if it doesn't exist.
func (r *PostPublishRepository) InitTable(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS metric_post_publishes (
  member_id TEXT NOT NULL,
  published_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("init metric post publishes table: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_metric_post_publishes_published_at_member_id
ON metric_post_publishes (published_at, member_id)`); err != nil {
		return fmt.Errorf("init metric post publishes index: %w", err)
	}

	return nil
}

func (r *PostPublishRepository) RecordPostPublished(ctx context.Context, memberID string, now time.Time) error {
	memberID = strings.TrimSpace(memberID)
	if memberID == "" {
		return nil
	}

	_, err := r.db.ExecContext(
		ctx,
		`INSERT INTO metric_post_publishes (member_id, published_at) VALUES (?, ?)`,
		memberID,
		now.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("record metric post publish: %w", err)
	}

	return nil
}

// MetricsRepository provides data for product gauge calculations.
type MetricsRepository struct {
	db *sql.DB
}

func NewMetricsRepository(db *sql.DB) (*MetricsRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("metrics repository: db is required")
	}

	return &MetricsRepository{db: db}, nil
}

func (r *MetricsRepository) CountConnectedUsers(ctx context.Context) (int, error) {
	return count(ctx, r.db, `SELECT COUNT(*) FROM linkedin_auth`)
}

func (r *MetricsRepository) CountSignupsSince(ctx context.Context, since time.Time) (int, error) {
	return count(ctx, r.db, `
SELECT COUNT(*)
FROM linkedin_auth
WHERE connected_at >= ?`, since.Format(time.RFC3339))
}

func (r *MetricsRepository) CountActivatedSince(ctx context.Context, since time.Time) (int, error) {
	return count(ctx, r.db, `
SELECT COUNT(*)
FROM (
  SELECT member_id, MIN(published_at) AS first_published_at
  FROM metric_post_publishes
  GROUP BY member_id
)
WHERE first_published_at >= ?`, since.Format(time.RFC3339))
}

func (r *MetricsRepository) CountActiveSince(ctx context.Context, since time.Time) (int, error) {
	return count(ctx, r.db, `
SELECT COUNT(DISTINCT member_id)
FROM metric_post_publishes
WHERE published_at >= ?`, since.Format(time.RFC3339))
}

func (r *MetricsRepository) CountUsersWithMinPostsSince(ctx context.Context, minPosts int, since time.Time) (int, error) {
	return count(ctx, r.db, `
SELECT COUNT(*)
FROM (
  SELECT member_id
  FROM metric_post_publishes
  WHERE published_at >= ?
  GROUP BY member_id
  HAVING COUNT(*) >= ?
)`, since.Format(time.RFC3339), minPosts)
}

func count(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}
