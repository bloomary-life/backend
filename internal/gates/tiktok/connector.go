// Package tiktok is the TikTok gate: Login Kit (web) OAuth v2 and Content Posting API inbox upload (FILE_UPLOAD).
// Docs (checked 2026-11-03): https://developers.tiktok.com/doc/login-kit-web,
// https://developers.tiktok.com/docs/en/oauth-get-a-user-access-token,
// https://developers.tiktok.com/docs/en/oauth-refresh-a-user-access-token,
// https://developers.tiktok.com/docs/en/oauth-revoke-a-user-access-token,
// https://developers.tiktok.com/doc/content-posting-api-reference-upload-video,
// https://developers.tiktok.com/doc/content-posting-api-media-transfer-guide,
// https://developers.tiktok.com/doc/content-posting-api-reference-get-video-status.
package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

const (
	DefaultAuthorizeURL = "https://www.tiktok.com/v2/auth/authorize/"
	DefaultAPIBaseURL   = "https://open.tiktokapis.com"
	// Scopes: read open_id and display name; upload to the creator's inbox.
	Scopes = "user.info.basic,video.upload"

	network = "tiktok"
)

// Config is Bloomary's TikTok app. Upload carries video chunks and needs a long timeout (nil = HTTP).
type Config struct {
	HTTP         httpclient.Client
	Upload       httpclient.Client
	Media        netcall.MediaSource
	ClientKey    string
	ClientSecret string
	AuthorizeURL string
	APIBaseURL   string
	// StatusPollInterval and MaxStatusWait bound the wait for TikTok to hand the upload to the inbox
	// (0 = 5 s and 2 minutes; the status endpoint allows 30 requests per minute per token).
	StatusPollInterval time.Duration
	MaxStatusWait      time.Duration
}

func (cfg *Config) defaults() error {
	if strings.TrimSpace(cfg.ClientKey) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return fmt.Errorf("tiktok gate: client key and secret are required")
	}
	if cfg.HTTP == nil {
		return fmt.Errorf("tiktok gate: http client is required")
	}
	if cfg.Upload == nil {
		cfg.Upload = cfg.HTTP
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = DefaultAuthorizeURL
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = DefaultAPIBaseURL
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/")
	if cfg.StatusPollInterval <= 0 {
		cfg.StatusPollInterval = 5 * time.Second
	}
	if cfg.MaxStatusWait <= 0 {
		cfg.MaxStatusWait = 2 * time.Minute
	}
	return nil
}

// Connector is Login Kit for web. The web flow documents no PKCE (only desktop and mobile), so none is sent.
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

func (c *Connector) Provider() crosspostDomain.Provider { return crosspostDomain.ProviderTikTok }

func (c *Connector) StartConnect(_ context.Context, request crosspostDomain.ConnectRequest) (crosspostDomain.ConnectStart, error) {
	query := url.Values{
		"client_key":    {c.cfg.ClientKey},
		"scope":         {Scopes},
		"response_type": {"code"},
		"redirect_uri":  {request.RedirectURI},
		"state":         {request.State},
	}
	return crosspostDomain.ConnectStart{AuthorizationURL: c.cfg.AuthorizeURL + "?" + query.Encode()}, nil
}

func (c *Connector) FinishConnect(ctx context.Context, callback crosspostDomain.ConnectCallback) (crosspostDomain.Credentials, []crosspostDomain.DestinationCandidate, error) {
	credentials, openID, err := c.token(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {callback.Code}, "redirect_uri": {callback.RedirectURI},
	})
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	info, err := c.userInfo(ctx, credentials.AccessToken)
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	if info.OpenID != "" {
		openID = info.OpenID
	}
	if !netcall.SafeID(openID) {
		return crosspostDomain.Credentials{}, nil, fmt.Errorf("tiktok: answer has no valid open_id")
	}
	return credentials, []crosspostDomain.DestinationCandidate{{
		Network: crosspostDomain.TikTok, AccountID: openID, AccountName: info.DisplayName,
	}}, nil
}

func (c *Connector) Check(ctx context.Context, credentials crosspostDomain.Credentials) error {
	_, err := c.userInfo(ctx, credentials.AccessToken)
	return err
}

// Refresh: TikTok rotates refresh tokens; the old one stops working at once, so the new one must be stored.
func (c *Connector) Refresh(ctx context.Context, credentials crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	if credentials.RefreshToken == "" {
		return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, "no_refresh_token")
	}
	refreshed, _, err := c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credentials.RefreshToken}})
	return refreshed, err
}

