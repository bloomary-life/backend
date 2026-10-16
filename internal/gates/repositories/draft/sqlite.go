package draft

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
)

type SQLiteRepository struct {
	db *sql.DB
}

var _ draftDomain.Repository = (*SQLiteRepository)(nil)

func NewSQLiteRepository(db *sql.DB) (*SQLiteRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("draft sqlite repository: db is required")
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(
		ctx, `
CREATE TABLE IF NOT EXISTS synced_drafts (
  member_id TEXT NOT NULL,
  draft_id TEXT NOT NULL,
  body TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY(member_id, draft_id)
)`,
	)
	if err != nil {
		return fmt.Errorf("init synced drafts sqlite table: %w", err)
	}
	_, _ = r.db.ExecContext(ctx, `ALTER TABLE synced_drafts ADD COLUMN status TEXT NOT NULL DEFAULT 'draft'`)
	_, err = r.db.ExecContext(
		ctx, `
CREATE TABLE IF NOT EXISTS synced_draft_images (
  member_id TEXT NOT NULL,
  draft_id TEXT NOT NULL,
  image_id TEXT NOT NULL,
  object_key TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  checksum_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY(member_id, draft_id, image_id),
  UNIQUE(member_id, draft_id)
)`,
	)
	if err != nil {
		return fmt.Errorf("init synced draft images sqlite table: %w", err)
	}

	return nil
}

func (r *SQLiteRepository) ByID(ctx context.Context, memberID, draftID string) (draftDomain.Draft, error) {
	var draft draftDomain.Draft
	var createdAt, updatedAt string
	var imageID, objectKey, mimeType, checksum sql.NullString
	var sizeBytes sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT d.draft_id,d.body,d.status,d.created_at,d.updated_at,i.image_id,i.object_key,i.mime_type,i.size_bytes,i.checksum_sha256 FROM synced_drafts d LEFT JOIN synced_draft_images i ON i.member_id=d.member_id AND i.draft_id=d.draft_id WHERE d.member_id=? AND d.draft_id=?`, memberID, draftID).Scan(&draft.DraftID, &draft.Body, &draft.Status, &createdAt, &updatedAt, &imageID, &objectKey, &mimeType, &sizeBytes, &checksum)
	if err != nil {
		return draftDomain.Draft{}, err
	}
	draft.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	draft.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if imageID.Valid {
		draft.Image = &draftDomain.DraftImage{ImageID: imageID.String, DraftID: draftID, ObjectKey: objectKey.String, MimeType: mimeType.String, SizeBytes: sizeBytes.Int64, ChecksumSHA256: checksum.String}
	}
	return draft, nil
}

func (r *SQLiteRepository) ListByMemberID(ctx context.Context, memberID string) ([]draftDomain.Draft, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin list drafts member_id=%q: %w", memberID, err)
	}
	defer func() { _ = tx.Rollback() }()
	drafts, err := listDrafts(ctx, tx, memberID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list drafts member_id=%q: %w", memberID, err)
	}
	return drafts, nil
}

func (r *SQLiteRepository) CreateIfAbsent(ctx context.Context, memberID, draftID, body string, maxActive int, updatedAt time.Time) (draftDomain.Draft, bool, error) {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	result, err := r.db.ExecContext(ctx, `
INSERT INTO synced_drafts(member_id,draft_id,body,updated_at)
SELECT ?,?,?,? WHERE (SELECT COUNT(*) FROM synced_drafts WHERE member_id=?) < ?
ON CONFLICT(member_id,draft_id) DO NOTHING`,
		memberID, draftID, body, updatedAt.UTC().Format(time.RFC3339Nano), memberID, maxActive)
	if err != nil {
		return draftDomain.Draft{}, false, fmt.Errorf("create MCP draft member_id=%q draft_id=%q: %w", memberID, draftID, err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return draftDomain.Draft{}, false, err
	}
	if created == 0 {
		draft, err := r.ByID(ctx, memberID, draftID)
		if err == sql.ErrNoRows {
			return draftDomain.Draft{}, false, draftDomain.ErrTooManyDrafts
		}
		return draft, false, err
	}
	draft, err := r.ByID(ctx, memberID, draftID)
	return draft, true, err
}

func (r *SQLiteRepository) UpdateUnpublished(ctx context.Context, memberID, draftID, body string, updatedAt time.Time) (draftDomain.Draft, error) {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	result, err := r.db.ExecContext(ctx, `UPDATE synced_drafts SET body=?,updated_at=? WHERE member_id=? AND draft_id=? AND status='draft'`, body, updatedAt.UTC().Format(time.RFC3339Nano), memberID, draftID)
	if err != nil {
		return draftDomain.Draft{}, fmt.Errorf("update MCP draft member_id=%q draft_id=%q: %w", memberID, draftID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return draftDomain.Draft{}, err
	}
	if updated == 0 {
		return r.ByID(ctx, memberID, draftID)
	}
	return r.ByID(ctx, memberID, draftID)
}

func (r *SQLiteRepository) MarkPublished(ctx context.Context, memberID, draftID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE synced_drafts SET status='published' WHERE member_id=? AND draft_id=?`, memberID, draftID)
	return err
}

