package objectstore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestR2OpenStreamsPutStreamUploadsAndSignedURLUsesTTL(t *testing.T) {
	// Verifies: BE-083
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=synthetic-key/"))
		require.Equal(t, "/bucket-1/media/a.mp4", r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "15")
			_, _ = w.Write([]byte("synthetic video"))
		case http.MethodPut:
			require.Equal(t, "video/mp4", r.Header.Get("Content-Type"))
			require.EqualValues(t, 9, r.ContentLength)
			stored, _ = io.ReadAll(r.Body)
		}
	}))
	defer server.Close()
	store, err := NewR2ObjectStore(R2Config{Endpoint: server.URL, Bucket: "bucket-1", AccessKeyID: "synthetic-key", SecretKey: "synthetic-secret"})
	require.NoError(t, err)
	ctx := context.Background()

	body, size, contentType, err := store.Open(ctx, "media/a.mp4")
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "synthetic video", string(data))
	require.EqualValues(t, 15, size)
	require.Equal(t, "video/mp4", contentType)

	require.NoError(t, store.PutStream(ctx, "media/a.mp4", "video/mp4", bytes.NewReader([]byte("new bytes")), 9))
	require.Equal(t, "new bytes", string(stored))

	link, err := store.SignedURL(ctx, "media/a.mp4", time.Hour)
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "3600", parsed.Query().Get("X-Amz-Expires"))
	require.NotEmpty(t, parsed.Query().Get("X-Amz-Signature"))
	require.NotContains(t, link, "synthetic-secret")
}
