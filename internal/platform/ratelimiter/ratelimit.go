package ratelimiter

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type RateLimitConfig struct {
	Burst int
	RPS   float64
}

type RateLimiter interface {
	Wait(ctx context.Context) error
}

type KeyedRateLimiter interface {
	Wait(ctx context.Context, key string) error
}

func NewRateLimiter(config RateLimitConfig) RateLimiter {
	return rate.NewLimiter(rate.Limit(config.RPS), config.Burst)
}

type keyedRateLimiter struct {
	config    RateLimitConfig
	ttl       time.Duration
	mu        sync.Mutex
	items     map[string]*keyedRateLimitItem
	lastSweep time.Time
	now       func() time.Time
}

type keyedRateLimitItem struct {
	limiter  *rate.Limiter
	lastUsed time.Time
	refs     int
}

func NewKeyedRateLimiter(config RateLimitConfig) KeyedRateLimiter {
	return &keyedRateLimiter{
		config: config,
		ttl:    30 * time.Minute,
		items:  map[string]*keyedRateLimitItem{},
		now:    time.Now,
	}
}

func (limiter *keyedRateLimiter) Wait(ctx context.Context, key string) error {
	if key == "" {
		key = "global"
	}

	nowFunc := limiter.now
	if nowFunc == nil {
		nowFunc = time.Now
	}

	now := nowFunc()
	limiter.mu.Lock()
	limiter.sweepExpired(now)
	item := limiter.items[key]
	if item == nil {
		item = &keyedRateLimitItem{limiter: rate.NewLimiter(rate.Limit(limiter.config.RPS), limiter.config.Burst)}
		limiter.items[key] = item
	}

	item.lastUsed = now
	item.refs++
	limiter.mu.Unlock()
	defer func() {
		limiter.mu.Lock()
		item.refs--
		item.lastUsed = nowFunc()
		limiter.mu.Unlock()
	}()
	return item.limiter.Wait(ctx)
}

func (limiter *keyedRateLimiter) sweepExpired(now time.Time) {
	if limiter.ttl <= 0 || now.Sub(limiter.lastSweep) < limiter.ttl {
		return
	}

	limiter.lastSweep = now
	for key, item := range limiter.items {
		if item.refs == 0 && now.Sub(item.lastUsed) >= limiter.ttl {
			delete(limiter.items, key)
		}
	}
}
