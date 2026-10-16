package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	metricsDomain "bloomary-api/internal/domain/metrics"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/platform/ratelimiter"
)

const (
	//nolint:gosec // LinkedIn OAuth endpoint, not a credential.
	tokenPath     = "/oauth/v2/accessToken"
	userInfoPath  = "/v2/userinfo"
	postsPath     = "/rest/posts"
	imagesPath    = "/rest/images"
	documentsPath = "/rest/documents"
)

type API struct {
	clientID     string
	clientSecret string
	baseURL      string
	oauthBaseURL string
	httpClient   httpclient.Client
	rateLimiter  ratelimiter.RateLimiter
	counters     metricsDomain.Recorder
	mu           sync.Mutex
	stats        map[string]rateLimitStats
}

var _ linkedinDomain.Gate = (*API)(nil)

type rateLimitStats struct {
	Status        int
	RetryAfter    string
	CooldownUntil time.Time
	Headers       map[string]string
	UpdatedAt     time.Time
}

func New(
	clientID, clientSecret, baseURL, oauthBaseURL string, httpClient httpclient.Client,
	rateLimiter ratelimiter.RateLimiter, counters metricsDomain.Recorder,
) (*API, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("linkedin api: client id is required")
	}
	if strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("linkedin api: client secret is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("linkedin api: base url is required")
	}
	if strings.TrimSpace(oauthBaseURL) == "" {
		return nil, fmt.Errorf("linkedin api: oauth base url is required")
	}
	if httpClient == nil {
		return nil, fmt.Errorf("linkedin api: http client is required")
	}
	if rateLimiter == nil {
		return nil, fmt.Errorf("linkedin api: rate limiter is required")
	}

	api := &API{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      strings.TrimRight(baseURL, "/"),
		oauthBaseURL: strings.TrimRight(oauthBaseURL, "/"),
		httpClient:   httpClient,
		rateLimiter:  rateLimiter,
		counters:     counters,
		stats:        make(map[string]rateLimitStats),
	}
	return api, nil
}

func (a *API) ExchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (linkedinDomain.Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {a.clientID},
		"client_secret": {a.clientSecret},
	}
	if strings.TrimSpace(codeVerifier) != "" {
		form.Set("code_verifier", strings.TrimSpace(codeVerifier))
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.oauthBaseURL+tokenPath, bytes.NewBufferString(form.Encode()),
	)
	if err != nil {
		return linkedinDomain.Token{}, fmt.Errorf("new linkedin token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bloomary-api/1.0")

	var token tokenResponse
	status, err := a.doJSON(req, "linkedin_token", "token", false, &token)
	if err != nil {
		a.counters.RecordLinkedInAuth(metricsDomain.ResultError)
		return linkedinDomain.Token{}, err
	}

	if token.AccessToken == "" {
		a.counters.RecordLinkedInAuth(metricsDomain.ResultError)
		return linkedinDomain.Token{}, fmt.Errorf("linkedin token response missing access token")
	}

	a.counters.RecordLinkedInAuth(metricsDomain.ResultSuccess)
	return linkedinDomain.Token{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
		ExpiresIn:   token.ExpiresIn,
		StatusCode:  status,
	}, nil
}

func (a *API) UserInfo(ctx context.Context, accessToken string) (linkedinDomain.Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+userInfoPath, nil)
	if err != nil {
		return linkedinDomain.Profile{}, fmt.Errorf("new linkedin userinfo request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bloomary-api/1.0")

	var profile profileResponse
	if _, err := a.doJSON(req, "linkedin_userinfo", "userinfo", false, &profile); err != nil {
		return linkedinDomain.Profile{}, err
	}

	if profile.MemberID == "" {
		return linkedinDomain.Profile{}, fmt.Errorf("linkedin userinfo response missing sub")
	}

	return linkedinDomain.Profile{
		MemberID:   profile.MemberID,
		Name:       profile.Name,
		PictureURL: profile.PictureURL,
		Email:      profile.Email,
	}, nil
}

