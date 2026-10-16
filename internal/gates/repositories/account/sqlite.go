package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	perror "bloomary-api/internal/platform/error"
)

type SQLiteRepository struct {
	db *sql.DB
}

var _ accountDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("account sqlite repository: db is required")
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(
		ctx, `
CREATE TABLE IF NOT EXISTS linkedin_auth (
  member_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  email TEXT NOT NULL,
  picture_url TEXT NOT NULL,
  scope TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  connected_at TEXT NOT NULL,
  access_token TEXT NOT NULL DEFAULT ''
)`,
	)
	if err != nil {
		return fmt.Errorf("init linkedin_auth sqlite table: %w", err)
	}

	_, _ = r.db.ExecContext(ctx, `ALTER TABLE linkedin_auth ADD COLUMN access_token TEXT NOT NULL DEFAULT ''`)

	return nil
}

func (r *SQLiteRepository) Insert(ctx context.Context, token accountDomain.Token, profile accountDomain.Profile) error {
	connectedAt := time.Now().UTC()
	expiresAt := ""
	if token.ExpiresIn > 0 {
		expiresAt = connectedAt.Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	_, err := r.db.ExecContext(
		ctx, `
INSERT INTO linkedin_auth (
  member_id, name, email, picture_url, scope, expires_at, connected_at, access_token
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(member_id) DO UPDATE SET
  name = excluded.name,
  email = excluded.email,
  picture_url = excluded.picture_url,
  scope = excluded.scope,
  expires_at = excluded.expires_at,
  connected_at = excluded.connected_at,
  access_token = excluded.access_token`,
		profile.MemberID,
		profile.Name,
		profile.Email,
		profile.PictureURL,
		token.Scope,
		expiresAt,
		connectedAt.Format(time.RFC3339),
		token.AccessToken,
	)
	if err != nil {
		return perror.New(
			perror.ErrCodeStoreFailed,
			fmt.Errorf("error inserting to linkedin_auth: %w", err),
			fmt.Sprintf("member_id=%q", profile.MemberID),
		)
	}

	return nil
}

func (r *SQLiteRepository) ByMemberID(ctx context.Context, memberID string) (accountDomain.Profile, error) {
	var profile accountDomain.Profile
	err := r.db.QueryRowContext(
		ctx, `
SELECT member_id, name, email, picture_url
FROM linkedin_auth
WHERE member_id = ?`, memberID,
	).Scan(
		&profile.MemberID,
		&profile.Name,
		&profile.Email,
		&profile.PictureURL,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return accountDomain.Profile{}, accountDomain.ErrNotFound
	}

	if err != nil {
		return accountDomain.Profile{}, fmt.Errorf("select linkedin account sqlite row member_id=%q: %w", memberID, err)
	}

	return profile, nil
}

func (r *SQLiteRepository) UniqueMemberIDByEmail(ctx context.Context, email string) (
	accountDomain.EmailFallbackMatch, error,
) {
	email = strings.TrimSpace(email)
	if email == "" {
		return accountDomain.EmailFallbackMatch{}, nil
	}

	rows, err := r.db.QueryContext(
		ctx, `
SELECT member_id
FROM linkedin_auth
WHERE lower(email) = lower(?)
ORDER BY member_id
LIMIT 2`, email,
	)
	if err != nil {
		return accountDomain.EmailFallbackMatch{}, fmt.Errorf("select linkedin account sqlite rows by email: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	match := accountDomain.EmailFallbackMatch{}
	for rows.Next() {
		match.MatchCount++
		if match.MatchCount == 1 {
			if err := rows.Scan(&match.MemberID); err != nil {
				return accountDomain.EmailFallbackMatch{}, fmt.Errorf(
					"scan linkedin account sqlite row by email: %w", err,
				)
			}

			continue
		}

		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			return accountDomain.EmailFallbackMatch{}, fmt.Errorf(
				"scan linkedin account sqlite ambiguous row by email: %w", err,
			)
		}

		match.MemberID = ""
	}

	if err := rows.Err(); err != nil {
		return accountDomain.EmailFallbackMatch{}, fmt.Errorf("iterate linkedin account sqlite rows by email: %w", err)
	}

	return match, nil
}

func (r *SQLiteRepository) Delete(ctx context.Context, memberID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin linkedin account sqlite delete tx member_id=%q: %w", memberID, err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, `DELETE FROM linkedin_auth WHERE member_id = ?`, memberID); err != nil {
		return fmt.Errorf("delete linkedin account sqlite row member_id=%q: %w", memberID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit linkedin account sqlite delete tx member_id=%q: %w", memberID, err)
	}

	return nil
}

func (r *SQLiteRepository) AllActive(ctx context.Context) ([]accountDomain.TokenPair, error) {
	now := time.Now().UTC()
	rows, err := r.db.QueryContext(
		ctx, `
SELECT access_token, member_id, expires_at
FROM linkedin_auth
WHERE access_token != '' AND expires_at > ?`, now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("select active linkedin tokens: %w", err)
	}
	defer rows.Close()

	var pairs []accountDomain.TokenPair
	for rows.Next() {
		var pair accountDomain.TokenPair
		var expiresAt string
		if err := rows.Scan(&pair.AccessToken, &pair.MemberID, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan active linkedin token row: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
			pair.ExpiresAt = t
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active linkedin token rows: %w", err)
	}

	return pairs, nil
}

func (r *SQLiteRepository) ActiveByMemberID(ctx context.Context, memberID string) (accountDomain.TokenPair, error) {
	var pair accountDomain.TokenPair
	var expiresAt string
	err := r.db.QueryRowContext(ctx, `SELECT access_token,member_id,expires_at FROM linkedin_auth WHERE member_id=? AND access_token!=''`, memberID).Scan(&pair.AccessToken, &pair.MemberID, &expiresAt)
	if err != nil {
		return accountDomain.TokenPair{}, err
	}
	pair.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
	return pair, err
}
