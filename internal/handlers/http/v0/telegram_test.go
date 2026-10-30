package v0

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	telegramDomain "bloomary-api/internal/domain/telegram"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestTelegramWebhookRejectsRequestsWithoutTheSecret(t *testing.T) {
	// Verifies: BE-009
	bot := telegramMocks.NewMockBotStory(t)
	svc := &service{telegramBotStory: bot, config: Config{TelegramWebhookSecret: "webhook-secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(`{"update_id":1,"message":{"from":{"id":1},"chat":{"id":1,"type":"private"},"text":"hi"}}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestTelegramWebhookPassesVoiceUpdateToBot(t *testing.T) {
	// Verifies: BE-010
	handled := make(chan telegramDomain.Update, 1)
	bot := telegramMocks.NewMockBotStory(t)
	bot.EXPECT().HandleUpdate(mock.Anything, mock.Anything).Run(func(_ context.Context, update telegramDomain.Update) { handled <- update }).Return(nil)
	svc := &service{telegramBotStory: bot, config: Config{TelegramWebhookSecret: "webhook-secret"}}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", strings.NewReader(
		`{"update_id":1,"message":{"message_id":5,"from":{"id":77,"is_bot":false,"username":"kat"},"chat":{"id":77,"type":"private"},"voice":{"file_id":"f-1","file_unique_id":"u","duration":12,"mime_type":"audio/ogg","file_size":2048}}}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", "webhook-secret")
	response := httptest.NewRecorder()

	svc.TelegramWebhook(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	select {
	case update := <-handled:
		require.Equal(t, int64(77), update.Message.From.ID)
		require.Equal(t, "private", update.Message.ChatType)
		require.Equal(t, &telegramDomain.Voice{FileID: "f-1", MimeType: "audio/ogg", Duration: 12, FileSize: 2048}, update.Message.Voice)
	case <-time.After(2 * time.Second):
		t.Fatal("update was not handled")
	}
}

func TestTelegramLinkStartReturnsDeepLinkForSignedInMember(t *testing.T) {
	// Verifies: BE-009
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	links := telegramMocks.NewMockLinkStory(t)
	links.EXPECT().Start(mock.Anything, "member-1").Return(telegramDomain.LinkStart{DeepLink: "https://t.me/BloomaryBot?start=l_x", Number: 42, ExpiresAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}, nil)
	svc := &service{linkedIn: linkedIn, telegramLinkStory: links}
	request := httptest.NewRequest(http.MethodPost, "/api/telegram/link?memberId=member-1", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()

	svc.TelegramLink(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var body telegramLinkResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, telegramLinkResponse{DeepLink: "https://t.me/BloomaryBot?start=l_x", Number: 42, ExpiresAt: "2026-10-30T12:00:00Z"}, body)
}
