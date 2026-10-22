package v0

import (
	"net/http"
	"net/http/httptest"
	"testing"

	linkedinDomain "bloomary-api/internal/domain/linkedin"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLinkedInScheduleDeleteCancelsDraftSchedule(t *testing.T) {
	// Verifies: BE-002
	linkedIn := linkedinMocks.NewMockGate(t)
	linkedIn.On("UserInfo", mock.Anything, "access-token").
		Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().Cancel(mock.Anything, "member-1", "draft-1").Return(nil).Once()
	service := &service{linkedIn: linkedIn, scheduledPostStory: schedules}

	request := httptest.NewRequest(http.MethodDelete, "/api/linkedin/schedule?memberId=member-1&draftId=DRAFT-1", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	service.LinkedInSchedule(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
}
