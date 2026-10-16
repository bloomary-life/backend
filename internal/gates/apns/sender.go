package apns

import (
	domain "bloomary-api/internal/domain/notification"
	"bloomary-api/internal/platform/httpclient"
	"bloomary-api/internal/platform/logger"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// Apple requires refreshing the provider token no more than once every 20 minutes and no
	// less than once every 60 minutes; 45m sits inside that window.
	// See docs/ADR-002-secrets-and-apns-push.md.
	tokenTTL = 45 * time.Minute

	// A forced refresh is only allowed when APNs itself rejects the cached token, and only once
	// per Send, so a retry loop can never mint tokens faster than Apple permits.
	maxTokenRefreshes = 1

	maxAttempts    = 3
	retryBaseDelay = 500 * time.Millisecond

	defaultProductionHost = "https://api.push.apple.com"
	defaultSandboxHost    = "https://api.sandbox.push.apple.com"

	environmentSandbox = "sandbox"

	// Keeps Prometheus path cardinality bounded: the real path ends in a device token.
	pathLabel = "/3/device"
)

type sendOutcome int

const (
	outcomeDelivered sendOutcome = iota
	outcomeInvalidDevice
	outcomeStaleProviderToken
	outcomeTokenThrottled
	outcomeRetryable
	outcomePermanent
)

type Sender struct {
	key                  *ecdsa.PrivateKey
	keyID, teamID, topic string
	client               httpclient.Client
	environments         map[string]struct{}
	productionHost       string
	sandboxHost          string

	mu      sync.Mutex
	jwt     string
	issued  time.Time
	skipped map[string]struct{}
}

var _ domain.Sender = (*Sender)(nil)

// New builds an APNs sender. environments lists the APNs environments the signing key is
// authorized for; a device registered in an unlisted environment is skipped, because Apple
// rejects an environment-scoped key with 403 BadEnvironmentKeyInToken.
func New(
	keyPEM, keyID, teamID, topic string,
	client httpclient.Client,
	environments []string,
) (*Sender, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("APNs key must be PEM PKCS8")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse APNs key: %w", err)
	}

	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() || keyID == "" || teamID == "" || topic == "" {
		return nil, fmt.Errorf("APNs requires P256 key, key ID, team ID and bundle ID")
	}

	if client == nil {
		return nil, fmt.Errorf("http client is missing")
	}

	supported := make(map[string]struct{}, len(environments))
	for _, environment := range environments {
		if value := strings.TrimSpace(environment); value != "" {
			supported[value] = struct{}{}
		}
	}

	if len(supported) == 0 {
		return nil, fmt.Errorf("APNs requires at least one environment")
	}

	return &Sender{
		key:            ec,
		keyID:          keyID,
		teamID:         teamID,
		topic:          topic,
		client:         client,
		environments:   supported,
		productionHost: defaultProductionHost,
		sandboxHost:    defaultSandboxHost,
		skipped:        make(map[string]struct{}, 2),
	}, nil
}

// authorization returns a cached provider token, minting a new one when it is missing, older
// than tokenTTL, or when force is set after APNs rejected the cached one. Minting happens
// under the mutex so concurrent sends cannot each generate a token.
func (s *Sender) authorization(force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !force && s.jwt != "" && time.Since(s.issued) < tokenTTL {
		return s.jwt, nil
	}

	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": s.keyID})
	if err != nil {
		return "", err
	}

	claims, err := json.Marshal(map[string]any{"iss": s.teamID, "iat": time.Now().Unix()})
	if err != nil {
		return "", err
	}

	value := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(value))
	r, sigS, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}

	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	sigS.FillBytes(signature[32:])
	s.jwt = value + "." + base64.RawURLEncoding.EncodeToString(signature)
	s.issued = time.Now()

	return s.jwt, nil
}

func (s *Sender) Send(ctx context.Context, d domain.Device, jobID, status string) (bool, error) {
	if _, supported := s.environments[d.Environment]; !supported {
		s.noteSkippedEnvironment(ctx, d.Environment)
		return false, nil
	}

	payload, err := marshalPayload(jobID, status)
	if err != nil {
		return false, err
	}

	var lastErr error
	force := false
	refreshes := 0

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := wait(ctx, retryBaseDelay<<uint(attempt-2)); err != nil {
				return false, err
			}
		}

		token, err := s.authorization(force)
		if err != nil {
			return false, err
		}

		force = false

		outcome, err := s.post(ctx, token, d, jobID, payload)
		lastErr = err

		switch outcome {
		case outcomeDelivered:
			return false, nil
		case outcomeInvalidDevice:
			return true, err
		case outcomeStaleProviderToken:
			if refreshes >= maxTokenRefreshes {
				return false, err
			}

			refreshes++
			force = true
		case outcomeTokenThrottled, outcomePermanent:
			// Retrying a token throttle would deepen it; permanent errors never succeed.
			return false, err
		case outcomeRetryable:
		}
	}

	return false, lastErr
}

func (s *Sender) post(
	ctx context.Context,
	token string,
	d domain.Device,
	jobID string,
	payload []byte,
) (sendOutcome, error) {
	host := s.productionHost
	if d.Environment == environmentSandbox {
		host = s.sandboxHost
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+pathLabel+"/"+d.Token, bytes.NewReader(payload))
	if err != nil {
		return outcomePermanent, err
	}

	req.Header.Set("authorization", "bearer "+token)
	req.Header.Set("apns-topic", s.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", "0")
	req.Header.Set("apns-collapse-id", jobID)
	req.Header.Set("content-type", "application/json")

	resp, err := s.client.Do(req, httpclient.WithPathLabel(pathLabel))
	if err != nil {
		return outcomeRetryable, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return outcomeDelivered, nil
	}

	apnsID := resp.Header.Get("apns-id")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return outcomeRetryable, fmt.Errorf("APNs status %d: read response: %w", resp.StatusCode, err)
	}

	reason := parseReason(body)

	return classify(resp.StatusCode, reason),
		fmt.Errorf("APNs status %d reason %s apns-id %s", resp.StatusCode, reason, apnsID)
}

func classify(status int, reason string) sendOutcome {
	switch {
	case status == http.StatusForbidden && (reason == "ExpiredProviderToken" || reason == "InvalidProviderToken"):
		return outcomeStaleProviderToken
	case status == http.StatusTooManyRequests && reason == "TooManyProviderTokenUpdates":
		return outcomeTokenThrottled
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return outcomeRetryable
	case status == http.StatusGone || reason == "BadDeviceToken" || reason == "DeviceTokenNotForTopic":
		return outcomeInvalidDevice
	default:
		return outcomePermanent
	}
}

// noteSkippedEnvironment logs once per environment. It stays at Info level on purpose: Error
// would be picked up by the stage health digest and pushed to Telegram on every device.
func (s *Sender) noteSkippedEnvironment(ctx context.Context, environment string) {
	s.mu.Lock()
	_, seen := s.skipped[environment]
	if !seen {
		s.skipped[environment] = struct{}{}
	}

	s.mu.Unlock()

	if seen {
		return
	}

	logger.FromContext(ctx).
		WithField("environment", environment).
		Info("apns_environment_not_configured_push_skipped")
}

func marshalPayload(jobID, status string) ([]byte, error) {
	message := "Your scheduled post needs attention. Open Bloomary to check its status."
	if status == "published" {
		message = "Your scheduled post was published to LinkedIn."
	}

	return json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": "Bloomary", "body": message},
			"sound": "default",
		},
		"scheduleId": jobID,
	})
}

func parseReason(body []byte) string {
	var result struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return ""
	}

	return result.Reason
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
