package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

func TestDownloadFileReadsFileFromBotFilePath(t *testing.T) {
	// Verifies: BE-010
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot123:abc/getFile":
			body, _ := io.ReadAll(r.Body)
			require.JSONEq(t, `{"file_id":"voice-1"}`, string(body))
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/file_1.oga","file_size":4}}`))
		case "/file/bot123:abc/voice/file_1.oga":
			_, _ = w.Write([]byte("OggS"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)

	data, err := api.DownloadFile(context.Background(), "voice-1", 1024)

	require.NoError(t, err)
	require.Equal(t, "OggS", string(data))
}
