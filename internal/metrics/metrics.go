package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "bloomary"

// initCounter creates a counter metric with the given name, help text, and labels.
func initCounter(name, help string, labels []string) *prometheus.CounterVec {
	return promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
}

// initHistogram creates a histogram metric with the given name, help text, and labels.
func initHistogram(name, help string, labels []string) *prometheus.HistogramVec {
	return promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
}

// initGauge creates a gauge metric with the given name and help text.
func initGauge(name, help string) prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	})
}

// initGaugeVec creates a gauge vector metric with the given name, help text, and labels.
func initGaugeVec(name, help string, labels []string) *prometheus.GaugeVec {
	return promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	}, labels)
}

// Product gauges
func MustInitConnectedUsersMetric() prometheus.Gauge {
	return initGauge("connected_users", "Total users with a stored LinkedIn connection")
}

func MustInitSignupsYesterdayMetric() prometheus.Gauge {
	return initGauge("signups_yesterday", "Users who connected LinkedIn yesterday")
}

func MustInitActivatedYesterdayMetric() prometheus.Gauge {
	return initGauge("activated_yesterday", "Users whose first app-published post was yesterday")
}

func MustInitActiveYesterdayMetric() prometheus.Gauge {
	return initGauge("active_yesterday", "Users who published yesterday")
}

func MustInitUsers4PostsLast7dMetric() prometheus.Gauge {
	return initGauge("users_4_posts_last_7d", "Users with at least 4 app-published posts in the last 7 days")
}

// Product counters
func MustInitPostsPublishMetric() *prometheus.CounterVec {
	return initCounter("posts_publish_total", "LinkedIn post publish attempts", []string{"result"})
}

func MustInitLinkedInAuthMetric() *prometheus.CounterVec {
	return initCounter("linkedin_auth_total", "LinkedIn auth attempts", []string{"result"})
}

func MustInitFeedbackMetric() *prometheus.CounterVec {
	return initCounter("feedback_total", "Support and feedback submissions", []string{"result", "kind"})
}

func MustInitRateLimitMetric() *prometheus.CounterVec {
	return initCounter("rate_limit_total", "Quota and rate-limit blocks", []string{"scope"})
}

func MustInitPaddleWebhookMetric() *prometheus.CounterVec {
	return initCounter("paddle_webhook_total", "Paddle webhook processing outcomes", []string{"event_type", "result"})
}

// HTTP metrics
func MustInitHTTPRequestsMetric() *prometheus.CounterVec {
	return initCounter("http_requests_total", "HTTP request count", []string{"route", "method", "status"})
}

func MustInitHTTPRequestDurationMetric() *prometheus.HistogramVec {
	return initHistogram("http_request_duration_seconds", "HTTP request duration in seconds", []string{"route", "method", "status"})
}

// Outbound HTTP metrics
func MustInitOutboundHTTPRequestsMetric() *prometheus.CounterVec {
	return initCounter("outbound_http_requests_total", "Outbound HTTP request count", []string{"client", "host", "path", "method", "status"})
}

func MustInitOutboundHTTPRequestDurationMetric() *prometheus.HistogramVec {
	return initHistogram("outbound_http_request_duration_seconds", "Outbound HTTP request duration in seconds", []string{"client", "host", "path", "method", "status"})
}

func MustInitOutboundHTTPInFlightMetric() *prometheus.GaugeVec {
	return initGaugeVec("outbound_http_in_flight", "Outbound HTTP requests currently in flight", []string{"client", "host", "path", "method", "status"})
}
