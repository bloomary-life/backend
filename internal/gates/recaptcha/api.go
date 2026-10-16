package recaptcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	metricsDomain "bloomary-api/internal/domain/metrics"
	recaptchaDomain "bloomary-api/internal/domain/recaptcha"
	perror "bloomary-api/internal/platform/error"
	"bloomary-api/internal/platform/httpclient"
)

type API struct {
	httpClient       httpclient.Client
	verifyURL        string
	secret           string
	expectedHostname string
	expectedAction   string
	counters         metricsDomain.Recorder
}

var _ recaptchaDomain.Captcha = (*API)(nil)

func New(
	httpClient httpclient.Client, verifyURL, secret, expectedHostname, expectedAction string, counters metricsDomain.Recorder,
) (*API, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("http client is missing")
	}

	if strings.TrimSpace(verifyURL) == "" {
		return nil, fmt.Errorf("recaptcha verify url is missing")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("recaptcha secret is missing")
	}

	return &API{
		httpClient:       httpClient,
		verifyURL:        verifyURL,
		secret:           secret,
		expectedHostname: strings.TrimSpace(expectedHostname),
		expectedAction:   strings.TrimSpace(expectedAction),
		counters:         counters,
	}, nil
}

func (a *API) Verify(ctx context.Context, response, remoteAddr string) error {
	response = strings.TrimSpace(response)
	if response == "" {
		return errors.New("missing recaptcha response")
	}

	values := url.Values{}
	values.Set("secret", a.secret)
	values.Set("response", response)
	if remoteIP := extractRemoteIP(remoteAddr); remoteIP != "" {
		values.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.verifyURL, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req, httpclient.WithPathLabel("recaptcha_verify"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if resp.StatusCode == http.StatusTooManyRequests {
			a.counters.RecordRateLimit(metricsDomain.RateLimitRecaptchaAPI)
			return perror.New(perror.ErrCodeRateLimitHit, errors.New(http.StatusText(resp.StatusCode)))
		}

		return perror.New(perror.ErrCodeBadGateway, errors.New(http.StatusText(resp.StatusCode)))
	}

	var body struct {
		Success  bool   `json:"success"`
		Hostname string `json:"hostname"`
		Action   string `json:"action"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	if !body.Success {
		return errors.New("recaptcha verification failed")
	}

	if a.expectedHostname != "" && body.Hostname != a.expectedHostname {
		return fmt.Errorf("recaptcha hostname mismatch")
	}

	if a.expectedAction != "" && body.Action != a.expectedAction {
		return fmt.Errorf("recaptcha action mismatch")
	}

	return nil
}

func extractRemoteIP(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		remoteAddr = host
	}

	if net.ParseIP(remoteAddr) == nil {
		return ""
	}

	return remoteAddr
}
