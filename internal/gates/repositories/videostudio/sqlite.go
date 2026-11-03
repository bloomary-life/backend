// Package videostudio persists video requests and the producer job queue (tables from Goose migrations).
package videostudio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

const timeFormat = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(timeFormat, value.String)
	return t
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return videostudioDomain.ErrNotFound
	}
	return err
}

// RequestRepository implements videostudio.VideoRequestRepository.
type RequestRepository struct{ db *sql.DB }

var _ videostudioDomain.VideoRequestRepository = (*RequestRepository)(nil)

func NewRequestRepository(db *sql.DB) (*RequestRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("video request repository: db is required")
	}
	return &RequestRepository{db: db}, nil
}

const requestColumns = `id,member_id,idea,scenario,voice_over_key,render,status,messages,publication_id,state,version,created_at,updated_at`

// Create stores the request at version 1.
func (r *RequestRepository) Create(ctx context.Context, request videostudioDomain.VideoRequest) error {
	scenario, render, messages, state, err := encodeRequest(request)
	if err != nil {
		return err
	}
	now := formatTime(time.Now())
	_, err = r.db.ExecContext(ctx, `INSERT INTO bloomary_video_requests(id,member_id,idea,scenario,voice_over_key,render,status,messages,chat_id,publication_id,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		request.ID, request.MemberID, request.Idea, scenario, request.VoiceOverKey, render, request.Status, messages, request.Messages.ChatID, request.PublicationID, state, now, now)
	if err != nil {
		return fmt.Errorf("insert video request id=%q: %w", request.ID, err)
	}
	return nil
}

func (r *RequestRepository) Save(ctx context.Context, request videostudioDomain.VideoRequest) error {
	scenario, render, messages, state, err := encodeRequest(request)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE bloomary_video_requests SET idea=?,scenario=?,voice_over_key=?,render=?,status=?,messages=?,chat_id=?,publication_id=?,state=?,version=version+1,updated_at=? WHERE id=? AND member_id=? AND version=?`,
		request.Idea, scenario, request.VoiceOverKey, render, request.Status, messages, request.Messages.ChatID, request.PublicationID, state, formatTime(time.Now()), request.ID, request.MemberID, request.Version)
	if err != nil {
		return fmt.Errorf("update video request id=%q: %w", request.ID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return videostudioDomain.ErrVersionConflict
	}
	return nil
}

func (r *RequestRepository) ByID(ctx context.Context, memberID, id string) (videostudioDomain.VideoRequest, error) {
	return scanRequest(r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM bloomary_video_requests WHERE member_id=? AND id=?`, memberID, id))
}

// ByMessage finds the request that sent this Telegram message in this chat (newest first).
func (r *RequestRepository) ByMessage(ctx context.Context, chatID, messageID int64) (videostudioDomain.VideoRequest, error) {
	return scanRequest(r.db.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM bloomary_video_requests
WHERE chat_id=? AND EXISTS (SELECT 1 FROM json_tree(messages) WHERE type='integer' AND atom=? AND key<>'ChatID')
ORDER BY created_at DESC LIMIT 1`, chatID, messageID))
}

// ByStatus lists every request in the status, oldest first.
func (r *RequestRepository) ByStatus(ctx context.Context, status videostudioDomain.RequestStatus) ([]videostudioDomain.VideoRequest, error) {
	return r.list(ctx, `SELECT `+requestColumns+` FROM bloomary_video_requests WHERE status=? ORDER BY created_at,id`, status)
}

// FinishedBefore lists final requests last changed before cutoff that still hold producer uploads or a voice-over.
func (r *RequestRepository) FinishedBefore(ctx context.Context, cutoff time.Time) ([]videostudioDomain.VideoRequest, error) {
	return r.list(ctx, `SELECT `+requestColumns+` FROM bloomary_video_requests
WHERE status IN ('approved','discarded','cancelled','failed') AND updated_at<? AND (voice_over_key<>'' OR render<>'{}')
ORDER BY created_at,id`, formatTime(cutoff))
}

// DeleteCreatedBefore deletes requests created before cutoff and their producer jobs (foreign keys may be off).
func (r *RequestRepository) DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	at := formatTime(cutoff)
	if _, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_producer_jobs WHERE request_id IN (SELECT id FROM bloomary_video_requests WHERE created_at<?)`, at); err != nil {
		return 0, fmt.Errorf("delete old producer jobs: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_video_requests WHERE created_at<?`, at)
	if err != nil {
		return 0, fmt.Errorf("delete old video requests: %w", err)
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func (r *RequestRepository) list(ctx context.Context, query string, args ...any) ([]videostudioDomain.VideoRequest, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list video requests: %w", err)
	}
	defer rows.Close()
	var out []videostudioDomain.VideoRequest
	for rows.Next() {
		request, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, request)
	}
	return out, rows.Err()
}

func (r *RequestRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_video_requests WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete video requests member_id=%q: %w", memberID, err)
	}
	return nil
}

