package mcpoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mcpDomain "bloomary-api/internal/domain/mcp"
)

type SQLiteStateStore struct{ db *sql.DB }

func NewSQLiteStateStore(db *sql.DB) (*SQLiteStateStore, error) {
	if db == nil {
		return nil, errors.New("MCP OAuth sqlite store: db is required")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS mcp_oauth_state (kind TEXT NOT NULL, state_key TEXT NOT NULL, payload BLOB NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(kind, state_key))`)
	if err != nil {
		return nil, fmt.Errorf("init MCP OAuth state table: %w", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS app_sessions (session_id_hash TEXT PRIMARY KEY, member_id TEXT NOT NULL, expires_at INTEGER NOT NULL)`)
	if err != nil {
		return nil, fmt.Errorf("init app session table: %w", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS mcp_grants (id TEXT PRIMARY KEY, member_id TEXT NOT NULL, client_id TEXT NOT NULL, scopes BLOB NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER)`)
	if err != nil {
		return nil, fmt.Errorf("init MCP grants table: %w", err)
	}
	return &SQLiteStateStore{db: db}, nil
}

func (s *SQLiteStateStore) CreateGrant(ctx context.Context, grant mcpDomain.Grant) error {
	if grant.ID == "" || grant.MemberID == "" || grant.ClientID == "" {
		return errors.New("MCP grant ID, member, and client are required")
	}
	scopes, err := json.Marshal(grant.Scopes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_grants(id,member_id,client_id,scopes,created_at) VALUES(?,?,?,?,?)`, grant.ID, grant.MemberID, grant.ClientID, scopes, grant.CreatedAt.UTC().Unix())
	return err
}
func (s *SQLiteStateStore) GrantActive(ctx context.Context, id string) (bool, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_grants WHERE id=? AND revoked_at IS NULL)`, id).Scan(&active)
	return active == 1, err
}
func (s *SQLiteStateStore) RevokeGrant(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().UTC().Unix(), id)
	return err
}

func (s *SQLiteStateStore) ResolveMember(ctx context.Context, credential string) (string, error) {
	if credential == "" {
		return "", errors.New("app session credential is required")
	}
	var memberID string
	if _, err := s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE expires_at<=?`, time.Now().UTC().Unix()); err != nil {
		return "", err
	}
	err := s.db.QueryRowContext(ctx, `SELECT member_id FROM app_sessions WHERE session_id_hash=? AND expires_at>?`, sessionHash(credential), time.Now().UTC().Unix()).Scan(&memberID)
	return memberID, err
}

func (s *SQLiteStateStore) SaveAppSession(ctx context.Context, credential, memberID string, expiresAt time.Time) error {
	if credential == "" || memberID == "" || !expiresAt.After(time.Now()) {
		return errors.New("app session credential, member, and future expiry are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_sessions(session_id_hash,member_id,expires_at) VALUES(?,?,?) ON CONFLICT(session_id_hash) DO UPDATE SET member_id=excluded.member_id,expires_at=excluded.expires_at`, sessionHash(credential), memberID, expiresAt.UTC().Unix())
	return err
}

func (s *SQLiteStateStore) EstablishMemberSession(ctx context.Context, memberID string, expiresAt time.Time) (string, error) {
	if memberID == "" {
		return "", errors.New("app session member is required")
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	credential := base64.RawURLEncoding.EncodeToString(value)
	if err := s.SaveAppSession(ctx, credential, memberID, expiresAt); err != nil {
		return "", err
	}
	return credential, nil
}

func sessionHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *SQLiteStateStore) save(ctx context.Context, kind, key string, value any, expiry time.Time) error {
	if key == "" || !expiry.After(time.Now()) {
		return errors.New("MCP OAuth state key and future expiry are required")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_state WHERE expires_at<=?`, time.Now().UTC().Unix()); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_oauth_state(kind,state_key,payload,expires_at) VALUES(?,?,?,?) ON CONFLICT(kind,state_key) DO UPDATE SET payload=excluded.payload, expires_at=excluded.expires_at`, kind, key, payload, expiry.UTC().Unix())
	return err
}
func (s *SQLiteStateStore) take(ctx context.Context, kind, key string, now time.Time, target any) error {
	var payload []byte
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_state WHERE expires_at<=?`, now.UTC().Unix()); err != nil {
		return err
	}
	err := s.db.QueryRowContext(ctx, `DELETE FROM mcp_oauth_state WHERE kind=? AND state_key=? AND expires_at>? RETURNING payload`, kind, key, now.UTC().Unix()).Scan(&payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}
func (s *SQLiteStateStore) SaveAuthorization(ctx context.Context, key string, v mcpDomain.Authorization, expiry time.Time) error {
	return s.save(ctx, "authorization", key, v, expiry)
}
func (s *SQLiteStateStore) TakeAuthorization(ctx context.Context, key string, now time.Time) (mcpDomain.Authorization, error) {
	var v mcpDomain.Authorization
	err := s.take(ctx, "authorization", key, now, &v)
	return v, err
}
func (s *SQLiteStateStore) SaveConsent(ctx context.Context, v mcpDomain.Consent) error {
	return s.save(ctx, "consent", v.ID, v, v.ExpiresAt)
}
func (s *SQLiteStateStore) TakeConsent(ctx context.Context, key string, now time.Time) (mcpDomain.Consent, error) {
	var v mcpDomain.Consent
	err := s.take(ctx, "consent", key, now, &v)
	return v, err
}
func (s *SQLiteStateStore) SaveCode(ctx context.Context, v mcpDomain.AuthorizationCode) error {
	return s.save(ctx, "code", v.Value, v, v.ExpiresAt)
}
func (s *SQLiteStateStore) TakeCode(ctx context.Context, key string, now time.Time) (mcpDomain.AuthorizationCode, error) {
	var v mcpDomain.AuthorizationCode
	err := s.take(ctx, "code", key, now, &v)
	return v, err
}

var _ mcpDomain.AuthorizationStateStore = (*SQLiteStateStore)(nil)
var _ mcpDomain.AppSessionResolver = (*SQLiteStateStore)(nil)
var _ mcpDomain.GrantStore = (*SQLiteStateStore)(nil)
