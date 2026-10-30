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
