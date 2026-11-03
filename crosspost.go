package main

import (
	"fmt"
	"os"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	vaultGate "bloomary-api/internal/gates/vault"
	"bloomary-api/internal/platform"
	"bloomary-api/internal/platform/logger"
)

// newCredentialVault loads the keyring when BLOOMARY_CREDENTIALS_KEYRING_FILE is set; nil when it isn't.
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
