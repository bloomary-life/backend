package stories

import (
	"testing"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/require"
)

func TestLinkedInWorkerClaimsNothingWhileLinkedInIsPaused(t *testing.T) {
	// Verifies: BE-060
	f := newCrosspostFixture(t)
	require.NoError(t, f.pause.SetPaused(f.ctx, crosspostDomain.LinkedIn, true, "ops", time.Now()))
	guard, err := NewPublishGuard(f.pause, PublishGuardConfig{})
	require.NoError(t, err)
	// The jobs mock has no ClaimDue expectation: a claim would fail the test.
	story, err := NewLinkedInScheduleStory(scheduleMocks.NewMockRepository(t), accountMocks.NewMockRepository(t), draftMocks.NewMockRepository(t), draftMocks.NewMockObjectStore(t),
		entitlementMocks.NewMockAccessStory(t), linkedinMocks.NewMockGate(t), linkedinMocks.NewMockLinkedInPostStory(t), time.Minute, 31)
	require.NoError(t, err)
	story.(interface {
		UseGuard(crosspostDomain.PublishGuard)
	}).UseGuard(guard)

	require.False(t, story.(*linkedInScheduleStory).publishOne(f.ctx))
}
