package platform

import (
	"fmt"
	"strconv"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	APNsKeyPEM string `env:"APNS_KEY_PEM"`
	// APNS_KEY_PATH points at the .p8 on disk; env files cannot carry a multi-line PEM.
	APNsKeyPath        string   `env:"APNS_KEY_PATH"`
	APNsKeyID          string   `env:"APNS_KEY_ID"`
	APNsTeamID         string   `env:"APNS_TEAM_ID"`
	APNsEnvironments   []string `env:"APNS_ENVIRONMENTS" envSeparator:"," envDefault:"production,sandbox"`
	Addr               string   `env:"BLOOMARY_ADDR" envDefault:"0.0.0.0:4178"`
	InfraAddr          string   `env:"BLOOMARY_INFRA_ADDR" envDefault:"0.0.0.0:4444"`
	BloomaryAPIRootURL string   `env:"BLOOMARY_API_ROOT_URL" envDefault:"https://api.bloomary.life/"`
	DatabasePath       string   `env:"BLOOMARY_DB_PATH" envDefault:"/data/bloomary.db"`
	FeedbackTo         string   `env:"FEEDBACK_TO" envDefault:"info@dailymind.eu"`
	LinkedInBaseURL    string   `env:"LINKEDIN_BASE_URL" envDefault:"https://api.linkedin.com"`
	// OAuth client id is public; the required client secret remains env-only.
	LinkedInClientID        string        `env:"LINKEDIN_CLIENT_ID" envDefault:"77mq99fw9300ek"`
	LinkedInClientSecret    string        `env:"LINKEDIN_CLIENT_SECRET" required:"true"`
	LinkedInIOSRedirectURI  string        `env:"BLOOMARY_IOS_CALLBACK_REDIRECT_URI" envDefault:"bloomary://linkedin-connected"`
	LinkedInWebRedirectURI  string        `env:"LINKEDIN_WEB_REDIRECT_URI"`
	LinkedInDailyLimit      PositiveInt   `env:"LINKEDIN_DAILY_REQUEST_LIMIT" envDefault:"20"`
	LinkedInOAuthBaseURL    string        `env:"LINKEDIN_OAUTH_BASE_URL" envDefault:"https://www.linkedin.com"`
	LinkedInOAuthScope      string        `env:"LINKEDIN_OAUTH_SCOPE" envDefault:"openid profile email w_member_social"`
	LinkedInRateLimitBurst  PositiveInt   `env:"LINKEDIN_RATE_LIMIT_BURST" envDefault:"4"`
	LinkedInRateLimitRPS    PositiveFloat `env:"LINKEDIN_RATE_LIMIT_RPS" envDefault:"2"`
	LinkedInCLIFlowTTL      time.Duration `env:"LINKEDIN_CLI_FLOW_TTL" envDefault:"30m"`
	BillingFreeTierDays     PositiveInt   `env:"BILLING_FREE_TIER_DAYS" envDefault:"14"`
	BillingTrialHMACSecret  string        `env:"BILLING_TRIAL_HMAC_SECRET" required:"true"`
	MCPAccessTokenKeyID     string        `env:"MCP_ACCESS_TOKEN_KEY_ID" envDefault:"mcp-v1"`
	MCPAccessTokenSecret    string        `env:"MCP_ACCESS_TOKEN_SECRET"`
	MCPClientSigningSecret  string        `env:"MCP_CLIENT_SIGNING_SECRET"`
	MCPPreviousTokenKeyID   string        `env:"MCP_PREVIOUS_TOKEN_KEY_ID"`
	MCPPreviousTokenSecret  string        `env:"MCP_PREVIOUS_TOKEN_SECRET"`
	MCPPreviousTokenUntil   string        `env:"MCP_PREVIOUS_TOKEN_VERIFY_UNTIL"`
	AppleBundleID           string        `env:"APPLE_BUNDLE_ID" envDefault:"com.dailymind.bloomary"`
	AppleProductID          string        `env:"APPLE_PRODUCT_ID" envDefault:"bloomary.pro.monthly,bloomary.pro.yearly"`
	AppleEnvironment        string        `env:"APPLE_ENVIRONMENT" envDefault:"Production,Sandbox"`
	AppleReviewCode         string        `env:"APPLE_REVIEW_CODE"`
	PaddleWebhookSecret     string        `env:"PADDLE_WEBHOOK_SECRET" required:"true"`
	PaddleSignatureSkew     time.Duration `env:"PADDLE_SIGNATURE_SKEW" envDefault:"5m"`
	PaddleAPIKey            string        `env:"PADDLE_API_KEY"`
	PaddleAPIBaseURL        string        `env:"PADDLE_API_BASE_URL"`
	PaddleEnvironment       string        `env:"PADDLE_ENV" envDefault:"production"`
	LogFatalDelay           time.Duration `env:"LOG_FATAL_DELAY" envDefault:"1m"`
	LogFormat               string        `env:"LOG_FORMAT" envDefault:"text"`
	OutboundRequestTimeout  time.Duration `env:"OUTBOUND_REQUEST_TIMEOUT" envDefault:"12s"`
	RecaptchaSecret         string        `env:"RECAPTCHA_SECRET" required:"true"`
	RecaptchaHostname       string        `env:"RECAPTCHA_HOSTNAME"`
	RecaptchaAction         string        `env:"RECAPTCHA_ACTION"`
	RecaptchaVerifyURL      string        `env:"RECAPTCHA_VERIFY_URL" envDefault:"https://www.google.com/recaptcha/api/siteverify"`
	ResendAPIKey            string        `env:"RESEND_API_KEY" required:"true"`
	ResendEmailsURL         string        `env:"RESEND_EMAILS_URL" envDefault:"https://api.resend.com/emails"`
	ResendFrom              string        `env:"RESEND_FROM" envDefault:"Bloomary <info@dailymind.eu>"`
	R2AccountID             string        `env:"BLOOMARY_R2_ACCOUNT_ID"`
	R2Bucket                string        `env:"BLOOMARY_R2_BUCKET"`
	R2AccessKeyID           string        `env:"BLOOMARY_R2_ACCESS_KEY_ID"`
	R2SecretAccessKey       string        `env:"BLOOMARY_R2_SECRET_ACCESS_KEY"`
	R2Endpoint              string        `env:"BLOOMARY_R2_ENDPOINT"`
	R2DraftImageMaxBytes    PositiveInt   `env:"BLOOMARY_R2_DRAFT_IMAGE_MAX_BYTES" envDefault:"5242880"`
	R2DraftImagePresignTTL  time.Duration `env:"BLOOMARY_R2_DRAFT_IMAGE_PRESIGN_TTL" envDefault:"10m"`
	ScheduleMaxDays         PositiveInt   `env:"SCHEDULED_POST_MAX_AHEAD_DAYS" envDefault:"31"`
	SentryAggregationWindow time.Duration `env:"SENTRY_AGGREGATION_WINDOW" envDefault:"10m"`
	SentryDSN               string        `env:"SENTRY_DSN"`
	SentryEnabled           bool          `env:"SENTRY_ENABLED" envDefault:"false"`
	SentryEnvironment       string        `env:"SENTRY_ENVIRONMENT" envDefault:"production"`
	SentryRelease           string        `env:"SENTRY_RELEASE"`
	SupportAllowedOrigin    string        `env:"SUPPORT_ALLOWED_ORIGIN" envDefault:"https://bloomary.life"`
	WebAllowedOrigin        string        `env:"WEB_ALLOWED_ORIGIN" envDefault:"https://platform.bloomary.life"`
}

type PositiveFloat float64

func (value *PositiveFloat) UnmarshalText(text []byte) error {
	parsed, err := strconv.ParseFloat(string(text), 64)
	if err != nil {
		return err
	}

	if parsed <= 0 {
		return fmt.Errorf("value must be positive")
	}

	*value = PositiveFloat(parsed)
	return nil
}

type PositiveInt int

func (value *PositiveInt) UnmarshalText(text []byte) error {
	parsed, err := strconv.Atoi(string(text))
	if err != nil {
		return err
	}

	if parsed <= 0 {
		return fmt.Errorf("value must be positive")
	}

	*value = PositiveInt(parsed)
	return nil
}

func LoadConfig() (Config, error) {
	var config Config
	if err := env.Parse(&config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func LoadConfigWithEnvironment(environment map[string]string) (Config, error) {
	var config Config
	if err := env.ParseWithOptions(&config, env.Options{Environment: environment}); err != nil {
		return Config{}, err
	}

	return config, nil
}
