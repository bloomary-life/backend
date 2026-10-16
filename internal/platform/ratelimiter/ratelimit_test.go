package ratelimiter

import (
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestKeyedRateLimiterSweepsExpiredItems(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	limiter := &keyedRateLimiter{
		config: RateLimitConfig{Burst: 1, RPS: 1000},
		ttl:    time.Minute,
		items:  map[string]*keyedRateLimitItem{},
		now:    func() time.Time { return now },
	}
	if err := limiter.Wait(context.Background(), "member-1"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(limiter.items) != 1 {
		t.Fatalf("items = %d", len(limiter.items))
	}

	now = now.Add(2 * time.Minute)
	if err := limiter.Wait(context.Background(), "member-2"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if _, found := limiter.items["member-1"]; found {
		t.Fatal("expired key was not swept")
	}
	if _, found := limiter.items["member-2"]; !found {
		t.Fatal("new key was not retained")
	}
}

func TestKeyedRateLimiterDoesNotSweepActiveItem(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	limiter := &keyedRateLimiter{
		ttl:       time.Minute,
		lastSweep: now.Add(-2 * time.Minute),
		items: map[string]*keyedRateLimitItem{
			"member-1": {
				limiter:  rate.NewLimiter(rate.Limit(1), 1),
				lastUsed: now.Add(-2 * time.Minute),
				refs:     1,
			},
		},
	}

	limiter.mu.Lock()
	limiter.sweepExpired(now)
	_, found := limiter.items["member-1"]
	limiter.mu.Unlock()

	if !found {
		t.Fatal("active key was swept")
	}
}
