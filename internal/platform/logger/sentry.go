package logger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

type sentryAggregator struct {
	mu          sync.Mutex
	window      time.Duration
	environment string
	version     string
	buckets     map[string]sentryBucket
}

type sentryBucket struct {
	level     string
	message   string
	fields    Fields
	count     int
	firstSeen time.Time
	lastSeen  time.Time
	reported  time.Time
}

func newSentryAggregator(loggerConfig config) (*sentryAggregator, error) {
	dsn := strings.TrimSpace(loggerConfig.sentryDSN)
	if dsn == "" {
		return nil, errors.New("sentry dsn is required when sentry is enabled")
	}

	if err := sentry.Init(
		sentry.ClientOptions{
			Dsn:         dsn,
			Environment: loggerConfig.appEnvironment,
			Release:     loggerConfig.appVersion,
		},
	); err != nil {
		return nil, fmt.Errorf("sentry init failed: %w", err)
	}

	return &sentryAggregator{
		window:      loggerConfig.sentryWindow,
		environment: loggerConfig.appEnvironment,
		version:     loggerConfig.appVersion,
		buckets:     map[string]sentryBucket{},
	}, nil
}

func flushSentry(aggregator *sentryAggregator, timeout time.Duration) bool {
	aggregator.flush()
	return sentry.Flush(timeout)
}

func (a *sentryAggregator) report(ctx context.Context, level, message string, fields Fields) {
	now := time.Now()
	fields = mergeFields(ContextFields(ctx), fields)
	key := level + ":" + message + ":" + fmt.Sprint(fields["error"])

	a.mu.Lock()
	a.deleteReportedBefore(now.Add(-a.window))
	bucket := a.buckets[key]
	if bucket.count == 0 {
		bucket.firstSeen = now
	}

	bucket.level = level
	bucket.message = message
	bucket.fields = fields
	bucket.count++
	bucket.lastSeen = now
	if !bucket.reported.IsZero() && now.Sub(bucket.reported) < a.window {
		a.buckets[key] = bucket
		a.mu.Unlock()
		return
	}

	bucket.reported = now
	reported := bucket
	bucket.count = 0
	a.buckets[key] = bucket
	a.mu.Unlock()

	a.capture(reported)
}

func (a *sentryAggregator) deleteReportedBefore(cutoff time.Time) {
	for key, bucket := range a.buckets {
		if bucket.count == 0 && !bucket.reported.IsZero() && bucket.reported.Before(cutoff) {
			delete(a.buckets, key)
		}
	}
}

func (a *sentryAggregator) flush() {
	a.mu.Lock()
	buckets := make([]sentryBucket, 0, len(a.buckets))
	for key, bucket := range a.buckets {
		if bucket.count == 0 {
			continue
		}

		bucket.reported = time.Now()
		reported := bucket
		bucket.count = 0
		a.buckets[key] = bucket
		buckets = append(buckets, reported)
	}

	a.mu.Unlock()

	for _, bucket := range buckets {
		a.capture(bucket)
	}
}

func (a *sentryAggregator) capture(bucket sentryBucket) {
	sentry.WithScope(
		func(scope *sentry.Scope) {
			scope.SetLevel(sentry.Level(bucket.level))
			scope.SetTag("logger.level", bucket.level)
			if a.environment != "" {
				scope.SetTag("app.environment", a.environment)
			}

			if a.version != "" {
				scope.SetTag("app.version", a.version)
			}

			contextValues := sentry.Context{
				"aggregate_count":  bucket.count,
				"aggregate_window": a.window.String(),
				"first_seen":       bucket.firstSeen.Format(time.RFC3339),
				"last_seen":        bucket.lastSeen.Format(time.RFC3339),
			}
			for key, value := range bucket.fields {
				contextValues[key] = value
			}

			scope.SetContext("logger", contextValues)
			if err, ok := bucket.fields["error"].(error); ok {
				sentry.CaptureException(err)
				return
			}

			sentry.CaptureMessage(bucket.message)
		},
	)
}
