// Package crosspost persists connections, destinations, publications, deliveries, the pause switch and the audit
// log. Tables come from Goose migrations (gates/repositories/migrations), run after the inline Init at start.
package crosspost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

const timeFormat = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func parseTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(timeFormat, value.String)
	return t
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return crosspostDomain.ErrNotFound
	}
	return err
}

type scanner interface{ Scan(...any) error }

// ConnectionRepository implements crosspost.ConnectionRepository.
type ConnectionRepository struct{ db *sql.DB }

var _ crosspostDomain.ConnectionRepository = (*ConnectionRepository)(nil)

func NewConnectionRepository(db *sql.DB) (*ConnectionRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost connection repository: db is required")
	}
	return &ConnectionRepository{db: db}, nil
}

const connectionColumns = `id,member_id,provider,app_owner,account_id,sealed_key_id,sealed_schema,sealed_data,health,expires_at,version`

func (r *ConnectionRepository) Save(ctx context.Context, c crosspostDomain.Connection, expectedVersion int) error {
	now := formatTime(time.Now())
	if expectedVersion == 0 {
		_, err := r.db.ExecContext(ctx, `INSERT INTO bloomary_connections(`+connectionColumns+`,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			c.ID, c.MemberID, c.Provider, c.AppOwner, c.AccountID, c.Sealed.KeyID, c.Sealed.SchemaVersion, c.Sealed.Data, c.Health, nullTime(c.ExpiresAt), now, now)
		if err != nil {
			return fmt.Errorf("insert connection id=%q: %w", c.ID, err)
		}
		return nil
	}
	result, err := r.db.ExecContext(ctx, `UPDATE bloomary_connections SET app_owner=?,account_id=?,sealed_key_id=?,sealed_schema=?,sealed_data=?,health=?,expires_at=?,version=version+1,updated_at=? WHERE id=? AND member_id=? AND version=?`,
		c.AppOwner, c.AccountID, c.Sealed.KeyID, c.Sealed.SchemaVersion, c.Sealed.Data, c.Health, nullTime(c.ExpiresAt), now, c.ID, c.MemberID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update connection id=%q: %w", c.ID, err)
	}
	return oneRow(result)
}

func (r *ConnectionRepository) ByID(ctx context.Context, memberID, id string) (crosspostDomain.Connection, error) {
	return scanConnection(r.db.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM bloomary_connections WHERE member_id=? AND id=?`, memberID, id))
}

// ByProviderAccount lists the connections of one provider account across members.
func (r *ConnectionRepository) ByProviderAccount(ctx context.Context, provider crosspostDomain.Provider, accountID string) ([]crosspostDomain.Connection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM bloomary_connections WHERE provider=? AND account_id=? ORDER BY created_at,id`, provider, accountID)
	if err != nil {
		return nil, fmt.Errorf("connections by account: %w", err)
	}
	defer rows.Close()
	var out []crosspostDomain.Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *ConnectionRepository) ListByMember(ctx context.Context, memberID string) ([]crosspostDomain.Connection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM bloomary_connections WHERE member_id=? ORDER BY created_at,id`, memberID)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	connections := []crosspostDomain.Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, c)
	}
	return connections, rows.Err()
}

