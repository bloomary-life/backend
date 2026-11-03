package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	notificationDomain "bloomary-api/internal/domain/notification"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	crosspostRepo "bloomary-api/internal/gates/repositories/crosspost"
	deletionlogRepo "bloomary-api/internal/gates/repositories/deletionlog"
	vaultGate "bloomary-api/internal/gates/vault"
	v0 "bloomary-api/internal/handlers/http/v0"
	"bloomary-api/internal/metrics"
	"bloomary-api/internal/platform"
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

// logCrosspostConfig logs the publish mode at start (never key material).
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
}

// crosspostSetup carries what the crosspost wiring needs into newApplicationStories, and the background jobs it
// built back out (started by main once the stories exist).
type crosspostSetup struct {
	vault   *vaultGate.AESGCMVault
	config  platform.Config
	workers []func(context.Context)
}

// Network gates register here (strategy: one gate package + one map entry). Empty until internal/gates/{x,meta,
// youtube,tiktok} land: then networks answer "Not open yet" and dry_run still simulates.
func crosspostGates() (map[crosspostDomain.Provider]crosspostDomain.ProviderConnector, map[crosspostDomain.Network]crosspostDomain.NetworkPublisher) {
	return map[crosspostDomain.Provider]crosspostDomain.ProviderConnector{}, map[crosspostDomain.Network]crosspostDomain.NetworkPublisher{}
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
	repos := stories.CrosspostRepos{Connections: connections, Destinations: destinations, Publications: publications, Deliveries: deliveries,
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
	connectors, publishers := crosspostGates()
	var vault crosspostDomain.CredentialVault
	if setup.vault != nil {
		vault = setup.vault
	}
	deletion, err := stories.NewDeleteCrosspostDataStory(repos, vault, connectors, extras, objects, []byte(setup.config.AuditHMACKey))
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
	built := &v0.CrosspostStories{StatusLine: stories.CrosspostStatusLine, LinkedInLine: stories.LinkedInStatusLine}
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
	built.Approve, err = stories.NewApprovePublicationStory(repos, store.draftRepo, extras, access, schedule, store.scheduledPostRepo, objects, nil)
	collect(err)
	built.Cancel, err = stories.NewCancelDeliveryStory(repos, schedule)
	collect(err)
	built.Retry, err = stories.NewRetryDeliveryStory(repos)
	collect(err)
	built.Status, err = stories.NewPublicationStatusStory(repos, store.scheduledPostRepo)
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
	worker, err := stories.NewDeliveryWorker(repos, vault, guard, publishers, connectors, store.draftRepo, 30*time.Second, hooks)
	collect(err)
	checker, err := stories.NewConnectionChecker(connections, connections, vault, connectors)
	collect(err)
	retention, err := stories.NewRetentionJob(publications, repos.Audit, objects)
	collect(err)
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	setup.workers = append(setup.workers, worker.RunWorker, checker.Run, retention.Run)
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
	connectors, _ := crosspostGates()
	crosspostDeletion, err := stories.NewDeleteCrosspostDataStory(repos, credentialVault, connectors, extras, objects, []byte(config.AuditHMACKey))
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
