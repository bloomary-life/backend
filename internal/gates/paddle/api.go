package paddle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	paddleDomain "bloomary-api/internal/domain/paddle_entitlement"
	"bloomary-api/internal/platform/httpclient"
)

type API struct {
	httpClient httpclient.Client
	baseURL    string
	apiKey     string
}

var _ paddleDomain.PortalGate = (*API)(nil)

func New(httpClient httpclient.Client, baseURL, apiKey string) (*API, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("paddle http client is missing")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("paddle base url is missing")
	}
	return &API{httpClient: httpClient, baseURL: strings.TrimRight(baseURL, "/"), apiKey: strings.TrimSpace(apiKey)}, nil
}

func (a *API) CreateCustomerPortal(ctx context.Context, customerID string) (string, error) {
	if a.apiKey == "" {
		return "", fmt.Errorf("paddle api key is not configured")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		a.baseURL+"/customers/"+customerID+"/portal-sessions",
		bytes.NewBufferString(`{}`),
	)
	if err != nil {
		return "", fmt.Errorf("create paddle portal request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create paddle portal session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("create paddle portal session: paddle returned %s", resp.Status)
	}
	var payload struct {
		Data struct {
			URLs struct {
				General struct {
					Overview string `json:"overview"`
				} `json:"general"`
			} `json:"urls"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode paddle portal session: %w", err)
	}
	if strings.TrimSpace(payload.Data.URLs.General.Overview) == "" {
		return "", fmt.Errorf("create paddle portal session: missing overview url")
	}
	return payload.Data.URLs.General.Overview, nil
}
