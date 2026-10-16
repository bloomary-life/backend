package apple_entitlement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
)

type SQLiteRepository struct {
	db *sql.DB
}

var _ appleiapDomain.EntitlementRepository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("apple entitlement sqlite repository: db is required")
	}
	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS apple_entitlements (
  member_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  app_account_token TEXT NOT NULL,
  original_transaction_id TEXT PRIMARY KEY,
  transaction_id TEXT NOT NULL,
  product_id TEXT NOT NULL,
  entitlement_until TEXT,
  status TEXT NOT NULL,
  environment TEXT NOT NULL,
  event_occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apple_entitlements_member_id
ON apple_entitlements (member_id);
CREATE INDEX IF NOT EXISTS idx_apple_entitlements_app_account_token
ON apple_entitlements (app_account_token)`)
	if err != nil {
		return fmt.Errorf("init apple entitlement sqlite table: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) LatestEntitlement(
	ctx context.Context, memberID string,
) (appleiapDomain.Entitlement, bool, error) {
	var accessUntilRaw sql.NullString
	var statusRaw string
	err := r.db.QueryRowContext(ctx, `
SELECT entitlement_until, status
FROM apple_entitlements
WHERE member_id = ?
ORDER BY unixepoch(entitlement_until) DESC
LIMIT 1`, memberID).Scan(&accessUntilRaw, &statusRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return appleiapDomain.Entitlement{}, false, nil
	}
	if err != nil {
		return appleiapDomain.Entitlement{}, false, fmt.Errorf("select apple entitlement raw sqlite row member_id=%q: %w", memberID, err)
	}
	if !accessUntilRaw.Valid || accessUntilRaw.String == "" {
		return appleiapDomain.Entitlement{Status: appleiapDomain.Status(statusRaw)}, true, nil
	}

	accessUntil, err := parseSQLiteTime(accessUntilRaw.String)
	if err != nil {
		return appleiapDomain.Entitlement{}, false, fmt.Errorf("parse apple entitlement until sqlite row member_id=%q: %w", memberID, err)
	}
	return appleiapDomain.Entitlement{AccessUntil: accessUntil, Status: appleiapDomain.Status(statusRaw)}, true, nil
}

func (r *SQLiteRepository) ByMemberID(
	ctx context.Context, memberID string, now time.Time,
) (appleiapDomain.Entitlement, error) {
	entitlement, found, err := r.LatestEntitlement(ctx, memberID)
	if err != nil || !found {
		return appleiapDomain.Entitlement{}, err
	}
	if !hasAppleAccess(entitlement, now) {
		return appleiapDomain.Entitlement{}, nil
	}
	return entitlement, nil
}

func hasAppleAccess(e appleiapDomain.Entitlement, now time.Time) bool {
	if e.AccessUntil.IsZero() || !now.Before(e.AccessUntil) {
		return false
	}
	return e.Status == appleiapDomain.StatusActive || e.Status == appleiapDomain.StatusGracePeriod
}

func (r *SQLiteRepository) ByOriginalTransactionID(
	ctx context.Context, originalTransactionID string,
) (appleiapDomain.EntitlementRecord, error) {
	var record appleiapDomain.EntitlementRecord
	err := r.db.QueryRowContext(ctx, `
SELECT member_id, account_id, app_account_token, original_transaction_id
FROM apple_entitlements
WHERE original_transaction_id = ?
LIMIT 1`, originalTransactionID).Scan(
		&record.MemberID, &record.AccountID, &record.AppAccountToken, &record.OriginalTransactionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return appleiapDomain.EntitlementRecord{}, nil
	}
	if err != nil {
		return appleiapDomain.EntitlementRecord{}, fmt.Errorf(
			"select apple entitlement sqlite row original_transaction_id=%q: %w", originalTransactionID, err,
		)
	}
	return record, nil
}

func (r *SQLiteRepository) Apply(
	ctx context.Context, update appleiapDomain.EntitlementUpdate,
) (bool, error) {
	accessUntil := sql.NullString{}
	if !update.AccessUntil.IsZero() {
		accessUntil = sql.NullString{String: update.AccessUntil.UTC().Format(time.RFC3339), Valid: true}
	}

	eventOccurredAt := update.EventOccurredAt.UTC().Format(time.RFC3339)
	result, err := r.db.ExecContext(ctx, `
INSERT INTO apple_entitlements (
  member_id, account_id, app_account_token, original_transaction_id, transaction_id,
  product_id, entitlement_until, status, environment, event_occurred_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(original_transaction_id) DO UPDATE SET
  member_id = excluded.member_id,
  account_id = excluded.account_id,
  app_account_token = excluded.app_account_token,
  transaction_id = excluded.transaction_id,
  product_id = excluded.product_id,
  entitlement_until = excluded.entitlement_until,
  status = excluded.status,
  environment = excluded.environment,
  event_occurred_at = excluded.event_occurred_at
WHERE
  apple_entitlements.event_occurred_at IS NULL
  OR apple_entitlements.event_occurred_at = ''
  OR unixepoch(apple_entitlements.event_occurred_at) < unixepoch(excluded.event_occurred_at)`,
		update.MemberID,
		update.AccountID,
		update.AppAccountToken,
		update.OriginalTransactionID,
		update.TransactionID,
		update.ProductID,
		accessUntil,
		string(update.Status),
		update.Environment,
		eventOccurredAt,
	)
	if err != nil {
		return false, fmt.Errorf("apply apple entitlement sqlite row member_id=%q: %w", update.MemberID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read apple entitlement sqlite rows affected member_id=%q: %w", update.MemberID, err)
	}
	return rows > 0, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02 15:04:05", value)
}
