package linkedin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	"bloomary-api/internal/platform/logger"
)

type APIIntegrationSuite struct {
	suite.Suite
	server *httptest.Server
	api    *API
	ctx    context.Context
}

func TestAPIIntegrationSuite(t *testing.T) {
	suite.Run(t, new(APIIntegrationSuite))
}

func (s *APIIntegrationSuite) SetupTest() {
	log, _ := logger.New(logger.Output(io.Discard))
	s.ctx = log.ToContext(context.Background())
}

func (s *APIIntegrationSuite) TearDownTest() {
	if s.server != nil {
		s.server.Close()
	}
}

func (s *APIIntegrationSuite) TestExchangeCode_RequestFormat() {
	var receivedRequest *http.Request
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"access_token":"test-token","token_type":"Bearer","expires_in":5184000,"scope":"r_liteprofile"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	token, err := s.api.ExchangeCode(s.ctx, "auth-code", "http://localhost/callback", "code-verifier")

	s.Require().NoError(err)
	s.Equal("test-token", token.AccessToken)
	s.Equal("Bearer", token.TokenType)
	s.Equal(int64(5184000), token.ExpiresIn)
	s.Equal("r_liteprofile", token.Scope)

	// Verify request format
	s.Equal(http.MethodPost, receivedRequest.Method)
	s.Equal("/oauth/v2/accessToken", receivedRequest.URL.Path)
	s.Equal("application/x-www-form-urlencoded", receivedRequest.Header.Get("Content-Type"))
	s.Equal("application/json", receivedRequest.Header.Get("Accept"))
	s.Equal("bloomary-api/1.0", receivedRequest.Header.Get("User-Agent"))

	// Verify form data
	s.Contains(string(receivedBody), "grant_type=authorization_code")
	s.Contains(string(receivedBody), "code=auth-code")
	s.Contains(string(receivedBody), "redirect_uri=http%3A%2F%2Flocalhost%2Fcallback")
	s.Contains(string(receivedBody), "client_id=client-id")
	s.Contains(string(receivedBody), "client_secret=client-secret")
	s.Contains(string(receivedBody), "code_verifier=code-verifier")
}

func (s *APIIntegrationSuite) TestUserInfo_RequestFormat() {
	var receivedRequest *http.Request

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"sub":"member-123","name":"John Doe","email":"john@example.com","picture":"https://example.com/photo.jpg"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	profile, err := s.api.UserInfo(s.ctx, "access-token-xyz")

	s.Require().NoError(err)
	s.Equal("member-123", profile.MemberID)
	s.Equal("John Doe", profile.Name)
	s.Equal("john@example.com", profile.Email)
	s.Equal("https://example.com/photo.jpg", profile.PictureURL)

	// Verify request format
	s.Equal(http.MethodGet, receivedRequest.Method)
	s.Equal("/v2/userinfo", receivedRequest.URL.Path)
	s.Equal("Bearer access-token-xyz", receivedRequest.Header.Get("Authorization"))
	s.Equal("application/json", receivedRequest.Header.Get("Accept"))
	s.Equal("bloomary-api/1.0", receivedRequest.Header.Get("User-Agent"))
}

func (s *APIIntegrationSuite) TestPublishPost_RequestFormat() {
	var receivedRequest *http.Request
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Restli-Id", "urn:li:share:123")
		w.WriteHeader(http.StatusCreated)
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	post := linkedinDomain.Post{
		MemberID:   "member-123",
		Commentary: "𝐓𝐡𝐞 𝐬𝐭𝐚𝐜𝐤:\n• Cloudflare D1 (SQLite)\n• Cloudflare R2 (storage for videos)\n@name [link] <tag> {text}|*_~ \\ #cf #mvp #rehab",
		Visibility: "PUBLIC",
	}

	postID, err := s.api.PublishPost(s.ctx, "access-token", post)

	s.Require().NoError(err)
	s.Equal("urn:li:share:123", postID)

	// Verify request format
	s.Equal(http.MethodPost, receivedRequest.Method)
	s.Equal("/rest/posts", receivedRequest.URL.Path)
	s.Equal("Bearer access-token", receivedRequest.Header.Get("Authorization"))
	s.Equal("application/json", receivedRequest.Header.Get("Content-Type"))

	// Verify request body
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)
	s.Equal(`𝐓𝐡𝐞 𝐬𝐭𝐚𝐜𝐤:
• Cloudflare D1 \(SQLite\)
• Cloudflare R2 \(storage for videos\)
\@name \[link\] \<tag\> \{text\}\|\*\_\~ \\ \#cf \#mvp \#rehab`, body["commentary"])
	s.Equal("PUBLIC", body["visibility"])
}

func (s *APIIntegrationSuite) TestExchangeCode_ErrorResponses() {
	testCases := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		errorContains string
	}{
		{
			name:          "400 Bad Request",
			statusCode:    http.StatusBadRequest,
			responseBody:  `{"error":"invalid_code"}`,
			expectedError: true,
			errorContains: "rejected",
		},
		{
			name:          "401 Unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error":"unauthorized"}`,
			expectedError: true,
			errorContains: "rejected",
		},
		{
			name:          "429 Rate Limited",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  ``,
			expectedError: true,
			errorContains: "rate limit",
		},
		{
			name:          "500 Server Error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error":"internal"}`,
			expectedError: true,
			errorContains: "bad_gateway",
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				w.Write([]byte(tc.responseBody))
			}))

			client := &testHTTPClient{client: s.server.Client()}
			s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

			_, err := s.api.ExchangeCode(s.ctx, "code", "redirect", "verifier")

			if tc.expectedError {
				s.Error(err)
				s.Contains(err.Error(), tc.errorContains)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *APIIntegrationSuite) TestUserInfo_MalformedResponse() {
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"invalid":"json"`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	_, err := s.api.UserInfo(s.ctx, "access-token")

	s.Error(err)
	s.Contains(err.Error(), "decode")
}

func (s *APIIntegrationSuite) TestUserInfo_MissingRequiredFields() {
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"name":"John Doe"}`)) // Missing "sub" field
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	_, err := s.api.UserInfo(s.ctx, "access-token")

	s.Error(err)
	s.Contains(err.Error(), "missing sub")
}

func (s *APIIntegrationSuite) TestRateLimitHeaders_Tracked() {
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api = newTestAPI(s.T(), "client-id", "client-secret", s.server.URL, s.server.URL, client)

	_, err := s.api.ExchangeCode(s.ctx, "code", "redirect", "verifier")

	s.Error(err)

	// Verify rate limit stats were tracked
	s.api.mu.Lock()
	stats := s.api.stats["token"]
	s.api.mu.Unlock()

	s.Equal(http.StatusTooManyRequests, stats.Status)
	s.Equal("60", stats.RetryAfter)
	s.True(stats.CooldownUntil.After(time.Now()))
}
