package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	notificationDomain "bloomary-api/internal/domain/notification"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	mediaGate "bloomary-api/internal/gates/media"
	metaGate "bloomary-api/internal/gates/meta"
	objectstoreGate "bloomary-api/internal/gates/objectstore"
	crosspostRepo "bloomary-api/internal/gates/repositories/crosspost"
	deletionlogRepo "bloomary-api/internal/gates/repositories/deletionlog"
	videostudioRepo "bloomary-api/internal/gates/repositories/videostudio"
	tiktokGate "bloomary-api/internal/gates/tiktok"
	vaultGate "bloomary-api/internal/gates/vault"
	xGate "bloomary-api/internal/gates/x"
	youtubeGate "bloomary-api/internal/gates/youtube"
	v0 "bloomary-api/internal/handlers/http/v0"
	"bloomary-api/internal/metrics"
	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/stories"
)

// newCredentialVault loads the keyring when BLOOMARY_KEYRING_PATH is set; nil when it isn't.
// A set but unreadable or invalid keyring stops the start.
func newCredentialVault(config platform.Config) (*vaultGate.AESGCMVault, error) {
	if config.CredentialsKeyringFile == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(config.CredentialsKeyringFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials keyring: %w", err)
	}
	keyring, err := vaultGate.ParseKeyring(raw)
	if err != nil {
		return nil, err
	}
	return vaultGate.NewAESGCMVault(keyring)
}

// rewrapCredentials is `bloomary-api rewrap-credentials`: after a new active key is added to the keyring, every
// record sealed with an older key is sealed again with the active one; it prints how many. Remove an old key from the
// keyring only when bloomary_crosspost_sealed_records shows zero for it.
func rewrapCredentials(ctx context.Context, config platform.Config, store *authStore) error {
	vault, err := newCredentialVault(config)
	if err != nil {
		return err
	}
	if vault == nil {
		return errors.New("rewrap-credentials needs the credentials keyring (BLOOMARY_KEYRING_PATH)")
	}
	sealed, err := crosspostRepo.NewSealedRepository(store.db)
	if err != nil {
		return err
	}
	keys, err := stories.NewCredentialKeys(sealed, vault, nil)
	if err != nil {
		return err
	}
	n, err := keys.Rewrap(ctx)
	fmt.Printf("rewrapped %d records under key %s\n", n, vault.ActiveKeyID())
	return err
}

// logCrosspostConfig logs the publish mode at start (never key material) and exports it as bloomary_publish_mode.
func logCrosspostConfig(appLogger *logger.Logger, config platform.Config, vault *vaultGate.AESGCMVault) {
	keys := 0
	if vault != nil {
		keys = vault.KeyCount()
	}
	appLogger.WithFields(logger.Fields{
		"publish_mode":            crosspostDomain.ParsePublishMode(config.PublishMode),
		"publishing_paused_env":   config.PublishingPaused,
		"live_destination_count":  len(config.LiveDestinationIDs),
		"credentials_keyring_set": vault != nil,
		"credentials_key_count":   keys,
	}).Info("crosspost_config")
	metrics.MustInitPublishModeMetric(crosspostDomain.ParsePublishMode(config.PublishMode))
}

// crosspostSetup carries what the crosspost wiring needs into newApplicationStories, and the background jobs it
// built back out (started by main once the stories exist).
type crosspostSetup struct {
	vault     *vaultGate.AESGCMVault
	newClient func(name string, timeout time.Duration) (httpclient.Client, error)
	auditKey  []byte
	config    platform.Config
	workers   []func(context.Context)
	log *logger.Logger
	// bot is the user bot (nil when BLOOMARY_APP_TOKEN is empty): Telegram status messages and the studio need it.
	bot telegramDomain.Gate
	// Built by newCrosspostStories for the studio and the router.
	repos         stories.CrosspostRepos
	media         crosspostDomain.MediaWorker
	statusHook    *stories.CrosspostTelegramStatus
	metaCallbacks *v0.MetaCallbacks
}

// readAuditHMACKey reads BLOOMARY_AUDIT_HMAC_KEY_PATH (nil when unset). A set but unreadable or short key stops the start.
func readAuditHMACKey(config platform.Config) ([]byte, error) {
	if config.AuditHMACKeyFile == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(config.AuditHMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read audit hmac key: %w", err)
	}
	key := bytes.TrimSpace(raw)
	if len(key) < 32 {
		return nil, errors.New("audit hmac key: at least 32 bytes are required")
	}
	return key, nil
}

// crosspostGateSet is the registry: one gate package + one map entry per network (strategy).
type crosspostGateSet struct {
	connectors map[crosspostDomain.Provider]crosspostDomain.ProviderConnector
	publishers map[crosspostDomain.Network]crosspostDomain.NetworkPublisher
	media      crosspostDomain.MediaWorker
}

