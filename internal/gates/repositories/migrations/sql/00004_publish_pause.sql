-- +goose Up
-- Set only through the ops bot; network 'all' pauses every network, LinkedIn included.
CREATE TABLE bloomary_publish_pause (
  network TEXT PRIMARY KEY,
  paused INTEGER NOT NULL,
  changed_by TEXT NOT NULL,
  changed_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE bloomary_publish_pause;
