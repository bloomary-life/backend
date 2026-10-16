package v0

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	appleiapDomain "bloomary-api/internal/domain/appleiap"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	appleiapMocks "bloomary-api/internal/mocks/domain/appleiap"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	perror "bloomary-api/internal/platform/error"
)

func TestAppleTransactionSyncHandlerSyncsTransaction(t *testing.T) {
	now := time.Now().UTC()
	linkedIn := linkedinMocks.NewMockGate(t)
	story := appleiapMocks.NewMockTransactionSyncStory(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	story.On("SyncTransaction", mock.Anything, appleiapDomain.SyncTransactionInput{
		MemberID:          "member-1",
		SignedTransaction: "signed-jws",
	}).Return(appleiapDomain.SyncResult{Entitlement: appleiapDomain.Entitlement{AccessUntil: now.Add(time.Hour), Status: appleiapDomain.StatusActive}, IsPro: true}, nil)
	service := &service{linkedIn: linkedIn, appleTransactionSync: story}

	response := httptest.NewRecorder()
	service.AppleTransactionSync(response, jsonRequest(`{"memberId":"member-1","signedTransaction":"signed-jws"}`))

	require.Equal(t, http.StatusOK, response.Code)
	var body appleTransactionSyncResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "synced", body.Status)
	require.True(t, body.IsPro)
}

func TestAppleTransactionSyncHandlerRejectsInvalidTransaction(t *testing.T) {
	linkedIn := linkedinMocks.NewMockGate(t)
	story := appleiapMocks.NewMockTransactionSyncStory(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	story.On("SyncTransaction", mock.Anything, mock.Anything).
		Return(appleiapDomain.SyncResult{}, perror.New(perror.ErrCodeRejected, nil, "bad transaction"))
	service := &service{linkedIn: linkedIn, appleTransactionSync: story}

	response := httptest.NewRecorder()
	service.AppleTransactionSync(response, jsonRequest(`{"memberId":"member-1","signedTransaction":"bad"}`))

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "invalid_apple_transaction")
}

func TestAppleTransactionSyncHandlerRequiresLinkedInAuth(t *testing.T) {
	service := &service{}

	response := httptest.NewRecorder()
	service.AppleTransactionSync(response, httptest.NewRequest(http.MethodPost, "/api/apple/transactions/sync", bytes.NewBufferString(`{"memberId":"member-1","signedTransaction":"signed-jws"}`)))

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "missing_linkedin_auth")
}

func TestAppleNotificationsHandlerProcessesNotification(t *testing.T) {
	story := appleiapMocks.NewMockNotificationStory(t)
	story.On("ProcessNotification", mock.Anything, appleiapDomain.NotificationInput{
		SignedPayload: "signed-payload",
	}).Return(nil)
	service := &service{appleNotifications: story}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/apple/notifications", bytes.NewBufferString(`{"signedPayload":"signed-payload"}`))
	service.AppleNotifications(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "processed")
}

func TestAppleNotificationsHandlerRejectsInvalidNotification(t *testing.T) {
	story := appleiapMocks.NewMockNotificationStory(t)
	story.On("ProcessNotification", mock.Anything, mock.Anything).
		Return(perror.New(perror.ErrCodeRejected, nil, "bad notification"))
	service := &service{appleNotifications: story}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/apple/notifications", bytes.NewBufferString(`{"signedPayload":"bad"}`))
	service.AppleNotifications(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "invalid_apple_notification")
}

func jsonRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/apple/transactions/sync", bytes.NewBufferString(body))
	request = request.WithContext(context.Background())
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "application/json")
	return request
}
