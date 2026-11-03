package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	appleiapDomain "bloomary-api/internal/domain/appleiap"
	dailyDomain "bloomary-api/internal/domain/daily"
	draftDomain "bloomary-api/internal/domain/draft"
	limiterDomain "bloomary-api/internal/domain/limiter"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	mcpDomain "bloomary-api/internal/domain/mcp"
	metricsDomain "bloomary-api/internal/domain/metrics"
	notificationDomain "bloomary-api/internal/domain/notification"
	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	recaptchaDomain "bloomary-api/internal/domain/recaptcha"
	resendDomain "bloomary-api/internal/domain/resend"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	apnsGate "bloomary-api/internal/gates/apns"
	appleiapGate "bloomary-api/internal/gates/appleiap"
	deepgramGate "bloomary-api/internal/gates/deepgram"
	imagefetchGate "bloomary-api/internal/gates/imagefetch"
	linkedinGate "bloomary-api/internal/gates/linkedin"
	mcpSignerGate "bloomary-api/internal/gates/mcp"
	objectstoreGate "bloomary-api/internal/gates/objectstore"
	paddleGate "bloomary-api/internal/gates/paddle"
	recaptchaGate "bloomary-api/internal/gates/recaptcha"
	metricsRepo "bloomary-api/internal/gates/repositories/metrics"
	notificationRepo "bloomary-api/internal/gates/repositories/notification"
	oauthStateRepo "bloomary-api/internal/gates/repositories/oauth_state"
	quotaRepo "bloomary-api/internal/gates/repositories/quota"
	ratelimit "bloomary-api/internal/gates/repositories/ratelimit"
	resendGate "bloomary-api/internal/gates/resend"
	telegramGate "bloomary-api/internal/gates/telegram"
	v0 "bloomary-api/internal/handlers/http/v0"
	"bloomary-api/internal/metrics"

	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/platform/ratelimiter"
	"bloomary-api/internal/stories"
)

const linkedInCLICallbackPath = "/api/linkedin/callback"

// Telegram allows 1-256 characters A-Z, a-z, 0-9, _ and - in a webhook secret_token.
var telegramWebhookSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

var appVersion = "dev"

