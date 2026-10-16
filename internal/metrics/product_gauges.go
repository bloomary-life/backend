package metrics

import (
	"context"
	"errors"
	"sync"
	"time"

	metricsDomain "bloomary-api/internal/domain/metrics"
	"bloomary-api/internal/platform/logger"

	"github.com/prometheus/client_golang/prometheus"
)

const productGaugeCacheTTL = 24 * time.Hour

type ProductGauges struct {
	ConnectedUsers     prometheus.Gauge
	SignupsYesterday   prometheus.Gauge
	ActivatedYesterday prometheus.Gauge
	ActiveYesterday    prometheus.Gauge
	Users4PostsLast7d  prometheus.Gauge
}

type ProductGaugeValues struct {
	SignupsYesterday   float64
	ActivatedYesterday float64
	ActiveYesterday    float64
	Users4PostsLast7d  float64
}

type ProductGaugeCache struct {
	mu        sync.Mutex
	day       string
	expiresAt time.Time
	values    ProductGaugeValues
	hasValues bool
}

func NewProductGaugeCache() *ProductGaugeCache {
	return &ProductGaugeCache{}
}

func (c *ProductGaugeCache) Refresh(ctx context.Context, repo metricsDomain.MetricsRepository, gauges ProductGauges, now time.Time) error {
	if c == nil {
		return errors.New("product gauge cache is nil")
	}

	if repo == nil {
		return errors.New("metrics repository is nil")
	}

	yesterday := yesterdayStart(now)
	day := dayString(yesterday)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Current connection count must update even when daily gauges are cached.
	if gauges.ConnectedUsers != nil {
		connected, err := repo.CountConnectedUsers(ctx)
		if err != nil {
			logger.FromContext(ctx).WithError(err).Error("refresh_connected_users_failed")
			return err
		}
		gauges.ConnectedUsers.Set(float64(connected))
	}

	if c.hasValues && c.day == day && now.Before(c.expiresAt) {
		setProductGauges(gauges, c.values)
		return nil
	}

	today := yesterday.AddDate(0, 0, 1)
	weekStart := today.AddDate(0, 0, -7)

	signups, err := repo.CountSignupsSince(ctx, yesterday)
	if err != nil {
		if c.hasValues {
			setProductGauges(gauges, c.values)
		}
		logger.FromContext(ctx).WithError(err).Error("refresh_product_gauges_failed")
		return err
	}

	activated, err := repo.CountActivatedSince(ctx, yesterday)
	if err != nil {
		if c.hasValues {
			setProductGauges(gauges, c.values)
		}
		logger.FromContext(ctx).WithError(err).Error("refresh_product_gauges_failed")
		return err
	}

	active, err := repo.CountActiveSince(ctx, yesterday)
	if err != nil {
		if c.hasValues {
			setProductGauges(gauges, c.values)
		}
		logger.FromContext(ctx).WithError(err).Error("refresh_product_gauges_failed")
		return err
	}

	users4Posts, err := repo.CountUsersWithMinPostsSince(ctx, 4, weekStart)
	if err != nil {
		if c.hasValues {
			setProductGauges(gauges, c.values)
		}
		logger.FromContext(ctx).WithError(err).Error("refresh_product_gauges_failed")
		return err
	}

	values := ProductGaugeValues{
		SignupsYesterday:   float64(signups),
		ActivatedYesterday: float64(activated),
		ActiveYesterday:    float64(active),
		Users4PostsLast7d:  float64(users4Posts),
	}

	c.day = day
	c.expiresAt = now.Add(productGaugeCacheTTL)
	c.values = values
	c.hasValues = true
	setProductGauges(gauges, values)
	return nil
}

func setProductGauges(gauges ProductGauges, values ProductGaugeValues) {
	if gauges.SignupsYesterday != nil {
		gauges.SignupsYesterday.Set(values.SignupsYesterday)
	}

	if gauges.ActivatedYesterday != nil {
		gauges.ActivatedYesterday.Set(values.ActivatedYesterday)
	}

	if gauges.ActiveYesterday != nil {
		gauges.ActiveYesterday.Set(values.ActiveYesterday)
	}

	if gauges.Users4PostsLast7d != nil {
		gauges.Users4PostsLast7d.Set(values.Users4PostsLast7d)
	}
}

func yesterdayStart(now time.Time) time.Time {
	utc := now.UTC()
	today := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -1)
}

func dayString(day time.Time) string {
	return day.UTC().Format("2006-01-02")
}
