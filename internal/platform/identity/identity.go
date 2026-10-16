// Package identity provides app-account-token generation for Apple IAP.
//
// This lives in the platform layer (not domain) because the token seed
// encodes the identity provider (e.g. "linkedin"), which is an infra
// concern — the IAP domain must stay provider-agnostic.
package identity

import (
	"strings"

	"github.com/google/uuid"
)

// AppAccountTokenFor produces a deterministic UUID that Apple uses as
// appAccountToken in StoreKit receipts. The provider prefix ensures
// tokens from different auth sources never collide.
func AppAccountTokenFor(provider, identityID string) string {
	return uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("bloomary:"+strings.TrimSpace(provider)+":"+strings.TrimSpace(identityID)),
	).String()
}