// LockForRefresh takes the connection's refresh lock until the given time; false when another refresh holds it.
func (r *ConnectionRepository) LockForRefresh(ctx context.Context, id string, until time.Time) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE bloomary_connections SET refresh_locked_until=? WHERE id=? AND (refresh_locked_until IS NULL OR refresh_locked_until<=?)`,
		formatTime(until), id, formatTime(time.Now()))
	if err != nil {
		return false, fmt.Errorf("lock connection id=%q: %w", id, err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *ConnectionRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	return deleteByMember(ctx, r.db, "bloomary_connections", memberID)
}

func scanConnection(row scanner) (crosspostDomain.Connection, error) {
	var c crosspostDomain.Connection
	var expires sql.NullString
	err := row.Scan(&c.ID, &c.MemberID, &c.Provider, &c.AppOwner, &c.AccountID, &c.Sealed.KeyID, &c.Sealed.SchemaVersion, &c.Sealed.Data, &c.Health, &expires, &c.Version)
	if err != nil {
		return crosspostDomain.Connection{}, notFound(err)
	}
	c.ExpiresAt = parseTime(expires)
	return c, nil
}

// DestinationRepository implements crosspost.DestinationRepository.
type DestinationRepository struct{ db *sql.DB }

var _ crosspostDomain.DestinationRepository = (*DestinationRepository)(nil)

func NewDestinationRepository(db *sql.DB) (*DestinationRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost destination repository: db is required")
	}
	return &DestinationRepository{db: db}, nil
}

const destinationColumns = `id,member_id,connection_id,network,account_id,account_name,sealed_key_id,sealed_schema,sealed_data,audience_zone,enabled`

func (r *DestinationRepository) Save(ctx context.Context, d crosspostDomain.Destination) error {
	now := formatTime(time.Now())
	_, err := r.db.ExecContext(ctx, `INSERT INTO bloomary_destinations(`+destinationColumns+`,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET account_name=excluded.account_name,sealed_key_id=excluded.sealed_key_id,sealed_schema=excluded.sealed_schema,
sealed_data=excluded.sealed_data,audience_zone=excluded.audience_zone,enabled=excluded.enabled,updated_at=excluded.updated_at
WHERE bloomary_destinations.member_id=excluded.member_id`,
		d.ID, d.MemberID, d.ConnectionID, d.Network, d.AccountID, d.AccountName, d.Sealed.KeyID, d.Sealed.SchemaVersion, d.Sealed.Data, d.AudienceZone, d.Enabled, now, now)
	if err != nil {
		return fmt.Errorf("save destination id=%q: %w", d.ID, err)
	}
	return nil
}

func (r *DestinationRepository) ByID(ctx context.Context, memberID, id string) (crosspostDomain.Destination, error) {
	return scanDestination(r.db.QueryRowContext(ctx, `SELECT `+destinationColumns+` FROM bloomary_destinations WHERE member_id=? AND id=?`, memberID, id))
}

func (r *DestinationRepository) ListByMember(ctx context.Context, memberID string) ([]crosspostDomain.Destination, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+destinationColumns+` FROM bloomary_destinations WHERE member_id=? ORDER BY created_at,id`, memberID)
	if err != nil {
		return nil, fmt.Errorf("list destinations: %w", err)
	}
	defer rows.Close()
	destinations := []crosspostDomain.Destination{}
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		destinations = append(destinations, d)
	}
	return destinations, rows.Err()
}

func (r *DestinationRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	return deleteByMember(ctx, r.db, "bloomary_destinations", memberID)
}

func scanDestination(row scanner) (crosspostDomain.Destination, error) {
	var d crosspostDomain.Destination
	err := row.Scan(&d.ID, &d.MemberID, &d.ConnectionID, &d.Network, &d.AccountID, &d.AccountName, &d.Sealed.KeyID, &d.Sealed.SchemaVersion, &d.Sealed.Data, &d.AudienceZone, &d.Enabled)
	if err != nil {
		return crosspostDomain.Destination{}, notFound(err)
	}
	return d, nil
}

// PublicationRepository implements crosspost.PublicationRepository.
type PublicationRepository struct{ db *sql.DB }

var _ crosspostDomain.PublicationRepository = (*PublicationRepository)(nil)

func NewPublicationRepository(db *sql.DB) (*PublicationRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost publication repository: db is required")
	}
	return &PublicationRepository{db: db}, nil
}

// Create stores the publication and its deliveries (each at version 1).
func (r *PublicationRepository) Create(ctx context.Context, p crosspostDomain.Publication, deliveries []crosspostDomain.Delivery) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		created := p.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO bloomary_publications(id,member_id,draft_id,linkedin_schedule_id,created_at) VALUES(?,?,?,?,?)`,
			p.ID, p.MemberID, p.DraftID, p.LinkedInScheduleID, formatTime(created))
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: bloomary_publications.member_id") {
				return crosspostDomain.ErrOpenPublication
			}
			return fmt.Errorf("insert publication id=%q: %w", p.ID, err)
		}
		for _, d := range deliveries {
			if err := insertDelivery(ctx, tx, d); err != nil {
				return err
			}
		}
		return nil
	})
}

