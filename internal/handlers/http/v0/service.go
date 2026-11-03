package v0

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	feedbackDomain "bloomary-api/internal/domain/feedback"
	insightDomain "bloomary-api/internal/domain/insight"
	limiterDomain "bloomary-api/internal/domain/limiter"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	mcpDomain "bloomary-api/internal/domain/mcp"
	metricsDomain "bloomary-api/internal/domain/metrics"
	notificationDomain "bloomary-api/internal/domain/notification"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

const (
	maxJSONBodyBytes       = 10 << 20
	maxLinkedInUploadBytes = 20 << 20
)

type Config struct {
	WebSessionPath         string
	Notifications          notificationDomain.Service
	ClientID               string
	IOSCallbackRedirectURI string
	CLICallbackRedirectURI string
	WebCallbackRedirectURI string // defaults to IOSCallbackRedirectURI if empty
	LinkedInOAuthScope     string
	BillingFreeTierDays    int
	AppleReviewCode        string
	PaddleWebhookSecret    string
	PaddleSignatureSkew    time.Duration
	SupportAllowedOrigin   string
	WebAllowedOrigin       string
	ProductCounters        metricsDomain.Recorder
	ProductEvents          metricsDomain.PostPublishRepository
	ScheduledPostInterval  time.Duration
	ScheduleMaxDays        int
	MCPTokenSigningKey     string
	MCPClientSigningKey    string
	MCPAccessTokenKeyID    string
	MCPAccessTokenSecret   string
	MCPPreviousAccessKeyID string
	MCPPreviousAccessKey   string
	MCPPreviousAccessUntil time.Time
	MCPResourceURL         string
	MCPIssuerURL           string
	TelegramWebhookSecret  string
	TelegramBotUsername    string
	// VoiceMemberIDs may send voice notes to the bot; WeeklyNumbersMemberIDs get the weekly-numbers reply.
	VoiceMemberIDs         []string
	WeeklyNumbersMemberIDs []string
	// ProducerTokenSHA256 is BLOOMARY_PRODUCER_TOKEN_SHA256 (hex); unset = producer routes absent (404).
	ProducerTokenSHA256 string
}

func DefaultConfig() Config {
	return Config{}
}

type service struct {
	webSessions           webSessionStore
	linkedIn              linkedinDomain.Gate
	config                Config
	tokenLimiter          limiterDomain.DailyIPLimiter
	feedbackLimiter       limiterDomain.DailyIPLimiter
	cliStartLimiter       limiterDomain.DailyIPLimiter
	cliResultLimiter      limiterDomain.DailyIPLimiter
	entitlementLimiter    limiterDomain.DailyIPLimiter
	exportLimiter         limiterDomain.DailyIPLimiter
	linkedInPostStory     linkedinDomain.LinkedInPostStory
	feedbackStory         feedbackDomain.FeedbackStory
	linkedInAuthStory     accountDomain.LinkedInAuthStory
	syncDraftsStory       draftDomain.SyncStory
	exportDraftsStory     draftDomain.ExportStory
	listMCPDraftsStory    draftDomain.ListMCPDraftsStory
	getMCPDraftStory      draftDomain.GetMCPDraftStory
	createMCPDraftStory   draftDomain.CreateMCPDraftStory
	updateMCPDraftStory   draftDomain.UpdateMCPDraftStory
	listInsightsStory     insightDomain.ListMCPInsightsStory
	markInsightUsedStory  insightDomain.MarkMCPInsightUsedStory
	attachImageStory      draftDomain.AttachMCPImageStory
	notifyScheduledStory  reviewDomain.NotifyScheduledStory
	sendSuggestionStory   reviewDomain.SendSuggestionStory
	listFeedbackStory     reviewDomain.ListFeedbackStory
	editRequestsStory     reviewDomain.EditRequestsStory
	firstCommentStory     reviewDomain.FirstCommentStory
	mcpAuthorizationStory mcpDomain.AuthorizationStory
	mcpTokenVerifier      mcpDomain.AccessTokenVerifier
	mcpGrants             mcpDomain.GrantStore
	draftImagesStore      draftDomain.ObjectStore
	draftImagesStory      draftDomain.ImageStory
	appleTransactionSync  appleiapDomain.TransactionSyncStory
	appleNotifications    appleiapDomain.NotificationStory
	paddleWebhook         paddleDomain.WebhookStory
	paddleBilling         paddleDomain.BillingStory
	accountDeletionStory  accountDomain.AccountDeletionStory
	accessStory           entitlementDomain.AccessStory
	flows                 accountDomain.OAuthStateStore
	verifiedTokens        *linkedInTokenVerificationCache
	scheduledPostStory    scheduledDomain.Story
	telegramBotStory      telegramDomain.BotStory
	telegramLinkStory     telegramDomain.LinkStory
	telegramQueueOnce     sync.Once
	telegramQueue         *telegramUpdateQueue
	mcpHandler            http.Handler
	publishGuard          crosspostDomain.PublishGuard
	crosspost             *CrosspostStories
	producer              *ProducerStories
	producerGuard         *producerGuard
	metaCallbacks         *MetaCallbacks
}

