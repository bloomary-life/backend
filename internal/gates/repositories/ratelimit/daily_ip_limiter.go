package ratelimit

import (
	"fmt"
	"sync"
	"time"

	limiterDomain "bloomary-api/internal/domain/limiter"
)

// DailyIPLimiter is a small in-memory per-domain, per-IP daily counter.
type DailyIPLimiter struct {
	mu         sync.Mutex
	limit      int
	currentDay string
	counts     map[string]int
}

func NewDailyIPLimiter(limit int) (*DailyIPLimiter, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("daily ip limiter: limit must be positive")
	}

	return &DailyIPLimiter{limit: limit, counts: make(map[string]int)}, nil
}

func (l *DailyIPLimiter) TryConsume(domain limiterDomain.Type, ip string, now time.Time) bool {
	day := now.UTC().Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.currentDay != day {
		l.currentDay = day
		l.counts = make(map[string]int)
	}

	key := string(domain) + ":" + ip
	if l.counts[key] >= l.limit {
		return false
	}

	l.counts[key]++
	return true
}
