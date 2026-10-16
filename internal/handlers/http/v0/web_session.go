package v0

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const webSessionCookie = "__Host-bloomary-session"
const webSessionMarker = "web-session"

type webSession struct {
	Token     string
	MemberID  string
	ExpiresAt time.Time
}

type webSessionStore struct {
	mu       sync.Mutex
	path     string
	sessions map[string]webSession
}

// load restores sessions from the persistent data directory before serving requests.
// An empty path is reserved for in-memory test services.
func (store *webSessionStore) load(path string) error {
	store.path = path
	store.sessions = make(map[string]webSession)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read browser sessions: %w", err)
	}
	if err := json.Unmarshal(data, &store.sessions); err != nil {
		return fmt.Errorf("decode browser sessions: %w", err)
	}
	for key, session := range store.sessions {
		if !session.ExpiresAt.After(time.Now()) {
			delete(store.sessions, key)
		}
	}
	return nil
}

// save atomically replaces the private session file while the caller holds mu.
// Cookie values are never persisted; only their hashes and server-side tokens are.
func (store *webSessionStore) save() error {
	if store.path == "" {
		return nil
	}
	data, err := json.Marshal(store.sessions)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".browser-sessions-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), store.path)
}

// issueWebSession persists the session before issuing its HttpOnly cookie.
// LinkedIn credentials never return to browser JavaScript.
func (s *service) issueWebSession(w http.ResponseWriter, token apiTokenResponse) error {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	value := hex.EncodeToString(random[:])
	ttl := time.Duration(token.ExpiresIn) * time.Second
	if ttl <= 0 || ttl > linkedInTokenCacheTTL {
		ttl = linkedInTokenCacheTTL
	}
	now := time.Now()
	s.webSessions.mu.Lock()
	if s.webSessions.sessions == nil {
		s.webSessions.sessions = make(map[string]webSession)
	}
	for key, session := range s.webSessions.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.webSessions.sessions, key)
		}
	}
	if len(s.webSessions.sessions) >= 10000 {
		s.webSessions.mu.Unlock()
		return fmt.Errorf("browser session capacity reached")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	s.webSessions.sessions[key] = webSession{token.AccessToken, token.Profile.MemberID, now.Add(ttl)}
	if err := s.webSessions.save(); err != nil {
		delete(s.webSessions.sessions, key)
		s.webSessions.mu.Unlock()
		return fmt.Errorf("persist browser session: %w", err)
	}
	s.webSessions.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds())})
	return nil
}

func (s *service) authenticateWebSession(w http.ResponseWriter, r *http.Request) bool {
	if bearerToken(r.Header.Get("Authorization")) != webSessionMarker {
		return true
	}
	// The explicit marker and exact Origin check are required even for GETs.
	if r.Header.Get("Origin") == "" || r.Header.Get("Origin") != s.config.WebAllowedOrigin {
		writeAPIError(w, http.StatusForbidden, "invalid_origin", "invalid session origin")
		return false
	}
	cookie, err := r.Cookie(webSessionCookie)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "session_expired", "Reconnect LinkedIn.")
		return false
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(cookie.Value)))
	s.webSessions.mu.Lock()
	session, ok := s.webSessions.sessions[key]
	if ok && !session.ExpiresAt.After(time.Now()) {
		delete(s.webSessions.sessions, key)
		ok = false
	}
	s.webSessions.mu.Unlock()
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "session_expired", "Reconnect LinkedIn.")
		return false
	}
	r.Header.Set("Authorization", "Bearer "+session.Token)
	return true
}

// revokeWebSessions persists account-wide revocation before clearing the cookie.
func (s *service) revokeWebSessions(w http.ResponseWriter, memberID string) error {
	s.webSessions.mu.Lock()
	defer s.webSessions.mu.Unlock()
	removed := make(map[string]webSession)
	for key, session := range s.webSessions.sessions {
		if session.MemberID == memberID {
			removed[key] = session
			delete(s.webSessions.sessions, key)
		}
	}
	if err := s.webSessions.save(); err != nil {
		for key, session := range removed {
			s.webSessions.sessions[key] = session
		}
		return fmt.Errorf("persist browser session revocation: %w", err)
	}
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	return nil
}

func (s *service) webSessionResult(w http.ResponseWriter, raw []byte) ([]byte, error) {
	var result struct {
		Status string           `json:"status"`
		Token  apiTokenResponse `json:"token"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Status != "done" {
		return raw, nil
	}
	if err := s.issueWebSession(w, result.Token); err != nil {
		return nil, err
	}
	result.Token.AccessToken = webSessionMarker
	return json.Marshal(result)
}
