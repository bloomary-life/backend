package httpclient

import (
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type clientConfiguration struct {
	maxIdleConns        int
	maxConnsPerHost     int
	maxIdleConnsPerHost int
	tlsHandshakeTimeout time.Duration
	requestTimeout      time.Duration
	metricsClientName   string
	metricsEnabled      bool
	metricsDuration     *prometheus.HistogramVec
	metricsInFlight     *prometheus.GaugeVec
	metricsCounter      *prometheus.CounterVec
}

// ClientOptionFunc is a function that modifies Client configuration during initialization
type ClientOptionFunc func(*clientConfiguration) error

// buildConfiguration creates a configuration from the provided options
func buildConfiguration(opts []ClientOptionFunc) (*clientConfiguration, error) {
	config := &clientConfiguration{
		requestTimeout: defaultRequestTimeout,
	}

	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, fmt.Errorf("error applying client option: %w", err)
		}
	}

	return config, nil
}

// WithTLSHandshakeTimeout sets the TLS handshake timeout for the HTTP client
func WithTLSHandshakeTimeout(timeout time.Duration) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if timeout <= 0 {
			return errors.New("TLS handshake timeout must be positive")
		}

		c.tlsHandshakeTimeout = timeout
		return nil
	}
}

// WithRequestTimeout sets the request timeout for the HTTP client
func WithRequestTimeout(timeout time.Duration) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if timeout <= 0 {
			return errors.New("request timeout must be positive")
		}

		c.requestTimeout = timeout
		return nil
	}
}

// WithMaxIdleConns sets the maximum number of idle connections in the connection pool
func WithMaxIdleConns(count int) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if count < 0 {
			return errors.New("max idle connections cannot be negative")
		}

		c.maxIdleConns = count
		return nil
	}
}

// WithMaxConnsPerHost sets the maximum number of connections per host
func WithMaxConnsPerHost(count int) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if count < 0 {
			return errors.New("max connections per host cannot be negative")
		}

		c.maxConnsPerHost = count
		return nil
	}
}

// WithMaxIdleConnsPerHost sets the maximum number of idle connections per host
func WithMaxIdleConnsPerHost(count int) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if count < 0 {
			return errors.New("max idle connections per host cannot be negative")
		}

		c.maxIdleConnsPerHost = count
		return nil
	}
}

// WithMetrics enables metrics collection for HTTP requests
// All metric vectors must be non-nil and metricsClientName must not be empty
func WithMetrics(
	metricsClientName string,
	metricDuration *prometheus.HistogramVec,
	metricInFlight *prometheus.GaugeVec,
	metricCounter *prometheus.CounterVec,
) ClientOptionFunc {
	return func(c *clientConfiguration) error {
		if len(metricsClientName) == 0 {
			return errors.New("metrics client name cannot be empty")
		}

		if metricDuration == nil {
			return errors.New("metric duration histogram vector cannot be nil")
		}

		if metricInFlight == nil {
			return errors.New("metric in-flight gauge vector cannot be nil")
		}

		if metricCounter == nil {
			return errors.New("metric counter vector cannot be nil")
		}

		c.metricsClientName = metricsClientName
		c.metricsDuration = metricDuration
		c.metricsInFlight = metricInFlight
		c.metricsCounter = metricCounter
		c.metricsEnabled = true

		return nil
	}
}

type requestOptions struct {
	metricsPathLabel string
}

// RequestOptionFunc is a function that modifies per-request options
type RequestOptionFunc func(*requestOptions)

// buildRequestOptions creates request options from the provided option functions
func buildRequestOptions(opts []RequestOptionFunc) *requestOptions {
	options := &requestOptions{}
	for _, opt := range opts {
		opt(options)
	}

	return options
}

// WithPathLabel sets a custom path label for metrics
// This allows overriding the default path label (req.URL.Path) in metrics
func WithPathLabel(label string) RequestOptionFunc {
	return func(o *requestOptions) {
		o.metricsPathLabel = label
	}
}
