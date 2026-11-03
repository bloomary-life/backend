package crosspost

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

// Queries the crosspost stories need beyond the wave-1 ports: listing a member's deliveries, removing one
// connection, the publication's LinkedIn intent, the retention purge, and the in-memory login state.

var (
	_ crosspostDomain.DeliveryLister      = (*DeliveryRepository)(nil)
	_ crosspostDomain.ConnectionRemover   = (*ConnectionRepository)(nil)
	_ crosspostDomain.LinkedInIntentStore = (*LinkedInIntentRepository)(nil)
	_ crosspostDomain.RetentionStore      = (*PublicationRepository)(nil)
	_ crosspostDomain.ConnectStateStore   = (*ConnectStates)(nil)
	_ crosspostDomain.UnapprovedCloser    = (*PublicationRepository)(nil)
)

// CloseUnapproved cancels the proposed deliveries of the draft's open, never approved publication and closes it.
func (r *PublicationRepository) CloseUnapproved(ctx context.Context, memberID, draftID string, at time.Time) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		open := `SELECT id FROM bloomary_publications WHERE member_id=? AND draft_id=? AND closed_at IS NULL AND approved_at IS NULL`
		now := formatTime(at)
		if _, err := tx.ExecContext(ctx, `UPDATE bloomary_deliveries SET status='cancelled',version=version+1,updated_at=? WHERE status='proposed' AND publication_id IN (`+open+`)`,
			now, memberID, draftID); err != nil {
			return fmt.Errorf("cancel unapproved deliveries: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bloomary_publications SET closed_at=? WHERE id IN (`+open+`)`, now, memberID, draftID); err != nil {
			return fmt.Errorf("close unapproved publication: %w", err)
		}
		return nil
	})
}

