-- +goose Up
CREATE TABLE bloomary_video_requests (
  id TEXT PRIMARY KEY,
  member_id TEXT NOT NULL,
  idea TEXT NOT NULL,
  scenario TEXT NOT NULL DEFAULT '{}',
  voice_over_key TEXT NOT NULL DEFAULT '',
  render TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL,
  messages TEXT NOT NULL DEFAULT '{}',
  chat_id INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_bloomary_video_requests_member_id ON bloomary_video_requests(member_id);
CREATE INDEX idx_bloomary_video_requests_chat_id ON bloomary_video_requests(chat_id);

CREATE TABLE bloomary_producer_jobs (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL REFERENCES bloomary_video_requests(id) ON DELETE CASCADE,
  member_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  input TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'waiting',
  lease_token_sha256 TEXT NOT NULL DEFAULT '',
  lease_until TEXT,
  cancelled INTEGER NOT NULL DEFAULT 0,
  failure TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_bloomary_producer_jobs_waiting ON bloomary_producer_jobs(status, created_at);
CREATE INDEX idx_bloomary_producer_jobs_member_id ON bloomary_producer_jobs(member_id);

-- When the producer last called claim (one row).
CREATE TABLE bloomary_producer_seen (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  seen_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE bloomary_producer_seen;
DROP TABLE bloomary_producer_jobs;
DROP TABLE bloomary_video_requests;
