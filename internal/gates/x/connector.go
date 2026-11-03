package x

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

// accountIDPattern: X user IDs are 1–19 digits (the media_ids pattern in the create-post schema uses the same form).
var accountIDPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

// Connector is X's OAuth 2.0 Authorization Code flow with PKCE (S256) for Bloomary's confidential client.
type Connector struct {
	*client
}

var _ crosspostDomain.ProviderConnector = (*Connector)(nil)

func NewConnector(cfg Config) (*Connector, error) {
	c, err := newClient(cfg, false)
	if err != nil {
		return nil, err
	}
	return &Connector{client: c}, nil
}

func (c *Connector) Provider() crosspostDomain.Provider { return crosspostDomain.ProviderX }

// StartConnect builds the authorize URL with a fresh PKCE verifier (kept on the server with the state).
func (c *Connector) StartConnect(_ context.Context, request crosspostDomain.ConnectRequest) (crosspostDomain.ConnectStart, error) {
	verifier, err := newVerifier()
	if err != nil {
		return crosspostDomain.ConnectStart{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {request.RedirectURI},
		"scope":                 {Scopes},
		"state":                 {request.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return crosspostDomain.ConnectStart{AuthorizationURL: c.cfg.AuthorizeURL + "?" + query.Encode(), CodeVerifier: verifier}, nil
}

// FinishConnect exchanges the code and reads the account (GET /2/users/me).
func (c *Connector) FinishConnect(ctx context.Context, callback crosspostDomain.ConnectCallback) (crosspostDomain.Credentials, []crosspostDomain.DestinationCandidate, error) {
	credentials, err := c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {callback.Code},
		"redirect_uri":  {callback.RedirectURI},
		"code_verifier": {callback.CodeVerifier},
	}, "")
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	user, err := c.me(ctx, credentials.AccessToken)
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	return credentials, []crosspostDomain.DestinationCandidate{{
		Network: crosspostDomain.X, AccountID: user.ID, AccountName: "@" + user.Username,
	}}, nil
}

// Check reads the account with the stored token.
func (c *Connector) Check(ctx context.Context, credentials crosspostDomain.Credentials) error {
	_, err := c.me(ctx, credentials.AccessToken)
	return err
}

// Refresh uses the refresh token once; X returns a new refresh token every time (single use).
func (c *Connector) Refresh(ctx context.Context, credentials crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	if credentials.RefreshToken == "" {
		return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, "no_refresh_token")
	}
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credentials.RefreshToken}}, credentials.RefreshToken)
}

// Revoke revokes the refresh token, then the access token (POST /2/oauth2/revoke).
func (c *Connector) Revoke(ctx context.Context, credentials crosspostDomain.Credentials) error {
	for _, token := range []string{credentials.RefreshToken, credentials.AccessToken} {
		if token == "" {
			continue
		}
		req, err := c.formRequest(ctx, "/2/oauth2/revoke", url.Values{"token": {token}})
		if err != nil {
			return err
		}
		answer, err := netcall.Send(c.cfg.HTTP, req, "x_oauth2_revoke")
		if err != nil {
			return err
		}
		if !answer.OK() {
			return failure(answer, time.Now())
		}
	}
	return nil
}

type tokenAnswer struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

// token calls POST /2/oauth2/token with HTTP Basic client authentication (confidential client).
func (c *Connector) token(ctx context.Context, form url.Values, previousRefresh string) (crosspostDomain.Credentials, error) {
	req, err := c.formRequest(ctx, "/2/oauth2/token", form)
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	answer, err := netcall.Send(c.cfg.HTTP, req, "x_oauth2_token")
	if err != nil {
		return crosspostDomain.Credentials{}, err
	}
	if answer.Status == http.StatusBadRequest || answer.Status == http.StatusUnauthorized {
		// invalid_grant: code or refresh token used, expired or revoked.
		return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, network, fmt.Sprint(answer.Status))
	}
	if !answer.OK() {
		return crosspostDomain.Credentials{}, failure(answer, time.Now())
	}
	var out tokenAnswer
	if err := json.Unmarshal(answer.Body, &out); err != nil || out.AccessToken == "" {
		return crosspostDomain.Credentials{}, fmt.Errorf("x token answer has no access token")
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

func (c *Connector) formRequest(ctx context.Context, path string, form url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build x %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret) // base64(client_id:client_secret), as the docs show
	return req, nil
}

type user struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (c *client) me(ctx context.Context, accessToken string) (user, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+"/2/users/me", nil)
	if err != nil {
		return user{}, fmt.Errorf("build x users/me request: %w", err)
	}
	netcall.Bearer(req, accessToken)
	answer, err := netcall.Send(c.cfg.HTTP, req, "x_users_me")
	if err != nil {
		return user{}, err
	}
	if !answer.OK() {
		return user{}, failure(answer, time.Now())
	}
	var out struct {
		Data user `json:"data"`
	}
	if err := json.Unmarshal(answer.Body, &out); err != nil || !accountIDPattern.MatchString(out.Data.ID) {
		return user{}, fmt.Errorf("x users/me answer has no valid user id")
	}
	return out.Data, nil
}

// newVerifier is a 43-character PKCE code verifier (32 random bytes, base64url).
func newVerifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("pkce verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