func (r *DeliveryRepository) ListByMember(ctx context.Context, memberID string) ([]crosspostDomain.Delivery, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM bloomary_deliveries WHERE member_id=? ORDER BY publish_at,id`, memberID)
	if err != nil {
		return nil, fmt.Errorf("list member deliveries: %w", err)
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

// DeleteConnection deletes the member's connection and its destinations in one transaction.
func (r *ConnectionRepository) DeleteConnection(ctx context.Context, memberID, connectionID string) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bloomary_destinations WHERE member_id=? AND connection_id=?`, memberID, connectionID); err != nil {
			return fmt.Errorf("delete destinations connection_id=%q: %w", connectionID, err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM bloomary_connections WHERE member_id=? AND id=?`, memberID, connectionID)
		if err != nil {
			return fmt.Errorf("delete connection id=%q: %w", connectionID, err)
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return err
			}
			return crosspostDomain.ErrNotFound
		}
		return nil
	})
}

// DeleteClosedBefore deletes publications closed before cutoff, their deliveries and LinkedIn intents.
func (r *PublicationRepository) DeleteClosedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	deleted := 0
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		old := `SELECT id FROM bloomary_publications WHERE closed_at IS NOT NULL AND closed_at<?`
		at := formatTime(cutoff)
		if _, err := tx.ExecContext(ctx, `DELETE FROM bloomary_deliveries WHERE publication_id IN (`+old+`)`, at); err != nil {
			return fmt.Errorf("purge deliveries: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bloomary_publication_linkedin WHERE publication_id IN (`+old+`)`, at); err != nil {
			return fmt.Errorf("purge linkedin intents: %w", err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM bloomary_publications WHERE closed_at IS NOT NULL AND closed_at<?`, at)
		if err != nil {
			return fmt.Errorf("purge publications: %w", err)
		}
		n, err := result.RowsAffected()
		deleted = int(n)
		return err
	})
	return deleted, err
}

// LinkedInIntentRepository implements crosspost.LinkedInIntentStore on bloomary_publication_linkedin.
type LinkedInIntentRepository struct{ db *sql.DB }

func NewLinkedInIntentRepository(db *sql.DB) (*LinkedInIntentRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost linkedin intent repository: db is required")
	}
	return &LinkedInIntentRepository{db: db}, nil
}

func (r *LinkedInIntentRepository) SaveIntent(ctx context.Context, memberID, publicationID string, intent crosspostDomain.LinkedInIntent) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO bloomary_publication_linkedin(publication_id,member_id,proposed_at,skip_reason,created_at) VALUES(?,?,?,?,?)
ON CONFLICT(publication_id) DO UPDATE SET proposed_at=excluded.proposed_at,skip_reason=excluded.skip_reason
WHERE bloomary_publication_linkedin.member_id=excluded.member_id`,
		publicationID, memberID, formatTime(intent.ProposedAt), intent.SkipReason, formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save linkedin intent publication_id=%q: %w", publicationID, err)
	}
	return nil
}

func (r *LinkedInIntentRepository) Intent(ctx context.Context, memberID, publicationID string) (crosspostDomain.LinkedInIntent, error) {
	var intent crosspostDomain.LinkedInIntent
	var at sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT proposed_at,skip_reason FROM bloomary_publication_linkedin WHERE member_id=? AND publication_id=?`, memberID, publicationID).
		Scan(&at, &intent.SkipReason)
	if err != nil {
		return crosspostDomain.LinkedInIntent{}, notFound(err)
	}
	intent.ProposedAt = parseTime(at)
	return intent, nil
}

func (r *LinkedInIntentRepository) DeleteByMemberID(ctx context.Context, memberID string) error {
	return deleteByMember(ctx, r.db, "bloomary_publication_linkedin", memberID)
}

// ConnectStates keeps started provider logins in memory: single-use, 10 minutes. A restart forgets them; the
// member starts the login again.
type ConnectStates struct {
	states map[string]crosspostDomain.ConnectState
	mu     sync.Mutex
}

func NewConnectStates() *ConnectStates {
	return &ConnectStates{states: map[string]crosspostDomain.ConnectState{}}
}

func (s *ConnectStates) Put(_ context.Context, state string, value crosspostDomain.ConnectState) error {
	if strings.TrimSpace(state) == "" {
		return crosspostDomain.ErrStateInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, old := range s.states {
		if value.CreatedAt.Sub(old.CreatedAt) > crosspostDomain.ConnectStateTTL {
			delete(s.states, key)
		}
	}
	s.states[state] = value
	return nil
}

func (s *ConnectStates) Take(_ context.Context, state string, now time.Time) (crosspostDomain.ConnectState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.states[state]
	delete(s.states, state)
	if !ok || now.Sub(value.CreatedAt) > crosspostDomain.ConnectStateTTL {
		return crosspostDomain.ConnectState{}, crosspostDomain.ErrStateInvalid
	}
	return value, nil
}

const closedBefore = `SELECT id FROM bloomary_publications WHERE closed_at IS NOT NULL AND closed_at<?`

func (r *PublicationRepository) MediaClosedBefore(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT media_key FROM bloomary_deliveries WHERE media_key<>'' AND publication_id IN (`+closedBefore+`)
UNION SELECT cover_key FROM bloomary_deliveries WHERE cover_key<>'' AND publication_id IN (`+closedBefore+`)`, formatTime(cutoff), formatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("closed media keys: %w", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *PublicationRepository) ClearMediaClosedBefore(ctx context.Context, cutoff time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bloomary_deliveries SET media_key='',cover_key='',version=version+1,updated_at=? WHERE (media_key<>'' OR cover_key<>'') AND publication_id IN (`+closedBefore+`)`,
		formatTime(time.Now()), formatTime(cutoff))
	if err != nil {
		return fmt.Errorf("clear closed media keys: %w", err)
	}
	return nil
}

// ListAll lists every connection (the daily connection check).
func (r *ConnectionRepository) ListAll(ctx context.Context) ([]crosspostDomain.Connection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM bloomary_connections ORDER BY created_at,id`)
	if err != nil {
		return nil, fmt.Errorf("list all connections: %w", err)
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

// SealedRepository implements crosspost.SealedRecordStore over connections, destinations and delivery progress.
type SealedRepository struct{ db *sql.DB }

var _ crosspostDomain.SealedRecordStore = (*SealedRepository)(nil)

func NewSealedRepository(db *sql.DB) (*SealedRepository, error) {
	if db == nil {
		return nil, fmt.Errorf("crosspost sealed repository: db is required")
	}
	return &SealedRepository{db: db}, nil
}

func (r *SealedRepository) SealedRecords(ctx context.Context) ([]crosspostDomain.SealedRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT 'connection',id,member_id,provider,sealed_key_id,sealed_schema,sealed_data FROM bloomary_connections
UNION ALL
SELECT 'destination',d.id,d.member_id,c.provider,d.sealed_key_id,d.sealed_schema,d.sealed_data FROM bloomary_destinations d
  JOIN bloomary_connections c ON c.id=d.connection_id WHERE length(d.sealed_data)>0
UNION ALL
SELECT 'progress',id,member_id,network,progress_key_id,progress_schema,progress_data FROM bloomary_deliveries WHERE length(progress_data)>0`)
	if err != nil {
		return nil, fmt.Errorf("list sealed records: %w", err)
	}
	defer rows.Close()
	records := []crosspostDomain.SealedRecord{}
	for rows.Next() {
		var record crosspostDomain.SealedRecord
		var provider string
		if err := rows.Scan(&record.Kind, &record.ID, &record.MemberID, &provider, &record.Sealed.KeyID, &record.Sealed.SchemaVersion, &record.Sealed.Data); err != nil {
			return nil, err
		}
		record.Provider = crosspostDomain.Provider(provider)
		if record.Kind == crosspostDomain.SealedProgressRow {
			record.Provider = crosspostDomain.ProviderOf(crosspostDomain.Network(provider))
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ReplaceSealed writes the new sealed value only when the row still holds previous (no lost update with the worker).
func (r *SealedRepository) ReplaceSealed(ctx context.Context, record crosspostDomain.SealedRecord, previous crosspostDomain.SealedCredentials) error {
	query := map[crosspostDomain.SealedKind]string{
		crosspostDomain.SealedConnection:  `UPDATE bloomary_connections SET sealed_key_id=?,sealed_schema=?,sealed_data=?,version=version+1 WHERE id=? AND member_id=? AND sealed_key_id=? AND sealed_data=?`,
		crosspostDomain.SealedDestination: `UPDATE bloomary_destinations SET sealed_key_id=?,sealed_schema=?,sealed_data=? WHERE id=? AND member_id=? AND sealed_key_id=? AND sealed_data=?`,
		crosspostDomain.SealedProgressRow: `UPDATE bloomary_deliveries SET progress_key_id=?,progress_schema=?,progress_data=?,version=version+1 WHERE id=? AND member_id=? AND progress_key_id=? AND progress_data=?`,
	}[record.Kind]
	if query == "" {
		return fmt.Errorf("replace sealed: unknown kind %q", record.Kind)
	}
	result, err := r.db.ExecContext(ctx, query, record.Sealed.KeyID, record.Sealed.SchemaVersion, record.Sealed.Data, record.ID, record.MemberID, previous.KeyID, previous.Data)
	if err != nil {
		return fmt.Errorf("replace sealed %s id=%q: %w", record.Kind, record.ID, err)
	}
	return oneRow(result)
}