// ApproveAll marks the publication approved and saves every delivery at its read version, all or nothing.
func (r *PublicationRepository) ApproveAll(ctx context.Context, p crosspostDomain.Publication, deliveries []crosspostDomain.Delivery) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		approved := p.ApprovedAt
		if approved.IsZero() {
			approved = time.Now()
		}
		result, err := tx.ExecContext(ctx, `UPDATE bloomary_publications SET approved_at=?,linkedin_schedule_id=? WHERE id=? AND member_id=? AND approved_at IS NULL`,
			formatTime(approved), p.LinkedInScheduleID, p.ID, p.MemberID)
		if err != nil {
			return fmt.Errorf("approve publication id=%q: %w", p.ID, err)
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return err
			}
			return crosspostDomain.ErrAlreadyApproved
		}
		for _, d := range deliveries {
			if err := updateDelivery(ctx, tx, d); err != nil {
				return err
			}
		}
		return closeIfFinal(ctx, tx, p.ID)
	})
}

func (r *PublicationRepository) ByID(ctx context.Context, memberID, id string) (crosspostDomain.Publication, error) {
	var p crosspostDomain.Publication
	var created, approved, closed sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,member_id,draft_id,linkedin_schedule_id,created_at,approved_at,closed_at FROM bloomary_publications WHERE member_id=? AND id=?`, memberID, id).
		Scan(&p.ID, &p.MemberID, &p.DraftID, &p.LinkedInScheduleID, &created, &approved, &closed)
	if err != nil {
		return crosspostDomain.Publication{}, notFound(err)
	}
	p.CreatedAt, p.ApprovedAt, p.ClosedAt = parseTime(created), parseTime(approved), parseTime(closed)
	return p, nil
}

// ListApproved lists the member's approved publications that are open or closed since closedSince, newest first.
func (r *PublicationRepository) ListApproved(ctx context.Context, memberID string, closedSince time.Time) ([]crosspostDomain.Publication, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,member_id,draft_id,linkedin_schedule_id,created_at,approved_at,closed_at FROM bloomary_publications
WHERE member_id=? AND approved_at IS NOT NULL AND (closed_at IS NULL OR closed_at>=?) ORDER BY created_at DESC,id LIMIT 100`, memberID, formatTime(closedSince))
	if err != nil {
		return nil, fmt.Errorf("list publications: %w", err)
	}
	defer rows.Close()
	var out []crosspostDomain.Publication
	for rows.Next() {
		var p crosspostDomain.Publication
		var created, approved, closed sql.NullString
		if err := rows.Scan(&p.ID, &p.MemberID, &p.DraftID, &p.LinkedInScheduleID, &created, &approved, &closed); err != nil {
			return nil, err
		}
		p.CreatedAt, p.ApprovedAt, p.ClosedAt = parseTime(created), parseTime(approved), parseTime(closed)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PublicationRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	return deleteByMember(ctx, r.db, "bloomary_publications", memberID)
}

// DeliveryRepository implements crosspost.DeliveryRepository.
type DeliveryRepository struct{ db *sql.DB }

var _ crosspostDomain.DeliveryRepository = (*DeliveryRepository)(nil)

func NewDeliveryRepository(db *sql.DB) (*DeliveryRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost delivery repository: db is required")
	}
	return &DeliveryRepository{db: db}, nil
}

