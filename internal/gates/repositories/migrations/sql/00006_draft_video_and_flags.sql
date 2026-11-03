-- +goose Up
-- synced_drafts is created by the draft repository's inline Init (the baseline); this runs after it.
ALTER TABLE synced_drafts ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE synced_drafts ADD COLUMN ai_voice INTEGER NOT NULL DEFAULT 0;
ALTER TABLE synced_drafts ADD COLUMN ai_realistic_shots INTEGER NOT NULL DEFAULT 0;
ALTER TABLE synced_drafts ADD COLUMN ai_music INTEGER NOT NULL DEFAULT 0;

CREATE TABLE bloomary_draft_videos (
  member_id TEXT NOT NULL,
  draft_id TEXT NOT NULL,
  master_key TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  bytes INTEGER NOT NULL,
  seconds REAL NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  captions_key TEXT NOT NULL DEFAULT '',
  cover_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(member_id, draft_id)
);

-- +goose Down
DROP TABLE bloomary_draft_videos;
ALTER TABLE synced_drafts DROP COLUMN ai_music;
ALTER TABLE synced_drafts DROP COLUMN ai_realistic_shots;
ALTER TABLE synced_drafts DROP COLUMN ai_voice;
ALTER TABLE synced_drafts DROP COLUMN title;