func main() {
	config, err := platform.LoadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	appLogger, err := newLogger(config)
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx := appLogger.ToContext(signalCtx)
	store, err := newAuthStoreWithBilling(
		config.DatabasePath,
		int(config.BillingFreeTierDays),
		config.BillingTrialHMACSecret,
	)
	if err != nil {
		appLogger.Fatal(err)
	}
	defer store.Close()
	postPublishRepo, err := metricsRepo.NewPostPublishRepository(store.db)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init post publish repository: %w", err))
	}
	if err := postPublishRepo.InitTable(ctx); err != nil {
		appLogger.Fatal(err)
	}
	metricsRepoImpl, err := metricsRepo.NewMetricsRepository(store.db)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init metrics repository: %w", err))
	}

	//nolint:gosec // redirect URI is quoted and comes from deployment configuration.
	appLogger.WithFields(
		logger.Fields{
			"client_id": config.LinkedInClientID, "client_secret_present": config.LinkedInClientSecret != "",
			"redirect_expected": strings.TrimRight(config.BloomaryAPIRootURL, "/") + linkedInCLICallbackPath,
		},
	).Info("linkedin_auth_config")
	credentialVault, err := newCredentialVault(config)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init credential vault: %w", err))
	}
	logCrosspostConfig(appLogger, config, credentialVault)

	outboundDuration := metrics.MustInitOutboundHTTPRequestDurationMetric()
	outboundInFlight := metrics.MustInitOutboundHTTPInFlightMetric()
	outboundCounter := metrics.MustInitOutboundHTTPRequestsMetric()
	productCounters := metrics.NewProductCounters(
		metrics.MustInitPostsPublishMetric(),
		metrics.MustInitLinkedInAuthMetric(),
		metrics.MustInitFeedbackMetric(),
		metrics.MustInitRateLimitMetric(),
		metrics.MustInitPaddleWebhookMetric(),
	)
	productGauges := metrics.ProductGauges{
		ConnectedUsers:     metrics.MustInitConnectedUsersMetric(),
		SignupsYesterday:   metrics.MustInitSignupsYesterdayMetric(),
		ActivatedYesterday: metrics.MustInitActivatedYesterdayMetric(),
		ActiveYesterday:    metrics.MustInitActiveYesterdayMetric(),
		Users4PostsLast7d:  metrics.MustInitUsers4PostsLast7dMetric(),
	}
	productGaugeCache := metrics.NewProductGaugeCache()
	newTrackedClient := func(name string) (httpclient.Client, error) {
		return newHTTPClient(
			ctx, config, httpclient.WithMetrics(name, outboundDuration, outboundInFlight, outboundCounter),
		)
	}
	linkedInClient, err := newTrackedClient("linkedin")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init linkedin http client: %w", err))
	}
	resendClient, err := newTrackedClient("resend")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init resend http client: %w", err))
	}
	recaptchaClient, err := newTrackedClient("recaptcha")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init recaptcha http client: %w", err))
	}
	paddleClient, err := newTrackedClient("paddle")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init paddle http client: %w", err))
	}
	paddleBaseURL := strings.TrimSpace(config.PaddleAPIBaseURL)
	if paddleBaseURL == "" {
		paddleBaseURL = "https://api.paddle.com"
		if strings.EqualFold(config.PaddleEnvironment, "sandbox") {
			paddleBaseURL = "https://sandbox-api.paddle.com"
		}
	}
	paddleAPI, err := paddleGate.New(paddleClient, paddleBaseURL, config.PaddleAPIKey)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init paddle gate: %w", err))
	}
	appleVerifier, err := newAppleTransactionVerifier(config)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init apple transaction verifier: %w", err))
	}
	linkedInGate, err := newLinkedInGate(config, linkedInClient, productCounters)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init linkedin gate: %w", err))
	}
	resendGate, err := newResendGate(config, resendClient, productCounters)
	if err != nil {
		appLogger.Fatal(err)
	}

	recaptchaGate, err := newRecaptchaGate(config, recaptchaClient, productCounters)
	if err != nil {
		appLogger.Fatal(err)
	}
	draftImageStore, err := newDraftImageStore(config)
	if err != nil {
		appLogger.Fatal(err)
	}
	// Voice transcription and file downloads take longer than the default outbound timeout.
	newSlowClient := func(name string) (httpclient.Client, error) {
		return newHTTPClient(
			ctx, config, httpclient.WithMetrics(name, outboundDuration, outboundInFlight, outboundCounter),
			httpclient.WithRequestTimeout(2*time.Minute),
		)
	}
	userBot, transcriber, err := newUserBotGates(config, newSlowClient)
	if err != nil {
		appLogger.Fatal(err)
	}

	handlerCfg := handlerConfig(config, productCounters)
	handlerCfg.WebSessionPath = config.DatabasePath + ".web-sessions.json"
	handlerCfg.ProductEvents = postPublishRepo
	flows, err := oauthStateRepo.NewStore(config.LinkedInCLIFlowTTL)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init oauth state store: %w", err))
	}
	quota, err := newQuotaAndLimiters(store.dailyRepo, int(config.LinkedInDailyLimit))
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init quota: %w", err))
	}
	endpointLimiters, err := newEndpointLimiters()
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init endpoint limiters: %w", err))
	}
	handlerCfg.Notifications = newNotifications(config, newTrackedClient, appLogger)
	go pruneVoiceAttempts(ctx, store.insightRepo)
	appStories, err := newApplicationStories(
		store, draftImageStore, linkedInGate, resendGate, recaptchaGate,
		appleVerifier, appleVerifier, paddleAPI, quota, flows, userBot, transcriber, handlerCfg,
	)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init application stories: %w", err))
	}
	if userBot != nil {
		// The command menu is best effort: the bot works without it.
		if err := userBot.SetMyCommands(ctx, stories.BotCommands); err != nil {
			appLogger.WithError(err).Error("telegram_set_commands_failed")
		}
	}
	apiMux, drainTelegram, err := newMuxWithLinkedInConfig(
		ctx, draftImageStore, linkedInGate, store.accountRepo, flows, appStories,
		endpointLimiters, handlerCfg,
	)
	if err != nil {
		appLogger.Fatal(err)
	}

	requestsCounter := metrics.MustInitHTTPRequestsMetric()
	requestDuration := metrics.MustInitHTTPRequestDurationMetric()
	mux := metrics.InstrumentHTTP(requestLoggingMiddleware(appLogger, apiMux), requestsCounter, requestDuration)
	infraServer := &http.Server{
		Addr: config.InfraAddr,
		Handler: newInfraMux(
			store.db, appVersion, func(reqCtx context.Context) {
				refreshCtx, cancel := context.WithTimeout(appLogger.ToContext(reqCtx), 5*time.Second)
				defer cancel()
				_ = productGaugeCache.Refresh(refreshCtx, metricsRepoImpl, productGauges, time.Now())
			},
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	server := &http.Server{
		Addr:              config.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 2)
	go serveHTTP(errCh, "infra", infraServer, appLogger)
	go serveHTTP(errCh, "api", server, appLogger)

	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := infraServer.Shutdown(shutdownCtx); err != nil {
			appLogger.WithError(err).Error("bloomary_infra_shutdown_failed")
		}

		if err := server.Shutdown(shutdownCtx); err != nil {
			appLogger.WithError(err).Error("bloomary_api_shutdown_failed")
		}
		drainTelegram(shutdownCtx)
	case err := <-errCh:
		appLogger.Fatal(fmt.Errorf("bloomary server failed: %w", err))
	}
}

