package trial

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	trialDomain "bloomary-api/internal/domain/trial"
)

type SQLiteRepository struct {
	db               *sql.DB
	freeTierDuration time.Duration
	hmacSecret       string
}

var _ trialDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB, freeTierDays int, hmacSecret string) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("trial sqlite repository: db is required")
	}
	if freeTierDays <= 0 {
		return nil, fmt.Errorf("trial sqlite repository: free tier days must be positive")
	}
	if strings.TrimSpace(hmacSecret) == "" {
		return nil, fmt.Errorf("trial sqlite repository: hmac secret is required")
	}

	return &SQLiteRepository{
		db:               db,
		freeTierDuration: time.Duration(freeTierDays) * 24 * time.Hour,
		hmacSecret:       hmacSecret,
	}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(
		ctx, `
CREATE TABLE IF NOT EXISTS trial_history (
  pseudonymous_identifier TEXT PRIMARY KEY,
  trial_used_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
)`,
	)
	if err != nil {
		return fmt.Errorf("init trial_history sqlite table: %w", err)
	}

	return nil
}

func (r *SQLiteRepository) Ensure(ctx context.Context, email, memberID string, now time.Time) (trialDomain.Record, error) {
	identifier := r.identifier(email, memberID)
	if identifier == "" {
		return trialDomain.Record{}, fmt.Errorf("empty trial pseudonymous identifier")
	}

	_, err := r.db.ExecContext(
		ctx, `
INSERT INTO trial_history (pseudonymous_identifier, trial_used_at, expires_at)
VALUES (?, ?, ?)
ON CONFLICT(pseudonymous_identifier) DO NOTHING`,
		identifier,
		now.Format(time.RFC3339),
		now.Add(r.freeTierDuration).Format(time.RFC3339),
	)
	if err != nil {
		return trialDomain.Record{}, fmt.Errorf("ensure trial history sqlite row: %w", err)
	}

	return r.byIdentifier(ctx, identifier)
}

func (r *SQLiteRepository) identifier(email, memberID string) string {
	source := strings.ToLower(strings.TrimSpace(email))
	if source == "" {
		source = strings.TrimSpace(memberID)
	}

	if source == "" || r.hmacSecret == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(r.hmacSecret))
	_, _ = mac.Write([]byte(source))
	return hex.EncodeToString(mac.Sum(nil))
}

func (r *SQLiteRepository) byIdentifier(ctx context.Context, identifier string) (trialDomain.Record, error) {
	var usedAtRaw string
	var expiresAtRaw string
	err := r.db.QueryRowContext(
		ctx, `
SELECT trial_used_at, expires_at
FROM trial_history
WHERE pseudonymous_identifier = ?`, identifier,
	).Scan(&usedAtRaw, &expiresAtRaw)
	if err != nil {
		return trialDomain.Record{}, fmt.Errorf("select trial history sqlite row: %w", err)
	}

	usedAt, err := time.Parse(time.RFC3339, usedAtRaw)
	if err != nil {
		return trialDomain.Record{}, fmt.Errorf("parse trial used at sqlite row: %w", err)
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtRaw)
	if err != nil {
		return trialDomain.Record{}, fmt.Errorf("parse trial expires at sqlite row: %w", err)
	}

	return trialDomain.Record{PseudonymousIdentifier: identifier, TrialUsedAt: usedAt, ExpiresAt: expiresAt}, nil
}
