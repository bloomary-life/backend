package draft

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	draftDomain "bloomary-api/internal/domain/draft"
)

var _ draftDomain.ExtrasRepository = (*SQLiteRepository)(nil)

// Extras reads the draft's title, AI flags and video (columns and table from Goose migration 00006).
func (r *SQLiteRepository) Extras(ctx context.Context, memberID, draftID string) (draftDomain.Extras, error) {
	var extras draftDomain.Extras
	err := r.db.QueryRowContext(ctx, `SELECT title,ai_voice,ai_realistic_shots,ai_music FROM synced_drafts WHERE member_id=? AND draft_id=?`, memberID, draftID).
		Scan(&extras.Title, &extras.AIVoice, &extras.AIRealisticShots, &extras.AIMusic)
	if err != nil {
		return draftDomain.Extras{}, fmt.Errorf("draft extras draft_id=%q: %w", draftID, err)
	}
	var video draftDomain.Video
	err = r.db.QueryRowContext(ctx, `SELECT master_key,sha256,captions_key,cover_key,bytes,seconds,width,height FROM bloomary_draft_videos WHERE member_id=? AND draft_id=?`, memberID, draftID).
		Scan(&video.MasterKey, &video.SHA256, &video.CaptionsKey, &video.CoverKey, &video.Bytes, &video.Seconds, &video.Width, &video.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return extras, nil
	}
	if err != nil {
		return draftDomain.Extras{}, fmt.Errorf("draft video draft_id=%q: %w", draftID, err)
	}
	extras.Video = &video
	return extras, nil
}

func (r *SQLiteRepository) VideoKeys(ctx context.Context, memberID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT master_key,captions_key,cover_key FROM bloomary_draft_videos WHERE member_id=?`, memberID)
	if err != nil {
		return nil, fmt.Errorf("draft video keys: %w", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var master, captions, cover string
		if err := rows.Scan(&master, &captions, &cover); err != nil {
			return nil, err
		}
		for _, key := range []string{master, captions, cover} {
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys, rows.Err()
}

func (r *SQLiteRepository) DeleteVideosByMemberID(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_draft_videos WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete draft videos: %w", err)
	}
	return nil
}
