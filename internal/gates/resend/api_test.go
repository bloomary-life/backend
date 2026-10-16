package resend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	resendDomain "bloomary-api/internal/domain/resend"
	"bloomary-api/internal/metrics"
	httpclientMocks "bloomary-api/internal/mocks/platform/httpclient"
	perror "bloomary-api/internal/platform/error"
)

type APISuite struct {
	suite.Suite
	mockClient   *httpclientMocks.MockClient
	mockCounters metrics.ProductCounters
	api          *API
}

func TestAPISuite(t *testing.T) {
	suite.Run(t, new(APISuite))
}

func (s *APISuite) SetupTest() {
	s.mockClient = new(httpclientMocks.MockClient)
	s.mockCounters = metrics.ProductCounters{}
	var err error
	s.api, err = New(
		s.mockClient,
		"https://api.resend.com/emails",
		"test-api-key",
		"from@example.com",
		"feedback@example.com",
		s.mockCounters,
	)
	s.Require().NoError(err)
}

func (s *APISuite) TestSendFeedback_Success() {
	ctx := context.Background()
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Great product",
		Message:    "I love using Bloomary!",
	}

	s.mockClient.On("Do", mock.MatchedBy(func(req *http.Request) bool {
		return req.URL.String() == "https://api.resend.com/emails" &&
			req.Method == http.MethodPost &&
			req.Header.Get("Authorization") == "Bearer test-api-key" &&
			req.Header.Get("Content-Type") == "application/json"
	}), mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"email-123"}`)),
		}, nil)

	err := s.api.SendFeedback(ctx, feedback)

	s.Require().NoError(err)
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestSendFeedback_WithAttachments() {
	ctx := context.Background()
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
		},
	}

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"email-123"}`)),
		}, nil)

	err := s.api.SendFeedback(ctx, feedback)

	s.Require().NoError(err)
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestSendFeedback_APIError() {
	ctx := context.Background()
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Test",
		Message:    "Test message",
	}

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil)

	err := s.api.SendFeedback(ctx, feedback)

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeBadGateway))
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestSendFeedback_RateLimited() {
	ctx := context.Background()
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Test",
		Message:    "Test message",
	}

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil)

	err := s.api.SendFeedback(ctx, feedback)

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeBadGateway))
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestSendFeedback_NetworkError() {
	ctx := context.Background()
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Test",
		Message:    "Test message",
	}

	s.mockClient.On("Do", mock.Anything, mock.Anything).
		Return(nil, errors.New("network error"))

	err := s.api.SendFeedback(ctx, feedback)

	s.Error(err)
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestSendFeedback_ReplyToSet() {
	ctx := context.Background()
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Test",
		Message:    "Test message",
	}

	s.mockClient.On("Do", mock.MatchedBy(func(req *http.Request) bool {
		body, _ := io.ReadAll(req.Body)
		return strings.Contains(string(body), `"reply_to":"john@example.com"`)
	}), mock.Anything).
		Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"email-123"}`)),
		}, nil)

	err := s.api.SendFeedback(ctx, feedback)

	s.Require().NoError(err)
	s.mockClient.AssertExpectations(s.T())
}

func (s *APISuite) TestNew_ValidatesInputs() {
	_, err := New(nil, "https://api.resend.com/emails", "api-key", "from@example.com", "feedback@example.com", s.mockCounters)
	s.Error(err)
	s.Contains(err.Error(), "http client is missing")

	_, err = New(s.mockClient, "", "api-key", "from@example.com", "feedback@example.com", s.mockCounters)
	s.Error(err)
	s.Contains(err.Error(), "resend emails url is missing")

	_, err = New(s.mockClient, "https://api.resend.com/emails", "api-key", "", "feedback@example.com", s.mockCounters)
	s.Error(err)
	s.Contains(err.Error(), "resend from is missing")

	_, err = New(s.mockClient, "https://api.resend.com/emails", "api-key", "from@example.com", "", s.mockCounters)
	s.Error(err)
	s.Contains(err.Error(), "feedback recipient is missing")

	api, err := New(s.mockClient, "https://api.resend.com/emails", "api-key", "from@example.com", "feedback@example.com", s.mockCounters)
	s.NoError(err)
	s.NotNil(api)
}

func (s *APISuite) TestFeedbackText_FormatsCorrectly() {
	feedback := resendDomain.Feedback{
		Name:       "John Doe",
		LinkedInID: "member-123",
		Email:      "john@example.com",
		Title:      "Great product",
		Message:    "I love using Bloomary!",
		Attachments: []resendDomain.Attachment{
			{Filename: "file1.png", Content: "content1"},
			{Filename: "file2.pdf", Content: "content2"},
		},
	}

	text := feedbackText(feedback)

	s.Contains(text, "Bloomary feedback")
	s.Contains(text, "Name: John Doe")
	s.Contains(text, "LinkedIn ID: member-123")
	s.Contains(text, "Email to respond: john@example.com")
	s.Contains(text, "Title: Great product")
	s.Contains(text, "Attachments: 2")
	s.Contains(text, "Message:")
	s.Contains(text, "I love using Bloomary!")
}

func (s *APISuite) TestResendAttachments_FiltersEmpty() {
	attachments := []resendDomain.Attachment{
		{Filename: "file1.png", Content: "content1"},
		{Filename: "", Content: "content2"},  // Empty filename
		{Filename: "file3.pdf", Content: ""}, // Empty content
		{Filename: "file4.jpg", Content: "content4"},
	}

	result := resendAttachments(attachments)

	s.Len(result, 2)
	s.Equal("file1.png", result[0]["filename"])
	s.Equal("content1", result[0]["content"])
	s.Equal("file4.jpg", result[1]["filename"])
	s.Equal("content4", result[1]["content"])
}
