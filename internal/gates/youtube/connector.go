// Package youtube is the YouTube Data API v3 gate: Google OAuth (offline access) through Bloomary's one Google
// project, and resumable video uploads. Docs (checked 2026-11-03):
// https://developers.google.com/identity/protocols/oauth2/web-server,
// https://developers.google.com/youtube/v3/guides/using_resumable_upload_protocol,
// https://developers.google.com/youtube/v3/docs/videos, https://developers.google.com/youtube/v3/docs/errors.
package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

const (
	DefaultAuthURL       = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL      = "https://oauth2.googleapis.com/token"
	DefaultRevokeURL     = "https://oauth2.googleapis.com/revoke"
	DefaultAPIBaseURL    = "https://www.googleapis.com/youtube/v3"
	DefaultUploadBaseURL = "https://www.googleapis.com/upload/youtube/v3"
	// Scopes: upload videos; read the channel ID and name at connect (channels.list mine=true; the doc doesn't
	// name the scope it needs, youtube.readonly is the documented read scope).
	Scopes = "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"

	network = "youtube"
)

// channelIDPattern: "UC" + 22 characters of base64url.
var channelIDPattern = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)

// Config is Bloomary's Google OAuth web client. Upload carries the video bytes and needs a long timeout (nil = HTTP).
type Config struct {
	HTTP          httpclient.Client
	Upload        httpclient.Client
	Media         netcall.MediaSource
	ClientID      string
	ClientSecret  string
	AuthURL       string
	TokenURL      string
	RevokeURL     string
	APIBaseURL    string
	UploadBaseURL string
}

func (cfg *Config) defaults() error {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return fmt.Errorf("youtube gate: client id and secret are required")
	}
	if cfg.HTTP == nil {
		return fmt.Errorf("youtube gate: http client is required")
	}
	if cfg.Upload == nil {
		cfg.Upload = cfg.HTTP
	}
	for _, field := range []struct {
		value *string
		def   string
	}{{&cfg.AuthURL, DefaultAuthURL}, {&cfg.TokenURL, DefaultTokenURL}, {&cfg.RevokeURL, DefaultRevokeURL},
		{&cfg.APIBaseURL, DefaultAPIBaseURL}, {&cfg.UploadBaseURL, DefaultUploadBaseURL}} {
		if *field.value == "" {
			*field.value = field.def
		}
		*field.value = strings.TrimRight(*field.value, "/")
	}
	return nil
}

// Connector is Google's OAuth 2.0 web-server flow with offline access.
type Connector struct {
	cfg Config
}

var _ crosspostDomain.ProviderConnector = (*Connector)(nil)

func NewConnector(cfg Config) (*Connector, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	return &Connector{cfg: cfg}, nil
}

func (c *Connector) Provider() crosspostDomain.Provider { return crosspostDomain.ProviderGoogle }

// StartConnect: access_type=offline gives a refresh token; prompt=consent shows the consent screen every time so
// a reconnect gets a refresh token again. The web-server page documents no PKCE, so none is sent.
func (c *Connector) StartConnect(_ context.Context, request crosspostDomain.ConnectRequest) (crosspostDomain.ConnectStart, error) {
	query := url.Values{
		"client_id":     {c.cfg.ClientID},
		"redirect_uri":  {request.RedirectURI},
		"response_type": {"code"},
		"scope":         {Scopes},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {request.State},
	}
	return crosspostDomain.ConnectStart{AuthorizationURL: c.cfg.AuthURL + "?" + query.Encode()}, nil
}

func (c *Connector) FinishConnect(ctx context.Context, callback crosspostDomain.ConnectCallback) (crosspostDomain.Credentials, []crosspostDomain.DestinationCandidate, error) {
	credentials, err := c.token(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {callback.Code}, "redirect_uri": {callback.RedirectURI},
	}, "")
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	channel, err := c.channel(ctx, credentials.AccessToken)
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	return credentials, []crosspostDomain.DestinationCandidate{{
		Network: crosspostDomain.YouTube, AccountID: channel.ID, AccountName: channel.Snippet.Title,
	}}, nil
}

func (c *Connector) Check(ctx context.Context, credentials crosspostDomain.Credentials) error {
	_, err := c.channel(ctx, credentials.AccessToken)
	return err
}

// Refresh keeps the stored refresh token when Google doesn't send a new one.
func (c *Connector) Refresh(ctx context.Context, credentials crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	if credentials.RefreshToken == "" {
		return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, "no_refresh_token")
	}
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credentials.RefreshToken}}, credentials.RefreshToken)
}

