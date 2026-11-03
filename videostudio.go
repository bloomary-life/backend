package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	draftDomain "bloomary-api/internal/domain/draft"
	telegramDomain "bloomary-api/internal/domain/telegram"
	videostudioDomain "bloomary-api/internal/domain/videostudio"
	geminiGate "bloomary-api/internal/gates/gemini"
	objectstoreGate "bloomary-api/internal/gates/objectstore"
	videostudioRepo "bloomary-api/internal/gates/repositories/videostudio"
	vaultGate "bloomary-api/internal/gates/vault"
	v0 "bloomary-api/internal/handlers/http/v0"
	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/logger"
	"bloomary-api/internal/stories"
)

// geminiKeyBinding ties the sealed Gemini key to its use; changing it makes old sealed files unreadable.
var geminiKeyBinding = crosspostDomain.VaultBinding{RecordID: "gemini-tts", MemberID: "bloomary", Provider: crosspostDomain.ProviderGoogle}

// sealedFile is the JSON in BLOOMARY_GEMINI_KEY_SEALED_PATH.
type sealedFile struct {
	KeyID         string `json:"keyId"`
	Data          []byte `json:"data"`
	SchemaVersion int    `json:"schemaVersion"`
}

// sealGeminiKey is `bloomary-api seal-gemini-key`: it reads the Gemini key from stdin and prints it sealed with the
// keyring's active key; ops writes the output to BLOOMARY_GEMINI_KEY_SEALED_PATH (0400, owned by the API's user).
func sealGeminiKey(ctx context.Context, config platform.Config, in io.Reader, out io.Writer) error {
	vault, err := newCredentialVault(config)
	if err != nil {
		return err
	}
	if vault == nil {
		return errors.New("seal-gemini-key needs the credentials keyring (BLOOMARY_KEYRING_PATH)")
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	key := strings.TrimSpace(line)
	if key == "" {
		return errors.New("seal-gemini-key: no key on stdin")
	}
	sealed, err := vault.Seal(ctx, geminiKeyBinding, crosspostDomain.Credentials{AccessToken: key})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(sealedFile{KeyID: sealed.KeyID, SchemaVersion: sealed.SchemaVersion, Data: sealed.Data})
}

// geminiKeyOpener reads and opens the sealed key at each call (a re-sealed key needs no restart).
func geminiKeyOpener(path string, vault *vaultGate.AESGCMVault) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read sealed gemini key: %w", err)
		}
		var file sealedFile
		if err := json.Unmarshal(raw, &file); err != nil {
			return "", fmt.Errorf("sealed gemini key: %w", err)
		}
		credentials, err := vault.Open(ctx, geminiKeyBinding, crosspostDomain.SealedCredentials{KeyID: file.KeyID, SchemaVersion: file.SchemaVersion, Data: file.Data})
		if err != nil {
			return "", err
		}
		return credentials.AccessToken, nil
	}
}

// studioOff says why the studio stays off, or "" when everything it needs is configured.
func studioOff(setup *crosspostSetup, objects draftDomain.ObjectStore, crosspost *v0.CrosspostStories) string {
	cfg := setup.config
	_, onR2 := objects.(*objectstoreGate.R2ObjectStore)
	switch {
	case len(telegramDomain.NewMemberSet(cfg.VideoStudioMemberIDs)) == 0:
		return "no studio members"
	case crosspost == nil:
		return "cross-posting is off"
	case setup.bot == nil:
		return "the user bot is off"
	case !onR2:
		return "R2 isn't configured"
	case setup.media == nil:
		return "the media worker (ffmpeg) isn't configured"
	case cfg.GeminiKeySealedFile == "":
		return "the sealed Gemini key isn't configured"
	}
	return ""
}

// videoStudio is what the studio adds to the router and the bot.
type videoStudio struct {
	producer *v0.ProducerStories
	telegram *stories.StudioTelegram
}

