-- +goose Up
CREATE TABLE bloomary_connections (
  id TEXT PRIMARY KEY,
  member_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  app_owner TEXT NOT NULL,
  account_id TEXT NOT NULL,
  sealed_key_id TEXT NOT NULL,
  sealed_schema INTEGER NOT NULL,
  sealed_data BLOB NOT NULL,
  health TEXT NOT NULL,
  expires_at TEXT,
  refresh_locked_until TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(member_id, provider, account_id)
);
CREATE INDEX idx_bloomary_connections_member_id ON bloomary_connections(member_id);

CREATE TABLE bloomary_destinations (
  id TEXT PRIMARY KEY,
  member_id TEXT NOT NULL,
  connection_id TEXT NOT NULL REFERENCES bloomary_connections(id) ON DELETE CASCADE,
  network TEXT NOT NULL,
  account_id TEXT NOT NULL,
  account_name TEXT NOT NULL,
  sealed_key_id TEXT NOT NULL DEFAULT '',
  sealed_schema INTEGER NOT NULL DEFAULT 0,
  sealed_data BLOB,
  audience_zone TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(member_id, network, account_id)
);
CREATE INDEX idx_bloomary_destinations_member_id ON bloomary_destinations(member_id);

-- +goose Down
DROP TABLE bloomary_destinations;
DROP TABLE bloomary_connections;
