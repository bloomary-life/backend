-- +goose Up
-- A publication's LinkedIn part: its proposed time and, after approval, the scheduling refusal (the post itself is a
-- scheduled_posts row, linked by bloomary_publications.linkedin_schedule_id).
CREATE TABLE bloomary_publication_linkedin (
  publication_id TEXT PRIMARY KEY REFERENCES bloomary_publications(id) ON DELETE CASCADE,
  member_id TEXT NOT NULL,
  proposed_at TEXT NOT NULL,
  skip_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX idx_bloomary_publication_linkedin_member_id ON bloomary_publication_linkedin(member_id);

-- +goose Down
DROP TABLE bloomary_publication_linkedin;
