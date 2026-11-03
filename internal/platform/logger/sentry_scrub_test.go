package logger

import (
	"errors"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

func TestScrubSentryEventRemovesSecrets(t *testing.T) {
	// Verifies: BE-041
	presigned := "https://bucket.example.com/jobs/1/master.mp4?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=SYNTHETIC&X-Amz-Signature=0123456789abcdef0123456789abcdef"
	event := &sentry.Event{
		Message:   "upload failed: PUT " + presigned,
		Exception: []sentry.Exception{{Value: `post failed: Authorization: Bearer synthetic.token.value access_token=synthetic-access`}},
		Tags:      map[string]string{"url": "https://api.example.com/oauth/callback?code=synthetic-code&state=s1"},
		Contexts: map[string]sentry.Context{"logger": {
			"error":   errors.New(`refresh failed {"refresh_token":"synthetic-refresh"}`),
			"jwt":     "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c3ludGhldGljc2lnbmF0dXJl",
			"nested":  map[string]any{"api_key": "api_key=synthetic-key-value"},
			"draft":   "Plain words stay readable.",
			"attempt": 2,
			"status":  "status code: 502",
		}},
		Breadcrumbs: []*sentry.Breadcrumb{{Message: "GET " + presigned, Data: map[string]any{"url": presigned}}},
		Request: &sentry.Request{
			URL:         "https://api.example.com/api/producer/jobs?token=synthetic",
			QueryString: "token=synthetic",
			Cookies:     "session=synthetic",
			Headers:     map[string]string{"Authorization": "Bearer synthetic", "X-Telegram-Bot-Api-Secret-Token": "synthetic", "Accept": "application/json"},
			Env:         map[string]string{"REMOTE_ADDR": "192.0.2.1"},
		},
	}

	scrubbedEvent := scrubSentryEvent(event, nil)

	require.Equal(t, "upload failed: PUT https://bucket.example.com/jobs/1/master.mp4", scrubbedEvent.Message)
	require.Equal(t, "https://api.example.com/oauth/callback", scrubbedEvent.Tags["url"])
	require.Equal(t, "https://api.example.com/api/producer/jobs", scrubbedEvent.Request.URL)
	require.Empty(t, scrubbedEvent.Request.QueryString)
	require.Empty(t, scrubbedEvent.Request.Cookies)
	require.Nil(t, scrubbedEvent.Request.Env)
	require.Equal(t, "[redacted]", scrubbedEvent.Request.Headers["Authorization"])
	require.Equal(t, "[redacted]", scrubbedEvent.Request.Headers["X-Telegram-Bot-Api-Secret-Token"])
	require.Equal(t, "application/json", scrubbedEvent.Request.Headers["Accept"])
	require.Equal(t, "Plain words stay readable.", scrubbedEvent.Contexts["logger"]["draft"])
	require.Equal(t, 2, scrubbedEvent.Contexts["logger"]["attempt"])
	require.Equal(t, "status code: 502", scrubbedEvent.Contexts["logger"]["status"])
	require.Equal(t, "https://bucket.example.com/jobs/1/master.mp4", scrubbedEvent.Breadcrumbs[0].Data["url"])
	for _, text := range []string{
		scrubbedEvent.Exception[0].Value, scrubbedEvent.Breadcrumbs[0].Message,
		scrubbedEvent.Contexts["logger"]["error"].(string), scrubbedEvent.Contexts["logger"]["jwt"].(string),
		scrubbedEvent.Contexts["logger"]["nested"].(map[string]any)["api_key"].(string),
	} {
		require.NotContains(t, text, "synthetic", text)
		require.NotContains(t, text, "X-Amz", text)
	}
}
