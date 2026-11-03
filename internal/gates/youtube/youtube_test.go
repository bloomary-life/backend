package youtube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall/netcalltest"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

// Synthetic, hand-written fixtures only.
const (
	accessToken = "synthetic-google-access"
	channelID   = "UCabcdefghijklmnopqrstuv"
)

func config(server *httptest.Server) Config {
	return Config{HTTP: httpclient.MustNewClient(context.Background()), ClientID: "client-1", ClientSecret: "secret-1",
		AuthURL: "https://accounts.example/auth", TokenURL: server.URL + "/token", RevokeURL: server.URL + "/revoke",
		APIBaseURL: server.URL + "/youtube/v3", UploadBaseURL: server.URL + "/upload/youtube/v3",
		Media: netcalltest.FakeMedia{Objects: map[string][]byte{"m/v.mp4": []byte("synthetic video bytes")}, Types: map[string]string{"m/v.mp4": "video/mp4"}}}
}

func TestYouTubeConnectorOfflineAccessChannelRefreshRevoke(t *testing.T) {
	// Verifies: BE-077
	var revokedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "secret-1", r.PostForm.Get("client_secret"))
			if r.PostForm.Get("grant_type") == "authorization_code" {
				require.Equal(t, "code-1", r.PostForm.Get("code"))
				_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","expires_in":3599,"refresh_token":"synthetic-refresh","scope":"` + Scopes + `","token_type":"Bearer"}`))
				return
			}
			require.Equal(t, "synthetic-refresh", r.PostForm.Get("refresh_token"))
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","expires_in":3599,"token_type":"Bearer"}`))
		case "/youtube/v3/channels":
			require.Equal(t, "Bearer "+accessToken, r.Header.Get("Authorization"))
			require.Equal(t, "true", r.URL.Query().Get("mine"))
			_, _ = w.Write([]byte(`{"items":[{"id":"` + channelID + `","snippet":{"title":"Bloomary Test"}}]}`))
		case "/revoke":
			require.NoError(t, r.ParseForm())
			revokedToken = r.PostForm.Get("token")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	connector, err := NewConnector(config(server))
	require.NoError(t, err)
	ctx := context.Background()

	start, err := connector.StartConnect(ctx, crosspostDomain.ConnectRequest{RedirectURI: "https://app.example/cb", State: "s-1"})
	require.NoError(t, err)
	parsed, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "offline", parsed.Query().Get("access_type"))
	require.Equal(t, "consent", parsed.Query().Get("prompt"))
	require.Equal(t, Scopes, parsed.Query().Get("scope"))

	credentials, candidates, err := connector.FinishConnect(ctx, crosspostDomain.ConnectCallback{Code: "code-1", RedirectURI: "https://app.example/cb"})
	require.NoError(t, err)
	require.Equal(t, []crosspostDomain.DestinationCandidate{{Network: crosspostDomain.YouTube, AccountID: channelID, AccountName: "Bloomary Test"}}, candidates)
	require.NoError(t, connector.Check(ctx, credentials))

	refreshed, err := connector.Refresh(ctx, credentials)
	require.NoError(t, err)
	require.Equal(t, "fresh-access", refreshed.AccessToken)
	require.Equal(t, "synthetic-refresh", refreshed.RefreshToken, "Google sends no new refresh token: keep the stored one")

	require.NoError(t, connector.Revoke(ctx, credentials))
	require.Equal(t, "synthetic-refresh", revokedToken)
}

func TestYouTubePublisherResumableUploadWithChoicesAndProgress(t *testing.T) {
	// Verifies: BE-078
	var resource map[string]map[string]any
	var uploaded []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+accessToken, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/upload/youtube/v3/videos":
			require.Equal(t, "resumable", r.URL.Query().Get("uploadType"))
			require.Equal(t, "21", r.Header.Get("X-Upload-Content-Length"))
			require.Equal(t, "video/mp4", r.Header.Get("X-Upload-Content-Type"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&resource))
			w.Header().Set("Location", server.URL+"/upload/youtube/v3/videos?uploadType=resumable&upload_id=synthetic-session")
		case r.Method == http.MethodPut && r.URL.Query().Get("upload_id") == "synthetic-session":
			uploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"dQw4w9WgXcQ","status":{"uploadStatus":"uploaded"}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	publisher, err := NewPublisher(config(server))
	require.NoError(t, err)
	madeForKids := false
	var saved crosspostDomain.Credentials

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery: crosspostDomain.Delivery{Network: crosspostDomain.YouTube, Title: "A title", Text: "Description.", MediaKey: "m/v.mp4",
			AILabel: true, YouTube: crosspostDomain.YouTubeChoices{Privacy: "unlisted", MadeForKids: &madeForKids}},
		Destination: crosspostDomain.Destination{AccountID: channelID},
		Credentials: crosspostDomain.Credentials{AccessToken: accessToken},
		SaveProgress: func(_ context.Context, progress crosspostDomain.Credentials) error {
			saved = progress
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusPublished, result.Status)
	require.Equal(t, "dQw4w9WgXcQ", result.ExternalPostID)
	require.Equal(t, "synthetic video bytes", string(uploaded))
	require.Equal(t, progressStep, saved.Scope)
	require.Contains(t, saved.AccessToken, "upload_id=synthetic-session")
	require.Equal(t, map[string]any{"title": "A title", "description": "Description."}, resource["snippet"])
	require.Equal(t, map[string]any{"privacyStatus": "unlisted", "selfDeclaredMadeForKids": false, "containsSyntheticMedia": true}, resource["status"])
}

func TestYouTubePublisherResumesASavedSession(t *testing.T) {
	// Verifies: BE-078
	var contentRange string
	var rest []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method, "a saved session is resumed, not started again")
		if r.Header.Get("Content-Range") == "bytes */21" {
			w.Header().Set("Range", "bytes=0-9")
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		contentRange = r.Header.Get("Content-Range")
		rest, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"abcDEF12345"}`))
	}))
	defer server.Close()
	publisher, err := NewPublisher(config(server))
	require.NoError(t, err)
	madeForKids := true

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery:    crosspostDomain.Delivery{Title: "T", MediaKey: "m/v.mp4", YouTube: crosspostDomain.YouTubeChoices{Privacy: "private", MadeForKids: &madeForKids}},
		Destination: crosspostDomain.Destination{AccountID: channelID},
		Credentials: crosspostDomain.Credentials{AccessToken: accessToken},
		Progress:    crosspostDomain.Credentials{Scope: progressStep, AccessToken: server.URL + "/upload/youtube/v3/videos?upload_id=old"},
	})
	require.NoError(t, err)
	require.Equal(t, "abcDEF12345", result.ExternalPostID)
	require.Equal(t, "bytes 10-20/21", contentRange)
	require.Equal(t, "video bytes", string(rest))
}