func serveHTTP(errCh chan<- error, name string, server *http.Server, appLogger *logger.Logger) {
	appLogger.WithFields(logger.Fields{"addr": server.Addr, "server": name}).Info("bloomary_server_listening")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s listen: %w", name, err)
	}
}

func newHTTPClient(ctx context.Context, config platform.Config, opts ...httpclient.ClientOptionFunc) (httpclient.Client, error) {
	options := append(
		[]httpclient.ClientOptionFunc{httpclient.WithRequestTimeout(config.OutboundRequestTimeout)}, opts...,
	)
	return httpclient.NewClient(ctx, options...)
}

func newLinkedInGate(
	config platform.Config, client httpclient.Client, counters metrics.ProductCounters,
) (linkedinDomain.Gate, error) {
	return linkedinGate.New(
		config.LinkedInClientID,
		config.LinkedInClientSecret,
		config.LinkedInBaseURL,
		config.LinkedInOAuthBaseURL,
		client,
		ratelimiter.NewRateLimiter(
			ratelimiter.RateLimitConfig{
				Burst: int(config.LinkedInRateLimitBurst),
				RPS:   float64(config.LinkedInRateLimitRPS),
			},
		),
		counters,
	)
}

func newResendGate(
	config platform.Config, client httpclient.Client, counters metricsDomain.Recorder,
) (resendDomain.Gate, error) {
	return resendGate.New(
		client,
		config.ResendEmailsURL,
		config.ResendAPIKey,
		config.ResendFrom,
		config.FeedbackTo,
		counters,
	)
}

func newRecaptchaGate(
	config platform.Config, client httpclient.Client, counters metricsDomain.Recorder,
) (recaptchaDomain.Captcha, error) {
	return recaptchaGate.New(
		client, config.RecaptchaVerifyURL, config.RecaptchaSecret, config.RecaptchaHostname, config.RecaptchaAction, counters,
	)
}

func newAppleTransactionVerifier(config platform.Config) (*appleiapGate.SignedTransactionVerifier, error) {
	productIDs := strings.Split(config.AppleProductID, ",")
	return appleiapGate.NewSignedTransactionVerifier(
		config.AppleBundleID, productIDs,
		appleiapGate.WithExpectedEnvironment(config.AppleEnvironment),
	)
}

func newDraftImageStore(config platform.Config) (draftDomain.ObjectStore, error) {
	values := []string{
		config.R2AccountID,
		config.R2Bucket,
		config.R2AccessKeyID,
		config.R2SecretAccessKey,
		config.R2Endpoint,
	}
	configured := false
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			configured = true
			break
		}
	}
	if !configured {
		return nil, nil
	}

	return objectstoreGate.NewR2ObjectStore(
		objectstoreGate.R2Config{
			AccountID:      config.R2AccountID,
			Endpoint:       config.R2Endpoint,
			Bucket:         config.R2Bucket,
			AccessKeyID:    config.R2AccessKeyID,
			SecretKey:      config.R2SecretAccessKey,
			Environment:    config.SentryEnvironment,
			PresignTTL:     config.R2DraftImagePresignTTL,
			MaxUploadBytes: int64(config.R2DraftImageMaxBytes),
		},
	)
}

type tokenResponse = v0.TokenResponse
type linkedInProfile = v0.LinkedInProfile

