// Package gemini is the Gemini text-to-speech gate behind videostudio.VoiceOver.
// Docs (checked 2026-11-03): https://aistudio.google.com/docs/speech-generation (models, voices, audio format),
// https://aistudio.google.com/api-reference/generate-content/generate-content (generateContent request fields).
package gemini

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
	"bloomary-api/internal/gates/netcall"
	"bloomary-api/internal/platform/httpclient"
)

const (
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	// DefaultModel and DefaultVoice are listed on the speech-generation page (2026-11-03).
	DefaultModel = "gemini-3.8-flash-tts"
	DefaultVoice = "Kore"

	// pcmRate: raw audio/l16 is 24 kHz mono 16-bit signed little-endian unless its MIME type says otherwise.
	pcmRate = 24000
)

// AudioStore keeps the voice-over (implemented by *objectstore.R2ObjectStore).
type AudioStore interface {
	Put(ctx context.Context, objectKey, contentType string, data []byte, metadata map[string]string) error
}

// TTSConfig: APIKey is the paid-tier key opened from the vault; it travels only in the x-goog-api-key header and is
// never logged. KeyPrefix is where voice-overs go in the bucket (e.g. "voice-over/stage").
type TTSConfig struct {
	HTTP      httpclient.Client
	Store     AudioStore
	APIKey    string
	Model     string
	Voice     string
	BaseURL   string
	KeyPrefix string
}

// TTS is videostudio.VoiceOver through generateContent with an AUDIO response.
type TTS struct {
	cfg TTSConfig
}

var _ videostudioDomain.VoiceOver = (*TTS)(nil)

func NewTTS(cfg TTSConfig) (*TTS, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("gemini tts: api key is required")
	}
	if cfg.HTTP == nil || cfg.Store == nil {
		return nil, fmt.Errorf("gemini tts: http client and audio store are required")
	}
	if strings.TrimSpace(cfg.KeyPrefix) == "" {
		return nil, fmt.Errorf("gemini tts: key prefix is required")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Voice == "" {
		cfg.Voice = DefaultVoice
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.KeyPrefix = strings.Trim(cfg.KeyPrefix, "/")
	if !netcall.SafeID(strings.ReplaceAll(cfg.Model, ".", "_")) {
		return nil, fmt.Errorf("gemini tts: invalid model id")
	}
	return &TTS{cfg: cfg}, nil
}

type generateAnswer struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

// Speak sends the voice-over text, stores the audio as WAV and returns its object key.
func (t *TTS) Speak(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("gemini tts: empty text")
	}
	payload, err := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"parts": []any{map[string]string{"text": text}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": t.cfg.Voice}},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode gemini tts request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.BaseURL+"/models/"+t.cfg.Model+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build gemini tts request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", t.cfg.APIKey)
	answer, err := netcall.Send(t.cfg.HTTP, req, "gemini_tts")
	if err != nil {
		return "", err
	}
	if answer.Status == http.StatusTooManyRequests {
		return "", fmt.Errorf("gemini tts: rate limited or quota reached (429)")
	}
	if !answer.OK() {
		return "", fmt.Errorf("gemini tts: status %d", answer.Status)
	}
	var out generateAnswer
	if err := json.Unmarshal(answer.Body, &out); err != nil {
		return "", fmt.Errorf("decode gemini tts answer: %w", err)
	}
	if out.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("gemini tts: blocked (%s)", out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 || out.Candidates[0].Content.Parts[0].InlineData.Data == "" {
		return "", fmt.Errorf("gemini tts: answer has no audio")
	}
	inline := out.Candidates[0].Content.Parts[0].InlineData
	audio, err := base64.StdEncoding.DecodeString(inline.Data)
	if err != nil {
		return "", fmt.Errorf("gemini tts: decode audio: %w", err)
	}
	wav, err := asWAV(inline.MimeType, audio)
	if err != nil {
		return "", err
	}
	key, err := t.newKey()
	if err != nil {
		return "", err
	}
	if err := t.cfg.Store.Put(ctx, key, "audio/wav", wav, map[string]string{"model": t.cfg.Model, "voice": t.cfg.Voice}); err != nil {
		return "", fmt.Errorf("gemini tts: store audio: %w", err)
	}
	return key, nil
}

// asWAV keeps WAV as it is; raw L16/PCM (headerless) gets a RIFF header with the rate from its MIME type.
func asWAV(mimeType string, audio []byte) ([]byte, error) {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "audio/wav"), strings.HasPrefix(lower, "audio/x-wav"):
		return audio, nil
	case strings.HasPrefix(lower, "audio/l16"), strings.HasPrefix(lower, "audio/pcm"):
		return wavHeader(audio, rateFrom(lower)), nil
	default:
		return nil, fmt.Errorf("gemini tts: unexpected audio type %q", mimeType)
	}
}

// rateFrom reads ";rate=NNNN" from a MIME type, else 24 kHz.
func rateFrom(mimeType string) int {
	for _, param := range strings.Split(mimeType, ";") {
		if value, found := strings.CutPrefix(strings.TrimSpace(param), "rate="); found {
			if rate, err := strconv.Atoi(value); err == nil && rate > 0 {
				return rate
			}
		}
	}
	return pcmRate
}

// wavHeader wraps mono 16-bit little-endian PCM in a 44-byte RIFF/WAVE header.
func wavHeader(pcm []byte, rate int) []byte {
	const channels, bits = 1, 16
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+len(pcm)))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(rate*channels*bits/8))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels*bits/8))
	_ = binary.Write(&out, binary.LittleEndian, uint16(bits))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(pcm)))
	out.Write(pcm)
	return out.Bytes()
}

func (t *TTS) newKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gemini tts: key: %w", err)
	}
	return t.cfg.KeyPrefix + "/" + hex.EncodeToString(buf) + ".wav", nil
}
