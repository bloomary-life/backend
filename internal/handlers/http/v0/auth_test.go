package v0

import (
	"testing"
	"time"
)

func TestLinkedInTokenVerificationCacheStoresAndReturnsMemberID(t *testing.T) {
	cache := newLinkedInTokenVerificationCache(time.Hour)

	cache.set("token-1", "member-a")
	memberID, ok := cache.get("token-1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if memberID != "member-a" {
		t.Fatalf("expected member-a, got %s", memberID)
	}
}

func TestLinkedInTokenVerificationCacheReturnsFalseForUnknownToken(t *testing.T) {
	cache := newLinkedInTokenVerificationCache(time.Hour)

	_, ok := cache.get("unknown-token")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestLinkedInTokenVerificationCacheExpiresEntries(t *testing.T) {
	cache := newLinkedInTokenVerificationCache(-time.Second)

	cache.set("expiring-token", "member-b")
	memberID, ok := cache.get("expiring-token")
	if ok {
		t.Fatalf("expected expired entry to be removed, got %s", memberID)
	}
}