// uploadTimeout bounds one media upload request (videos up to a few hundred MB).
const uploadTimeout = 30 * time.Minute

// crosspostGates registers each network whose app keys are set; others answer "Not open yet" and dry_run still
// simulates them. Media needs R2 (the publishers stream from it or hand the network a signed URL).
func crosspostGates(config platform.Config, objects draftDomain.ObjectStore, newClient func(string, time.Duration) (httpclient.Client, error)) (crosspostGateSet, error) {
	set := crosspostGateSet{connectors: map[crosspostDomain.Provider]crosspostDomain.ProviderConnector{}, publishers: map[crosspostDomain.Network]crosspostDomain.NetworkPublisher{}}
	media, ok := objects.(*objectstoreGate.R2ObjectStore)
	if !ok || newClient == nil {
		return set, nil
	}
	if config.FFmpegPath != "" {
		worker, err := mediaGate.NewFFmpeg(mediaGate.Config{Store: media, FFmpeg: config.FFmpegPath, FFprobe: config.FFprobePath, WorkDir: config.MediaWorkDir})
		if err != nil {
			return set, err
		}
		set.media = worker
	}
	clients := func(name string) (httpclient.Client, httpclient.Client, error) {
		api, err := newClient(name, 0)
		if err != nil {
			return nil, nil, err
		}
		upload, err := newClient(name+"_upload", uploadTimeout)
		return api, upload, err
	}
	if config.XClientID != "" && config.XClientSecret != "" {
		api, upload, err := clients("x")
		if err != nil {
			return set, err
		}
		cfg := xGate.Config{HTTP: api, Upload: upload, Media: media, ClientID: config.XClientID, ClientSecret: config.XClientSecret}
		connector, err := xGate.NewConnector(cfg)
		if err != nil {
			return set, err
		}
		publisher, err := xGate.NewPublisher(cfg)
		if err != nil {
			return set, err
		}
		set.connectors[connector.Provider()], set.publishers[publisher.Network()] = connector, publisher
	}
	// Meta needs the media worker too: Reel or Page video is decided by the video's length and shape.
	if config.MetaAppID != "" && config.MetaAppSecret != "" && set.media != nil {
		api, err := newClient("meta", uploadTimeout)
		if err != nil {
			return set, err
		}
		cfg := metaGate.Config{HTTP: api, Media: media, Probe: set.media, AppID: config.MetaAppID, AppSecret: config.MetaAppSecret}
		connector, err := metaGate.NewConnector(cfg)
		if err != nil {
			return set, err
		}
		facebook, err := metaGate.NewFacebookPagePublisher(cfg)
		if err != nil {
			return set, err
		}
		instagram, err := metaGate.NewInstagramPublisher(cfg)
		if err != nil {
			return set, err
		}
		set.connectors[connector.Provider()] = connector
		set.publishers[facebook.Network()], set.publishers[instagram.Network()] = facebook, instagram
	}
	if config.GoogleClientID != "" && config.GoogleClientSecret != "" {
		api, upload, err := clients("youtube")
		if err != nil {
			return set, err
		}
		cfg := youtubeGate.Config{HTTP: api, Upload: upload, Media: media, ClientID: config.GoogleClientID, ClientSecret: config.GoogleClientSecret}
		connector, err := youtubeGate.NewConnector(cfg)
		if err != nil {
			return set, err
		}
		publisher, err := youtubeGate.NewPublisher(cfg)
		if err != nil {
			return set, err
		}
		set.connectors[connector.Provider()], set.publishers[publisher.Network()] = connector, publisher
	}
	if config.TikTokClientKey != "" && config.TikTokClientSecret != "" {
		api, upload, err := clients("tiktok")
		if err != nil {
			return set, err
		}
		cfg := tiktokGate.Config{HTTP: api, Upload: upload, Media: media, ClientKey: config.TikTokClientKey, ClientSecret: config.TikTokClientSecret}
		connector, err := tiktokGate.NewConnector(cfg)
		if err != nil {
			return set, err
		}
		publisher, err := tiktokGate.NewPublisher(cfg)
		if err != nil {
			return set, err
		}
		set.connectors[connector.Provider()], set.publishers[publisher.Network()] = connector, publisher
	}
	return set, nil
}

func newCrosspostRepos(db *sql.DB) (stories.CrosspostRepos, *crosspostRepo.PublicationRepository, *crosspostRepo.ConnectionRepository, error) {
	connections, err := crosspostRepo.NewConnectionRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	destinations, err := crosspostRepo.NewDestinationRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	publications, err := crosspostRepo.NewPublicationRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	deliveries, err := crosspostRepo.NewDeliveryRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	linkedIn, err := crosspostRepo.NewLinkedInIntentRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	audit, err := crosspostRepo.NewAuditRepository(db)
	if err != nil {
		return stories.CrosspostRepos{}, nil, nil, err
	}
	repos := stories.CrosspostRepos{Connections: connections, Destinations: destinations, Publications: publications, Unapproved: publications, Deliveries: deliveries,
		DeliveryList: deliveries, Remover: connections, LinkedIn: linkedIn, Audit: audit, States: crosspostRepo.NewConnectStates()}
	return repos, publications, connections, nil
}

