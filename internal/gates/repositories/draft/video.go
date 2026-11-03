package draft

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

func (r *SQLiteRepository) SetVideo(ctx context.Context, memberID, draftID string, v draftDomain.Video) ([]string, error) {
	replaced, err := r.videoKeys(ctx, memberID, []string{draftID})
	if err != nil {
		return nil, err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO bloomary_draft_videos(member_id,draft_id,master_key,sha256,bytes,seconds,width,height,captions_key,cover_key,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(member_id,draft_id) DO UPDATE SET master_key=excluded.master_key,sha256=excluded.sha256,bytes=excluded.bytes,
seconds=excluded.seconds,width=excluded.width,height=excluded.height,captions_key=excluded.captions_key,cover_key=excluded.cover_key,created_at=excluded.created_at`,
		memberID, draftID, v.MasterKey, v.SHA256, v.Bytes, v.Seconds, v.Width, v.Height, v.CaptionsKey, v.CoverKey, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("set draft video draft_id=%q: %w", draftID, err)
	}
	return replaced, nil
}

func (r *SQLiteRepository) DeleteVideos(ctx context.Context, memberID string, draftIDs []string) ([]string, error) {
	keys, err := r.videoKeys(ctx, memberID, draftIDs)
	if err != nil {
		return nil, err
	}
	for _, draftID := range draftIDs {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_draft_videos WHERE member_id=? AND draft_id=?`, memberID, draftID); err != nil {
			return nil, fmt.Errorf("delete draft video draft_id=%q: %w", draftID, err)
		}
	}
	return keys, nil
}

func (r *SQLiteRepository) videoKeys(ctx context.Context, memberID string, draftIDs []string) ([]string, error) {
	keys := []string{}
	for _, draftID := range draftIDs {
		var master, captions, cover string
		err := r.db.QueryRowContext(ctx, `SELECT master_key,captions_key,cover_key FROM bloomary_draft_videos WHERE member_id=? AND draft_id=?`, memberID, draftID).
			Scan(&master, &captions, &cover)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("draft video keys draft_id=%q: %w", draftID, err)
		}
		for _, key := range []string{master, captions, cover} {
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}
