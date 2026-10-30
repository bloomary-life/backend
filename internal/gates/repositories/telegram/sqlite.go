package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	telegramDomain "bloomary-api/internal/domain/telegram"
)

type SQLiteRepository struct{ db *sql.DB }

var _ telegramDomain.LinkRepository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("telegram sqlite repository: db is required")
	}

	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS telegram_links (
			telegram_user_id INTEGER PRIMARY KEY, member_id TEXT NOT NULL UNIQUE, chat_id INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '', linked_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS telegram_link_attempts (
			nonce TEXT PRIMARY KEY, member_id TEXT NOT NULL, number INTEGER NOT NULL, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS telegram_updates (update_id INTEGER PRIMARY KEY, received_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_updates_received ON telegram_updates(received_at)`,
	} {
		if _, err := db.Exec(query); err != nil {
			return nil, fmt.Errorf("create telegram tables: %w", err)
		}
	}

	return &SQLiteRepository{db: db}, nil
}

// StartAttempt keeps one open attempt per member and drops expired ones.
func (r *SQLiteRepository) StartAttempt(ctx context.Context, attempt telegramDomain.LinkAttempt) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_link_attempts WHERE member_id=? OR expires_at<?`,
		attempt.MemberID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("clear telegram link attempts: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_link_attempts(nonce,member_id,number,expires_at) VALUES(?,?,?,?)`,
		attempt.Nonce, attempt.MemberID, attempt.Number, attempt.ExpiresAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("insert telegram link attempt: %w", err)
	}

	return tx.Commit()
}

func (r *SQLiteRepository) AttemptByNonce(ctx context.Context, nonce string, now time.Time) (telegramDomain.LinkAttempt, error) {
	return scanAttempt(r.db.QueryRowContext(ctx, `SELECT nonce,member_id,number,expires_at FROM telegram_link_attempts WHERE nonce=? AND expires_at>=?`,
		nonce, now.UTC().Format(time.RFC3339Nano)))
}

func (r *SQLiteRepository) ConsumeAttempt(ctx context.Context, nonce string, now time.Time) (telegramDomain.LinkAttempt, error) {
	attempt, err := scanAttempt(r.db.QueryRowContext(ctx, `DELETE FROM telegram_link_attempts WHERE nonce=? RETURNING nonce,member_id,number,expires_at`, nonce))
	if err != nil {
		return telegramDomain.LinkAttempt{}, err
	}

	if attempt.ExpiresAt.Before(now) {
		return telegramDomain.LinkAttempt{}, telegramDomain.ErrLinkNotFound
	}

	return attempt, nil
}

// SaveLink replaces any earlier link of the same member or the same Telegram user.
func (r *SQLiteRepository) SaveLink(ctx context.Context, link telegramDomain.Link) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE member_id=? OR telegram_user_id=?`, link.MemberID, link.TelegramUserID); err != nil {
		return fmt.Errorf("clear telegram link: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_links(telegram_user_id,member_id,chat_id,username,linked_at) VALUES(?,?,?,?,?)`,
		link.TelegramUserID, link.MemberID, link.ChatID, link.Username, link.LinkedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("insert telegram link: %w", err)
	}

	return tx.Commit()
}

func (r *SQLiteRepository) ByTelegramUser(ctx context.Context, telegramUserID int64) (telegramDomain.Link, error) {
	return scanLink(r.db.QueryRowContext(ctx, `SELECT telegram_user_id,member_id,chat_id,username,linked_at FROM telegram_links WHERE telegram_user_id=?`, telegramUserID))
}

func (r *SQLiteRepository) ByMemberID(ctx context.Context, memberID string) (telegramDomain.Link, error) {
	return scanLink(r.db.QueryRowContext(ctx, `SELECT telegram_user_id,member_id,chat_id,username,linked_at FROM telegram_links WHERE member_id=?`, memberID))
}

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM telegram_link_attempts WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete telegram link attempts: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, `DELETE FROM telegram_links WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete telegram link: %w", err)
	}

	return nil
}

type scanner interface{ Scan(...any) error }

func scanAttempt(row scanner) (telegramDomain.LinkAttempt, error) {
	var attempt telegramDomain.LinkAttempt
	var expiresAt string
	err := row.Scan(&attempt.Nonce, &attempt.MemberID, &attempt.Number, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return telegramDomain.LinkAttempt{}, telegramDomain.ErrLinkNotFound
	}

	if err != nil {
		return telegramDomain.LinkAttempt{}, err
	}

	attempt.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	return attempt, err
}

func scanLink(row scanner) (telegramDomain.Link, error) {
	var link telegramDomain.Link
	var linkedAt string
	err := row.Scan(&link.TelegramUserID, &link.MemberID, &link.ChatID, &link.Username, &linkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return telegramDomain.Link{}, telegramDomain.ErrNotLinked
	}

	if err != nil {
		return telegramDomain.Link{}, err
	}

	link.LinkedAt, err = time.Parse(time.RFC3339Nano, linkedAt)
	return link, err
}

// MarkUpdateSeen inserts the update ID and forgets IDs older than UpdateMemory.
func (r *SQLiteRepository) MarkUpdateSeen(ctx context.Context, updateID int64, at time.Time) (bool, error) {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE received_at<?`, at.Add(-telegramDomain.UpdateMemory).UTC().Format(time.RFC3339Nano)); err != nil {
		return false, fmt.Errorf("prune telegram updates: %w", err)
	}

	result, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO telegram_updates(update_id,received_at) VALUES(?,?)`, updateID, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("record telegram update: %w", err)
	}

	n, err := result.RowsAffected()
	return n == 1, err
}
