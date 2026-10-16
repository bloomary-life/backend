package paddle_entitlement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
)

type SQLiteRepository struct {
	db *sql.DB
}

var _ paddleDomain.Repository = (*SQLiteRepository)(nil)
var _ paddleDomain.BillingRepository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("paddle entitlement sqlite repository: db is required")
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(
		ctx, `
CREATE TABLE IF NOT EXISTS paddle_subscription_entitlements (
  subscription_id TEXT PRIMARY KEY,
  member_id TEXT NOT NULL,
  paddle_customer_id TEXT NOT NULL,
  pro_entitlement_until TEXT,
  paddle_event_occurred_at TEXT NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("init paddle entitlement sqlite table: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS paddle_subscription_entitlements_member_idx ON paddle_subscription_entitlements(member_id)`)
	if err != nil {
		return fmt.Errorf("index paddle subscription entitlements: %w", err)
	}
	// Preserve rows created by the previous one-row-per-member schema until their
	// existing access period expires.
	var legacyTableExists bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='paddle_entitlements')`).Scan(&legacyTableExists)
	if err != nil {
		return fmt.Errorf("check legacy paddle entitlements: %w", err)
	}
	if legacyTableExists {
		_, err = r.db.ExecContext(ctx, `
INSERT OR IGNORE INTO paddle_subscription_entitlements (
  subscription_id, member_id, paddle_customer_id, pro_entitlement_until, paddle_event_occurred_at
)
SELECT 'legacy:' || member_id, member_id, paddle_customer_id, pro_entitlement_until, paddle_event_occurred_at
FROM paddle_entitlements`)
		if err != nil {
			return fmt.Errorf("migrate legacy paddle entitlements: %w", err)
		}
	}

	return nil
}

func (r *SQLiteRepository) ByMemberID(ctx context.Context, memberID string) (
	paddleDomain.Entitlement, error,
) {
	var accessUntilRaw sql.NullString
	err := r.db.QueryRowContext(
		ctx, `
SELECT MAX(pro_entitlement_until)
FROM paddle_subscription_entitlements
WHERE member_id = ?`, memberID,
	).Scan(&accessUntilRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return paddleDomain.Entitlement{}, nil
	}

	if err != nil {
		return paddleDomain.Entitlement{}, fmt.Errorf(
			"select paddle entitlement sqlite row member_id=%q: %w", memberID, err,
		)
	}

	if !accessUntilRaw.Valid || accessUntilRaw.String == "" {
		return paddleDomain.Entitlement{}, nil
	}

	accessUntil, err := parseSQLiteTime(accessUntilRaw.String)
	if err != nil {
		return paddleDomain.Entitlement{}, fmt.Errorf(
			"parse paddle access until sqlite row member_id=%q: %w", memberID, err,
		)
	}

	return paddleDomain.Entitlement{AccessUntil: accessUntil}, nil
}

func (r *SQLiteRepository) CustomerIDByMemberID(ctx context.Context, memberID string) (string, error) {
	var customerID string
	err := r.db.QueryRowContext(
		ctx, `
SELECT paddle_customer_id
FROM paddle_subscription_entitlements
WHERE member_id = ? AND paddle_customer_id <> ''
ORDER BY paddle_event_occurred_at DESC
LIMIT 1`, memberID,
	).Scan(&customerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("select paddle customer sqlite row member_id=%q: %w", memberID, err)
	}
	return customerID, nil
}

func (r *SQLiteRepository) Apply(ctx context.Context, update paddleDomain.Update) (bool, error) {
	proUntil := sql.NullString{}
	if !update.ProUntil.IsZero() {
		proUntil = sql.NullString{String: update.ProUntil.UTC().Format(time.RFC3339), Valid: true}
	}

	eventOccurredAt := update.EventOccurredAt.UTC().Format(time.RFC3339)
	result, err := r.db.ExecContext(
		ctx, `
INSERT INTO paddle_subscription_entitlements (
  subscription_id, member_id, paddle_customer_id, pro_entitlement_until, paddle_event_occurred_at
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(subscription_id) DO UPDATE SET
  member_id = excluded.member_id,
  paddle_customer_id = excluded.paddle_customer_id,
  pro_entitlement_until = excluded.pro_entitlement_until,
  paddle_event_occurred_at = excluded.paddle_event_occurred_at
WHERE
	paddle_subscription_entitlements.paddle_event_occurred_at IS NULL
	OR paddle_subscription_entitlements.paddle_event_occurred_at = ''
	OR unixepoch(paddle_subscription_entitlements.paddle_event_occurred_at) < unixepoch(excluded.paddle_event_occurred_at)`,
		update.SubscriptionID,
		update.MemberID,
		update.PaddleCustomerID,
		proUntil,
		eventOccurredAt,
	)
	if err != nil {
		return false, fmt.Errorf("apply paddle entitlement sqlite row member_id=%q: %w", update.MemberID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf(
			"read paddle entitlement sqlite rows affected member_id=%q: %w", update.MemberID, err,
		)
	}

	return rows > 0, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}

	return time.Parse("2006-01-02 15:04:05", value)
}
