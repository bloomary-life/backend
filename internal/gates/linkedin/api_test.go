package linkedin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	"bloomary-api/internal/metrics"
	httpclientMocks "bloomary-api/internal/mocks/platform/httpclient"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/platform/ratelimiter"
)

// Mock-based unit tests (Phase 1)

type APIMockSuite struct {
	suite.Suite
	mockClient   *httpclientMocks.MockClient
	mockCounters metrics.ProductCounters
	api          *API
	ctx          context.Context
}

type noOpRateLimiter struct{}

func (noOpRateLimiter) Wait(context.Context) error {
	return nil
}

func newTestAPI(
	t testing.TB, clientID, clientSecret, baseURL, oauthBaseURL string, client httpclient.Client,
) *API {
	t.Helper()
	api, err := New(clientID, clientSecret, baseURL, oauthBaseURL, client, noOpRateLimiter{}, metrics.ProductCounters{})
	if err != nil {
		t.Fatalf("New API: %v", err)
	}
	return api
}

func TestAPIMockSuite(t *testing.T) {
	suite.Run(t, new(APIMockSuite))
}

func (s *APIMockSuite) SetupTest() {
	s.mockClient = new(httpclientMocks.MockClient)
	s.mockCounters = metrics.ProductCounters{}
	s.api = newTestAPI(
		s.T(),
		"test-client-id",
		"test-client-secret",
		"https://api.linkedin.com",
		"https://www.linkedin.com",
		s.mockClient,
	)
	log, _ := logger.New(logger.Output(io.Discard))
	s.ctx = log.ToContext(context.Background())
}

func (s *APIMockSuite) TestExchangeCode_Success() {
	responseBody := `{
		"access_token": "AQVhZ7X8Y9Z",
		"token_type": "Bearer",
		"expires_in": 5184000,
		"scope": "r_liteprofile"
	}`

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}, nil)

	token, err := s.api.ExchangeCode(s.ctx, "auth-code", "http://localhost/callback", "verifier")

	s.Require().NoError(err)
	s.Equal("AQVhZ7X8Y9Z", token.AccessToken)
	s.Equal("Bearer", token.TokenType)
	s.Equal(int64(5184000), token.ExpiresIn)
	s.Equal("r_liteprofile", token.Scope)
	s.Equal(http.StatusOK, token.StatusCode)

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestExchangeCode_RateLimited() {
	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"60"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil)

	_, err := s.api.ExchangeCode(s.ctx, "auth-code", "http://localhost/callback", "verifier")

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRateLimitHit))

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestExchangeCode_Rejected() {
	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"error": "invalid_code"}`)),
		}, nil)

	_, err := s.api.ExchangeCode(s.ctx, "bad-code", "http://localhost/callback", "verifier")

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestExchangeCode_NetworkError() {
	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(nil, errors.New("network error"))

	_, err := s.api.ExchangeCode(s.ctx, "auth-code", "http://localhost/callback", "verifier")

	s.Error(err)
	s.Contains(err.Error(), "network error")

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestUserInfo_Success() {
	responseBody := `{
		"sub": "member-123",
		"name": "John Doe",
		"email": "john@example.com",
		"picture": "https://example.com/photo.jpg"
	}`

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(responseBody)),
		}, nil)

	profile, err := s.api.UserInfo(s.ctx, "access-token")

	s.Require().NoError(err)
	s.Equal("member-123", profile.MemberID)
	s.Equal("John Doe", profile.Name)
	s.Equal("john@example.com", profile.Email)
	s.Equal("https://example.com/photo.jpg", profile.PictureURL)

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestUserInfo_NotFound() {
	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil)

	_, err := s.api.UserInfo(s.ctx, "access-token")

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))

	s.mockClient.AssertExpectations(s.T())
}

func (s *APIMockSuite) TestPublishPost_Success() {
	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"X-Restli-Id": []string{"urn:li:share:123"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil)

	post := linkedinDomain.Post{
		MemberID:   "member-123",
		Commentary: "Test post",
	}

	postID, err := s.api.PublishPost(s.ctx, "access-token", post)

	s.Require().NoError(err)
	s.Equal("urn:li:share:123", postID)

	s.mockClient.AssertExpectations(s.T())
}

// Integration tests using httptest.Server (existing)

type testHTTPClient struct {
	client *http.Client
}

func (c testHTTPClient) Do(req *http.Request, _ ...httpclient.RequestOptionFunc) (*http.Response, error) {
	return c.client.Do(req)
}

func snapshot(api *API, operation string) rateLimitStats {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.stats[operation]
}

func TestAPITracksRateLimitHeadersAndCooldown(t *testing.T) {
	var logs bytes.Buffer
	upstream := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "1")
				w.Header().Set("x-li-fabric", "prod")
				w.Header().Set("x-ratelimit-remaining", "0")
				w.WriteHeader(http.StatusTooManyRequests)
			},
		),
	)
	defer upstream.Close()

	log, err := logger.New(logger.Output(&logs))
	if err != nil {
		t.Fatalf("New logger: %v", err)
	}
	ctx := log.ToContext(context.Background())
	api := newTestAPI(t, "client-id", "secret", upstream.URL, upstream.URL, testHTTPClient{client: upstream.Client()})

	_, err = api.PublishPost(
		ctx, "token", linkedinDomain.Post{
			MemberID:   "member-1",
			Commentary: "Hello LinkedIn",
		},
	)

	if !perror.Is(err, perror.ErrCodeRateLimitHit) {
		t.Fatalf("error = %v, want ErrCodeRateLimitHit", err)
	}
	stats := snapshot(api, "post")
	if stats.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", stats.Status)
	}
	if stats.RetryAfter != "1" {
		t.Fatalf("retryAfter = %q, want 1", stats.RetryAfter)
	}
	if time.Until(stats.CooldownUntil) <= 0 {
		t.Fatalf("cooldownUntil was not set in the future")
	}
	if stats.Headers["Retry-After"] != "1" || stats.Headers["X-Li-Fabric"] != "prod" || stats.Headers["X-Ratelimit-Remaining"] != "0" {
		t.Fatalf("headers = %#v", stats.Headers)
	}
	logText := logs.String()
	for _, want := range []string{
		"operation=post",
		"status=429",
		`retryAfter=\"1\"`,
		"hasMemberID=true",
		`X-Li-Fabric=\"prod\"`,
		`X-Ratelimit-Remaining=\"0\"`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %q: %s", want, logText)
		}
	}
}