// newCrosspostStories wires every crosspost story, the delivery worker, the connection checker and the retention
// job. The account-deletion step is always built (crosspost rows from an earlier enabled run still go); the rest
// only with BLOOMARY_CROSSPOST_ENABLED, which needs the credentials keyring.
func newCrosspostStories(setup *crosspostSetup, store *authStore, objects draftDomain.ObjectStore, access entitlementDomain.AccessStory,
	schedule scheduledDomain.Story, guard crosspostDomain.PublishGuard, notifications notificationDomain.Service) (*v0.CrosspostStories, accountDomain.MemberDataDeleter, error) {
	repos, publications, connections, err := newCrosspostRepos(store.db)
	if err != nil {
		return nil, nil, err
	}
	extras, ok := store.draftRepo.(draftDomain.ExtrasRepository)
	if !ok {
		return nil, nil, errors.New("draft repository does not read draft extras")
	}
	gates, err := crosspostGates(setup.config, objects, setup.newClient)
	if err != nil {
		return nil, nil, err
	}
	connectors, publishers := gates.connectors, gates.publishers
	var vault crosspostDomain.CredentialVault
	if setup.vault != nil {
		vault = setup.vault
	}
	deletion, err := stories.NewDeleteCrosspostDataStory(repos, vault, connectors, extras, objects, setup.auditKey)
	if err != nil {
		return nil, nil, err
	}
	if !setup.config.CrosspostEnabled {
		return nil, deletion, nil
	}
	if vault == nil {
		return nil, nil, errors.New("BLOOMARY_CROSSPOST_ENABLED needs the credentials keyring (BLOOMARY_KEYRING_PATH)")
	}
	cfg := setup.config
	built := &v0.CrosspostStories{StatusLine: stories.CrosspostStatusLine, LinkedInLine: stories.LinkedInStatusLine, SummaryLine: stories.PublicationSummaryLine}
	var errs []error
	collect := func(err error) { errs = append(errs, err) }
	built.ListNetworks, err = stories.NewListNetworksStory(repos, connectors)
	collect(err)
	built.StartConnect, err = stories.NewStartConnectStory(repos.States, connectors, cfg.ConnectMemberIDs, cfg.NetworksRedirectURI)
	collect(err)
	built.FinishConnect, err = stories.NewFinishConnectStory(repos, vault, connectors)
	collect(err)
	built.TestConnection, err = stories.NewTestConnectionStory(repos, vault, connectors)
	collect(err)
	built.RemoveConn, err = stories.NewRemoveConnectionStory(repos, vault, connectors)
	collect(err)
	built.SetDestination, err = stories.NewSetDestinationStory(repos.Destinations)
	collect(err)
	built.Prepare, err = stories.NewPreparePublicationStory(repos, store.draftRepo, extras, access, store.scheduledPostRepo,
		stories.PrepareConfig{XPostsPerDay: cfg.XPostsPerDay, YouTubeUploadsPerDay: cfg.YouTubeUploadsPerDay})
	collect(err)
	built.EditDelivery, err = stories.NewEditDeliveryStory(repos)
	collect(err)
	built.Approve, err = stories.NewApprovePublicationStory(repos, store.draftRepo, extras, access, schedule, store.scheduledPostRepo, objects, gates.media)
	collect(err)
	built.Cancel, err = stories.NewCancelDeliveryStory(repos, schedule)
	collect(err)
	built.Retry, err = stories.NewRetryDeliveryStory(repos)
	collect(err)
	built.Status, err = stories.NewPublicationStatusStory(repos, store.scheduledPostRepo)
	collect(err)
	built.List, err = stories.NewListPublicationsStory(repos, publications, store.scheduledPostRepo)
	collect(err)
	hooks := []crosspostDomain.DeliveryFinished{}
	metricsHook, err := stories.NewCrosspostMetricsHook(metrics.MustInitCrosspostDeliveriesMetric())
	collect(err)
	hooks = append(hooks, metricsHook)
	if notifications != nil {
		push, err := stories.NewCrosspostPushHook(notifications)
		collect(err)
		hooks = append(hooks, push)
	}
	if setup.bot != nil {
		requests, err := videostudioRepo.NewRequestRepository(store.db)
		collect(err)
		setup.statusHook, err = stories.NewCrosspostTelegramStatus(store.telegramLinkRepo, setup.bot, built.Status, built.Cancel, built.Retry, requests,
			telegramDomain.NewMemberSet(cfg.VideoStudioMemberIDs))
		collect(err)
		hooks = append(hooks, setup.statusHook)
	}
	if cfg.MetaAppSecret != "" {
		signed, err := metaGate.NewDeletionCallback(cfg.MetaAppSecret)
		collect(err)
		story, err := stories.NewMetaCallbackStory(connections, built.RemoveConn, repos.Audit)
		collect(err)
		setup.metaCallbacks = &v0.MetaCallbacks{Requests: signed, Story: story, StatusURL: strings.TrimRight(cfg.BloomaryAPIRootURL, "/") + "/api/meta/data-deletion/status"}
	}
	worker, err := stories.NewDeliveryWorker(repos, vault, guard, publishers, connectors, store.draftRepo, 30*time.Second, hooks)
	collect(err)
	checker, err := stories.NewConnectionChecker(connections, connections, vault, connectors)
	collect(err)
	retention, err := stories.NewRetentionJob(publications, repos.Audit, objects)
	collect(err)
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	sealed, err := crosspostRepo.NewSealedRepository(store.db)
	if err != nil {
		return nil, nil, err
	}
	keys, err := stories.NewCredentialKeys(sealed, setup.vault, metrics.MustInitCrosspostKeysMetric())
	if err != nil {
		return nil, nil, err
	}
	setup.workers = append(setup.workers, worker.RunWorker, checker.Run, retention.Run, keys.RunCounts)
	setup.repos, setup.media = repos, gates.media
	return built, deletion, nil
}

