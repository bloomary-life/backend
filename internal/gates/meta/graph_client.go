// Package meta is the Graph API gate for Facebook Pages and Instagram (Instagram API with Facebook Login),
// ported from the feat/facebook-pages branch. Docs (checked 2026-11-03):
// https://developers.facebook.com/docs/facebook-login/guides/advanced/manual-flow/,
// https://developers.facebook.com/docs/graph-api/reference/user/accounts/,
// https://developers.facebook.com/docs/video-api/guides/reels-publishing/,
// https://developers.facebook.com/docs/instagram-platform/content-publishing/,
// https://developers.facebook.com/docs/graph-api/guides/error-handling/.
package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

const (
	// GraphVersion is the Graph API version Bloomary calls (latest on the versions page, 2026-11-03).
	GraphVersion          = "v26.0"
	DefaultGraphBaseURL   = "https://graph.facebook.com"
	DefaultDialogBaseURL  = "https://www.facebook.com"
	DefaultRuploadBaseURL = "https://rupload.facebook.com"

	// mediaURLTTL: how long a network may pull a frozen media file from its signed URL.
	mediaURLTTL = time.Hour
)

// Config is Bloomary's Meta app. Graph calls carry the token in the Authorization header (Bearer, as the
// Instagram publishing guide shows on graph.facebook.com); only Login's code and token exchanges put the app
// secret and tokens in the query, as the manual-flow doc requires, and those URLs never reach logs or errors.
type Config struct {
	HTTP           httpclient.Client
	Media          netcall.MediaSource
	Probe          crosspostDomain.MediaWorker // video length and shape: Reel (3–90 s, vertical) or Page video
	AppID          string
	AppSecret      string
	GraphBaseURL   string
	DialogBaseURL  string
	RuploadBaseURL string
	// PollInterval and MaxPoll bound the wait for an Instagram container (the docs: once a minute, at most 5 minutes).
	PollInterval time.Duration
	MaxPoll      time.Duration
}

// GraphClient is the shared Graph API caller.
type GraphClient struct {
	cfg Config
}

func NewGraphClient(cfg Config) (*GraphClient, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, fmt.Errorf("meta gate: app id and secret are required")
	}
	if cfg.HTTP == nil {
		return nil, fmt.Errorf("meta gate: http client is required")
	}
	for _, field := range []struct {
		value *string
		def   string
	}{{&cfg.GraphBaseURL, DefaultGraphBaseURL}, {&cfg.DialogBaseURL, DefaultDialogBaseURL}, {&cfg.RuploadBaseURL, DefaultRuploadBaseURL}} {
		if *field.value == "" {
			*field.value = field.def
		}
		*field.value = strings.TrimRight(*field.value, "/")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.MaxPoll <= 0 {
		cfg.MaxPoll = 5 * time.Minute
	}
	return &GraphClient{cfg: cfg}, nil
}

func (g *GraphClient) graphURL(path string) string {
	return g.cfg.GraphBaseURL + "/" + GraphVersion + path
}

// call sends a Graph request with the token in the Authorization header and decodes the JSON answer into out.
// form (optional) goes as an urlencoded body.
func (g *GraphClient) call(ctx context.Context, method, path, token string, form url.Values, label string, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, g.graphURL(path), body)
	if err != nil {
		return fmt.Errorf("build meta %s request: %w", label, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		netcall.Bearer(req, token)
	}
	return g.decode(req, label, out)
}

func (g *GraphClient) decode(req *http.Request, label string, out any) error {
	answer, err := netcall.Send(g.cfg.HTTP, req, label)
	if err != nil {
		return err
	}
	if !answer.OK() {
		return graphFailure(answer)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(answer.Body, out); err != nil {
		return fmt.Errorf("decode meta %s answer: %w", label, err)
	}
	return nil
}

// graphError is Graph's error body: {error:{message,type,code,error_subcode,fbtrace_id}}.
type graphError struct {
	Error struct {
		Type    string `json:"type"`
		Code    int    `json:"code"`
		Subcode int    `json:"error_subcode"`
	} `json:"error"`
}

// graphFailure maps Graph error codes (graph-api/guides/error-handling, overview/rate-limiting).
func graphFailure(answer netcall.Answer) error {
	var body graphError
	_ = json.Unmarshal(answer.Body, &body)
	code := strconv.Itoa(body.Error.Code)
	if body.Error.Subcode != 0 {
		code += "/" + strconv.Itoa(body.Error.Subcode)
	}
	if body.Error.Code == 0 {
		code = "http_" + strconv.Itoa(answer.Status)
	}
	switch c := body.Error.Code; {
	case c == 190 || c == 102 || c == 10 || (c >= 200 && c <= 299):
		// Expired, revoked or invalid token, or a permission the member no longer grants.
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, "meta", code)
	case c == 4 || c == 17 || c == 32 || c == 613 || c == 80001 || c == 80002:
		return netcall.Fail(netcall.RateLimited(regainAccess(answer.Header)), "meta", code)
	case c == 1 || c == 2 || c == 341 || answer.Status >= 500:
		return fmt.Errorf("meta: temporary error %s", code)
	case answer.Status == http.StatusUnauthorized:
		return netcall.Fail(crosspostDomain.ErrNeedsReconnect, "meta", code)
	default:
		return netcall.Fail(crosspostDomain.ErrContentRejected, "meta", code)
	}
}

// regainAccess reads estimated_time_to_regain_access (minutes) from X-Business-Use-Case-Usage; the docs give no
// Retry-After header.
func regainAccess(header http.Header) time.Duration {
	var usage map[string][]struct {
		Minutes int `json:"estimated_time_to_regain_access"`
	}
	if json.Unmarshal([]byte(header.Get("X-Business-Use-Case-Usage")), &usage) == nil {
		longest := 0
		for _, entries := range usage {
			for _, entry := range entries {
				longest = max(longest, entry.Minutes)
			}
		}
		if longest > 0 {
			return time.Duration(longest) * time.Minute
		}
	}
	return netcall.DefaultRetryAfter
}

// sent turns an error after the publish request went out into outcome unknown, unless Meta answered definitively.
func sent(err error) error {
	var network crosspostDomain.NetworkError
	if err == nil || errors.As(err, &network) {
		return err
	}
	return netcall.Unknown("meta", err)
}

// numericID: Page, Instagram user, container and video IDs are numeric.
func numericID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