const deliveryColumns = `id,publication_id,member_id,destination_id,network,text,title,hashtags,media_key,media_sha256,ai_label,
youtube_privacy,youtube_made_for_kids,publish_at,status,skip_reason,warnings,progress_key_id,progress_schema,progress_data,
external_post_id,external_url,attempts,next_attempt_at,last_error_code,cover_key,version`

// Save updates the delivery only when it is still at d.Version and closes its publication when the last delivery is final.
func (r *DeliveryRepository) Save(ctx context.Context, d crosspostDomain.Delivery) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := updateDelivery(ctx, tx, d); err != nil {
			return err
		}
		if !d.Status.Final() {
			return nil
		}
		return closeIfFinal(ctx, tx, d.PublicationID)
	})
}

// ClaimDue moves the oldest due scheduled delivery to publishing, skipping paused networks and turned-off destinations.
func (r *DeliveryRepository) ClaimDue(ctx context.Context, now time.Time, paused []crosspostDomain.Network) (crosspostDomain.Delivery, error) {
	at := formatTime(now)
	args := []any{formatTime(time.Now()), at}
	notPaused := ""
	if len(paused) > 0 {
		notPaused = ` AND network NOT IN (?` + strings.Repeat(",?", len(paused)-1) + `)`
		for _, network := range paused {
			args = append(args, network)
		}
	}
	row := r.db.QueryRowContext(ctx, `UPDATE bloomary_deliveries SET status='publishing',version=version+1,updated_at=?
WHERE id=(SELECT id FROM bloomary_deliveries WHERE status='scheduled' AND COALESCE(next_attempt_at,publish_at)<=?`+notPaused+`
AND destination_id IN (SELECT id FROM bloomary_destinations WHERE enabled=1) ORDER BY publish_at,id LIMIT 1)
RETURNING `+deliveryColumns, args...)
	return scanDelivery(row)
}

func (r *DeliveryRepository) ByPublication(ctx context.Context, memberID, publicationID string) ([]crosspostDomain.Delivery, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM bloomary_deliveries WHERE member_id=? AND publication_id=? ORDER BY publish_at,id`, memberID, publicationID)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := []crosspostDomain.Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// TakenTimes lists publish times from `from` on of the member's deliveries to the network that will post or did post.
func (r *DeliveryRepository) TakenTimes(ctx context.Context, memberID string, network crosspostDomain.Network, from time.Time) ([]time.Time, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT publish_at FROM bloomary_deliveries WHERE member_id=? AND network=? AND publish_at>=?
AND status IN ('scheduled','publishing','published','sent_to_inbox','needs_review','needs_reconnect','simulated') ORDER BY publish_at`,
		memberID, network, formatTime(from))
	if err != nil {
		return nil, fmt.Errorf("taken times: %w", err)
	}
	defer rows.Close()
	times := []time.Time{}
	for rows.Next() {
		var at sql.NullString
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		times = append(times, parseTime(at))
	}
	return times, rows.Err()
}