func encodeRequest(request videostudioDomain.VideoRequest) (scenario, render, messages, state string, err error) {
	parts := make([]string, 4)
	for i, value := range []any{request.Scenario, request.Render, request.Messages, request.State} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", "", "", "", fmt.Errorf("encode video request id=%q: %w", request.ID, err)
		}
		parts[i] = string(encoded)
	}
	// An empty render is stored as {} so FinishedBefore can tell a request without uploads.
	if request.Render == (videostudioDomain.Render{}) {
		parts[1] = "{}"
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

type scanner interface{ Scan(...any) error }

func scanRequest(row scanner) (videostudioDomain.VideoRequest, error) {
	var request videostudioDomain.VideoRequest
	var scenario, render, messages, state string
	var created, updated sql.NullString
	err := row.Scan(&request.ID, &request.MemberID, &request.Idea, &scenario, &request.VoiceOverKey, &render, &request.Status, &messages, &request.PublicationID, &state,
		&request.Version, &created, &updated)
	if err != nil {
		return videostudioDomain.VideoRequest{}, notFound(err)
	}
	request.CreatedAt, request.UpdatedAt = parseTime(created), parseTime(updated)
	for i, raw := range []string{scenario, render, messages, state} {
		target := []any{&request.Scenario, &request.Render, &request.Messages, &request.State}[i]
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			return videostudioDomain.VideoRequest{}, fmt.Errorf("decode video request id=%q: %w", request.ID, err)
		}
	}
	return request, nil
}

// JobQueue implements videostudio.ProducerJobQueue. Lease tokens are stored only as SHA-256 and cleared on use.
type JobQueue struct{ db *sql.DB }

var _ videostudioDomain.ProducerJobQueue = (*JobQueue)(nil)

func NewJobQueue(db *sql.DB) (*JobQueue, error) {
	if db == nil {
		return nil, fmt.Errorf("producer job queue: db is required")
	}
	return &JobQueue{db: db}, nil
}

func (q *JobQueue) Enqueue(ctx context.Context, job videostudioDomain.ProducerJob) error {
	now := formatTime(time.Now())
	_, err := q.db.ExecContext(ctx, `INSERT INTO bloomary_producer_jobs(id,request_id,member_id,kind,input,status,created_at,updated_at) VALUES(?,?,?,?,?,'waiting',?,?)`,
		job.ID, job.RequestID, job.MemberID, job.Kind, job.Input, now, now)
	if err != nil {
		return fmt.Errorf("enqueue producer job id=%q: %w", job.ID, err)
	}
	return nil
}

// Claim records the producer as seen and leases the oldest waiting job, or a job whose lease ran out.
func (q *JobQueue) Claim(ctx context.Context, now time.Time, lease time.Duration) (videostudioDomain.ProducerJob, error) {
	at := formatTime(now)
	if _, err := q.db.ExecContext(ctx, `INSERT INTO bloomary_producer_seen(id,seen_at) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET seen_at=excluded.seen_at`, at); err != nil {
		return videostudioDomain.ProducerJob{}, fmt.Errorf("record producer seen: %w", err)
	}
	token, err := newLeaseToken()
	if err != nil {
		return videostudioDomain.ProducerJob{}, err
	}
	job := videostudioDomain.ProducerJob{LeaseToken: token}
	var until sql.NullString
	var claimed, created sql.NullString
	err = q.db.QueryRowContext(ctx, `UPDATE bloomary_producer_jobs SET status='leased',lease_token_sha256=?,lease_until=?,claimed_at=COALESCE(claimed_at,?),updated_at=?
WHERE id=(SELECT id FROM bloomary_producer_jobs WHERE cancelled=0 AND (status='waiting' OR (status='leased' AND lease_until<=?)) ORDER BY created_at,id LIMIT 1)
RETURNING id,request_id,member_id,kind,input,lease_until,claimed_at,created_at`,
		hashToken(token), formatTime(now.Add(lease)), at, at, at).Scan(&job.ID, &job.RequestID, &job.MemberID, &job.Kind, &job.Input, &until, &claimed, &created)
	if err != nil {
		return videostudioDomain.ProducerJob{}, notFound(err)
	}
	job.LeaseUntil, job.ClaimedAt, job.CreatedAt = parseTime(until), parseTime(claimed), parseTime(created)
	return job, nil
}

func (q *JobQueue) Heartbeat(ctx context.Context, id, leaseToken string, until time.Time) error {
	if err := q.checkLease(ctx, id, leaseToken); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `UPDATE bloomary_producer_jobs SET lease_until=?,updated_at=? WHERE id=? AND status='leased'`, formatTime(until), formatTime(time.Now()), id)
	return err
}

func (q *JobQueue) Complete(ctx context.Context, id, leaseToken string) error {
	return q.finish(ctx, id, leaseToken, "done", "")
}

func (q *JobQueue) Fail(ctx context.Context, id, leaseToken, reason string) error {
	return q.finish(ctx, id, leaseToken, "failed", reason)
}