// escapeCommentary encodes plain text for LinkedIn's little text format.
// JSON encoding alone does not escape characters reserved by that grammar.
func escapeCommentary(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if strings.ContainsRune(`|{}@[]()<>#\*_~`, character) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}

func (a *API) PublishPost(ctx context.Context, accessToken string, post linkedinDomain.Post) (string, error) {
	visibility := post.Visibility
	if visibility == "" {
		visibility = "PUBLIC"
	}

	payload := map[string]any{
		"author":     "urn:li:person:" + post.MemberID,
		"commentary": escapeCommentary(post.Commentary),
		"visibility": visibility,
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []any{},
			"thirdPartyDistributionChannels": []any{},
		},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	if post.MediaURN != "" {
		payload["content"] = map[string]any{
			"media": map[string]any{
				"title": post.MediaTitle,
				"id":    post.MediaURN,
			},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		a.counters.RecordPostPublish(metricsDomain.ResultError)
		return "", fmt.Errorf("marshal linkedin post payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+postsPath, bytes.NewReader(body))
	if err != nil {
		a.counters.RecordPostPublish(metricsDomain.ResultError)
		return "", fmt.Errorf("new linkedin post request: %w", err)
	}

	a.setLinkedInHeaders(req, accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.do(req, "linkedin_post", "post", post.MemberID != "")
	if err != nil {
		a.counters.RecordPostPublish(metricsDomain.ResultError)
		return "", err
	}
	defer resp.Body.Close()
	a.counters.RecordPostPublish(metricsDomain.ResultSuccess)
	return resp.Header.Get("x-restli-id"), nil
}

func (a *API) UploadMedia(
	ctx context.Context, accessToken string, upload linkedinDomain.Upload, contentType string, body io.Reader,
) (string, error) {
	uploadURL, mediaURN, err := a.initializeUpload(ctx, accessToken, upload)
	if err != nil {
		a.counters.RecordPostPublish(metricsDomain.ResultError)
		return "", err
	}

	if err := a.upload(ctx, uploadURL, string(upload.Kind), upload.Owner != "", contentType, body); err != nil {
		a.counters.RecordPostPublish(metricsDomain.ResultError)
		return "", err
	}

	return mediaURN, nil
}

func (a *API) initializeUpload(ctx context.Context, accessToken string, upload linkedinDomain.Upload) (
	string, string, error,
) {
	endpoint, urnField, err := a.uploadEndpoint(upload.Kind)
	if err != nil {
		return "", "", err
	}

	body, err := json.Marshal(
		map[string]any{
			"initializeUploadRequest": map[string]string{"owner": upload.Owner},
		},
	)
	if err != nil {
		return "", "", fmt.Errorf("marshal linkedin initialize upload payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("new linkedin initialize upload request: %w", err)
	}

	a.setLinkedInHeaders(req, accessToken)
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		Value struct {
			UploadURL string `json:"uploadUrl"`
			Image     string `json:"image"`
			Document  string `json:"document"`
		} `json:"value"`
	}
	if _, err := a.doJSON(
		req, "linkedin_initialize_upload", string(upload.Kind), upload.Owner != "", &out,
	); err != nil {
		return "", "", err
	}

	mediaURN := out.Value.Image
	if urnField == string(linkedinDomain.UploadKindDocument) {
		mediaURN = out.Value.Document
	}

	if out.Value.UploadURL == "" || mediaURN == "" {
		return "", "", fmt.Errorf("linkedin upload response missing upload url or media urn")
	}

	return out.Value.UploadURL, mediaURN, nil
}

func (a *API) upload(
	ctx context.Context, uploadURL, operation string, hasMemberID bool, contentType string, body io.Reader,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return fmt.Errorf("new linkedin upload request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	resp, err := a.do(req, "linkedin_upload", operation, hasMemberID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (a *API) doJSON(req *http.Request, pathLabel, operation string, hasMemberID bool, out any) (int, error) {
	resp, err := a.do(req, pathLabel, operation, hasMemberID)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("decode linkedin response: %w", err)
	}

	return resp.StatusCode, nil
}

func (a *API) do(req *http.Request, pathLabel, operation string, hasMemberID bool) (*http.Response, error) {
	if until := a.cooldownUntil(operation); !until.IsZero() {
		timer := time.NewTimer(time.Until(until))
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		}
	}

	if a.rateLimiter != nil {
		if err := a.rateLimiter.Wait(req.Context()); err != nil {
			a.counters.RecordRateLimit(metricsDomain.RateLimitLinkedInAPI)
			return nil, perror.New(perror.ErrCodeRateLimitHit, err, "linkedin rate limiter")
		}
	}

	started := time.Now()
	resp, err := a.httpClient.Do(req, httpclient.WithPathLabel(pathLabel))
	duration := time.Since(started).Round(time.Millisecond)
	if err != nil {
		a.logf(
			req.Context(), "linkedin outbound operation=%s status=0 duration=%s retryAfter=%q hasMemberID=%t error=%q",
			operation, duration, "", hasMemberID, err.Error(),
		)
		return nil, fmt.Errorf("do linkedin request: %w", err)
	}

	headers := rateLimitHeaders(resp.Header)
	retryAfter := resp.Header.Get("Retry-After")
	cooldownUntil := time.Time{}
	if resp.StatusCode == http.StatusTooManyRequests {
		cooldownUntil = time.Now().Add(parseRetryAfter(retryAfter))
	}

	a.track(
		operation, rateLimitStats{
			Status:        resp.StatusCode,
			RetryAfter:    retryAfter,
			CooldownUntil: cooldownUntil,
			Headers:       headers,
			UpdatedAt:     time.Now(),
		},
	)
	a.logf(
		req.Context(), "linkedin outbound operation=%s status=%d duration=%s retryAfter=%q hasMemberID=%t headers=%s",
		operation, resp.StatusCode, duration, retryAfter, hasMemberID, formatHeaders(headers),
	)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			a.counters.RecordRateLimit(metricsDomain.RateLimitLinkedInAPI)
			return nil, perror.New(perror.ErrCodeRateLimitHit, fmt.Errorf("linkedin rate limit hit"))
		case resp.StatusCode >= http.StatusInternalServerError:
			return nil, perror.New(
				perror.ErrCodeBadGateway,
				fmt.Errorf("linkedin %s returned %s", operation, resp.Status),
			)
		default:
			return nil, perror.New(
				perror.ErrCodeRejected,
				fmt.Errorf("linkedin %s returned %s", operation, resp.Status),
			)
		}
	}

	return resp, nil
}

func (a *API) cooldownUntil(operation string) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats[operation].CooldownUntil
}