func (c *Connector) Revoke(ctx context.Context, credentials crosspostDomain.Credentials) error {
	form := url.Values{"client_key": {c.cfg.ClientKey}, "client_secret": {c.cfg.ClientSecret}, "token": {credentials.AccessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIBaseURL+"/v2/oauth/revoke/", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build tiktok revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answer, err := netcall.Send(c.cfg.HTTP, req, "tiktok_oauth_revoke")
	if err != nil {
		return err
	}
	if !answer.OK() {
		return fmt.Errorf("tiktok revoke: status %d", answer.Status)
	}
	return nil
}

// tokenAnswer is the OAuth answer; errors come as {error, error_description, log_id} (oauth-error-handling).
type tokenAnswer struct {
	OpenID       string `json:"open_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Connector) token(ctx context.Context, form url.Values) (crosspostDomain.Credentials, string, error) {
	form.Set("client_key", c.cfg.ClientKey)
	form.Set("client_secret", c.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIBaseURL+"/v2/oauth/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return crosspostDomain.Credentials{}, "", fmt.Errorf("build tiktok token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answer, err := netcall.Send(c.cfg.HTTP, req, "tiktok_oauth_token")
	if err != nil {
		return crosspostDomain.Credentials{}, "", err
	}
	var out tokenAnswer
	_ = json.Unmarshal(answer.Body, &out)
	if out.Error == "invalid_grant" || answer.Status == http.StatusUnauthorized {
		return crosspostDomain.Credentials{}, "", netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, "invalid_grant")
	}
	if !answer.OK() || out.AccessToken == "" {
		return crosspostDomain.Credentials{}, "", fmt.Errorf("tiktok token: status %d %s", answer.Status, out.Error)
	}
	credentials := crosspostDomain.Credentials{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Scope: out.Scope}
	if out.ExpiresIn > 0 {
		credentials.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return credentials, out.OpenID, nil
}

type userInfo struct {
	OpenID      string `json:"open_id"`
	DisplayName string `json:"display_name"`
}

// userInfo: GET /v2/user/info/?fields=open_id,display_name (scope user.info.basic).
func (c *Connector) userInfo(ctx context.Context, accessToken string) (userInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+"/v2/user/info/?fields=open_id,display_name", nil)
	if err != nil {
		return userInfo{}, fmt.Errorf("build tiktok user info request: %w", err)
	}
	netcall.Bearer(req, accessToken)
	var out struct {
		Data struct {
			User userInfo `json:"user"`
		} `json:"data"`
	}
	if err := call(c.cfg.HTTP, req, "tiktok_user_info", &out); err != nil {
		return userInfo{}, err
	}
	return out.Data.User, nil
}

// apiError is {error:{code, message, log_id}}; code is "ok" on success (tiktok-api-v2-error-handling).
type apiError struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// call sends a JSON API request and decodes it, mapping TikTok's error codes to domain errors.
func call(client httpclient.Client, req *http.Request, label string, out any) error {
	answer, err := netcall.Send(client, req, label)
	if err != nil {
		return err
	}
	var body apiError
	_ = json.Unmarshal(answer.Body, &body)
	if !answer.OK() || (body.Error.Code != "" && body.Error.Code != "ok") {
		return failure(answer.Status, body.Error.Code, answer.Header)
	}
	if err := json.Unmarshal(answer.Body, out); err != nil {
		return fmt.Errorf("decode tiktok %s answer: %w", label, err)
	}
	return nil
}

func failure(status int, code string, header http.Header) error {
	if code == "" {
		code = fmt.Sprint(status)
	}
	switch {
	case code == "access_token_invalid" || code == "scope_not_authorized" || status == http.StatusUnauthorized:
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, code)
	case code == "rate_limit_exceeded" || status == http.StatusTooManyRequests:
		return netcall.Fail(netcall.RateLimited(netcall.RetryAfter(header, time.Now())), network, code)
	case code == "spam_risk_too_many_pending_share":
		// At most 5 pending inbox shares per 24 hours.
		return netcall.Fail(netcall.RateLimited(24*time.Hour), network, code)
	case status >= 400 && status < 500:
		return netcall.Fail(crosspostDomain.ErrContentRejected, network, code)
	default:
		return fmt.Errorf("tiktok: status %d %s", status, code)
	}
}
