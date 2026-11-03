package stories

import (
	"bytes"
	"testing"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	crosspostRepo "bloomary-api/internal/gates/repositories/crosspost"
	vaultGate "bloomary-api/internal/gates/vault"

	"github.com/stretchr/testify/require"
)

type keyCounts map[string]int

func (k *keyCounts) RecordKeyCounts(counts map[string]int) { *k = counts }

func TestRewrapSealsEveryRecordUnderTheActiveKeyAndCountsRecordsPerKey(t *testing.T) {
	// Verifies: BE-065
	f := newCrosspostFixture(t) // seals with k1
	x := f.destination(t, crosspostDomain.X)
	rotated, err := vaultGate.NewAESGCMVault(vaultGate.Keyring{ActiveID: "k2", Keys: map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32), "k2": bytes.Repeat([]byte{9}, 32)}})
	require.NoError(t, err)
	sealed, err := crosspostRepo.NewSealedRepository(f.db)
	require.NoError(t, err)
	counts := keyCounts{}
	keys, err := NewCredentialKeys(sealed, rotated, &counts)
	require.NoError(t, err)

	keys.CountKeys(f.ctx)
	require.Equal(t, keyCounts{"k1": 1}, counts)
	n, err := keys.Rewrap(f.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	keys.CountKeys(f.ctx)
	require.Equal(t, keyCounts{"k2": 1}, counts)

	connection, err := f.connections.ByID(f.ctx, xMember, x.ConnectionID)
	require.NoError(t, err)
	opened, err := rotated.Open(f.ctx, crosspostDomain.VaultBinding{RecordID: connection.ID, MemberID: xMember, Provider: crosspostDomain.ProviderX}, connection.Sealed)
	require.NoError(t, err)
	require.Equal(t, "synthetic-token", opened.AccessToken)
}
