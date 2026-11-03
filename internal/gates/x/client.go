// Package x is the X API v2 gate: OAuth 2.0 with PKCE through Bloomary's X app, chunked media upload, posts.
// Docs (checked 2026-11-03): https://docs.x.com/x-api/posts/create-post,
// https://docs.x.com/x-api/media/quickstart/media-upload-chunked,
// https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code,
// https://docs.x.com/fundamentals/authentication/oauth-2-0/user-access-token.
package x

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

const (
	// DefaultAPIBaseURL and DefaultAuthorizeURL are X's public endpoints.
	DefaultAPIBaseURL   = "https://api.x.com"
	DefaultAuthorizeURL = "https://x.com/i/oauth2/authorize"
	// Scopes: post with media, read the account, refresh tokens (offline.access).
	Scopes = "tweet.read tweet.write users.read media.write offline.access"

	network = "x"
)

// Config is Bloomary's X app (OAuth 2.0 confidential client). HTTP serves short calls; Upload serves media
// segments and may have a longer timeout (nil = HTTP).
type Config struct {
	HTTP         httpclient.Client
	Upload       httpclient.Client
	Media        netcall.MediaSource
	ClientID     string
	ClientSecret string
	APIBaseURL   string // empty = DefaultAPIBaseURL
	AuthorizeURL string // empty = DefaultAuthorizeURL
	// MaxProcessingWait bounds the wait for X to process an uploaded video (0 = 10 minutes).
	MaxProcessingWait time.Duration
}

type client struct {
	cfg Config
}

func newClient(cfg Config, needMedia bool) (*client, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, fmt.Errorf("x gate: client id and secret are required")
	}
	if cfg.HTTP == nil {
		return nil, fmt.Errorf("x gate: http client is required")
	}
	if needMedia && cfg.Media == nil {
		return nil, fmt.Errorf("x gate: media source is required")
	}
	if cfg.Upload == nil {
		cfg.Upload = cfg.HTTP
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = DefaultAPIBaseURL
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = DefaultAuthorizeURL
	}
	if cfg.MaxProcessingWait <= 0 {
		cfg.MaxProcessingWait = 10 * time.Minute
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/")
	return &client{cfg: cfg}, nil
}

// problem is X's error body: problem JSON (title, detail, type, status) or {errors:[{code,message}]}.
type problem struct {
	Title  string `json:"title"`
	Type   string `json:"type"`
	Errors []struct {
		Code int `json:"code"`
	} `json:"errors"`
	Status int `json:"status"`
}

// failure maps a non-2xx answer to a domain error. https://docs.x.com/x-api/fundamentals/response-codes-and-errors
func failure(answer netcall.Answer, now time.Time) error {
	code := strconv.Itoa(answer.Status)
	var body problem
	_ = json.Unmarshal(answer.Body, &body)
	if len(body.Errors) > 0 && body.Errors[0].Code != 0 {
		code += "/" + strconv.Itoa(body.Errors[0].Code)
	}
	switch {
	case answer.Status == http.StatusTooManyRequests:
		return netcall.Fail(netcall.RateLimited(netcall.RetryAfter(answer.Header, now)), network, code)
	case answer.Status == http.StatusUnauthorized:
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, code)
	case answer.Status >= 400 && answer.Status < 500:
		return netcall.Fail(crosspostDomain.ErrContentRejected, network, code)
	default:
		return fmt.Errorf("x: status %s", code)
	}
}
