package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	apnsGate "bloomary-api/internal/gates/apns"
	appleiapGate "bloomary-api/internal/gates/appleiap"
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
	v0 "bloomary-api/internal/handlers/http/v0"
	"bloomary-api/internal/metrics"

	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/platform/ratelimiter"
	"bloomary-api/internal/stories"
)

const linkedInCLICallbackPath = "/api/linkedin/callback"

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
	appStories, err := newApplicationStories(
		store, draftImageStore, linkedInGate, resendGate, recaptchaGate,
		appleVerifier, appleVerifier, paddleAPI, quota, flows, handlerCfg,
	)
	if err != nil {
		appLogger.Fatal(fmt.Errorf("init application stories: %w", err))
	}
	apiMux, err := newMuxWithLinkedInConfig(
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
	if draftImages != nil {
		draftImageStory, err = stories.NewDraftImages(draftImages)
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
	accountDeletion, err := stories.NewAccountDeletionStory(store.dailyRepo, store.draftRepo, store.accountRepo, draftImages)
	if err != nil {
		return v0.ApplicationStories{}, err
	}
	return v0.ApplicationStories{
		LinkedInPost: post, Feedback: feedback, LinkedInAuth: linkedinAuth,
		SyncDrafts: syncDrafts, ExportDrafts: exportDrafts, ListMCPDrafts: listMCPDrafts, GetMCPDraft: getMCPDraft,
		CreateMCPDraft: createMCPDraft, UpdateMCPDraft: updateMCPDraft, DraftImages: draftImageStory,
		AppleTransaction: appleSync, AppleNotifications: appleNotifications,
		PaddleWebhook: paddleWebhook, PaddleBilling: paddleBilling, AccountDeletion: accountDeletion,
		Access: access, ScheduledPost: schedule, MCPAuthorization: mcpAuthorization, MCPTokenVerifier: mcpTokenVerifier, MCPGrants: store.mcpOAuthState,
	}, nil
}

func newMuxWithLinkedInConfig(
	ctx context.Context,
	draftImages draftDomain.ObjectStore,
	linkedIn linkedinDomain.Gate,
	accountRepo accountDomain.Repository,
	flows accountDomain.OAuthStateStore,
	appStories v0.ApplicationStories,
	limiters [6]limiterDomain.DailyIPLimiter,
	config v0.Config,
) (*http.ServeMux, error) {
	service, err := v0.NewService(draftImages, linkedIn, accountRepo, appStories, flows, limiters, config)
	if err != nil {
		return nil, err
	}
	return service.Mux(ctx), nil
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
	}
}
