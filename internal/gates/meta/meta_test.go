package meta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall/netcalltest"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

// Synthetic, hand-written fixtures only.
const (
	userToken = "synthetic-user-token"
	pageToken = "synthetic-page-token"
)

type fixedProbe struct{ info crosspostDomain.MediaInfo }

func (f fixedProbe) Probe(context.Context, string) (crosspostDomain.MediaInfo, error) { return f.info, nil }
func (f fixedProbe) Variant(context.Context, string, crosspostDomain.VariantSpec) (crosspostDomain.MediaRef, error) {
	return crosspostDomain.MediaRef{}, nil
}

func config(server *httptest.Server, info crosspostDomain.MediaInfo) Config {
	return Config{HTTP: httpclient.MustNewClient(context.Background()), AppID: "app-1", AppSecret: "secret-1",
		GraphBaseURL: server.URL, DialogBaseURL: "https://facebook.example", RuploadBaseURL: server.URL + "/rupload",
		Media: netcalltest.FakeMedia{URLBase: "https://media.example"}, Probe: fixedProbe{info: info},
		PollInterval: time.Millisecond, MaxPoll: time.Second}
}

func TestMetaConnectorLoginPagesAndInstagram(t *testing.T) {
	// Verifies: BE-072
	revoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch r.URL.Path {
		case "/v26.0/oauth/access_token":
			require.Equal(t, "secret-1", query.Get("client_secret"))
			if query.Get("grant_type") == "fb_exchange_token" {
				require.Equal(t, "short-token", query.Get("fb_exchange_token"))
				_, _ = w.Write([]byte(`{"access_token":"` + userToken + `","token_type":"bearer","expires_in":5183944}`))
				return
			}
			require.Equal(t, "code-1", query.Get("code"))
			_, _ = w.Write([]byte(`{"access_token":"short-token","token_type":"bearer","expires_in":3600}`))
		case "/v26.0/me/accounts":
			require.Equal(t, "Bearer "+userToken, r.Header.Get("Authorization"))
			require.Empty(t, query.Get("access_token"))
			_, _ = w.Write([]byte(`{"data":[{"id":"101","name":"Bloomary Test Page","access_token":"` + pageToken + `","instagram_business_account":{"id":"17841400000000001","username":"bloomary_test"}},{"id":"102","name":"Second Page","access_token":"page-2"}]}`))
		case "/v26.0/me":
			_, _ = w.Write([]byte(`{"id":"900"}`))
		case "/v26.0/me/permissions":
			require.Equal(t, http.MethodDelete, r.Method)
			revoked = true
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	connector, err := NewConnector(config(server, crosspostDomain.MediaInfo{}))
	require.NoError(t, err)
	ctx := context.Background()

	start, err := connector.StartConnect(ctx, crosspostDomain.ConnectRequest{RedirectURI: "https://app.example/cb", State: "s-1"})
	require.NoError(t, err)
	parsed, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "/v26.0/dialog/oauth", parsed.Path)
	require.Equal(t, Scopes, parsed.Query().Get("scope"))
	require.Equal(t, "s-1", parsed.Query().Get("state"))

	credentials, candidates, err := connector.FinishConnect(ctx, crosspostDomain.ConnectCallback{Code: "code-1", RedirectURI: "https://app.example/cb"})
	require.NoError(t, err)
	require.Equal(t, userToken, credentials.AccessToken)
	require.WithinDuration(t, time.Now().Add(60*24*time.Hour), credentials.ExpiresAt, 2*time.Hour)
	require.Len(t, candidates, 3)
	require.Equal(t, crosspostDomain.DestinationCandidate{Network: crosspostDomain.FacebookPage, AccountID: "101", AccountName: "Bloomary Test Page", Token: crosspostDomain.Credentials{AccessToken: pageToken}}, candidates[0])
	require.Equal(t, crosspostDomain.DestinationCandidate{Network: crosspostDomain.Instagram, AccountID: "17841400000000001", AccountName: "@bloomary_test", Token: crosspostDomain.Credentials{AccessToken: pageToken}}, candidates[1])
	require.Equal(t, "102", candidates[2].AccountID)

	require.NoError(t, connector.Check(ctx, credentials))
	require.NoError(t, connector.Revoke(ctx, credentials))
	require.True(t, revoked)
}