// Revoke: POST /revoke with the refresh token (or the access token); removes every scope of the project for the user.
func (c *Connector) Revoke(ctx context.Context, credentials crosspostDomain.Credentials) error {
	token := credentials.RefreshToken
	if token == "" {
		token = credentials.AccessToken
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.RevokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return fmt.Errorf("build google revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answer, err := netcall.Send(c.cfg.HTTP, req, "google_oauth_revoke")
	if err != nil {
		return err
	}
	if answer.Status == http.StatusBadRequest {
		return nil // already revoked or expired: nothing left to revoke
	}
	if !answer.OK() {
		return fmt.Errorf("google revoke: status %d", answer.Status)
	}
	return nil
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Connector) token(ctx context.Context, form url.Values, previousRefresh string) (crosspostDomain.Credentials, error) {
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return crosspostDomain.Credentials{}, fmt.Errorf("build google token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answer, err := netcall.Send(c.cfg.HTTP, req, "google_oauth_token")
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	if answer.Status == http.StatusBadRequest || answer.Status == http.StatusUnauthorized {
		var oauthErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(answer.Body, &oauthErr)
		return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, oauthErr.Error)
	}
	if !answer.OK() {
		return crosspostDomain.Credentials{}, fmt.Errorf("google token: status %d", answer.Status)
	}
	var out tokenAnswer
	if err := json.Unmarshal(answer.Body, &out); err != nil || out.AccessToken == "" {
		return crosspostDomain.Credentials{}, fmt.Errorf("google token answer has no access token")
	}
	refresh := out.RefreshToken
	if refresh == "" {
		refresh = previousRefresh
	}
	credentials := crosspostDomain.Credentials{AccessToken: out.AccessToken, RefreshToken: refresh, Scope: out.Scope}
	if out.ExpiresIn > 0 {
		credentials.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return credentials, nil
}

type channel struct {
	ID      string `json:"id"`
	Snippet struct {
		Title string `json:"title"`
	} `json:"snippet"`
}

// channel: GET channels?part=snippet&mine=true (https://developers.google.com/youtube/v3/docs/channels/list).
func (c *Connector) channel(ctx context.Context, accessToken string) (channel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+"/channels?part=snippet&mine=true", nil)
	if err != nil {
		return channel{}, fmt.Errorf("build youtube channels request: %w", err)
	}
	netcall.Bearer(req, accessToken)
	answer, err := netcall.Send(c.cfg.HTTP, req, "youtube_channels_mine")
	if err != nil {
		return channel{}, err
	}
	if !answer.OK() {
		return channel{}, failure(answer)
	}
	var out struct {
		Items []channel `json:"items"`
	}
	if err := json.Unmarshal(answer.Body, &out); err != nil || len(out.Items) == 0 || !channelIDPattern.MatchString(out.Items[0].ID) {
		// No channel on this Google account (youtubeSignupRequired) or an unexpected answer.
		return channel{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, "no_channel")
	}
	return out.Items[0], nil
}

// apiError is the Data API error body: {error:{code, errors:[{reason}]}}.
type apiError struct {
	Error struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Code int `json:"code"`
	} `json:"error"`
}

// failure maps a Data API error (https://developers.google.com/youtube/v3/docs/errors).
func failure(answer netcall.Answer) error {
	var body apiError
	_ = json.Unmarshal(answer.Body, &body)
	reason := fmt.Sprint(answer.Status)
	if len(body.Error.Errors) > 0 && body.Error.Errors[0].Reason != "" {
		reason += "/" + body.Error.Errors[0].Reason
	}
	switch {
	case answer.Status == http.StatusUnauthorized:
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, reason)
	case strings.HasSuffix(reason, "/insufficientPermissions"):
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, reason)
	case strings.HasSuffix(reason, "/quotaExceeded"), strings.HasSuffix(reason, "/uploadLimitExceeded"), answer.Status == http.StatusTooManyRequests:
		// Daily quota (project) or the channel's daily upload limit: try again tomorrow.
		return netcall.Fail(netcall.RateLimited(24*time.Hour), network, reason)
	case answer.Status >= 400 && answer.Status < 500:
		return netcall.Fail(crosspostDomain.ErrContentRejected, network, reason)
	default:
		return fmt.Errorf("youtube: status %s", reason)
	}
}
