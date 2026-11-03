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
)

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
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT media_key FROM bloomary_deliveries WHERE media_key<>'' AND publication_id IN (`+closedBefore+`)`, formatTime(cutoff))
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
	_, err := r.db.ExecContext(ctx, `UPDATE bloomary_deliveries SET media_key='',version=version+1,updated_at=? WHERE media_key<>'' AND publication_id IN (`+closedBefore+`)`,
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
