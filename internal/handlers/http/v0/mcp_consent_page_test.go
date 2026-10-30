package v0

import (
	"strings"
	"testing"
)

func TestConsentPagePolicyAllowsTheClientRedirectAfterTheFormPost(t *testing.T) {
	policy := consentPagePolicy("http://localhost:53682/callback?x=1")
	if !strings.Contains(policy, "form-action 'self' http://localhost:53682;") {
		t.Fatalf("form-action must allow the client's redirect origin, got %q", policy)
	}
	if !strings.Contains(consentPagePolicy("https://claude.ai/api/mcp/auth_callback"), "form-action 'self' https://claude.ai;") {
		t.Fatal("https client origin missing")
	}
}

func TestValidMCPRedirectURIAcceptsOnlyLoopbackNamesCSPCanAllow(t *testing.T) {
	for _, uri := range []string{"http://localhost:53682/callback", "http://127.0.0.1:53682/callback", "https://claude.ai/api/mcp/auth_callback"} {
		if !validMCPRedirectURI(uri) {
			t.Fatalf("%s must be accepted", uri)
		}
	}
	if validMCPRedirectURI("http://[::1]:53682/callback") {
		t.Fatal("[::1] cannot be named in CSP form-action, so it must be refused at registration")
	}
}
