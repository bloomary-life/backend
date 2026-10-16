package paddle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"bloomary-api/internal/platform/httpclient"
)

func TestCreateCustomerPortal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer live-key" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"urls":{"general":{"overview":"https://customer-portal.paddle.com/session"}}}}`))
	}))
	defer server.Close()

	client, err := httpclient.NewClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(client, server.URL, "live-key")
	if err != nil {
		t.Fatal(err)
	}
	url, err := api.CreateCustomerPortal(context.Background(), "ctm_123")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://customer-portal.paddle.com/session" {
		t.Fatalf("url = %q", url)
	}
}