func (a *API) track(operation string, stats rateLimitStats) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats[operation] = stats
}

func (a *API) logf(ctx context.Context, format string, args ...any) {
	logger.FromContext(ctx).Info(fmt.Sprintf(format, args...))
}

func rateLimitHeaders(header http.Header) map[string]string {
	out := make(map[string]string)
	for key, values := range header {
		lower := strings.ToLower(key)
		if lower == "retry-after" || strings.HasPrefix(lower, "x-li-") || strings.HasPrefix(lower, "x-ratelimit-") {
			out[key] = strings.Join(values, ",")
		}
	}

	return out
}

func formatHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Quote(headers[key]))
	}

	return "{" + strings.Join(parts, ",") + "}"
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}

	return 0
}

func (a *API) uploadEndpoint(kind linkedinDomain.UploadKind) (string, string, error) {
	switch kind {
	case linkedinDomain.UploadKindImage:
		return a.baseURL + imagesPath + "?action=initializeUpload", string(linkedinDomain.UploadKindImage), nil
	case linkedinDomain.UploadKindDocument:
		return a.baseURL + documentsPath + "?action=initializeUpload", string(linkedinDomain.UploadKindDocument), nil
	default:
		return "", "", fmt.Errorf("unsupported linkedin upload kind %q", kind)
	}
}

func (a *API) setLinkedInHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Linkedin-Version", "202608")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("User-Agent", "bloomary-api/1.0")
}
