// Package metrics defines domain-level contracts for metrics recording.
// Stories and other domain objects depend on these interfaces, not on
// concrete infrastructure implementations.
package metrics

// Recorder is the domain interface for recording product metrics.
// Implementations track events like post publishes, auth attempts,
// feedback submissions, rate limits, and webhook processing.
type Recorder interface {
	RecordPostPublish(result string)
	RecordLinkedInAuth(result string)
	RecordFeedback(result, kind string)
	RecordRateLimit(scope string)
	RecordPaddleWebhook(eventType, result string)
}

// Result labels.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// Feedback kind labels.
const (
	FeedbackKindFeedback     = "feedback"
	FeedbackKindConfirmation = "confirmation"
)

// Rate limit scope labels.
const (
	RateLimitDailyLinkedIn = "daily_linkedin"
	RateLimitLinkedInAPI   = "linkedin_api"
	RateLimitResendAPI     = "resend_api"
	RateLimitRecaptchaAPI  = "recaptcha_api"
	RateLimitEntitlement   = "entitlement"
)

// Paddle webhook labels.
const (
	PaddleWebhookSubscriptionCreated  = "subscription_created"
	PaddleWebhookSubscriptionUpdated  = "subscription_updated"
	PaddleWebhookSubscriptionCanceled = "subscription_canceled"
	PaddleWebhookUnknown              = "unknown"

	PaddleWebhookSuccess      = "success"
	PaddleWebhookIgnored      = "ignored"
	PaddleWebhookUserNotFound = "user_not_found"
	PaddleWebhookError        = "error"
)
