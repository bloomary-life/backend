package v0

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLinkedInPostHandlerPublishesForTrialMember(t *testing.T) {
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial}, nil)
	posts := linkedinMocks.NewMockLinkedInPostStory(t)
	posts.EXPECT().CreatePost(mock.Anything, mock.MatchedBy(func(in linkedinDomain.CreatePostInput) bool {
		return in.MemberID == "member-1" && in.Commentary == "Hello LinkedIn"
	})).Return(linkedinDomain.CreatePostOutput{PostID: "urn:li:share:1"}, nil)
	service := &service{linkedIn: linkedIn, accessStory: access, linkedInPostStory: posts}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("memberId", "member-1"))
	require.NoError(t, form.WriteField("commentary", "Hello LinkedIn"))
	require.NoError(t, form.Close())
	request := httptest.NewRequest(http.MethodPost, "/api/linkedin/post", &body)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", form.FormDataContentType())

	response := httptest.NewRecorder()
	service.LinkedInPost(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var out linkedInPostResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &out))
	require.Equal(t, "urn:li:share:1", out.PostID)
}
