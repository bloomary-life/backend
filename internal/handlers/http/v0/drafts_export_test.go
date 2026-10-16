package v0

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	draftDomain "bloomary-api/internal/domain/draft"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	"bloomary-api/internal/gates/repositories/ratelimit"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	"bloomary-api/internal/stories"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDraftExportHandlerReturnsZip(t *testing.T) {
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ListByMemberID(mock.Anything, "member-1").
		Return([]draftDomain.Draft{{DraftID: "draft-1", Body: "Hello", Status: "draft"}}, nil)
	export, err := stories.NewExportDrafts(drafts, nil)
	require.NoError(t, err)
	limiter, err := ratelimit.NewDailyIPLimiter(1)
	require.NoError(t, err)
	service := &service{linkedIn: linkedIn, exportDraftsStory: export, exportLimiter: limiter}

	request := httptest.NewRequest(http.MethodGet, "/api/drafts/export?memberId=member-1", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	service.DraftExport(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "application/zip", response.Header().Get("Content-Type"))
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	require.NoError(t, err)
	require.Len(t, archive.File, 1)
	require.Equal(t, "draft-1.md", archive.File[0].Name)
}
