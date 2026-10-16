package v0

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLinkedInProfileRefreshesPhotoWithExistingToken(t *testing.T) {
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "existing-token").Return(linkedinDomain.Profile{
		MemberID: "member-1", Name: "Kat", PictureURL: "https://media.licdn.com/fresh-photo",
	}, nil)
	svc := &service{linkedIn: linkedIn}
	request := httptest.NewRequest(http.MethodGet, "/api/linkedin/profile?memberId=member-1", nil)
	request.Header.Set("Authorization", "Bearer existing-token")
	response := httptest.NewRecorder()
	svc.LinkedInProfile(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var profile APILinkedInProfile
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &profile))
	require.Equal(t, "https://media.licdn.com/fresh-photo", profile.PictureURL)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.NotContains(t, response.Body.String(), "existing-token")
}
