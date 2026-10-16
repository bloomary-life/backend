package resend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"

	resendDomain "bloomary-api/internal/domain/resend"
	"bloomary-api/internal/metrics"
	"bloomary-api/internal/platform/httpclient"
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
	s.ctx = context.Background()
}

func (s *APIIntegrationSuite) TearDownTest() {
	if s.server != nil {
		s.server.Close()
	}
}

func (s *APIIntegrationSuite) TestSendFeedback_RequestFormat() {
	var receivedRequest *http.Request
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"email-123"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Great product",
		Message:    "I love using Bloomary!",
	}

	err := s.api.SendFeedback(s.ctx, feedback)

	s.Require().NoError(err)

	// Verify request format
	s.Equal(http.MethodPost, receivedRequest.Method)
	s.Equal("/", receivedRequest.URL.Path)
	s.Equal("Bearer test-api-key", receivedRequest.Header.Get("Authorization"))
	s.Equal("application/json", receivedRequest.Header.Get("Content-Type"))
	s.Equal("application/json", receivedRequest.Header.Get("Accept"))
	s.Equal("bloomary-api/1.0", receivedRequest.Header.Get("User-Agent"))

	// Verify request body
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)
	s.Equal("from@example.com", body["from"])
	s.Contains(body["to"], "feedback@example.com")
	s.Contains(body["subject"].(string), "Bloomary feedback")
	s.Contains(body["subject"].(string), "John Doe")
	s.Equal("john@example.com", body["reply_to"])
	s.Contains(body["text"].(string), "I love using Bloomary!")
}

func (s *APIIntegrationSuite) TestSendFeedback_WithAttachments() {
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"email-123"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Bug report",
		Message:    "Found a bug",
		Attachments: []resendDomain.Attachment{
			{
				Filename: "screenshot.png",
				Content:  "base64-encoded-content",
			},
			{
				Filename: "log.txt",
				Content:  "error-log-content",
			},
		},
	}

	err := s.api.SendFeedback(s.ctx, feedback)

	s.Require().NoError(err)

	// Verify attachments in request body
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)

	attachments, ok := body["attachments"].([]interface{})
	s.True(ok)
	s.Len(attachments, 2)

	att1 := attachments[0].(map[string]interface{})
	s.Equal("screenshot.png", att1["filename"])
	s.Equal("base64-encoded-content", att1["content"])

	att2 := attachments[1].(map[string]interface{})
	s.Equal("log.txt", att2["filename"])
	s.Equal("error-log-content", att2["content"])
}

func (s *APIIntegrationSuite) TestSendFeedback_NoReplyToWhenNoEmail() {
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"email-123"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

	feedback := resendDomain.Feedback{
		Name:       "Anonymous User",
		LinkedInID: "member-123",
		Email:      "", // No email
		Title:      "Feedback",
		Message:    "Some feedback",
	}

	err := s.api.SendFeedback(s.ctx, feedback)

	s.Require().NoError(err)

	// Verify no reply_to in request body
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)
	_, hasReplyTo := body["reply_to"]
	s.False(hasReplyTo, "reply_to should not be present when email is empty")
}

func (s *APIIntegrationSuite) TestSendFeedback_ErrorResponses() {
	testCases := []struct {
		name          string
		statusCode    int
		responseBody  string
		errorContains string
	}{
		{
			name:          "400 Bad Request",
			statusCode:    http.StatusBadRequest,
			responseBody:  `{"error":"invalid_request"}`,
			errorContains: "bad_gateway",
		},
		{
			name:          "401 Unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error":"invalid_api_key"}`,
			errorContains: "bad_gateway",
		},
		{
			name:          "429 Rate Limited",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error":"rate_limit"}`,
			errorContains: "bad_gateway",
		},
		{
			name:          "500 Server Error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error":"internal"}`,
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
			s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

			feedback := resendDomain.Feedback{
				Name:       "John Doe",
				LinkedInID: "member-123",
				Email:      "john@example.com",
				Title:      "Test",
				Message:    "Test message",
			}

			err := s.api.SendFeedback(s.ctx, feedback)

			s.Error(err)
			s.Contains(err.Error(), tc.errorContains)
		})
	}
}

func (s *APIIntegrationSuite) TestSendFeedback_SubjectFormatting() {
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"email-123"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

	feedback := resendDomain.Feedback{
		Name:       "Jane Smith",
		LinkedInID: "member-456",
		Email:      "jane@example.com",
		Title:      "Feature Request",
		Message:    "Please add dark mode",
	}

	err := s.api.SendFeedback(s.ctx, feedback)

	s.Require().NoError(err)

	// Verify subject formatting
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)
	subject := body["subject"].(string)
	s.Contains(subject, "Bloomary feedback")
	s.Contains(subject, "Jane Smith")
}

func (s *APIIntegrationSuite) TestSendFeedback_TextFormatting() {
	var receivedBody []byte

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"email-123"}`))
	}))

	client := &testHTTPClient{client: s.server.Client()}
	s.api, _ = New(client, s.server.URL, "test-api-key", "from@example.com", "feedback@example.com", metrics.ProductCounters{})

	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Bug Report",
		Message:    "The app crashes when I click save",
		Attachments: []resendDomain.Attachment{
			{Filename: "screenshot.png", Content: "base64"},
		},
	}

	err := s.api.SendFeedback(s.ctx, feedback)

	s.Require().NoError(err)

	// Verify text formatting
	var body map[string]interface{}
	json.Unmarshal(receivedBody, &body)
	text := body["text"].(string)

	s.Contains(text, "Bloomary feedback")
	s.Contains(text, "Name: John Doe")
	s.Contains(text, "LinkedIn ID: member-123")
	s.Contains(text, "Email to respond: john@example.com")
	s.Contains(text, "Title: Bug Report")
	s.Contains(text, "Attachments: 1")
	s.Contains(text, "Message:")
	s.Contains(text, "The app crashes when I click save")
}

// testHTTPClient wraps http.Client to implement httpclient.Client interface
type testHTTPClient struct {
	client *http.Client
}

func (c *testHTTPClient) Do(req *http.Request, opts ...httpclient.RequestOptionFunc) (*http.Response, error) {
	return c.client.Do(req)
}