// MarkInterrupted runs at start: a delivery left in publishing may already be live, so it needs review.
func (r *DeliveryRepository) MarkInterrupted(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bloomary_deliveries SET status='needs_review',last_error_code='interrupted',version=version+1,updated_at=? WHERE status='publishing'`,
		formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("mark interrupted deliveries: %w", err)
	}
	return nil
}

func (r *DeliveryRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	return deleteByMember(ctx, r.db, "bloomary_deliveries", memberID)
}

func insertDelivery(ctx context.Context, tx *sql.Tx, d crosspostDomain.Delivery) error {
	hashtags, warnings, err := encodeLists(d)
	if err != nil {
		return err
	}
	now := formatTime(time.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO bloomary_deliveries(`+deliveryColumns+`,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		d.ID, d.PublicationID, d.MemberID, d.DestinationID, d.Network, d.Text, d.Title, hashtags, d.MediaKey, d.MediaSHA256, d.AILabel,
		d.YouTube.Privacy, d.YouTube.MadeForKids, formatTime(d.PublishAt), d.Status, d.SkipReason, warnings,
		d.Progress.KeyID, d.Progress.SchemaVersion, d.Progress.Data, d.ExternalPostID, d.ExternalURL, d.Attempts, nullTime(d.NextAttemptAt), d.LastErrorCode,
		d.CoverKey, now, now)
	if err != nil {
		return fmt.Errorf("insert delivery id=%q: %w", d.ID, err)
	}
	return nil
}

func updateDelivery(ctx context.Context, tx *sql.Tx, d crosspostDomain.Delivery) error {
	hashtags, warnings, err := encodeLists(d)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE bloomary_deliveries SET text=?,title=?,hashtags=?,media_key=?,media_sha256=?,ai_label=?,
youtube_privacy=?,youtube_made_for_kids=?,publish_at=?,status=?,skip_reason=?,warnings=?,progress_key_id=?,progress_schema=?,progress_data=?,
external_post_id=?,external_url=?,attempts=?,next_attempt_at=?,last_error_code=?,cover_key=?,version=version+1,updated_at=?
WHERE id=? AND member_id=? AND version=?`,
		d.Text, d.Title, hashtags, d.MediaKey, d.MediaSHA256, d.AILabel,
		d.YouTube.Privacy, d.YouTube.MadeForKids, formatTime(d.PublishAt), d.Status, d.SkipReason, warnings,
		d.Progress.KeyID, d.Progress.SchemaVersion, d.Progress.Data, d.ExternalPostID, d.ExternalURL, d.Attempts, nullTime(d.NextAttemptAt), d.LastErrorCode,
		d.CoverKey, formatTime(time.Now()), d.ID, d.MemberID, d.Version)
	if err != nil {
		return fmt.Errorf("update delivery id=%q: %w", d.ID, err)
	}
	return oneRow(result)
}

func closeIfFinal(ctx context.Context, tx *sql.Tx, publicationID string) error {
	final := `'` + strings.Join(statusStrings(crosspostDomain.FinalStatuses), `','`) + `'`
	_, err := tx.ExecContext(ctx, `UPDATE bloomary_publications SET closed_at=? WHERE id=? AND closed_at IS NULL
AND NOT EXISTS (SELECT 1 FROM bloomary_deliveries WHERE publication_id=? AND status NOT IN (`+final+`))`,
		formatTime(time.Now()), publicationID, publicationID)
	if err != nil {
		return fmt.Errorf("close publication id=%q: %w", publicationID, err)
	}
	return nil
}

func statusStrings(statuses []crosspostDomain.DeliveryStatus) []string {
	values := make([]string, 0, len(statuses))
	for _, s := range statuses {
		values = append(values, string(s))
	}
	return values
}

func encodeLists(d crosspostDomain.Delivery) (string, string, error) {
	hashtags, err := json.Marshal(nonNil(d.Hashtags))
	if err != nil {
		return "", "", err
	}
	warnings, err := json.Marshal(nonNil(d.Warnings))
	return string(hashtags), string(warnings), err
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func scanDelivery(row scanner) (crosspostDomain.Delivery, error) {
	var d crosspostDomain.Delivery
	var hashtags, warnings string
	var publishAt, nextAttempt sql.NullString
	var madeForKids sql.NullBool
	err := row.Scan(&d.ID, &d.PublicationID, &d.MemberID, &d.DestinationID, &d.Network, &d.Text, &d.Title, &hashtags, &d.MediaKey, &d.MediaSHA256, &d.AILabel,
		&d.YouTube.Privacy, &madeForKids, &publishAt, &d.Status, &d.SkipReason, &warnings, &d.Progress.KeyID, &d.Progress.SchemaVersion, &d.Progress.Data,
		&d.ExternalPostID, &d.ExternalURL, &d.Attempts, &nextAttempt, &d.LastErrorCode, &d.CoverKey, &d.Version)
	if err != nil {
		return crosspostDomain.Delivery{}, notFound(err)
	}
	if madeForKids.Valid {
		d.YouTube.MadeForKids = &madeForKids.Bool
	}
	d.PublishAt, d.NextAttemptAt = parseTime(publishAt), parseTime(nextAttempt)
	if err := json.Unmarshal([]byte(hashtags), &d.Hashtags); err != nil {
		return crosspostDomain.Delivery{}, fmt.Errorf("delivery id=%q hashtags: %w", d.ID, err)
	}
	if err := json.Unmarshal([]byte(warnings), &d.Warnings); err != nil {
		return crosspostDomain.Delivery{}, fmt.Errorf("delivery id=%q warnings: %w", d.ID, err)
	}
	return d, nil
}

// PauseRepository implements crosspost.PauseStore on bloomary_publish_pause.
type PauseRepository struct{ db *sql.DB }

var _ crosspostDomain.PauseStore = (*PauseRepository)(nil)

func NewPauseRepository(db *sql.DB) (*PauseRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost pause repository: db is required")
	}
	return &PauseRepository{db: db}, nil
}