// newAccountDeletion runs the crosspost step before the drafts and appends each deleted member to the deletion log
// <db path>.deletion-log after the deletion succeeds.
func newAccountDeletion(store *authStore, crosspostDeletion accountDomain.MemberDataDeleter, memberData []accountDomain.MemberDataDeleter,
	draftImages draftDomain.ObjectStore, databasePath string) (accountDomain.AccountDeletionStory, error) {
	deletion, err := newUnloggedAccountDeletion(store, crosspostDeletion, memberData, draftImages)
	if err != nil {
		return nil, err
	}
	log, err := deletionlogRepo.NewFile(databasePath + ".deletion-log")
	if err != nil {
		return nil, err
	}
	return stories.NewLoggedAccountDeletion(deletion, log)
}

func newUnloggedAccountDeletion(store *authStore, crosspostDeletion accountDomain.MemberDataDeleter, memberData []accountDomain.MemberDataDeleter,
	draftImages draftDomain.ObjectStore) (accountDomain.AccountDeletionStory, error) {
	return stories.NewAccountDeletionStoryWithSteps(store.dailyRepo, store.draftRepo, store.accountRepo,
		[]accountDomain.MemberDataDeleter{crosspostDeletion}, memberData, draftImages)
}

// replayDeletions is `bloomary-api replay-deletions <backup time, RFC3339>`: after a restore (API stopped), every
// member in <db path>.deletion-log deleted after the backup's time is deleted again through account deletion
// (ops notes/backup-restore.md step 4). It prints each replayed member ID to stdout.
func replayDeletions(ctx context.Context, config platform.Config, store *authStore, productEvents any, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bloomary-api replay-deletions <backup time, RFC3339, e.g. 2026-11-03T03:15:00Z>")
	}
	since, err := time.Parse(time.RFC3339, args[0])
	if err != nil {
		return fmt.Errorf("backup time: %w", err)
	}
	objects, err := newDraftImageStore(config)
	if err != nil {
		return err
	}
	repos, _, _, err := newCrosspostRepos(store.db)
	if err != nil {
		return err
	}
	extras, ok := store.draftRepo.(draftDomain.ExtrasRepository)
	if !ok {
		return errors.New("draft repository does not read draft extras")
	}
	vault, err := newCredentialVault(config)
	if err != nil {
		return err
	}
	var credentialVault crosspostDomain.CredentialVault
	if vault != nil {
		credentialVault = vault
	}
	gates, err := crosspostGates(config, objects, nil)
	if err != nil {
		return err
	}
	connectors := gates.connectors
	auditKey, err := readAuditHMACKey(config)
	if err != nil {
		return err
	}
	crosspostDeletion, err := stories.NewDeleteCrosspostDataStory(repos, credentialVault, connectors, extras, objects, auditKey)
	if err != nil {
		return err
	}
	deletion, err := newUnloggedAccountDeletion(store, crosspostDeletion, memberDataDeleters(store, productEvents), objects)
	if err != nil {
		return err
	}
	log, err := deletionlogRepo.NewFile(config.DatabasePath + ".deletion-log")
	if err != nil {
		return err
	}
	replayed, err := stories.ReplayDeletions(ctx, log, deletion, since)
	for _, memberID := range replayed {
		fmt.Println(memberID)
	}
	return err
}
