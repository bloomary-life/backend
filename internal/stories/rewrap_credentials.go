package stories

import (
	"context"
	"fmt"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/platform/logger"
)

// ActiveKeyVault is the vault plus the keyring key that seals new records.
type ActiveKeyVault interface {
	crosspostDomain.CredentialVault
	ActiveKeyID() string
}

// CredentialKeys re-wraps sealed records under the active keyring key and counts records per key ID.
type CredentialKeys struct {
	records crosspostDomain.SealedRecordStore
	vault   ActiveKeyVault
	counter crosspostDomain.KeyCounter
}

// NewCredentialKeys: counter may be nil (the rewrap command doesn't publish metrics).
func NewCredentialKeys(records crosspostDomain.SealedRecordStore, vault ActiveKeyVault, counter crosspostDomain.KeyCounter) (*CredentialKeys, error) {
	if err := requireDeps("credential keys", map[string]any{"sealed records": records, "vault": vault}); err != nil {
		return nil, err
	}
	return &CredentialKeys{records: records, vault: vault, counter: counter}, nil
}

// Rewrap opens every record sealed with an older key and seals it again with the active key (security.md §2.3).
// A record changed meanwhile (e.g. a refresh) is skipped: its new value is already under the active key.
func (k *CredentialKeys) Rewrap(ctx context.Context) (int, error) {
	records, err := k.records.SealedRecords(ctx)
	if err != nil {
		return 0, err
	}
	active, rewrapped := k.vault.ActiveKeyID(), 0
	for _, record := range records {
		if record.Sealed.KeyID == active {
			continue
		}
		binding := crosspostDomain.VaultBinding{RecordID: record.ID, MemberID: record.MemberID, Provider: record.Provider}
		credentials, err := k.vault.Open(ctx, binding, record.Sealed)
		if err != nil {
			return rewrapped, fmt.Errorf("open %s %s: %w", record.Kind, record.ID, err)
		}
		previous := record.Sealed
		if record.Sealed, err = k.vault.Seal(ctx, binding, credentials); err != nil {
			return rewrapped, err
		}
		if err := k.records.ReplaceSealed(ctx, record, previous); err != nil {
			if isVersionConflict(err) {
				continue
			}
			return rewrapped, err
		}
		rewrapped++
	}
	return rewrapped, nil
}

// CountKeys publishes the records per key ID.
func (k *CredentialKeys) CountKeys(ctx context.Context) {
	records, err := k.records.SealedRecords(ctx)
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("crosspost_key_count_failed")
		return
	}
	counts := map[string]int{}
	for _, record := range records {
		counts[record.Sealed.KeyID]++
	}
	if k.counter != nil {
		k.counter.RecordKeyCounts(counts)
	}
}

// RunCounts counts once a day until ctx ends.
func (k *CredentialKeys) RunCounts(ctx context.Context) { runDaily(ctx, k.CountKeys) }
