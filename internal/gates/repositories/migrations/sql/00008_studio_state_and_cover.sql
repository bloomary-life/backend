-- +goose Up
-- The video studio's publication and remembered choices; the producer job's first claim (time limit); a delivery's
-- frozen cover image (Instagram cover_url).
ALTER TABLE bloomary_video_requests ADD COLUMN publication_id TEXT NOT NULL DEFAULT '';
ALTER TABLE bloomary_video_requests ADD COLUMN state TEXT NOT NULL DEFAULT '{}';
CREATE INDEX idx_bloomary_video_requests_status ON bloomary_video_requests(status, created_at);
ALTER TABLE bloomary_producer_jobs ADD COLUMN claimed_at TEXT;
ALTER TABLE bloomary_deliveries ADD COLUMN cover_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE bloomary_deliveries DROP COLUMN cover_key;
ALTER TABLE bloomary_producer_jobs DROP COLUMN claimed_at;
DROP INDEX idx_bloomary_video_requests_status;
ALTER TABLE bloomary_video_requests DROP COLUMN state;
ALTER TABLE bloomary_video_requests DROP COLUMN publication_id;
