package stories

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	feedbackDomain "bloomary-api/internal/domain/feedback"
	"bloomary-api/internal/metrics"
	recaptchaMocks "bloomary-api/internal/mocks/domain/recaptcha"
	resendMocks "bloomary-api/internal/mocks/domain/resend"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type FeedbackStorySuite struct {
	suite.Suite
	Ctx context.Context
}

func TestFeedbackStorySuite(t *testing.T) {
	suite.Run(t, new(FeedbackStorySuite))
}

func (s *FeedbackStorySuite) SetupTest() {
	log, _ := logger.New(logger.Output(io.Discard))
	s.Ctx = log.ToContext(context.Background())
}

func (s *FeedbackStorySuite) TestSubmitFeedback_Success() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	mockRecaptchaGate.On("Verify", s.Ctx, "valid-captcha", "1.2.3.4").Return(nil)
	mockResendGate.On("SendFeedback", s.Ctx, mock.Anything).Return(nil)

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:              "John Doe",
		LinkedInID:        "member-123",
		Email:             "john@example.com",
		Title:             "Great product",
		Message:           "I love using Bloomary!",
		RecaptchaResponse: "valid-captcha",
		ClientIP:          "1.2.3.4",
	})

	s.Require().NoError(err)
	mockResendGate.AssertExpectations(s.T())
	mockRecaptchaGate.AssertExpectations(s.T())
}

func (s *FeedbackStorySuite) TestSubmitFeedback_EmptyMessage() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:    "John Doe",
		Message: "",
	})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))
	s.Contains(err.Error(), "message is required")

	mockResendGate.AssertNotCalled(s.T(), "SendFeedback")
	mockRecaptchaGate.AssertNotCalled(s.T(), "Verify")
}

func (s *FeedbackStorySuite) TestSubmitFeedback_RecaptchaFails() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	mockRecaptchaGate.On("Verify", s.Ctx, "invalid-captcha", "1.2.3.4").
		Return(errors.New("recaptcha failed"))

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:              "John Doe",
		Message:           "Test message",
		RecaptchaResponse: "invalid-captcha",
		ClientIP:          "1.2.3.4",
	})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeRejected))
	s.Contains(err.Error(), "recaptcha verification failed")

	mockResendGate.AssertNotCalled(s.T(), "SendFeedback")
	mockRecaptchaGate.AssertExpectations(s.T())
}

func (s *FeedbackStorySuite) TestSubmitFeedback_SendFails() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	mockRecaptchaGate.On("Verify", s.Ctx, "valid-captcha", "").Return(nil)
	mockResendGate.On("SendFeedback", s.Ctx, mock.Anything).
		Return(errors.New("send failed"))

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:              "John Doe",
		Message:           "Test message",
		RecaptchaResponse: "valid-captcha",
	})

	s.Error(err)
	s.True(perror.Is(err, perror.ErrCodeBadGateway))
	s.Contains(err.Error(), "failed to send feedback")

	mockResendGate.AssertExpectations(s.T())
	mockRecaptchaGate.AssertExpectations(s.T())
}

func (s *FeedbackStorySuite) TestSubmitFeedback_WithAttachments() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	mockRecaptchaGate.On("Verify", s.Ctx, "valid-captcha", "1.2.3.4").Return(nil)
	mockResendGate.On("SendFeedback", s.Ctx, mock.MatchedBy(func(f interface{}) bool {
		return true
	})).Return(nil)

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:              "John Doe",
		Message:           "Test with attachments",
		RecaptchaResponse: "valid-captcha",
		ClientIP:          "1.2.3.4",
		Attachments: []feedbackDomain.Attachment{
			{Filename: "file1.txt", Content: "content1"},
			{Filename: "file2.txt", Content: "content2"},
		},
	})

	s.Require().NoError(err)
	mockResendGate.AssertExpectations(s.T())
	mockRecaptchaGate.AssertExpectations(s.T())
}

func (s *FeedbackStorySuite) TestSubmitFeedback_EmptyRecaptchaStillVerified() {
	mockResendGate := new(resendMocks.MockGate)
	mockRecaptchaGate := new(recaptchaMocks.MockCaptcha)
	counters := metrics.ProductCounters{}

	mockRecaptchaGate.On("Verify", s.Ctx, "", "1.2.3.4").Return(nil)
	mockResendGate.On("SendFeedback", s.Ctx, mock.Anything).Return(nil)

	story, err := NewFeedbackStory(mockResendGate, mockRecaptchaGate, counters)
	s.Require().NoError(err)

	err = story.SubmitFeedback(s.Ctx, feedbackDomain.Input{
		Name:              "John Doe",
		Message:           "Test message",
		RecaptchaResponse: "",
		ClientIP:          "1.2.3.4",
	})

	s.Require().NoError(err)
	mockResendGate.AssertExpectations(s.T())
	mockRecaptchaGate.AssertExpectations(s.T())
}