func (s *service) setPlatformCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	if origin == s.config.SupportAllowedOrigin || origin == s.config.WebAllowedOrigin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
		w.Header().Set("Vary", "Origin")
	}
}

func (s *service) acceptWebPreflight(w http.ResponseWriter, r *http.Request) bool {
	s.setPlatformCORS(w, r)
	if r.Method != http.MethodOptions {
		return false
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

type ApplicationStories struct {
	LinkedInPost       linkedinDomain.LinkedInPostStory
	Feedback           feedbackDomain.FeedbackStory
	LinkedInAuth       accountDomain.LinkedInAuthStory
	SyncDrafts         draftDomain.SyncStory
	ExportDrafts       draftDomain.ExportStory
	ListMCPDrafts      draftDomain.ListMCPDraftsStory
	GetMCPDraft        draftDomain.GetMCPDraftStory
	CreateMCPDraft     draftDomain.CreateMCPDraftStory
	UpdateMCPDraft     draftDomain.UpdateMCPDraftStory
	ListMCPInsights    insightDomain.ListMCPInsightsStory
	MarkMCPInsightUsed insightDomain.MarkMCPInsightUsedStory
	// AttachMCPImage stays nil while R2 draft image storage is not configured.
	AttachMCPImage draftDomain.AttachMCPImageStory
	ListFeedback   reviewDomain.ListFeedbackStory
	EditRequests   reviewDomain.EditRequestsStory
	// NotifyScheduled, SendSuggestion and FirstComments stay nil while the user bot is not configured.
	NotifyScheduled    reviewDomain.NotifyScheduledStory
	FirstComments      reviewDomain.FirstCommentStory
	SendSuggestion     reviewDomain.SendSuggestionStory
	DraftImages        draftDomain.ImageStory
	AppleTransaction   appleiapDomain.TransactionSyncStory
	AppleNotifications appleiapDomain.NotificationStory
	PaddleWebhook      paddleDomain.WebhookStory
	PaddleBilling      paddleDomain.BillingStory
	AccountDeletion    accountDomain.AccountDeletionStory
	Access             entitlementDomain.AccessStory
	ScheduledPost      scheduledDomain.Story
	MCPAuthorization   mcpDomain.AuthorizationStory
	MCPTokenVerifier   mcpDomain.AccessTokenVerifier
	MCPGrants          mcpDomain.GrantStore
	// TelegramBot and TelegramLink stay nil when the user bot token is not configured.
	TelegramBot  telegramDomain.BotStory
	TelegramLink telegramDomain.LinkStory
	// PublishGuard pauses Post now with the publishing pause switch (nil = never paused).
	PublishGuard crosspostDomain.PublishGuard
	// Crosspost stays nil while BLOOMARY_CROSSPOST_ENABLED is off.
	Crosspost *CrosspostStories
	// Producer stays nil while the video studio is off; MetaCallbacks while Bloomary's Meta app isn't configured.
	Producer      *ProducerStories
	MetaCallbacks *MetaCallbacks
}

func NewService(
	draftImagesStore draftDomain.ObjectStore,
	linkedIn linkedinDomain.Gate,
	accountRepo accountDomain.Repository,
	appStories ApplicationStories,
	flows accountDomain.OAuthStateStore,
	limiters [6]limiterDomain.DailyIPLimiter,
	config Config,
) (*service, error) {
	if accountRepo == nil {
		return nil, fmt.Errorf("empty account repo")
	}
	if flows == nil {
		return nil, fmt.Errorf("empty oauth state store")
	}
	if err := validateApplicationStories(appStories); err != nil {
		return nil, err
	}
	if config.MCPTokenSigningKey != "" && (appStories.MCPAuthorization == nil || appStories.MCPTokenVerifier == nil || appStories.MCPGrants == nil) {
		return nil, fmt.Errorf("MCP authorization story, token verifier, and grant store are required when MCP signing is enabled")
	}
	if config.PaddleSignatureSkew <= 0 {
		config.PaddleSignatureSkew = 5 * time.Minute
	}

	cache := newLinkedInTokenVerificationCache(linkedInTokenCacheTTL)
	pairs, err := accountRepo.AllActive(context.Background())
	if err != nil {
		return nil, fmt.Errorf("warm verified tokens: %w", err)
	}
	for _, pair := range pairs {
		if remaining := time.Until(pair.ExpiresAt); remaining > 0 {
			cache.setWithTTL(pair.AccessToken, pair.MemberID, remaining)
		}
	}

	svc := &service{
		linkedIn:              linkedIn,
		config:                config,
		tokenLimiter:          limiters[0],
		feedbackLimiter:       limiters[1],
		cliStartLimiter:       limiters[2],
		cliResultLimiter:      limiters[3],
		entitlementLimiter:    limiters[4],
		exportLimiter:         limiters[5],
		linkedInPostStory:     appStories.LinkedInPost,
		feedbackStory:         appStories.Feedback,
		linkedInAuthStory:     appStories.LinkedInAuth,
		syncDraftsStory:       appStories.SyncDrafts,
		exportDraftsStory:     appStories.ExportDrafts,
		listMCPDraftsStory:    appStories.ListMCPDrafts,
		getMCPDraftStory:      appStories.GetMCPDraft,
		createMCPDraftStory:   appStories.CreateMCPDraft,
		updateMCPDraftStory:   appStories.UpdateMCPDraft,
		listInsightsStory:     appStories.ListMCPInsights,
		markInsightUsedStory:  appStories.MarkMCPInsightUsed,
		attachImageStory:      appStories.AttachMCPImage,
		notifyScheduledStory:  appStories.NotifyScheduled,
		sendSuggestionStory:   appStories.SendSuggestion,
		listFeedbackStory:     appStories.ListFeedback,
		editRequestsStory:     appStories.EditRequests,
		firstCommentStory:     appStories.FirstComments,
		mcpAuthorizationStory: appStories.MCPAuthorization,
		mcpTokenVerifier:      appStories.MCPTokenVerifier,
		mcpGrants:             appStories.MCPGrants,
		draftImagesStore:      draftImagesStore,
		draftImagesStory:      appStories.DraftImages,
		appleTransactionSync:  appStories.AppleTransaction,
		appleNotifications:    appStories.AppleNotifications,
		paddleWebhook:         appStories.PaddleWebhook,
		paddleBilling:         appStories.PaddleBilling,
		accountDeletionStory:  appStories.AccountDeletion,
		accessStory:           appStories.Access,
		flows:                 flows,
		verifiedTokens:        cache,
		scheduledPostStory:    appStories.ScheduledPost,
		telegramBotStory:      appStories.TelegramBot,
		telegramLinkStory:     appStories.TelegramLink,
		publishGuard:          appStories.PublishGuard,
		crosspost:             appStories.Crosspost,
		producer:              appStories.Producer,
		producerGuard:         newProducerGuard(),
		metaCallbacks:         appStories.MetaCallbacks,
	}
	if err := svc.webSessions.load(config.WebSessionPath); err != nil {
		return nil, err
	}
	svc.mcpHandler = svc.newMCPHandler()
	return svc, nil
}

func validateApplicationStories(app ApplicationStories) error {
	checks := []struct {
		name  string
		value any
	}{
		{"LinkedIn post", app.LinkedInPost}, {"feedback", app.Feedback},
		{"LinkedIn auth", app.LinkedInAuth}, {"draft sync", app.SyncDrafts}, {"draft export", app.ExportDrafts},
		{"MCP list drafts", app.ListMCPDrafts}, {"MCP get draft", app.GetMCPDraft},
		{"MCP create draft", app.CreateMCPDraft}, {"MCP update draft", app.UpdateMCPDraft},
		{"MCP list insights", app.ListMCPInsights}, {"MCP mark insight used", app.MarkMCPInsightUsed},
		{"MCP list feedback", app.ListFeedback},
		{"Apple transaction", app.AppleTransaction},
		{"Apple notifications", app.AppleNotifications}, {"Paddle webhook", app.PaddleWebhook}, {"Paddle billing", app.PaddleBilling},
		{"account deletion", app.AccountDeletion}, {"entitlement access", app.Access},
		{"scheduled post", app.ScheduledPost},
	}
	for _, check := range checks {
		if check.value == nil {
			return fmt.Errorf("%s story is missing", check.name)
		}
	}
	return nil
}

func (s *service) Mux(ctx context.Context) *http.ServeMux {
	go s.scheduledPostStory.RunWorker(ctx)
	appLogger := logger.FromContext(ctx)
	mux := http.NewServeMux()
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(
			pattern, func(w http.ResponseWriter, r *http.Request) {
				s.setPlatformCORS(w, r)
				if !s.authenticateWebSession(w, r) {
					return
				}
				handler(w, r.WithContext(appLogger.ToContext(r.Context())))
			},
		)
	}
	handle("/api/notifications/token", s.NotificationToken)
	handle("/api/entitlement", s.Entitlement)
	handle("/api/paddle/webhook", s.PaddleWebhook)
	handle("/api/paddle/checkout-identity", s.PaddleCheckoutIdentity)
	handle("/api/paddle/portal", s.PaddlePortal)
	handle("/api/apple/transactions/sync", s.AppleTransactionSync)
	handle("/api/apple/notifications", s.AppleNotifications)
	handle("/api/drafts/sync", s.DraftSync)
	handle("/api/drafts/export", s.DraftExport)
	handle("/oauth/register", s.MCPRegister)
	handle("/oauth/authorize", s.MCPAuthorize)
	handle("/oauth/authorize/consent", s.MCPConsent)
	handle("/oauth/token", s.MCPExchangeToken)
	handle("/.well-known/oauth-protected-resource", s.MCPResourceMetadata)
	handle("/.well-known/oauth-authorization-server", s.MCPOAuthMetadata)
	handle("/api/draft-images/presign-upload", s.DraftImagePresignUpload)
	handle("/api/draft-images/presign-download", s.DraftImagePresignDownload)
	handle("/api/linkedin/profile", s.LinkedInProfile)
	handle("/api/linkedin/post", s.LinkedInPost)
	handle("/api/linkedin/schedule", s.LinkedInSchedule)
	handle("/api/linkedin/schedule/status", s.LinkedInScheduleStatus)
	handle("/api/linkedin/schedules", s.LinkedInSchedules)
	handle("/api/feedback", s.Feedback)
	handle("/api/support/feedback", s.SupportFeedback)
	handle("/api/linkedin/disconnect", s.LinkedInDisconnect)
	handle("/api/account/delete", s.AccountDelete)
	handle("/api/linkedin/token", s.LinkedInToken)
	handle("/api/linkedin/cli/start", s.LinkedInCLIStart)
	handle("/api/linkedin/callback", s.LinkedInCallback)
	handle("/api/linkedin/cli/result", s.LinkedInCLIResult)
	handle("/api/telegram/webhook", s.TelegramWebhook)
	handle("/api/telegram/link", s.TelegramLink)
	if s.crosspost != nil {
		handle("/api/content-rules", s.ContentRules)
		handle("/api/networks", s.Networks)
		handle("/api/networks/connect", s.NetworksConnect)
		handle("/api/networks/callback", s.NetworksCallback)
		handle("/api/networks/test", s.NetworksTest)
		handle("/api/networks/connection", s.NetworksConnection)
		handle("/api/networks/destination", s.NetworksDestination)
		handle("/api/publications", s.Publications)
		handle("/api/publications/approve", s.PublicationsApprove)
		handle("/api/publications/delivery", s.PublicationsDelivery)
		handle("/api/publications/cancel", s.PublicationsCancel)
		handle("/api/publications/retry", s.PublicationsRetry)
		handle("/api/publications/list", s.PublicationsList)
	}
	s.registerProducerRoutes(handle)
	s.registerMetaRoutes(handle)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mcpHandler.ServeHTTP(w, r.WithContext(appLogger.ToContext(r.Context())))
	}))
	return mux
}

type APIError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"errorDescription"`
}

func writeAPIError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(
		APIError{
			Error:            code,
			ErrorDescription: description,
		},
	)
}