func (q *JobQueue) finish(ctx context.Context, id, leaseToken, status, reason string) error {
	if err := q.checkLease(ctx, id, leaseToken); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `UPDATE bloomary_producer_jobs SET status=?,failure=?,lease_token_sha256='',lease_until=NULL,updated_at=? WHERE id=? AND status='leased'`,
		status, reason, formatTime(time.Now()), id)
	return err
}

// Cancel stops the request's unfinished jobs; the producer's next heartbeat gets ErrJobCancelled.
func (q *JobQueue) Cancel(ctx context.Context, requestID string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE bloomary_producer_jobs SET cancelled=1,status='cancelled',lease_token_sha256='',updated_at=? WHERE request_id=? AND status IN ('waiting','leased')`,
		formatTime(time.Now()), requestID)
	if err != nil {
		return fmt.Errorf("cancel producer jobs request_id=%q: %w", requestID, err)
	}
	return nil
}

// LastSeen is when the producer last called claim; zero when never.
func (q *JobQueue) LastSeen(ctx context.Context) (time.Time, error) {
	var seen sql.NullString
	err := q.db.QueryRowContext(ctx, `SELECT seen_at FROM bloomary_producer_seen WHERE id=1`).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("producer last seen: %w", err)
	}
	return parseTime(seen), nil
}

const jobColumns = `id,request_id,member_id,kind,input,lease_until,claimed_at,created_at,cancelled`

// Leased returns the job (without its lease token) when leaseToken holds its lease.
func (q *JobQueue) Leased(ctx context.Context, id, leaseToken string) (videostudioDomain.ProducerJob, error) {
	if err := q.checkLease(ctx, id, leaseToken); err != nil {
		return videostudioDomain.ProducerJob{}, err
	}
	jobs, err := q.list(ctx, `SELECT `+jobColumns+` FROM bloomary_producer_jobs WHERE id=?`, id)
	if err != nil {
		return videostudioDomain.ProducerJob{}, err
	}
	if len(jobs) == 0 {
		return videostudioDomain.ProducerJob{}, videostudioDomain.ErrNotFound
	}
	return jobs[0], nil
}

// StartedSince lists the creation times of the member's jobs that start work (not the render after a scenario).
func (q *JobQueue) StartedSince(ctx context.Context, memberID string, since time.Time) ([]time.Time, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT created_at FROM bloomary_producer_jobs WHERE member_id=? AND kind<>? AND created_at>=? ORDER BY created_at`,
		memberID, videostudioDomain.JobRenderVideo, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("producer jobs since: %w", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var created sql.NullString
		if err := rows.Scan(&created); err != nil {
			return nil, err
		}
		out = append(out, parseTime(created))
	}
	return out, rows.Err()
}

func (q *JobQueue) OverTime(ctx context.Context, claimedBefore time.Time) ([]videostudioDomain.ProducerJob, error) {
	return q.list(ctx, `SELECT `+jobColumns+` FROM bloomary_producer_jobs WHERE cancelled=0 AND status IN ('waiting','leased') AND claimed_at<? ORDER BY created_at,id`, formatTime(claimedBefore))
}

func (q *JobQueue) WaitingSince(ctx context.Context, before time.Time) ([]videostudioDomain.ProducerJob, error) {
	return q.list(ctx, `SELECT `+jobColumns+` FROM bloomary_producer_jobs WHERE cancelled=0 AND status='waiting' AND claimed_at IS NULL AND created_at<? ORDER BY created_at,id`, formatTime(before))
}

func (q *JobQueue) list(ctx context.Context, query string, args ...any) ([]videostudioDomain.ProducerJob, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list producer jobs: %w", err)
	}
	defer rows.Close()
	var out []videostudioDomain.ProducerJob
	for rows.Next() {
		var job videostudioDomain.ProducerJob
		var until, claimed, created sql.NullString
		if err := rows.Scan(&job.ID, &job.RequestID, &job.MemberID, &job.Kind, &job.Input, &until, &claimed, &created, &job.Cancelled); err != nil {
			return nil, err
		}
		job.LeaseUntil, job.ClaimedAt, job.CreatedAt = parseTime(until), parseTime(claimed), parseTime(created)
		out = append(out, job)
	}
	return out, rows.Err()
}

func (q *JobQueue) DeleteByMemberID(ctx context.Context, memberID string) error {
	if _, err := q.db.ExecContext(ctx, `DELETE FROM bloomary_producer_jobs WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete producer jobs member_id=%q: %w", memberID, err)
	}
	return nil
}

func (q *JobQueue) checkLease(ctx context.Context, id, leaseToken string) error {
	var cancelled bool
	var status, stored string
	var until sql.NullString
	err := q.db.QueryRowContext(ctx, `SELECT cancelled,status,lease_token_sha256,lease_until FROM bloomary_producer_jobs WHERE id=?`, id).Scan(&cancelled, &status, &stored, &until)
	if err != nil {
		return notFound(err)
	}
	if cancelled {
		return videostudioDomain.ErrJobCancelled
	}
	if status != "leased" || stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(hashToken(leaseToken))) != 1 || !time.Now().Before(parseTime(until)) {
		return videostudioDomain.ErrLeaseLost
	}
	return nil
}

func newLeaseToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("lease token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
