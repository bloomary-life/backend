-- +goose Up
CREATE TABLE bloomary_publications (
  id TEXT PRIMARY KEY,
  member_id TEXT NOT NULL,
  draft_id TEXT NOT NULL,
  linkedin_schedule_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  approved_at TEXT,
  closed_at TEXT
);
-- One open publication per draft.
CREATE UNIQUE INDEX idx_bloomary_publications_open_draft ON bloomary_publications(member_id, draft_id) WHERE closed_at IS NULL;

CREATE TABLE bloomary_deliveries (
  id TEXT PRIMARY KEY,
  publication_id TEXT NOT NULL REFERENCES bloomary_publications(id) ON DELETE CASCADE,
  member_id TEXT NOT NULL,
  destination_id TEXT NOT NULL,
  network TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  hashtags TEXT NOT NULL DEFAULT '[]',
  media_key TEXT NOT NULL DEFAULT '',
  media_sha256 TEXT NOT NULL DEFAULT '',
  ai_label INTEGER NOT NULL DEFAULT 0,
  youtube_privacy TEXT NOT NULL DEFAULT '',
  youtube_made_for_kids INTEGER,
  publish_at TEXT NOT NULL,
  status TEXT NOT NULL,
  skip_reason TEXT NOT NULL DEFAULT '',
  warnings TEXT NOT NULL DEFAULT '[]',
  progress_key_id TEXT NOT NULL DEFAULT '',
  progress_schema INTEGER NOT NULL DEFAULT 0,
  progress_data BLOB,
  external_post_id TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  last_error_code TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_bloomary_deliveries_due ON bloomary_deliveries(status, publish_at) WHERE status = 'scheduled';
CREATE INDEX idx_bloomary_deliveries_publication_id ON bloomary_deliveries(publication_id);
CREATE INDEX idx_bloomary_deliveries_member_network ON bloomary_deliveries(member_id, network, publish_at);

-- +goose Down
DROP TABLE bloomary_deliveries;
DROP TABLE bloomary_publications;
