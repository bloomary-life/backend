package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	key, contentType string
	data             []byte
}

func (m *memoryStore) Put(_ context.Context, key, contentType string, data []byte, _ map[string]string) error {
	m.key, m.contentType, m.data = key, contentType, data
	return nil
}

func TestGeminiTTSStoresVoiceOverAsWAV(t *testing.T) {
	// Verifies: BE-081
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0} // synthetic 16-bit samples
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1beta/models/"+DefaultModel+":generateContent", r.URL.Path)
		require.Equal(t, "synthetic-key", r.Header.Get("x-goog-api-key"))
		require.Empty(t, r.URL.RawQuery, "the key never goes in the URL")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` +
			base64.StdEncoding.EncodeToString(pcm) + `"}}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()
	store := &memoryStore{}
	tts, err := NewTTS(TTSConfig{HTTP: httpclient.MustNewClient(context.Background()), Store: store, APIKey: "synthetic-key",
		BaseURL: server.URL + "/v1beta", KeyPrefix: "voice-over/test"})
	require.NoError(t, err)

	key, err := tts.Speak(context.Background(), "Hello from Bloomary.")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, "voice-over/test/") && strings.HasSuffix(key, ".wav"))
	require.Equal(t, key, store.key)
	require.Equal(t, "audio/wav", store.contentType)
	require.Equal(t, "RIFF", string(store.data[:4]))
	require.Equal(t, "WAVE", string(store.data[8:12]))
	require.Len(t, store.data, 44+len(pcm))
	require.True(t, bytes.HasSuffix(store.data, pcm))
	config := request["generationConfig"].(map[string]any)
	require.Equal(t, []any{"AUDIO"}, config["responseModalities"])
	voice := config["speechConfig"].(map[string]any)["voiceConfig"].(map[string]any)["prebuiltVoiceConfig"].(map[string]any)
	require.Equal(t, DefaultVoice, voice["voiceName"])
}
