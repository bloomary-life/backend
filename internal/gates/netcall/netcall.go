// Package netcall holds what the cross-posting network gates share: sending a request without leaking its URL,
// reading a bounded answer, mapping it to the crosspost domain errors, and the media source they read from.
package netcall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	"bloomary-api/internal/platform/httpclient"
)

// MaxAnswerBytes bounds a JSON answer from a network.
const MaxAnswerBytes = 1 << 20

// DefaultRetryAfter is used when a network says "slow down" without saying for how long.
const DefaultRetryAfter = 15 * time.Minute

// MediaSource is where frozen delivery media lives (implemented by *objectstore.R2ObjectStore).
type MediaSource interface {
	// Open streams an object; the caller closes it.
	Open(ctx context.Context, key string) (body io.ReadCloser, size int64, contentType string, err error)
	// SignedURL is a time-limited GET link a network can pull the object from.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Answer is a network's reply, read in full (bounded by MaxAnswerBytes).
type Answer struct {
	Header http.Header
	Body   []byte
	Status int
}

// OK reports a 2xx status.
func (a Answer) OK() bool { return a.Status >= 200 && a.Status <= 299 }

// Send performs the request and reads the answer. A transport error loses its URL (it can hold codes or a
// session URI). The caller decides what a failure means (before or after the create request).
func Send(client httpclient.Client, req *http.Request, label string) (Answer, error) {
	resp, err := client.Do(req, httpclient.WithPathLabel(label))
	if err != nil {
		return Answer{}, fmt.Errorf("%s: %w", label, StripURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswerBytes))
	if err != nil {
		return Answer{}, fmt.Errorf("%s: read answer: %w", label, StripURL(err))
	}
	return Answer{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// StripURL replaces a *url.Error by its inner error, so no URL (query tokens, session URIs) reaches an error text.
func StripURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// Bearer sets the access token as the only place it travels (security.md §2).
func Bearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

// Fail wraps a domain error with the network and its own code, for LastErrorCode (category + network code).
func Fail(category error, network, code string) error {
	return crosspostDomain.NetworkError{Err: category, Network: network, Code: code}
}

// Unknown marks a failure after the create/publish request went out: needs_review, never retried automatically.
func Unknown(network string, err error) error {
	return fmt.Errorf("%w: %s: %w", crosspostDomain.ErrOutcomeUnknown, network, StripURL(err))
}

// RetryAfter reads Retry-After (seconds) or an epoch-seconds reset header (X: x-rate-limit-reset).
func RetryAfter(header http.Header, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if reset, err := strconv.ParseInt(strings.TrimSpace(header.Get("x-rate-limit-reset")), 10, 64); err == nil {
		if wait := time.Unix(reset, 0).Sub(now); wait > 0 {
			return wait
		}
	}
	return DefaultRetryAfter
}

// RateLimited is the domain's "wait and try again".
func RateLimited(wait time.Duration) error {
	if wait <= 0 {
		wait = DefaultRetryAfter
	}
	return crosspostDomain.RateLimitedError{RetryAfter: wait}
}

// AllowedHost reports whether link is https on host or a subdomain of it, or exactly the test base URL.
func AllowedHost(link, base string, hosts ...string) bool {
	parsed, err := url.Parse(link)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if b, err := url.Parse(base); err == nil && b.Host != "" && parsed.Scheme == b.Scheme && parsed.Host == b.Host {
		return true
	}
	if parsed.Scheme != "https" {
		return false
	}
	name := strings.ToLower(parsed.Hostname())
	for _, host := range hosts {
		if name == host || strings.HasSuffix(name, "."+host) {
			return true
		}
	}
	return false
}

// SafeID reports whether a network ID can go into a URL path: non-empty, at most 128 bytes, letters, digits, _ and -.
func SafeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Sleep waits for d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
