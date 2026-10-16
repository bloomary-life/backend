package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	metricsDomain "bloomary-api/internal/domain/metrics"
)

type ProductCounters struct {
	PostsPublish  *prometheus.CounterVec
	LinkedInAuth  *prometheus.CounterVec
	Feedback      *prometheus.CounterVec
	RateLimit     *prometheus.CounterVec
	PaddleWebhook *prometheus.CounterVec
}

func NewProductCounters(
	postsPublish *prometheus.CounterVec,
	linkedInAuth *prometheus.CounterVec,
	feedback *prometheus.CounterVec,
	rateLimit *prometheus.CounterVec,
	paddleWebhook *prometheus.CounterVec,
) ProductCounters {
	counters := ProductCounters{
		PostsPublish:  postsPublish,
		LinkedInAuth:  linkedInAuth,
		Feedback:      feedback,
		RateLimit:     rateLimit,
		PaddleWebhook: paddleWebhook,
	}
	counters.prewarm()
	return counters
}

func (c ProductCounters) RecordPostPublish(result string) {
	if c.PostsPublish != nil {
		c.PostsPublish.WithLabelValues(result).Inc()
	}
}

func (c ProductCounters) RecordLinkedInAuth(result string) {
	if c.LinkedInAuth != nil {
		c.LinkedInAuth.WithLabelValues(result).Inc()
	}
}

func (c ProductCounters) RecordFeedback(result, kind string) {
	if c.Feedback != nil {
		c.Feedback.WithLabelValues(result, kind).Inc()
	}
}

func (c ProductCounters) RecordRateLimit(scope string) {
	if c.RateLimit != nil {
		c.RateLimit.WithLabelValues(scope).Inc()
	}
}

func (c ProductCounters) RecordPaddleWebhook(eventType, result string) {
	if c.PaddleWebhook != nil {
		c.PaddleWebhook.WithLabelValues(eventType, result).Inc()
	}
}

func (c ProductCounters) prewarm() {
	for _, result := range []string{metricsDomain.ResultSuccess, metricsDomain.ResultError} {
		if c.PostsPublish != nil {
			c.PostsPublish.WithLabelValues(result).Add(0)
		}

		if c.LinkedInAuth != nil {
			c.LinkedInAuth.WithLabelValues(result).Add(0)
		}

		for _, kind := range []string{metricsDomain.FeedbackKindFeedback, metricsDomain.FeedbackKindConfirmation} {
			if c.Feedback != nil {
				c.Feedback.WithLabelValues(result, kind).Add(0)
			}
		}
	}

	for _, scope := range []string{
		metricsDomain.RateLimitDailyLinkedIn,
		metricsDomain.RateLimitLinkedInAPI,
		metricsDomain.RateLimitResendAPI,
		metricsDomain.RateLimitRecaptchaAPI,
		metricsDomain.RateLimitEntitlement,
	} {
		if c.RateLimit != nil {
			c.RateLimit.WithLabelValues(scope).Add(0)
		}
	}

	for _, eventType := range []string{
		metricsDomain.PaddleWebhookSubscriptionCreated,
		metricsDomain.PaddleWebhookSubscriptionUpdated,
		metricsDomain.PaddleWebhookSubscriptionCanceled,
		metricsDomain.PaddleWebhookUnknown,
	} {
		for _, result := range []string{
			metricsDomain.PaddleWebhookSuccess,
			metricsDomain.PaddleWebhookIgnored,
			metricsDomain.PaddleWebhookUserNotFound,
			metricsDomain.PaddleWebhookError,
		} {
			if c.PaddleWebhook != nil {
				c.PaddleWebhook.WithLabelValues(eventType, result).Add(0)
			}
		}
	}
}