func newApplicationStories(
	store *authStore,
	draftImages draftDomain.ObjectStore,
	linkedIn linkedinDomain.Gate,
	resend resendDomain.Gate,
	recaptcha recaptchaDomain.Captcha,
	appleVerifier appleiapDomain.TransactionVerifier,
	appleNotificationVerifier appleiapDomain.NotificationVerifier,
	paddlePortal paddleDomain.PortalGate,
	quota dailyDomain.QuotaConsumer,
	flows accountDomain.OAuthStateStore,
	userBot telegramDomain.Gate,
	transcriber telegramDomain.Transcriber,
	config v0.Config,
) (v0.ApplicationStories, error) {
	access, err := stories.NewEntitlementAccessStory(store.accountRepo, store.paddleRepo, store.appleEntitlementRepo, store.trialRepo, store.dailyRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	post, err := stories.NewLinkedInPostStory(linkedIn, quota, access, store.dailyRepo, config.ProductEvents, config.ProductCounters)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	schedule, err := stories.NewLinkedInScheduleStory(store.scheduledPostRepo, store.accountRepo, store.draftRepo, draftImages, access, linkedIn, post, config.ScheduledPostInterval, config.ScheduleMaxDays, config.Notifications)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	feedback, err := stories.NewFeedbackStory(resend, recaptcha, config.ProductCounters)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	linkedinAuth, err := stories.NewLinkedInAuthStory(linkedIn, store.accountRepo, store.trialRepo, flows)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	syncDrafts, err := stories.NewSyncDrafts(access, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	exportDrafts, err := stories.NewExportDrafts(store.draftRepo, draftImages)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	listMCPDrafts, err := stories.NewListMCPDrafts(access, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	getMCPDraft, err := stories.NewGetMCPDraft(access, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	createMCPDraft, err := stories.NewCreateMCPDraft(access, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	updateMCPDraft, err := stories.NewUpdateMCPDraft(access, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	listFeedback, err := stories.NewListFeedback(access, store.reviewRepo, store.draftRepo, store.scheduledPostRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	listMCPInsights, err := stories.NewListMCPInsights(access, store.insightRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	markMCPInsightUsed, err := stories.NewMarkMCPInsightUsed(access, store.insightRepo, store.draftRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	var mcpAuthorization mcpDomain.AuthorizationStory
	var mcpTokenVerifier mcpDomain.AccessTokenVerifier
	if config.MCPAccessTokenSecret != "" {
		current := mcpSignerGate.SigningKey{ID: config.MCPAccessTokenKeyID, Secret: config.MCPAccessTokenSecret}
		var previous *mcpSignerGate.SigningKey
		if config.MCPPreviousAccessKey != "" {
			if config.MCPPreviousAccessUntil.IsZero() {
				return v0.ApplicationStories{}, errors.New("previous MCP signing key requires verify-until")
			}
			if config.MCPPreviousAccessUntil.After(time.Now()) {
				previous = &mcpSignerGate.SigningKey{ID: config.MCPPreviousAccessKeyID, Secret: config.MCPPreviousAccessKey, VerifyUntil: config.MCPPreviousAccessUntil}
			}
		}
		signer, signerErr := mcpSignerGate.NewTokenSigner(current, previous)
		if signerErr != nil {
			return v0.ApplicationStories{}, signerErr
		}
		issuer := strings.TrimRight(config.MCPIssuerURL, "/")
		resource := config.MCPResourceURL
		mcpAuthorization, err = stories.NewMCPAuthorizationStory(store.mcpOAuthState, store.mcpOAuthState, store.mcpOAuthState, access, signer, issuer, resource)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		mcpTokenVerifier = signer
	}
	var draftImageStory draftDomain.ImageStory
	var attachMCPImage draftDomain.AttachMCPImageStory
	if draftImages != nil {
		draftImageStory, err = stories.NewDraftImages(draftImages)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		attachMCPImage, err = stories.NewAttachMCPImage(access, store.draftRepo, draftImages, imagefetchGate.New(30*time.Second))
		if err != nil {
			return v0.ApplicationStories{}, err
		}
	}
	appleSync, err := stories.NewAppleTransactionSync(appleVerifier, store.appleEntitlementRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	appleNotifications, err := stories.NewAppleNotifications(appleNotificationVerifier, store.appleEntitlementRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	paddleWebhook, err := stories.NewPaddleWebhook(store.paddleRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	paddleBilling, err := stories.NewPaddleBilling(store.paddleBillingRepo, paddlePortal)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	memberData := []accountDomain.MemberDataDeleter{store.insightRepo, store.telegramLinkRepo, store.reviewRepo, store.mcpOAuthState, store.scheduledPostRepo}
	memberData = append(memberData, store.crosspostMemberData...)
	if events, ok := config.ProductEvents.(accountDomain.MemberDataDeleter); ok {
		memberData = append(memberData, events)
	}
	accountDeletion, err := stories.NewAccountDeletionStoryWithMemberData(store.dailyRepo, store.draftRepo, store.accountRepo, memberData, draftImages)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	var telegramBot telegramDomain.BotStory
	var telegramLink telegramDomain.LinkStory
	var notifyScheduled reviewDomain.NotifyScheduledStory
	var sendSuggestion reviewDomain.SendSuggestionStory
	var firstComments reviewDomain.FirstCommentStory
	editRequests, err := stories.NewEditRequests(access, store.reviewRepo)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	if userBot != nil {
		voiceMembers := telegramDomain.NewMemberSet(config.VoiceMemberIDs)
		telegramLink, err = stories.NewTelegramLink(store.telegramLinkRepo, config.TelegramBotUsername, userBot, voiceMembers)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		notifier, err := stories.NewTelegramNotifier(store.telegramLinkRepo, userBot)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		notify, err := stories.NewNotifyScheduled(access, store.draftRepo, store.scheduledPostRepo, store.reviewRepo, draftImages, notifier, config.WebAllowedOrigin)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		notifyScheduled = notify
		planned, err := stories.NewPlannedPosts(store.scheduledPostRepo, store.draftRepo, notify, userBot)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		editReplies, err := stories.NewEditReplies(store.reviewRepo, store.scheduledPostRepo, store.draftRepo)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		trendReplies, err := stories.NewTrendReplies(store.reviewRepo, store.insightRepo)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		comments, err := stories.NewFirstComments(store.reviewRepo, notifier)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		firstComments = comments
		if hooks, ok := schedule.(scheduledDomain.PublishedHooks); ok {
			hooks.OnPublished(comments)
		}
		sendSuggestion, err = stories.NewSendSuggestion(access, store.reviewRepo, notifier)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		skips, err := stories.NewSkipCallbacks(store.reviewRepo, store.scheduledPostRepo, userBot)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		scheduleNext, err := stories.NewScheduleNextCallbacks(store.reviewRepo, store.draftRepo, schedule, comments, notify)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		suggestions, err := stories.NewSuggestionCallbacks(store.reviewRepo, userBot)
		if err != nil {
			return v0.ApplicationStories{}, err
		}
		telegramBot, err = stories.NewTelegramBot(store.telegramLinkRepo, userBot, transcriber, store.insightRepo, store.accountRepo, config.WebAllowedOrigin, stories.TelegramBotHandlers{
			VoiceMembers: voiceMembers, NumbersMembers: telegramDomain.NewMemberSet(config.WeeklyNumbersMemberIDs),
			Replies: []telegramDomain.ReplyHandler{editReplies, trendReplies}, Callbacks: []telegramDomain.CallbackHandler{skips, scheduleNext, suggestions, planned}, Commands: []telegramDomain.CommandHandler{planned}})
		if err != nil {
			return v0.ApplicationStories{}, err
		}
	}
	return v0.ApplicationStories{
		TelegramBot: telegramBot, TelegramLink: telegramLink, NotifyScheduled: notifyScheduled, SendSuggestion: sendSuggestion,
		ListFeedback: listFeedback, EditRequests: editRequests, FirstComments: firstComments,
		LinkedInPost: post, Feedback: feedback, LinkedInAuth: linkedinAuth,
		SyncDrafts: syncDrafts, ExportDrafts: exportDrafts, ListMCPDrafts: listMCPDrafts, GetMCPDraft: getMCPDraft,
		CreateMCPDraft: createMCPDraft, UpdateMCPDraft: updateMCPDraft, DraftImages: draftImageStory,
		ListMCPInsights: listMCPInsights, MarkMCPInsightUsed: markMCPInsightUsed, AttachMCPImage: attachMCPImage,
		AppleTransaction: appleSync, AppleNotifications: appleNotifications,
		PaddleWebhook: paddleWebhook, PaddleBilling: paddleBilling, AccountDeletion: accountDeletion,
		Access: access, ScheduledPost: schedule, MCPAuthorization: mcpAuthorization, MCPTokenVerifier: mcpTokenVerifier, MCPGrants: store.mcpOAuthState,
	}, nil
}

// newUserBotGates builds the user Telegram bot and its Deepgram transcriber.
// Both stay nil while BLOOMARY_APP_TOKEN is empty; the transcriber also needs DEEPGRAM_API_KEY.
func newUserBotGates(
	config platform.Config, newClient func(string) (httpclient.Client, error),
) (telegramDomain.Gate, telegramDomain.Transcriber, error) {
	if strings.TrimSpace(config.UserBotToken) == "" {
		return nil, nil, nil
	}
	if !telegramWebhookSecretPattern.MatchString(config.UserBotWebhookSecret) {
		return nil, nil, errors.New("BLOOMARY_USER_BOT_WEBHOOK_SECRET must be 1-256 characters of A-Z, a-z, 0-9, _ or -")
	}
	if strings.TrimSpace(config.UserBotUsername) == "" {
		return nil, nil, errors.New("BLOOMARY_USER_BOT_USERNAME is required with BLOOMARY_APP_TOKEN")
	}
	telegramClient, err := newClient("telegram")
	if err != nil {
		return nil, nil, fmt.Errorf("init telegram http client: %w", err)
	}
	bot, err := telegramGate.New(telegramClient, telegramGate.DefaultBaseURL, config.UserBotToken)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(config.DeepgramAPIKey) == "" {
		return bot, nil, nil
	}
	deepgramClient, err := newClient("deepgram")
	if err != nil {
		return nil, nil, fmt.Errorf("init deepgram http client: %w", err)
	}
	transcriber, err := deepgramGate.New(deepgramClient, deepgramGate.DefaultBaseURL, config.DeepgramAPIKey)
	if err != nil {
		return nil, nil, err
	}
	return bot, transcriber, nil
}

// newMuxWithLinkedInConfig returns the API mux and a drain func that finishes queued Telegram updates on shutdown.
func newMuxWithLinkedInConfig(
	ctx context.Context,
	draftImages draftDomain.ObjectStore,
	linkedIn linkedinDomain.Gate,
	accountRepo accountDomain.Repository,
	flows accountDomain.OAuthStateStore,
	appStories v0.ApplicationStories,
	limiters [6]limiterDomain.DailyIPLimiter,
	config v0.Config,
) (*http.ServeMux, func(context.Context), error) {
	service, err := v0.NewService(draftImages, linkedIn, accountRepo, appStories, flows, limiters, config)
	if err != nil {
		return nil, nil, err
	}
	return service.Mux(ctx), service.DrainTelegram, nil
}

func newQuotaAndLimiters(dailyRepo dailyDomain.Repository, dailyLimit int) (dailyDomain.QuotaConsumer, error) {
	if dailyLimit <= 0 {
		dailyLimit = 100
	}
	return quotaRepo.NewDailyQuota(dailyRepo, dailyLimit)
}

func newEndpointLimiters() ([6]limiterDomain.DailyIPLimiter, error) {
	// Draft export reads every draft image from R2, so it gets a low cap.
	limits := [6]int{3, 10, 30, 300, 300, 20}
	var limiters [6]limiterDomain.DailyIPLimiter
	for i, limit := range limits {
		l, err := ratelimit.NewDailyIPLimiter(limit)
		if err != nil {
			return limiters, err
		}
		limiters[i] = l
	}
	return limiters, nil
}

// newNotifications wires the device registrations to the APNs sender.
func newNotifications(
	config platform.Config,
	newClient func(string) (httpclient.Client, error),
	appLogger *logger.Logger,
) notificationDomain.Service {
	devices, err := notificationRepo.NewFiles(config.DatabasePath + ".notifications")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init notification registrations: %w", err))
	}

	return stories.NewNotifications(devices, newPushSender(config, newClient, appLogger))
}

// newPushSender builds the APNs sender, or returns nil when no key material is configured.
// A nil sender keeps push disabled and leaves scheduled posts on foreground polling.
func newPushSender(
	config platform.Config,
	newClient func(string) (httpclient.Client, error),
	appLogger *logger.Logger,
) notificationDomain.Sender {
	apnsKeyPEM := config.APNsKeyPEM
	if apnsKeyPEM == "" && config.APNsKeyPath != "" {
		keyFile, err := os.ReadFile(config.APNsKeyPath)
		if err != nil {
			appLogger.Fatal(fmt.Errorf("read APNs key: %w", err))
		}

		apnsKeyPEM = string(keyFile)
	}

	if apnsKeyPEM == "" && config.APNsKeyID == "" && config.APNsTeamID == "" {
		appLogger.Info("apns_not_configured_foreground_schedule_polling_only")

		return nil
	}

	apnsClient, err := newClient("apns")
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init APNs http client: %w", err))
	}

	sender, err := apnsGate.New(
		apnsKeyPEM, config.APNsKeyID, config.APNsTeamID, config.AppleBundleID,
		apnsClient, config.APNsEnvironments,
	)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init APNs: %w", err))
	}

	return sender
}

func newLogger(config platform.Config) (*logger.Logger, error) {
	return logger.New(
		logger.UseJSONFormat(config.LogFormat == "json"),
		logger.FatalDelay(config.LogFatalDelay),
		logger.SentryEnabled(config.SentryEnabled),
		logger.SentryDSN(config.SentryDSN),
		logger.SentryEnvironment(config.SentryEnvironment),
		logger.SentryRelease(config.SentryRelease),
		logger.SentryAggregationWindow(config.SentryAggregationWindow),
	)
}

func handlerConfig(config platform.Config, productCounters ...metrics.ProductCounters) v0.Config {
	counters := metrics.ProductCounters{}
	if len(productCounters) > 0 {
		counters = productCounters[0]
	}

	return v0.Config{
		ClientID:               config.LinkedInClientID,
		IOSCallbackRedirectURI: config.LinkedInIOSRedirectURI,
		WebCallbackRedirectURI: config.LinkedInWebRedirectURI,
		CLICallbackRedirectURI: strings.TrimRight(config.BloomaryAPIRootURL, "/") + linkedInCLICallbackPath,
		LinkedInOAuthScope:     config.LinkedInOAuthScope,
		BillingFreeTierDays:    int(config.BillingFreeTierDays),
		AppleReviewCode:        config.AppleReviewCode,
		PaddleWebhookSecret:    config.PaddleWebhookSecret,
		PaddleSignatureSkew:    config.PaddleSignatureSkew,
		MCPClientSigningKey:    config.MCPClientSigningSecret,
		MCPAccessTokenKeyID:    config.MCPAccessTokenKeyID,
		MCPAccessTokenSecret:   config.MCPAccessTokenSecret,
		MCPTokenSigningKey:     config.MCPAccessTokenSecret,
		MCPPreviousAccessKeyID: config.MCPPreviousTokenKeyID,
		MCPPreviousAccessKey:   config.MCPPreviousTokenSecret,
		MCPPreviousAccessUntil: func() time.Time { v, _ := time.Parse(time.RFC3339, config.MCPPreviousTokenUntil); return v }(),
		MCPResourceURL:         strings.TrimRight(config.BloomaryAPIRootURL, "/") + "/mcp",
		MCPIssuerURL:           strings.TrimRight(config.BloomaryAPIRootURL, "/"),
		SupportAllowedOrigin:   config.SupportAllowedOrigin,
		WebAllowedOrigin:       config.WebAllowedOrigin,
		ScheduleMaxDays:        int(config.ScheduleMaxDays),
		ProductCounters:        counters,
		TelegramWebhookSecret:  config.UserBotWebhookSecret,
		TelegramBotUsername:    config.UserBotUsername,
		VoiceMemberIDs:         config.VoiceMemberIDs,
		WeeklyNumbersMemberIDs: config.WeeklyNumbersMemberIDs,
	}
}

// pruneVoiceAttempts deletes voice-note records older than 24 hours every hour, so none is kept much longer than
// the cap needs, even on days without voice notes (privacy policy: kept about 24 hours).
func pruneVoiceAttempts(ctx context.Context, insights any) {
	pruner, ok := insights.(interface {
		PruneVoiceAttempts(context.Context, time.Time) error
	})
	if !ok {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := pruner.PruneVoiceAttempts(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
			logger.FromContext(ctx).WithError(err).Error("voice_attempts_prune_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
