package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"bloomary-api/internal/platform/logger"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	defaultMaxIdleConnections        = 100
	defaultMaxConnectionsPerHost     = 100
	defaultMaxIdleConnectionsPerHost = 100
	defaultTLSHandshakeTimeout       = 5 * time.Second
	defaultRequestTimeout            = 10 * time.Second
)

type httpClient struct {
	client            *http.Client
	metricsEnabled    bool
	metricsClientName string
	metricsDuration   *prometheus.HistogramVec
	metricsInFlight   *prometheus.GaugeVec
	metricsCounter    *prometheus.CounterVec
}

// MustNewClient creates a new Client instance with the provided options
// It panics if options are invalid or if initialization fails
func MustNewClient(ctx context.Context, opts ...ClientOptionFunc) Client {
	client, err := NewClient(ctx, opts...)
	if err != nil {
		panic(fmt.Sprintf("failed to initialize HTTP client: %v", err))
	}

	return client
}

// NewClient creates a new Client instance with the provided options
func NewClient(_ context.Context, opts ...ClientOptionFunc) (Client, error) {
	config, err := buildConfiguration(opts)
	if err != nil {
		return nil, fmt.Errorf("error building HTTP client configuration: %w", err)
	}

	transport := buildTransport(config)
	client := &http.Client{
		Timeout:   config.requestTimeout,
		Transport: transport,
	}

	return &httpClient{
		client:            client,
		metricsEnabled:    config.metricsEnabled,
		metricsClientName: config.metricsClientName,
		metricsDuration:   config.metricsDuration,
		metricsInFlight:   config.metricsInFlight,
		metricsCounter:    config.metricsCounter,
	}, nil
}

// buildTransport creates and configures an HTTP transport based on the provided configuration
func buildTransport(config *clientConfiguration) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	transport.MaxIdleConns = defaultMaxIdleConnections
	if config.maxIdleConns > 0 {
		transport.MaxIdleConns = config.maxIdleConns
	}

	transport.MaxConnsPerHost = defaultMaxConnectionsPerHost
	if config.maxConnsPerHost > 0 {
		transport.MaxConnsPerHost = config.maxConnsPerHost
	}

	transport.MaxIdleConnsPerHost = defaultMaxIdleConnectionsPerHost
	if config.maxIdleConnsPerHost > 0 {
		transport.MaxIdleConnsPerHost = config.maxIdleConnsPerHost
	}

	transport.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	if config.tlsHandshakeTimeout > 0 {
		transport.TLSHandshakeTimeout = config.tlsHandshakeTimeout
	}

	return transport
}

// Do performs an HTTP request and records metrics if enabled
func (c *httpClient) Do(req *http.Request, opts ...RequestOptionFunc) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("HTTP request cannot be nil")
	}

	requestOptions := buildRequestOptions(opts)
	var inFlightLabels []string
	if c.metricsEnabled {
		inFlightLabels = c.inFlightMetricLabels(req, requestOptions)
		c.metricsInFlight.WithLabelValues(inFlightLabels...).Inc()
		defer c.metricsInFlight.WithLabelValues(inFlightLabels...).Dec()
	}

	requestStartTime := time.Now()
	response, err := c.client.Do(req) //nolint:gosec // Shared client executes prebuilt requests; URL validation belongs to caller boundaries.
	requestDuration := time.Since(requestStartTime)

	if c.metricsEnabled {
		c.recordMetrics(req, response, requestDuration, requestOptions)
	}

	c.logRequest(req, response, err, requestDuration, requestOptions)

	return response, err
}

// recordMetrics records Prometheus metrics for the HTTP request
func (c *httpClient) recordMetrics(
	req *http.Request,
	response *http.Response,
	duration time.Duration,
	options *requestOptions,
) {
	labels := c.metricLabels(req, response, options)

	c.metricsDuration.WithLabelValues(labels...).Observe(duration.Seconds())
	c.metricsCounter.WithLabelValues(labels...).Add(1)
}

func (c *httpClient) metricLabels(req *http.Request, response *http.Response, options *requestOptions) []string {
	return []string{
		c.metricsClientName,
		req.URL.Host,
		extractPathLabel(req, options),
		req.Method,
		extractStatusCode(response),
	}
}

func (c *httpClient) inFlightMetricLabels(req *http.Request, options *requestOptions) []string {
	return []string{
		c.metricsClientName,
		req.URL.Host,
		extractPathLabel(req, options),
		req.Method,
		"in_flight",
	}
}

func (c *httpClient) logRequest(
	req *http.Request,
	response *http.Response,
	err error,
	duration time.Duration,
	options *requestOptions,
) {
	log, ok := logger.LookupContext(req.Context())
	if !ok {
		return
	}

	fields := logger.Fields{
		"client":      c.metricsClientName,
		"host":        req.URL.Host,
		"path":        extractPathLabel(req, options),
		"method":      req.Method,
		"status":      extractStatusCode(response),
		"duration_ms": duration.Milliseconds(),
	}
	if err != nil {
		log.WithFields(fields).WithError(err).Error("http_client_request")
		return
	}

	log.WithFields(fields).Info("http_client_request")
}

// extractStatusCode extracts the status code from the response as a string
func extractStatusCode(response *http.Response) string {
	if response == nil {
		return ""
	}

	return strconv.Itoa(response.StatusCode)
}

// extractPathLabel extracts the path label for metrics, using custom label if provided
func extractPathLabel(req *http.Request, options *requestOptions) string {
	if len(options.metricsPathLabel) > 0 {
		return options.metricsPathLabel
	}

	return req.URL.Path
}
