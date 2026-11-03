package gemini

import (
	"context"
	"fmt"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

// SealedKeyTTS opens the Gemini key from the vault for each voice-over (so a rotated key needs no restart) and speaks
// through TTS; the key lives only for the call.
type SealedKeyTTS struct {
	openKey func(ctx context.Context) (string, error)
	cfg     TTSConfig
}

var _ videostudioDomain.VoiceOver = (*SealedKeyTTS)(nil)

// NewSealedKeyTTS: cfg is a TTSConfig without APIKey; openKey returns the opened key.
func NewSealedKeyTTS(cfg TTSConfig, openKey func(ctx context.Context) (string, error)) (*SealedKeyTTS, error) {
	if openKey == nil {
		return nil, fmt.Errorf("gemini tts: key opener is required")
	}
	cfg.APIKey = "checked-per-call"
	if _, err := NewTTS(cfg); err != nil {
		return nil, err
	}
	cfg.APIKey = ""
	return &SealedKeyTTS{openKey: openKey, cfg: cfg}, nil
}

func (s *SealedKeyTTS) Speak(ctx context.Context, text string) (string, error) {
	key, err := s.openKey(ctx)
	if err != nil {
		return "", fmt.Errorf("gemini tts: open key: %w", err)
	}
	cfg := s.cfg
	cfg.APIKey = key
	tts, err := NewTTS(cfg)
	if err != nil {
		return "", err
	}
	return tts.Speak(ctx, text)
}
