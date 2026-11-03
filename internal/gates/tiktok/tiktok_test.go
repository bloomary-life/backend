package tiktok

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall/netcalltest"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

// Synthetic, hand-written fixtures only.
const accessToken = "synthetic-tiktok-access"

func config(server *httptest.Server, video []byte) Config {
	return Config{HTTP: httpclient.MustNewClient(context.Background()), ClientKey: "key-1", ClientSecret: "secret-1",
		AuthorizeURL: "https://tiktok.example/auth", APIBaseURL: server.URL, StatusPollInterval: time.Millisecond,
		Media: netcalltest.FakeMedia{Objects: map[string][]byte{"m/t.mp4": video}, Types: map[string]string{"m/t.mp4": "video/mp4"}}}
}

func TestTikTokConnectorLoginKitRefreshRevoke(t *testing.T) {
	// Verifies: BE-079
	var revoked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/oauth/token/":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "key-1", r.PostForm.Get("client_key"))
			if r.PostForm.Get("grant_type") == "authorization_code" {
				_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","expires_in":7200,"open_id":"open-id-1","refresh_token":"refresh-1","refresh_expires_in":15552000,"scope":"user.info.basic,video.upload","token_type":"Bearer"}`))
				return
			}
			require.Equal(t, "refresh-1", r.PostForm.Get("refresh_token"))
			_, _ = w.Write([]byte(`{"access_token":"access-2","expires_in":7200,"open_id":"open-id-1","refresh_token":"refresh-2","token_type":"Bearer"}`))
		case "/v2/user/info/":
			require.Equal(t, "Bearer "+accessToken, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":{"user":{"open_id":"open-id-1","display_name":"Bloomary Test"}},"error":{"code":"ok","message":"","log_id":"synthetic"}}`))
		case "/v2/oauth/revoke/":
			require.NoError(t, r.ParseForm())
			revoked = r.PostForm.Get("token")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	connector, err := NewConnector(config(server, nil))
	require.NoError(t, err)
	ctx := context.Background()

	start, err := connector.StartConnect(ctx, crosspostDomain.ConnectRequest{RedirectURI: "https://app.example/cb", State: "s-1"})
	require.NoError(t, err)
	parsed, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "key-1", parsed.Query().Get("client_key"))
	require.Equal(t, Scopes, parsed.Query().Get("scope"))

	credentials, candidates, err := connector.FinishConnect(ctx, crosspostDomain.ConnectCallback{Code: "code-1", RedirectURI: "https://app.example/cb"})
	require.NoError(t, err)
	require.Equal(t, []crosspostDomain.DestinationCandidate{{Network: crosspostDomain.TikTok, AccountID: "open-id-1", AccountName: "Bloomary Test"}}, candidates)
	require.NoError(t, connector.Check(ctx, credentials))
	refreshed, err := connector.Refresh(ctx, credentials)
	require.NoError(t, err)
	require.Equal(t, "refresh-2", refreshed.RefreshToken, "TikTok rotates refresh tokens")
	require.NoError(t, connector.Revoke(ctx, credentials))
	require.Equal(t, accessToken, revoked)
}

func TestTikTokPublisherInboxUploadInChunks(t *testing.T) {
	// Verifies: BE-080
	video := make([]byte, singleChunkMax+chunkBytes+chunkBytes/2) // 64 MB + 48 MB → 3 chunks of 32 MB, the last 48 MB
	var source map[string]any
	ranges := []string{}
	received := 0
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/post/publish/inbox/video/init/":
			require.Equal(t, "Bearer "+accessToken, r.Header.Get("Authorization"))
			var body map[string]map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			source = body["source_info"]
			_, _ = w.Write([]byte(`{"data":{"publish_id":"v_inbox_file~v2.123","upload_url":"http://` + r.Host + `/upload/?upload_id=1&upload_token=synthetic"},"error":{"code":"ok","message":"","log_id":"x"}}`))
		case "/upload/":
			require.Empty(t, r.Header.Get("Authorization"), "the upload URL carries its own token")
			ranges = append(ranges, r.Header.Get("Content-Range"))
			n, _ := io.Copy(io.Discard, r.Body)
			received += int(n)
			if received == len(video) {
				w.WriteHeader(http.StatusCreated)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
		case "/v2/post/publish/status/fetch/":
			polls++
			status := "PROCESSING_UPLOAD"
			if polls > 1 {
				status = "SEND_TO_USER_INBOX"
			}
			_, _ = w.Write([]byte(`{"data":{"status":"` + status + `","uploaded_bytes":0},"error":{"code":"ok","message":"","log_id":"x"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	publisher, err := NewPublisher(config(server, video))
	require.NoError(t, err)

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery:    crosspostDomain.Delivery{Network: crosspostDomain.TikTok, MediaKey: "m/t.mp4"},
		Destination: crosspostDomain.Destination{AccountID: "open-id-1"},
		Credentials: crosspostDomain.Credentials{AccessToken: accessToken},
	})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusSentToInbox, result.Status)
	require.Equal(t, "v_inbox_file~v2.123", result.ExternalPostID)
	require.Equal(t, "FILE_UPLOAD", source["source"])
	require.EqualValues(t, len(video), source["video_size"])
	require.EqualValues(t, chunkBytes, source["chunk_size"])
	require.EqualValues(t, 3, source["total_chunk_count"])
	require.Equal(t, []string{"bytes 0-33554431/117440512", "bytes 33554432-67108863/117440512", "bytes 67108864-117440511/117440512"}, ranges)
	require.Equal(t, len(video), received)
	require.Equal(t, 2, polls)
}
