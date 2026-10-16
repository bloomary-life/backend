package limiter

import "time"

type DailyIPLimiter interface {
	TryConsume(domain Type, ip string, now time.Time) bool
}