// Paused is true when the network or every network ("all") is paused.
func (r *PauseRepository) Paused(ctx context.Context, network crosspostDomain.Network) (bool, error) {
	var paused int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bloomary_publish_pause WHERE paused=1 AND network IN (?,?)`, network, crosspostDomain.AllNetworks).Scan(&paused)
	if err != nil {
		return false, fmt.Errorf("read publish pause: %w", err)
	}
	return paused > 0, nil
}

func (r *PauseRepository) SetPaused(ctx context.Context, network crosspostDomain.Network, paused bool, changedBy string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO bloomary_publish_pause(network,paused,changed_by,changed_at) VALUES(?,?,?,?)
ON CONFLICT(network) DO UPDATE SET paused=excluded.paused,changed_by=excluded.changed_by,changed_at=excluded.changed_at`,
		network, paused, changedBy, formatTime(at))
	if err != nil {
		return fmt.Errorf("set publish pause network=%q: %w", network, err)
	}
	return nil
}

// AuditRepository implements crosspost.AuditLog on the append-only bloomary_crosspost_audit.
type AuditRepository struct{ db *sql.DB }

var _ crosspostDomain.AuditLog = (*AuditRepository)(nil)

func NewAuditRepository(db *sql.DB) (*AuditRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost audit repository: db is required")
	}
	return &AuditRepository{db: db}, nil
}

func (r *AuditRepository) Append(ctx context.Context, a crosspostDomain.AuditRecord) error {
	at := a.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO bloomary_crosspost_audit(member_id,action,record_ids,media_sha256,text_sha256,network_post_id,telegram_user_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		a.MemberID, a.Action, strings.Join(a.RecordIDs, ","), a.MediaSHA256, a.TextSHA256, a.NetworkPostID, a.TelegramUserID, formatTime(at))
	if err != nil {
		return fmt.Errorf("append audit action=%q: %w", a.Action, err)
	}
	return nil
}

func (r *AuditRepository) StripMember(ctx context.Context, memberID, memberHMAC string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bloomary_crosspost_audit SET member_id=?,member_stripped=1,text_sha256='',network_post_id='',telegram_user_id=0 WHERE member_id=? AND member_stripped=0`,
		memberHMAC, memberID)
	if err != nil {
		return fmt.Errorf("strip audit rows: %w", err)
	}
	return nil
}

func (r *AuditRepository) PurgeBefore(ctx context.Context, cutoff time.Time) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM bloomary_crosspost_audit WHERE created_at<?`, formatTime(cutoff)); err != nil {
		return fmt.Errorf("purge audit rows: %w", err)
	}
	return nil
}

func deleteByMember(ctx context.Context, db *sql.DB, table, memberID string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE member_id=?`, memberID); err != nil {
		return fmt.Errorf("delete %s member_id=%q: %w", table, memberID, err)
	}
	return nil
}

func oneRow(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return crosspostDomain.ErrVersionConflict
	}
	return nil
}

func inTx(ctx context.Context, db *sql.DB, work func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := work(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
