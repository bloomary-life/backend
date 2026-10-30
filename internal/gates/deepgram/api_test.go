package deepgram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

func TestTranscribeSendsRawAudioWithLanguageDetectionAndOptOut(t *testing.T) {
	// Verifies: BE-010
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/listen", r.URL.Path)
		require.Equal(t, "nova-3-general", r.URL.Query().Get("model"))
		require.Equal(t, "true", r.URL.Query().Get("detect_language"))
		require.Equal(t, "true", r.URL.Query().Get("smart_format"))
		require.Equal(t, "true", r.URL.Query().Get("mip_opt_out"))
		require.Equal(t, "Token test-key", r.Header.Get("Authorization"))
		require.Equal(t, "audio/ogg", r.Header.Get("Content-Type"))
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, "fake-ogg", string(body))
		_, _ = w.Write([]byte(`{"results":{"channels":[{"detected_language":"ru","alternatives":[{"transcript":"Привет, это идея."}]}]}}`))
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "test-key")
	require.NoError(t, err)

	transcript, err := api.Transcribe(context.Background(), []byte("fake-ogg"), "audio/ogg")

	require.NoError(t, err)
	require.Equal(t, "Привет, это идея.", transcript.Text)
	require.Equal(t, "ru", transcript.Language)
}