// newVideoStudio wires the studio when every part it needs is on (nil otherwise, logged with the reason): request
// stories, the Telegram conversation (also the studio's notifier), producer stories, the voice-over and the worker.
func newVideoStudio(ctx context.Context, setup *crosspostSetup, store *authStore, objects draftDomain.ObjectStore, crosspost *v0.CrosspostStories,
	transcriberOn bool, voiceMembers telegramDomain.MemberSet, platformURL string) (*videoStudio, error) {
	if reason := studioOff(setup, objects, crosspost); reason != "" {
		logger.FromContext(ctx).WithFields(logger.Fields{"reason": reason}).Info("video_studio_off")
		return nil, nil
	}
	cfg := setup.config
	r2 := objects.(*objectstoreGate.R2ObjectStore)
	requests, err := videostudioRepo.NewRequestRepository(store.db)
	if err != nil {
		return nil, err
	}
	jobs, err := videostudioRepo.NewJobQueue(store.db)
	if err != nil {
		return nil, err
	}
	extras, ok := store.draftRepo.(draftDomain.ExtrasRepository)
	if !ok {
		return nil, errors.New("draft repository does not write draft extras")
	}
	members := telegramDomain.NewMemberSet(cfg.VideoStudioMemberIDs)
	limits := videostudioDomain.DefaultLimits
	if cfg.StudioPaidShotsPerVideo > 0 {
		limits.PaidShotsPerVideo = cfg.StudioPaidShotsPerVideo
	}
	deps := stories.StudioDeps{Requests: requests, Jobs: jobs, Media: objectstoreGate.NewStudioMedia(r2), Allowance: stories.NewStudioAllowlist(members),
		Audit: setup.repos.Audit, Publications: crosspost.Status, Limits: limits}

	var errs []error
	collect := func(err error) { errs = append(errs, err) }
	tg := stories.StudioTelegramConfig{Links: store.telegramLinkRepo, Bot: setup.bot, Requests: requests, Media: deps.Media, Publications: crosspost.Status,
		Edit: crosspost.EditDelivery, Approve: crosspost.Approve, Members: members, VoiceMembers: voiceMembers, VoiceOn: transcriberOn, PlatformURL: platformURL, Limits: limits}
	tg.Start, err = stories.NewStartVideoRequestStory(deps)
	collect(err)
	tg.Changes, err = stories.NewRequestChangesStory(deps)
	collect(err)
	tg.PaidShots, err = stories.NewApprovePaidShotsStory(deps)
	collect(err)
	tg.ApproveVideo, err = stories.NewApproveVideoStory(deps, store.draftRepo, extras, setup.repos.Destinations, crosspost.Prepare, crosspost.EditDelivery)
	collect(err)
	tg.Cancel, err = stories.NewCancelVideoRequestStory(deps)
	collect(err)
	tg.Discard, err = stories.NewDiscardVideoRequestStory(deps, crosspost.Cancel)
	collect(err)
	tg.Retry, err = stories.NewRetryVideoRequestStory(deps)
	collect(err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	telegram, err := stories.NewStudioTelegram(tg)
	if err != nil {
		return nil, err
	}
	deps.Notifier = telegram

	producer := &v0.ProducerStories{}
	producer.Claim, err = stories.NewClaimProducerJobStory(deps)
	collect(err)
	producer.Heartbeat, err = stories.NewHeartbeatProducerJobStory(deps)
	collect(err)
	producer.Uploads, err = stories.NewPrepareUploadsStory(deps)
	collect(err)
	producer.Scenario, err = stories.NewSubmitScenarioStory(deps, crosspost.EditDelivery)
	collect(err)
	producer.Render, err = stories.NewSubmitRenderStory(deps, setup.media)
	collect(err)
	producer.Failure, err = stories.NewReportProducerFailureStory(deps)
	collect(err)
	httpClient, err := setup.newClient("gemini_tts", 2*time.Minute)
	collect(err)
	voice, err := geminiGate.NewSealedKeyTTS(geminiGate.TTSConfig{HTTP: httpClient, Store: r2, KeyPrefix: "voice-over/" + cfg.SentryEnvironment},
		geminiKeyOpener(cfg.GeminiKeySealedFile, setup.vault))
	collect(err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	voiceOver, err := stories.NewMakeVoiceOverStory(deps, voice)
	if err != nil {
		return nil, err
	}
	worker, err := stories.NewStudioWorker(deps, voiceOver, requests, 15*time.Second)
	if err != nil {
		return nil, err
	}
	setup.workers = append(setup.workers, worker.Run, worker.RunRetention)
	logger.FromContext(ctx).WithFields(logger.Fields{"members": len(members), "producer_api": cfg.ProducerTokenSHA256 != "", "paid_shots_per_video": limits.PaidShotsPerVideo}).Info("video_studio_on")
	return &videoStudio{producer: producer, telegram: telegram}, nil
}
