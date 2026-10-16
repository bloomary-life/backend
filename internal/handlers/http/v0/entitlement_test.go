package v0

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	entitlementDomain "bloomary-api/internal/domain/entitlement"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	limiterMocks "bloomary-api/internal/mocks/domain/limiter"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// App Review must see the paywall: the demo member is not hardcoded Pro.
func TestEntitlementHandlerResolvesDemoMember(t *testing.T) {
	limiter := limiterMocks.NewMockDailyIPLimiter(t)
	limiter.EXPECT().TryConsume(mock.Anything, mock.Anything, mock.Anything).Return(true)
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, demoMemberID, mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial, DraftLimit: entitlementDomain.ProDraftLimit}, nil)
	service := &service{config: Config{AppleReviewCode: "review-code"}, entitlementLimiter: limiter, accessStory: access}

	request := httptest.NewRequest(http.MethodGet, "/api/entitlement?memberId="+demoMemberID, nil)
	request.Header.Set("Authorization", "Bearer "+demoTokenPrefix+"review-code")
	response := httptest.NewRecorder()
	service.Entitlement(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var body entitlementResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, entitlementDomain.TierTrial, body.Tier)
	require.False(t, body.IsPro)
	require.Equal(t, entitlementDomain.ProDraftLimit, body.DraftLimit)
}
