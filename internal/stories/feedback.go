package stories

import (
	"context"
	"fmt"

	feedbackDomain "bloomary-api/internal/domain/feedback"
	metricsDomain "bloomary-api/internal/domain/metrics"
	recaptchaDomain "bloomary-api/internal/domain/recaptcha"
	resendDomain "bloomary-api/internal/domain/resend"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/logger"
)

type feedbackStory struct {
	resendGate    resendDomain.Gate
	recaptchaGate recaptchaDomain.Captcha
	counters      metricsDomain.Recorder
}

// NewFeedbackStory creates a new FeedbackStory implementation.
func NewFeedbackStory(
	resendGate resendDomain.Gate,
	recaptchaGate recaptchaDomain.Captcha,
	counters metricsDomain.Recorder,
) (feedbackDomain.FeedbackStory, error) {
	if resendGate == nil {
		return nil, fmt.Errorf("feedback story: resend gate is required")
	}
	if recaptchaGate == nil {
		return nil, fmt.Errorf("feedback story: recaptcha gate is required")
	}
	if counters == nil {
		return nil, fmt.Errorf("feedback story: counters recorder is required")
	}

	return &feedbackStory{
		resendGate:    resendGate,
		recaptchaGate: recaptchaGate,
		counters:      counters,
	}, nil
}

// SubmitFeedback submits user feedback via email.
// It optionally verifies CAPTCHA and sends the feedback email.
func (s *feedbackStory) SubmitFeedback(ctx context.Context, input feedbackDomain.Input) error {
	// Validate message is present
	if input.Message == "" {
		return perror.New(perror.ErrCodeRejected, fmt.Errorf("message is empty"), "feedback message is required")
	}

	// Verify CAPTCHA
	if !input.Authorized {
		if err := s.recaptchaGate.Verify(ctx, input.RecaptchaResponse, input.ClientIP); err != nil {
			logger.FromContext(ctx).WithError(err).Error("recaptcha_verification_failed")
			s.counters.RecordFeedback(metricsDomain.ResultError, metricsDomain.FeedbackKindFeedback)
			return perror.New(perror.ErrCodeRejected, err, "recaptcha verification failed")
		}

	}

	// Build resend feedback
	feedback := resendDomain.Feedback{
		Name:       input.Name,
		LinkedInID: input.LinkedInID,
		Email:      input.Email,
		Title:      input.Title,
		Message:    input.Message,
	}

	// Convert attachments
	if len(input.Attachments) > 0 {
		feedback.Attachments = make([]resendDomain.Attachment, 0, len(input.Attachments))
		for _, att := range input.Attachments {
			feedback.Attachments = append(feedback.Attachments, resendDomain.Attachment{
				Filename: att.Filename,
				Content:  att.Content,
			})
		}
	}

	// Send feedback via Resend
	if err := s.resendGate.SendFeedback(ctx, feedback); err != nil {
		logger.FromContext(ctx).WithError(err).Error("feedback_send_failed")
		s.counters.RecordFeedback(metricsDomain.ResultError, metricsDomain.FeedbackKindFeedback)
		return perror.New(perror.ErrCodeBadGateway, err, "failed to send feedback")
	}

	// Record success metrics
	s.counters.RecordFeedback(metricsDomain.ResultSuccess, metricsDomain.FeedbackKindFeedback)
	logger.FromContext(ctx).Info("feedback_sent_successfully")

	return nil
}
