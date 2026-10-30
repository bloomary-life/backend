package deepgram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/httpclient"
)

const DefaultBaseURL = "https://api.deepgram.com"

// API transcribes pre-recorded audio with Deepgram (POST /v1/listen with raw bytes).
// mip_opt_out=true keeps the request out of Deepgram's Model Improvement Program, so
// Deepgram retains the audio only while processing it.
type API struct {
	client  httpclient.Client
	baseURL string
	apiKey  string
}

var _ telegramDomain.Transcriber = (*API)(nil)

func New(client httpclient.Client, baseURL, apiKey string) (*API, error) {
	if client == nil {
		return nil, fmt.Errorf("deepgram gate: http client is required")
	}

	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("deepgram gate: API key is required")
	}

	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}

	return &API{client: client, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey}, nil
}

type listenResponse struct {
	Results struct {
		Channels []struct {
			DetectedLanguage string `json:"detected_language"`
			Alternatives     []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"channels"`
	} `json:"results"`
}

func (a *API) Transcribe(ctx context.Context, audio []byte, mimeType string) (telegramDomain.Transcript, error) {
	query := url.Values{}
	query.Set("model", "nova-3-general")
	query.Set("detect_language", "true")
	query.Set("smart_format", "true")
	query.Set("mip_opt_out", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/listen?"+query.Encode(), bytes.NewReader(audio))
	if err != nil {
		return telegramDomain.Transcript{}, fmt.Errorf("deepgram listen request: %w", err)
	}

	if strings.TrimSpace(mimeType) == "" {
		mimeType = "audio/ogg"
	}

	req.Header.Set("Authorization", "Token "+a.apiKey)
	req.Header.Set("Content-Type", mimeType)
	resp, err := a.client.Do(req, httpclient.WithPathLabel("/v1/listen"))
	if err != nil {
		return telegramDomain.Transcript{}, fmt.Errorf("deepgram listen: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return telegramDomain.Transcript{}, fmt.Errorf("deepgram listen: status %d", resp.StatusCode)
	}

	var decoded listenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&decoded); err != nil {
		return telegramDomain.Transcript{}, fmt.Errorf("deepgram listen: decode: %w", err)
	}

	if len(decoded.Results.Channels) == 0 || len(decoded.Results.Channels[0].Alternatives) == 0 {
		return telegramDomain.Transcript{}, telegramDomain.ErrTranscriptEmpty
	}

	channel := decoded.Results.Channels[0]
	text := strings.TrimSpace(channel.Alternatives[0].Transcript)
	if text == "" {
		return telegramDomain.Transcript{}, telegramDomain.ErrTranscriptEmpty
	}

	return telegramDomain.Transcript{Text: text, Language: channel.DetectedLanguage}, nil
}
