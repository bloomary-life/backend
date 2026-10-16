package v0

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDraftImagePresignDownloadHandlerPresignsForAuthenticatedMember(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Minute)
	linkedIn := linkedinMocks.NewMockGate(t)
	story := draftMocks.NewMockImageStory(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	story.On("PresignDownload", mock.Anything, draftDomain.PresignDownloadInput{
		MemberID: "member-1",
		DraftID:  "draft-1",
		ImageID:  "image-1",
	}).Return(draftDomain.PresignDownload{
		ImageID:     "image-1",
		DownloadURL: "https://r2.example/download",
		ExpiresAt:   expiresAt,
	}, nil)
	service := &service{linkedIn: linkedIn, draftImagesStory: story}

	response := httptest.NewRecorder()
	service.DraftImagePresignDownload(
		response,
		draftImageRequest("/api/draft-images/presign-download?memberId=member-1", `{"draftId":"draft-1","imageId":"image-1"}`),
	)

	require.Equal(t, http.StatusOK, response.Code)
	var body draftImagePresignDownloadResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "image-1", body.ImageID)
	require.Equal(t, "https://r2.example/download", body.DownloadURL)
	require.Equal(t, expiresAt.Format(time.RFC3339Nano), body.ExpiresAt)
}

// A lapsed member may still save a new draft while under the free limit, so the
// image for that new draft must be stageable.
func TestDraftImagePresignUploadHandlerAllowsLapsedNewDraft(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Minute)
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierLapsed, DraftLimit: entitlementDomain.FreeDraftLimit}, nil)
	sync := draftMocks.NewMockSyncStory(t)
	sync.EXPECT().Sync(mock.Anything, "member-1", []draftDomain.Upsert(nil), []string(nil), draftDomain.ObjectStore(nil)).
		Return([]draftDomain.Draft{{DraftID: "draft-1"}, {DraftID: "draft-2"}}, nil)
	story := draftMocks.NewMockImageStory(t)
	story.On("PresignUpload", mock.Anything, draftDomain.PresignUploadInput{
		MemberID:       "member-1",
		DraftID:        "draft-3",
		MimeType:       "image/png",
		SizeBytes:      10,
		ChecksumSHA256: "abc",
	}).Return(draftDomain.PresignUpload{ImageID: "image-1", UploadURL: "https://r2.example/upload", ExpiresAt: expiresAt}, nil)
	service := &service{linkedIn: linkedIn, accessStory: access, syncDraftsStory: sync, draftImagesStory: story}

	response := httptest.NewRecorder()
	service.DraftImagePresignUpload(
		response,
		draftImageRequest(
			"/api/draft-images/presign-upload?memberId=member-1",
			`{"draftId":"draft-3","mimeType":"image/png","sizeBytes":10,"checksumSHA256":"abc"}`,
		),
	)

	require.Equal(t, http.StatusOK, response.Code)
	var body draftImagePresignUploadResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "image-1", body.ImageID)
}

func draftImageRequest(target, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "application/json")
	return request
}
