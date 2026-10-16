package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveRouteLabelUsesUnknownForUnmatchedRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/scanner/noise", nil)

	if got := resolveRouteLabel(req); got != "unknown" {
		t.Fatalf("resolveRouteLabel = %q, want unknown", got)
	}
}

func TestResolveRouteLabelUsesPattern(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/feedback", nil)
	req.Pattern = "/api/feedback"

	if got := resolveRouteLabel(req); got != "/api/feedback" {
		t.Fatalf("resolveRouteLabel = %q, want /api/feedback", got)
	}
}