func (r *SQLiteRepository) Sync(
	ctx context.Context, memberID string, upserts []draftDomain.Upsert, deletes []string, maxActive int,
) ([]draftDomain.Draft, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin synced drafts tx member_id=%q: %w", memberID, err)
	}

	defer func() {
		_ = tx.Rollback()
	}()
	for _, draftID := range deletes {
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_posts SET status='deleted_draft', updated_at=? WHERE member_id=? AND draft_id=? AND status IN ('pending','retry')`, time.Now().UTC().Format(time.RFC3339Nano), memberID, draftID); err != nil {
			return nil, fmt.Errorf("mark schedules for deleted draft: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM synced_draft_images WHERE member_id = ? AND draft_id = ?`, memberID, draftID); err != nil {
			return nil, fmt.Errorf("delete synced draft image metadata member_id=%q draft_id=%q: %w", memberID, draftID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM synced_drafts WHERE member_id = ? AND draft_id = ?`, memberID, draftID); err != nil {
			return nil, fmt.Errorf("delete synced draft member_id=%q draft_id=%q: %w", memberID, draftID, err)
		}
	}

	for _, upsert := range upserts {
		if err := upsertDraft(ctx, tx, memberID, upsert, maxActive); err != nil {
			return nil, err
		}
		if upsert.Image != nil {
			if err := upsertDraftImage(ctx, tx, memberID, upsert.DraftID, *upsert.Image); err != nil {
				return nil, err
			}
		} else if upsert.RemoveImage {
			if _, err := tx.ExecContext(ctx, `DELETE FROM synced_draft_images WHERE member_id = ? AND draft_id = ?`, memberID, upsert.DraftID); err != nil {
				return nil, fmt.Errorf("remove synced draft image member_id=%q draft_id=%q: %w", memberID, upsert.DraftID, err)
			}
		}
	}

	drafts, err := listDrafts(ctx, tx, memberID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit synced drafts tx member_id=%q: %w", memberID, err)
	}

	return drafts, nil
}

func (r *SQLiteRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete synced drafts member_id=%q: %w", memberID, err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, `DELETE FROM synced_draft_images WHERE member_id = ?`, memberID); err != nil {
		return fmt.Errorf("delete synced draft image metadata member_id=%q: %w", memberID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM synced_drafts WHERE member_id = ?`, memberID); err != nil {
		return fmt.Errorf("delete synced drafts member_id=%q: %w", memberID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete synced drafts member_id=%q: %w", memberID, err)
	}

	return nil
}

func upsertDraft(ctx context.Context, tx *sql.Tx, memberID string, draft draftDomain.Upsert, maxActive int) error {
	updatedAt := draft.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	updatedAt = updatedAt.UTC()

	result, err := tx.ExecContext(
		ctx, `
INSERT INTO synced_drafts (member_id, draft_id, body, updated_at)
SELECT ?, ?, ?, ?
WHERE
  EXISTS (SELECT 1 FROM synced_drafts WHERE member_id = ? AND draft_id = ?)
  OR (SELECT COUNT(*) FROM synced_drafts WHERE member_id = ?) < ?
ON CONFLICT(member_id, draft_id) DO UPDATE SET
  body = CASE
    WHEN julianday(excluded.updated_at) >= julianday(synced_drafts.updated_at) THEN excluded.body
    ELSE synced_drafts.body
  END,
  updated_at = CASE
    WHEN julianday(excluded.updated_at) >= julianday(synced_drafts.updated_at) THEN excluded.updated_at
    ELSE synced_drafts.updated_at
  END`,
		memberID, draft.DraftID, draft.Body, updatedAt.Format(time.RFC3339Nano),
		memberID, draft.DraftID,
		memberID, maxActive,
	)
	if err != nil {
		return fmt.Errorf("upsert synced draft member_id=%q draft_id=%q: %w", memberID, draft.DraftID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read synced draft rows affected member_id=%q draft_id=%q: %w", memberID, draft.DraftID, err)
	}

	if affected == 0 {
		return draftDomain.ErrTooManyDrafts
	}

	return nil
}

func upsertDraftImage(ctx context.Context, tx *sql.Tx, memberID, draftID string, image draftDomain.UpsertImage) error {
	_, err := tx.ExecContext(
		ctx, `
INSERT INTO synced_draft_images (
  member_id, draft_id, image_id, object_key, mime_type, size_bytes, checksum_sha256
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(member_id, draft_id) DO UPDATE SET
  image_id = excluded.image_id,
  object_key = excluded.object_key,
  mime_type = excluded.mime_type,
  size_bytes = excluded.size_bytes,
  checksum_sha256 = excluded.checksum_sha256`,
		memberID, draftID, image.ImageID, image.ObjectKey, image.MimeType, image.SizeBytes, image.ChecksumSHA256,
	)
	if err != nil {
		return fmt.Errorf("upsert synced draft image member_id=%q draft_id=%q image_id=%q: %w", memberID, draftID, image.ImageID, err)
	}

	return nil
}

func listDrafts(ctx context.Context, tx *sql.Tx, memberID string) ([]draftDomain.Draft, error) {
	rows, err := tx.QueryContext(
		ctx, `
SELECT
  d.draft_id, d.body, d.status, d.created_at, d.updated_at,
  i.image_id, i.object_key, i.mime_type, i.size_bytes, i.checksum_sha256
FROM synced_drafts d
LEFT JOIN synced_draft_images i ON i.member_id = d.member_id AND i.draft_id = d.draft_id
WHERE d.member_id = ?
ORDER BY d.updated_at DESC, d.draft_id`, memberID,
	)
	if err != nil {
		return nil, fmt.Errorf("list synced drafts member_id=%q: %w", memberID, err)
	}

	defer func() {
		_ = rows.Close()
	}()
	var drafts []draftDomain.Draft
	for rows.Next() {
		var draft draftDomain.Draft
		var createdAt, updatedAt string
		var imageID, objectKey, mimeType, checksum sql.NullString
		var sizeBytes sql.NullInt64
		if err := rows.Scan(
			&draft.DraftID, &draft.Body, &draft.Status, &createdAt, &updatedAt,
			&imageID, &objectKey, &mimeType, &sizeBytes, &checksum,
		); err != nil {
			return nil, fmt.Errorf("scan synced draft member_id=%q: %w", memberID, err)
		}

		parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse synced draft created_at member_id=%q draft_id=%q: %w", memberID, draft.DraftID, err)
		}

		parsedUpdatedAt, err := time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse synced draft updated_at member_id=%q draft_id=%q: %w", memberID, draft.DraftID, err)
		}

		draft.CreatedAt = parsedCreatedAt
		draft.UpdatedAt = parsedUpdatedAt
		if imageID.Valid {
			draft.Image = &draftDomain.DraftImage{
				ImageID:        imageID.String,
				DraftID:        draft.DraftID,
				ObjectKey:      objectKey.String,
				MimeType:       mimeType.String,
				SizeBytes:      sizeBytes.Int64,
				ChecksumSHA256: checksum.String,
			}
		}
		drafts = append(drafts, draft)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate synced drafts member_id=%q: %w", memberID, err)
	}

	return drafts, nil
}
