package v0

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A newly constructed service must accept the same cookie after restart, while
// preserving its expiry, privacy attributes, and durable account-wide revocation.
func TestWebSessionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web-sessions.json")
	config := Config{WebAllowedOrigin: "https://platform.bloomary.life"}
	start := func() *service {
		t.Helper()
		server := &service{config: config}
		if err := server.webSessions.load(path); err != nil {
			t.Fatal(err)
		}
		return server
	}
	server := start()
	response := httptest.NewRecorder()
	if err := server.issueWebSession(response, apiTokenResponse{
		AccessToken: "test-linkedin-token", ExpiresIn: 3600,
		Profile: APILinkedInProfile{MemberID: "test-member"},
	}); err != nil {
		t.Fatal(err)
	}
	cookie := response.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie privacy attributes changed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), cookie.Value) {
		t.Fatal("raw cookie persisted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session file permissions: %v", info.Mode().Perm())
	}
	var expiresAt time.Time
	for _, session := range server.webSessions.sessions {
		expiresAt = session.ExpiresAt
	}
	server = start()
	request := httptest.NewRequest(http.MethodGet, "/api/entitlement", nil)
	request.Header.Set("Authorization", "Bearer "+webSessionMarker)
	request.Header.Set("Origin", config.WebAllowedOrigin)
	request.AddCookie(cookie)
	if !server.authenticateWebSession(httptest.NewRecorder(), request) {
		t.Fatal("session lost after restart")
	}
	if request.Header.Get("Authorization") != "Bearer test-linkedin-token" {
		t.Fatal("restored session did not authenticate")
	}
	for _, session := range server.webSessions.sessions {
		if !session.ExpiresAt.Equal(expiresAt) {
			t.Fatal("restart changed session expiry")
		}
	}
	if err := server.revokeWebSessions(httptest.NewRecorder(), "test-member"); err != nil {
		t.Fatal(err)
	}
	server = start()
	if len(server.webSessions.sessions) != 0 {
		t.Fatal("revoked session restored after restart")
	}
}
