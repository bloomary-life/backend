package x

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/gates/netcall/netcalltest"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

// Synthetic, hand-written fixtures only.
const (
	userToken    = "synthetic-user-token"
	refreshToken = "synthetic-refresh-token"
)

func newConfig(t *testing.T, server *httptest.Server, media netcall.MediaSource) Config {
	t.Helper()
	return Config{HTTP: httpclient.MustNewClient(context.Background()), Media: media, ClientID: "client-1", ClientSecret: "secret-1",
		APIBaseURL: server.URL, AuthorizeURL: "https://x.example/authorize"}
}

func TestXConnectorPKCEFlowRefreshAndRevoke(t *testing.T) {
	// Verifies: BE-070
	var verifierSeen string
	revoked := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token", "/2/oauth2/revoke":
			id, secret, ok := r.BasicAuth()
			require.True(t, ok)
			require.Equal(t, "client-1", id)
			require.Equal(t, "secret-1", secret)
			require.NoError(t, r.ParseForm())
			if r.URL.Path == "/2/oauth2/revoke" {
				revoked = append(revoked, r.PostForm.Get("token"))
				_, _ = w.Write([]byte(`{"revoked":true}`))
				return
			}
			if r.PostForm.Get("grant_type") == "authorization_code" {
				require.Equal(t, "code-1", r.PostForm.Get("code"))
				require.Equal(t, "https://app.example/networks/callback", r.PostForm.Get("redirect_uri"))
				verifierSeen = r.PostForm.Get("code_verifier")
				_, _ = w.Write([]byte(`{"token_type":"bearer","expires_in":7200,"access_token":"` + userToken + `","refresh_token":"` + refreshToken + `","scope":"tweet.write"}`))
				return
			}
			require.Equal(t, refreshToken, r.PostForm.Get("refresh_token"))
			_, _ = w.Write([]byte(`{"token_type":"bearer","expires_in":7200,"access_token":"new-access","refresh_token":"new-refresh"}`))
		case "/2/users/me":
			require.Equal(t, "Bearer "+userToken, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":{"id":"1234567890","username":"bloomary_test","name":"Test"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	connector, err := NewConnector(newConfig(t, server, nil))
	require.NoError(t, err)
	ctx := context.Background()

	start, err := connector.StartConnect(ctx, crosspostDomain.ConnectRequest{RedirectURI: "https://app.example/networks/callback", State: "state-1"})
	require.NoError(t, err)
	authURL, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	query := authURL.Query()
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	sum := sha256.Sum256([]byte(start.CodeVerifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), query.Get("code_challenge"))
	require.Equal(t, Scopes, query.Get("scope"))
	require.Equal(t, "state-1", query.Get("state"))
	require.Len(t, start.CodeVerifier, 43)

	credentials, candidates, err := connector.FinishConnect(ctx, crosspostDomain.ConnectCallback{
		Code: "code-1", RedirectURI: "https://app.example/networks/callback", CodeVerifier: start.CodeVerifier,
	})
	require.NoError(t, err)
	require.Equal(t, start.CodeVerifier, verifierSeen)
	require.Equal(t, userToken, credentials.AccessToken)
	require.Equal(t, refreshToken, credentials.RefreshToken)
	require.False(t, credentials.ExpiresAt.IsZero())
	require.Equal(t, []crosspostDomain.DestinationCandidate{{Network: crosspostDomain.X, AccountID: "1234567890", AccountName: "@bloomary_test"}}, candidates)

	require.NoError(t, connector.Check(ctx, credentials))
	refreshed, err := connector.Refresh(ctx, credentials)
	require.NoError(t, err)
	require.Equal(t, "new-access", refreshed.AccessToken)
	require.Equal(t, "new-refresh", refreshed.RefreshToken)

	require.NoError(t, connector.Revoke(ctx, credentials))
	require.Equal(t, []string{refreshToken, userToken}, revoked)
}

func TestXPublisherUploadsVideoInSegmentsThenPosts(t *testing.T) {
	// Verifies: BE-071
	video := make([]byte, segmentBytes+1000) // two segments
	segments := map[string]int{}
	var post map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+userToken, r.Header.Get("Authorization"))
		require.NotContains(t, r.URL.RawQuery, userToken)
		switch {
		case r.URL.Path == "/2/media/upload/initialize":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "video/mp4", body["media_type"])
			require.Equal(t, "tweet_video", body["media_category"])
			require.EqualValues(t, len(video), body["total_bytes"])
			_, _ = w.Write([]byte(`{"data":{"id":"1880028106020515840","media_key":"13_1880028106020515840","expires_after_secs":86400}}`))
		case r.URL.Path == "/2/media/upload/1880028106020515840/append":
			require.NoError(t, r.ParseMultipartForm(16<<20))
			file, _, err := r.FormFile("media")
			require.NoError(t, err)
			data, _ := io.ReadAll(file)
			segments[r.FormValue("segment_index")] = len(data)
			_, _ = w.Write([]byte(`{"data":{"expires_at":1893456000}}`))
		case r.URL.Path == "/2/media/upload/1880028106020515840/finalize":
			_, _ = w.Write([]byte(`{"data":{"id":"1880028106020515840","processing_info":{"state":"pending","check_after_secs":0}}}`))
		case r.URL.Path == "/2/media/upload" && r.Method == http.MethodGet:
			require.Equal(t, "STATUS", r.URL.Query().Get("command"))
			require.Equal(t, "1880028106020515840", r.URL.Query().Get("media_id"))
			_, _ = w.Write([]byte(`{"data":{"id":"1880028106020515840","processing_info":{"state":"succeeded","progress_percent":100}}}`))
		case r.URL.Path == "/2/tweets":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&post))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"1445880548472328192","text":"x"}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	media := netcalltest.FakeMedia{Objects: map[string][]byte{"m/x.mp4": video}, Types: map[string]string{"m/x.mp4": "video/mp4"}}
	publisher, err := NewPublisher(newConfig(t, server, media))
	require.NoError(t, err)

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery:    crosspostDomain.Delivery{Network: crosspostDomain.X, Text: "A short post.", Hashtags: []string{"writing"}, MediaKey: "m/x.mp4"},
		Destination: crosspostDomain.Destination{AccountID: "1234567890"},
		Credentials: crosspostDomain.Credentials{AccessToken: userToken},
	})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusPublished, result.Status)
	require.Equal(t, "1445880548472328192", result.ExternalPostID)
	require.True(t, strings.HasSuffix(result.ExternalURL, "/1445880548472328192"))
	require.Equal(t, map[string]int{"0": segmentBytes, "1": 1000}, segments)
	require.Equal(t, "A short post.\n\n#writing", post["text"])
	require.Equal(t, map[string]any{"media_ids": []any{"1880028106020515840"}}, post["media"])
}
