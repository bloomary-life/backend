package meta

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/gates/netcall"
)

// Scopes: list Pages and post to them (feed, photos, videos, Reels), and publish to the Pages' Instagram accounts.
const Scopes = "pages_show_list,pages_read_engagement,pages_manage_posts,instagram_basic,instagram_content_publish"

// Connector is Facebook Login (manual flow). PKCE needs the OIDC flavour Meta marks "still in testing", so the
// code flow uses the app secret on the server; the login is bound to the signed-in member by the stories' state.
type Connector struct {
	*GraphClient
}

var _ crosspostDomain.ProviderConnector = (*Connector)(nil)

func NewConnector(cfg Config) (*Connector, error) {
	graph, err := NewGraphClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Connector{GraphClient: graph}, nil
}

func (c *Connector) Provider() crosspostDomain.Provider { return crosspostDomain.ProviderMeta }

func (c *Connector) StartConnect(_ context.Context, request crosspostDomain.ConnectRequest) (crosspostDomain.ConnectStart, error) {
	query := url.Values{
		"client_id":     {c.cfg.AppID},
		"redirect_uri":  {request.RedirectURI},
		"state":         {request.State},
		"scope":         {Scopes},
		"response_type": {"code"},
	}
	return crosspostDomain.ConnectStart{AuthorizationURL: c.cfg.DialogBaseURL + "/" + GraphVersion + "/dialog/oauth?" + query.Encode()}, nil
}

// FinishConnect: code → user token → long-lived user token (about 60 days) → Pages with their Page tokens (no expiry
// when read with a long-lived user token) → each Page's Instagram professional account.
func (c *Connector) FinishConnect(ctx context.Context, callback crosspostDomain.ConnectCallback) (crosspostDomain.Credentials, []crosspostDomain.DestinationCandidate, error) {
	short, err := c.token(ctx, url.Values{"client_id": {c.cfg.AppID}, "client_secret": {c.cfg.AppSecret},
		"redirect_uri": {callback.RedirectURI}, "code": {callback.Code}})
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	credentials, err := c.token(ctx, url.Values{"grant_type": {"fb_exchange_token"}, "client_id": {c.cfg.AppID},
		"client_secret": {c.cfg.AppSecret}, "fb_exchange_token": {short.AccessToken}})
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	candidates, err := c.pages(ctx, credentials.AccessToken)
	if err != nil {
		return crosspostDomain.Credentials{}, nil, err
	}
	return credentials, candidates, nil
}

// Check reads the person's ID with the long-lived user token.
func (c *Connector) Check(ctx context.Context, credentials crosspostDomain.Credentials) error {
	var me struct {
		ID string `json:"id"`
	}
	return c.call(ctx, http.MethodGet, "/me?fields=id", credentials.AccessToken, nil, "meta_me", &me)
}

// Refresh: Meta documents no refresh token for user tokens; when the long-lived user token runs out the member
// logs in again. Page tokens read with it keep working, so publishing doesn't stop.
func (c *Connector) Refresh(_ context.Context, _ crosspostDomain.Credentials) (crosspostDomain.Credentials, error) {
	return crosspostDomain.Credentials{}, netcall.Fail(crosspostDomain.ErrNeedsReconnect, "meta", "login_again")
}

// Revoke: DELETE /me/permissions removes the app and invalidates all of the person's tokens for it.
func (c *Connector) Revoke(ctx context.Context, credentials crosspostDomain.Credentials) error {
	return c.call(ctx, http.MethodDelete, "/me/permissions", credentials.AccessToken, nil, "meta_revoke", nil)
}

type tokenAnswer struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// token: GET /oauth/access_token. The doc shows only GET with the app secret, code or user token in the query;
// netcall.Send and the shared HTTP client never put that URL in errors or logs.
func (c *Connector) token(ctx context.Context, query url.Values) (crosspostDomain.Credentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.graphURL("/oauth/access_token")+"?"+query.Encode(), nil)
	if err != nil {
		return crosspostDomain.Credentials{}, fmt.Errorf("build meta token request")
	}
	var out tokenAnswer
	if err := c.decode(req, "meta_oauth_access_token", &out); err != nil {
		return crosspostDomain.Credentials{}, err
	}
	if out.AccessToken == "" {
		return crosspostDomain.Credentials{}, fmt.Errorf("meta token answer has no access token")
	}
	credentials := crosspostDomain.Credentials{AccessToken: out.AccessToken}
	if out.ExpiresIn > 0 {
		credentials.ExpiresAt = time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return credentials, nil
}

type accountsAnswer struct {
	Data []struct {
		ID                       string `json:"id"`
		Name                     string `json:"name"`
		AccessToken              string `json:"access_token"`
		InstagramBusinessAccount *struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"instagram_business_account"`
	} `json:"data"`
}

// pages: GET /me/accounts with each Page's token and linked Instagram account.
func (c *Connector) pages(ctx context.Context, userToken string) ([]crosspostDomain.DestinationCandidate, error) {
	query := url.Values{"fields": {"id,name,access_token,instagram_business_account{id,username}"}, "limit": {"100"}}
	var out accountsAnswer
	if err := c.call(ctx, http.MethodGet, "/me/accounts?"+query.Encode(), userToken, nil, "meta_me_accounts", &out); err != nil {
		return nil, err
	}
	candidates := []crosspostDomain.DestinationCandidate{}
	for _, page := range out.Data {
		if !numericID(page.ID) || page.AccessToken == "" {
			continue // no Page token: the person's role on this Page can't post
		}
		pageToken := crosspostDomain.Credentials{AccessToken: page.AccessToken}
		candidates = append(candidates, crosspostDomain.DestinationCandidate{
			Network: crosspostDomain.FacebookPage, AccountID: page.ID, AccountName: page.Name, Token: pageToken,
		})
		if ig := page.InstagramBusinessAccount; ig != nil && numericID(ig.ID) {
			candidates = append(candidates, crosspostDomain.DestinationCandidate{
				Network: crosspostDomain.Instagram, AccountID: ig.ID, AccountName: "@" + ig.Username, Token: pageToken,
			})
		}
	}
	return candidates, nil
}
