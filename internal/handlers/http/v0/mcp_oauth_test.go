package v0

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMCPRegisterClientCanBeVerifiedAfterRestart(t *testing.T) {
	svc := &service{config: Config{
		MCPTokenSigningKey: "test-signing-key",
		MCPResourceURL:     "https://api.bloomary.life/mcp",
		MCPIssuerURL:       "https://api.bloomary.life",
	}}
	request := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewBufferString(`{"client_name":"ChatGPT","redirect_uris":["https://chatgpt.com/connector/oauth/callback"],"token_endpoint_auth_method":"none"}`))
	response := httptest.NewRecorder()
	svc.MCPRegister(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("registration status = %d; want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var registration struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	if registration.ClientID == "" {
		t.Fatal("registration returned an empty client id")
	}

	restarted := &service{config: Config{MCPTokenSigningKey: "test-signing-key"}}
	client, err := restarted.parseMCPClient(registration.ClientID)
	if err != nil {
		t.Fatalf("client id did not survive restart: %v", err)
	}
	if client.Name != "ChatGPT" || len(client.RedirectURIs) != 1 || client.RedirectURIs[0] != "https://chatgpt.com/connector/oauth/callback" {
		t.Fatalf("unexpected registered client: %#v", client)
	}
}

func TestMCPOAuthPKCEAndToken(t *testing.T) {
	service := &service{config: Config{
		MCPTokenSigningKey: "test-signing-key",
		MCPResourceURL:     "https://api.bloomary.life/mcp",
		MCPIssuerURL:       "https://api.bloomary.life",
	}}
	verifier := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("test PKCE verifier length = %d", len(verifier))
	}
	challenge := pkceChallenge(verifier)
	if !validPKCE(verifier, challenge) {
		t.Fatal("valid PKCE verifier did not match challenge")
	}

	token, err := service.signMCPToken(mcpTokenClaims{
		Issuer: service.config.MCPIssuerURL, Subject: "member-1", Audience: service.config.MCPResourceURL,
		IssuedAt: time.Now().Unix(), Expires: time.Now().Add(time.Minute).Unix(), Scope: []string{"drafts:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.parseMCPToken(context.Background(), token)
	if err != nil {
		t.Fatalf("parse signed token: %v", err)
	}
	if claims.Subject != "member-1" || !hasMCPClaim(claims.Scope, "drafts:read") {
		t.Fatalf("unexpected token claims: %#v", claims)
	}
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
