package httpclient

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bloomary-api/internal/platform/logger"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestClient_Do_Success(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	ctx := context.Background()
	client := MustNewClient(ctx)

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

func TestClient_Do_NilRequest(t *testing.T) {
	ctx := context.Background()
	client := MustNewClient(ctx)

	resp, err := client.Do(nil)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "cannot be nil")
}

func TestClient_Do_WithPathLabel(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	ctx := context.Background()
	client := MustNewClient(ctx)

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req, WithPathLabel("custom-path"))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

func TestClient_Do_WithCustomTimeout(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	ctx := context.Background()
	client, err := NewClient(ctx, WithRequestTimeout(5*time.Second))
	require.NoError(t, err)

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

func TestClientLogsFromRequestContext(t *testing.T) {
	var logs bytes.Buffer
	log, err := logger.New(logger.Output(&logs))
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client := MustNewClient(context.Background())
	req, err := http.NewRequestWithContext(log.ToContext(context.Background()), http.MethodPost, server.URL+"/token", nil)
	require.NoError(t, err)

	resp, err := client.Do(req, WithPathLabel("token"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	for _, want := range []string{"msg=http_client_request", "host=" + req.URL.Host, "path=token", "method=POST", "status=202"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q: %s", want, logs.String())
		}
	}
}

func TestClient_WithInvalidOptions(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		opts    []ClientOptionFunc
		wantErr bool
	}{
		{
			name:    "negative request timeout",
			opts:    []ClientOptionFunc{WithRequestTimeout(-1 * time.Second)},
			wantErr: true,
		},
		{
			name:    "negative TLS handshake timeout",
			opts:    []ClientOptionFunc{WithTLSHandshakeTimeout(-1 * time.Second)},
			wantErr: true,
		},
		{
			name:    "negative max idle conns",
			opts:    []ClientOptionFunc{WithMaxIdleConns(-1)},
			wantErr: true,
		},
		{
			name:    "negative max conns per host",
			opts:    []ClientOptionFunc{WithMaxConnsPerHost(-1)},
			wantErr: true,
		},
		{
			name:    "negative max idle conns per host",
			opts:    []ClientOptionFunc{WithMaxIdleConnsPerHost(-1)},
			wantErr: true,
		},
		{
			name:    "empty metrics client name",
			opts:    []ClientOptionFunc{WithMetrics("", nil, nil, nil)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(ctx, tt.opts...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClientMetricsUseStableInFlightLabel(t *testing.T) {
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "test_http_duration_seconds"}, []string{"client", "host", "path", "method", "status"})
	inFlight := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_http_in_flight"}, []string{"client", "host", "path", "method", "status"})
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_http_requests_total"}, []string{"client", "host", "path", "method", "status"})
	inFlightSeen := make(chan float64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlightSeen <- readGauge(t, inFlight.WithLabelValues("linkedin", r.Host, "/metrics", http.MethodGet, "in_flight"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(context.Background(), WithMetrics("linkedin", duration, inFlight, counter))
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/metrics", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	if got := <-inFlightSeen; got != 1 {
		t.Fatalf("in-flight gauge during request = %v", got)
	}
	if got := readGauge(t, inFlight.WithLabelValues("linkedin", req.URL.Host, "/metrics", http.MethodGet, "in_flight")); got != 0 {
		t.Fatalf("in-flight gauge after request = %v", got)
	}
	if got := readCounter(t, counter.WithLabelValues("linkedin", req.URL.Host, "/metrics", http.MethodGet, "200")); got != 1 {
		t.Fatalf("counter = %v", got)
	}
}

func readGauge(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return out.GetGauge().GetValue()
}

func readCounter(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return out.GetCounter().GetValue()
}