func TestFacebookPagePublisherTextImageReelAndVideo(t *testing.T) {
	// Verifies: BE-073
	forms := map[string]url.Values{}
	var ruploadHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/rupload/") {
			require.Equal(t, "/rupload/video-upload/v26.0/555", r.URL.Path)
			ruploadHeader = r.Header.Clone()
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		require.Equal(t, "Bearer "+pageToken, r.Header.Get("Authorization"))
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"permalink_url":"https://www.facebook.com/101/posts/1"}`))
			return
		}
		require.NoError(t, r.ParseForm())
		key := r.URL.Path
		if phase := r.PostForm.Get("upload_phase"); phase != "" {
			key += ":" + phase
		}
		forms[key] = r.PostForm
		switch key {
		case "/v26.0/101/feed":
			_, _ = w.Write([]byte(`{"id":"101_1"}`))
		case "/v26.0/101/photos":
			_, _ = w.Write([]byte(`{"id":"2","post_id":"101_2"}`))
		case "/v26.0/101/video_reels:start":
			_, _ = w.Write([]byte(`{"video_id":"555","upload_url":"https://rupload.facebook.com/video-upload/v26.0/555"}`))
		case "/v26.0/101/video_reels:finish":
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/v26.0/101/videos":
			_, _ = w.Write([]byte(`{"id":"777"}`))
		default:
			t.Fatalf("unexpected %s", key)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	publish := func(info crosspostDomain.MediaInfo, mediaKey string, save func(context.Context, crosspostDomain.Credentials) error) crosspostDomain.PublishResult {
		publisher, err := NewFacebookPagePublisher(config(server, info))
		require.NoError(t, err)
		result, err := publisher.Publish(ctx, crosspostDomain.PublishRequest{
			Delivery:    crosspostDomain.Delivery{Text: "Hello.", Hashtags: []string{"one"}, Title: "Title", MediaKey: mediaKey, AILabel: true},
			Destination: crosspostDomain.Destination{AccountID: "101"}, Credentials: crosspostDomain.Credentials{AccessToken: pageToken},
			SaveProgress: save,
		})
		require.NoError(t, err)
		require.Equal(t, crosspostDomain.StatusPublished, result.Status)
		require.Equal(t, "https://www.facebook.com/101/posts/1", result.ExternalURL)
		return result
	}

	require.Equal(t, "101_1", publish(crosspostDomain.MediaInfo{}, "", nil).ExternalPostID)
	require.Equal(t, "Hello.\n\n#one", forms["/v26.0/101/feed"].Get("message"))

	require.Equal(t, "101_2", publish(crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaImage}, "m/i.jpg", nil).ExternalPostID)
	require.Equal(t, "https://media.example/m/i.jpg?X-Amz-Signature=synthetic", forms["/v26.0/101/photos"].Get("url"))
	require.Equal(t, "Hello.\n\n#one", forms["/v26.0/101/photos"].Get("caption"))

	var saved crosspostDomain.Credentials
	reel := publish(crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaVideo, Seconds: 30, Width: 1080, Height: 1920}, "m/r.mp4",
		func(_ context.Context, progress crosspostDomain.Credentials) error { saved = progress; return nil })
	require.Equal(t, "555", reel.ExternalPostID)
	require.Equal(t, crosspostDomain.Credentials{Scope: reelProgressStep, AccessToken: "555"}, saved)
	require.Equal(t, "OAuth "+pageToken, ruploadHeader.Get("Authorization"))
	require.Equal(t, "https://media.example/m/r.mp4?X-Amz-Signature=synthetic", ruploadHeader.Get("file_url"))
	finish := forms["/v26.0/101/video_reels:finish"]
	require.Equal(t, "555", finish.Get("video_id"))
	require.Equal(t, "PUBLISHED", finish.Get("video_state"))
	require.Equal(t, "true", finish.Get("is_ai_generated"))
	require.Equal(t, "Hello.\n\n#one", finish.Get("description"))

	require.Equal(t, "777", publish(crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaVideo, Seconds: 300, Width: 1920, Height: 1080}, "m/long.mp4", nil).ExternalPostID)
	require.Equal(t, "https://media.example/m/long.mp4?X-Amz-Signature=synthetic", forms["/v26.0/101/videos"].Get("file_url"))
	require.Equal(t, "Title", forms["/v26.0/101/videos"].Get("title"))
}

func TestInstagramPublisherContainerProgressPollPublish(t *testing.T) {
	// Verifies: BE-074
	var container url.Values
	polls := 0
	published := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+pageToken, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v26.0/17841400000000001/media":
			require.NoError(t, r.ParseForm())
			container = r.PostForm
			_, _ = w.Write([]byte(`{"id":"17889455560051444"}`))
		case "/v26.0/17889455560051444":
			polls++
			status := "IN_PROGRESS"
			if polls > 1 {
				status = "FINISHED"
			}
			_, _ = w.Write([]byte(`{"status_code":"` + status + `","id":"17889455560051444"}`))
		case "/v26.0/17841400000000001/media_publish":
			require.NoError(t, r.ParseForm())
			published = r.PostForm.Get("creation_id")
			_, _ = w.Write([]byte(`{"id":"17920238422030506"}`))
		case "/v26.0/17920238422030506":
			_, _ = w.Write([]byte(`{"permalink":"https://www.instagram.com/reel/synthetic/"}`))
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	publisher, err := NewInstagramPublisher(config(server, crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaVideo, Seconds: 30, Width: 1080, Height: 1920}))
	require.NoError(t, err)
	var saved crosspostDomain.Credentials

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery:    crosspostDomain.Delivery{Text: "Caption.", Hashtags: []string{"one", "two"}, MediaKey: "m/r.mp4", AILabel: true},
		Destination: crosspostDomain.Destination{AccountID: "17841400000000001"}, Credentials: crosspostDomain.Credentials{AccessToken: pageToken},
		CoverKey: "m/cover.jpg",
		SaveProgress: func(_ context.Context, progress crosspostDomain.Credentials) error {
			require.Empty(t, published, "progress is saved before publishing")
			saved = progress
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.StatusPublished, result.Status)
	require.Equal(t, "17920238422030506", result.ExternalPostID)
	require.Equal(t, "https://www.instagram.com/reel/synthetic/", result.ExternalURL)
	require.Equal(t, crosspostDomain.Credentials{Scope: igProgressStep, AccessToken: "17889455560051444"}, saved)
	require.Equal(t, "17889455560051444", published)
	require.Equal(t, 2, polls)
	require.Equal(t, "REELS", container.Get("media_type"))
	require.Equal(t, "https://media.example/m/r.mp4?X-Amz-Signature=synthetic", container.Get("video_url"))
	require.Equal(t, "https://media.example/m/cover.jpg?X-Amz-Signature=synthetic", container.Get("cover_url"))
	require.Equal(t, "true", container.Get("share_to_feed"))
	require.Equal(t, "true", container.Get("is_ai_generated"))
	require.Equal(t, "Caption.\n\n#one #two", container.Get("caption"))
}

func TestInstagramPublisherReusesSavedContainer(t *testing.T) {
	// Verifies: BE-074
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v26.0/17889455560051444":
			_, _ = w.Write([]byte(`{"status_code":"FINISHED"}`))
		case "/v26.0/17841400000000001/media_publish":
			_, _ = w.Write([]byte(`{"id":"17920238422030507"}`))
		case "/v26.0/17920238422030507":
			_, _ = w.Write([]byte(`{"permalink":"https://www.instagram.com/p/synthetic/"}`))
		default:
			t.Fatalf("a saved container must not be created again: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	publisher, err := NewInstagramPublisher(config(server, crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaImage}))
	require.NoError(t, err)

	result, err := publisher.Publish(context.Background(), crosspostDomain.PublishRequest{
		Delivery:    crosspostDomain.Delivery{Text: "Caption.", MediaKey: "m/i.jpg"},
		Destination: crosspostDomain.Destination{AccountID: "17841400000000001"}, Credentials: crosspostDomain.Credentials{AccessToken: pageToken},
		Progress:    crosspostDomain.Credentials{Scope: igProgressStep, AccessToken: "17889455560051444"},
	})
	require.NoError(t, err)
	require.Equal(t, "17920238422030507", result.ExternalPostID)
}

func TestDeletionCallbackVerifiesSignedRequest(t *testing.T) {
	// Verifies: BE-075
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"algorithm":"HMAC-SHA256","expires":1900000000,"issued_at":1899990000,"user_id":"218471"}`))
	mac := hmac.New(sha256.New, []byte("synthetic-app-secret"))
	mac.Write([]byte(payload))
	signed := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "." + payload
	callback, err := NewDeletionCallback("synthetic-app-secret")
	require.NoError(t, err)

	request, err := callback.Verify(signed)
	require.NoError(t, err)
	require.Equal(t, "218471", request.UserID)
	require.EqualValues(t, 1899990000, request.IssuedAt)
	answer, err := json.Marshal(DeletionResponse{URL: "https://app.example/deletion?id=abc123", ConfirmationCode: "abc123"})
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"https://app.example/deletion?id=abc123","confirmation_code":"abc123"}`, string(answer))
}
