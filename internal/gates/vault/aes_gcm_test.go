package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

func syntheticKey(fill byte) []byte { return bytes.Repeat([]byte{fill}, 32) }

func TestSealAndOpenRoundTrip(t *testing.T) {
	// Verifies: BE-035
	vault, err := NewAESGCMVault(Keyring{ActiveID: "k1", Keys: map[string][]byte{"k1": syntheticKey(1)}})
	require.NoError(t, err)
	ctx := context.Background()
	binding := crosspostDomain.VaultBinding{RecordID: "conn-1", MemberID: "member-1", Provider: crosspostDomain.ProviderX}
	credentials := crosspostDomain.Credentials{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Scope: "tweet.write", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}

	sealed, err := vault.Seal(ctx, binding, credentials)
	require.NoError(t, err)
	require.Equal(t, "k1", sealed.KeyID)
	require.Equal(t, SchemaVersion, sealed.SchemaVersion)
	require.NotContains(t, string(sealed.Data), "synthetic")

	opened, err := vault.Open(ctx, binding, sealed)
	require.NoError(t, err)
	require.Equal(t, credentials, opened)

	again, err := vault.Seal(ctx, binding, credentials)
	require.NoError(t, err)
	require.NotEqual(t, sealed.Data, again.Data, "a fresh data key and nonce per record")
}

func TestRecordSealedUnderOldKeyOpensAfterRotation(t *testing.T) {
	// Verifies: BE-035
	ctx := context.Background()
	binding := crosspostDomain.VaultBinding{RecordID: "dest-1", MemberID: "member-1", Provider: crosspostDomain.ProviderMeta}
	old, err := NewAESGCMVault(Keyring{ActiveID: "k1", Keys: map[string][]byte{"k1": syntheticKey(1)}})
	require.NoError(t, err)
	sealed, err := old.Seal(ctx, binding, crosspostDomain.Credentials{AccessToken: "synthetic-page-token"})
	require.NoError(t, err)

	keyring, err := ParseKeyring([]byte(`{"active":"k2","keys":{"k1":"` + base64.StdEncoding.EncodeToString(syntheticKey(1)) + `","k2":"` + base64.StdEncoding.EncodeToString(syntheticKey(2)) + `"}}`))
	require.NoError(t, err)
	rotated, err := NewAESGCMVault(keyring)
	require.NoError(t, err)
	require.Equal(t, 2, rotated.KeyCount())

	opened, err := rotated.Open(ctx, binding, sealed)
	require.NoError(t, err)
	require.Equal(t, "synthetic-page-token", opened.AccessToken)
	resealed, err := rotated.Seal(ctx, binding, opened)
	require.NoError(t, err)
	require.Equal(t, "k2", resealed.KeyID)
}
