-- +goose Up
-- Append-only (security.md §6). Text is stored only as SHA-256.
CREATE TABLE bloomary_crosspost_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  member_id TEXT NOT NULL,
  member_stripped INTEGER NOT NULL DEFAULT 0,
  action TEXT NOT NULL,
  record_ids TEXT NOT NULL DEFAULT '',
  media_sha256 TEXT NOT NULL DEFAULT '',
  text_sha256 TEXT NOT NULL DEFAULT '',
  network_post_id TEXT NOT NULL DEFAULT '',
  telegram_user_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_bloomary_crosspost_audit_member_id ON bloomary_crosspost_audit(member_id);

-- The only update allowed: the account-deletion strip, once per row (member ID → keyed HMAC; post ID,
-- text hash and Telegram user ID removed; everything else unchanged).
-- +goose StatementBegin
CREATE TRIGGER bloomary_crosspost_audit_no_update BEFORE UPDATE ON bloomary_crosspost_audit
WHEN NOT (
  OLD.member_stripped = 0 AND NEW.member_stripped = 1
  AND NEW.text_sha256 = '' AND NEW.network_post_id = '' AND NEW.telegram_user_id = 0
  AND NEW.id = OLD.id AND NEW.action = OLD.action AND NEW.record_ids = OLD.record_ids
  AND NEW.media_sha256 = OLD.media_sha256 AND NEW.created_at = OLD.created_at
)
BEGIN
  SELECT RAISE(ABORT, 'bloomary_crosspost_audit is append-only');
END;
-- +goose StatementEnd

-- The only delete allowed: the purge of rows older than 1 year.
-- +goose StatementBegin
CREATE TRIGGER bloomary_crosspost_audit_no_delete BEFORE DELETE ON bloomary_crosspost_audit
WHEN OLD.created_at >= strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 year')
BEGIN
  SELECT RAISE(ABORT, 'bloomary_crosspost_audit rows are kept for 1 year');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER bloomary_crosspost_audit_no_delete;
DROP TRIGGER bloomary_crosspost_audit_no_update;
DROP TABLE bloomary_crosspost_audit;