func TestAPIReturnsBadGatewayOnServerError(t *testing.T) {
	upstream := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		),
	)
	defer upstream.Close()
	api := newTestAPI(t, "client-id", "secret", upstream.URL, upstream.URL, testHTTPClient{client: upstream.Client()})
	log, err := logger.New(logger.Output(io.Discard))
	if err != nil {
		t.Fatalf("New logger: %v", err)
	}

	_, err = api.PublishPost(
		log.ToContext(context.Background()), "token", linkedinDomain.Post{
			MemberID:   "member-1",
			Commentary: "Hello LinkedIn",
		},
	)

	if !perror.Is(err, perror.ErrCodeBadGateway) {
		t.Fatalf("error = %v, want ErrCodeBadGateway", err)
	}
}

func TestAPIThrottlesAllLinkedInCallsWithSingleLimiter(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case userInfoPath:
					_, _ = w.Write([]byte(`{"sub":"member-1","name":"Kat Bletsch"}`))
				case postsPath:
					w.Header().Set("x-restli-id", "urn:li:share:1")
					w.WriteHeader(http.StatusCreated)
				default:
					t.Fatalf("path = %s", r.URL.Path)
				}
			},
		),
	)
	defer upstream.Close()

	log, err := logger.New(logger.Output(io.Discard))
	if err != nil {
		t.Fatalf("New logger: %v", err)
	}
	ctx := log.ToContext(context.Background())
	api, err := New(
		"client-id", "secret", upstream.URL, upstream.URL, testHTTPClient{client: upstream.Client()},
		ratelimiter.NewRateLimiter(
			ratelimiter.RateLimitConfig{
				Burst: 1,
				RPS:   10,
			},
		),
		metrics.ProductCounters{},
	)
	if err != nil {
		t.Fatalf("New API: %v", err)
	}

	started := time.Now()
	if _, err := api.UserInfo(ctx, "token"); err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if _, err := api.PublishPost(
		ctx, "token", linkedinDomain.Post{MemberID: "member-1", Commentary: "Hello"},
	); err != nil {
		t.Fatalf("PublishPost: %v", err)
	}

	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("elapsed = %s, want throttled delay", elapsed)
	}
}
