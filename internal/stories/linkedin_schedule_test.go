package stories

import (
	"context"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	scheduleDomain "bloomary-api/internal/domain/scheduledpost"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/mock"
)

func TestScheduleAllowsTrialMember(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1"}, nil)
	accounts := accountMocks.NewMockRepository(t)
	accounts.EXPECT().ActiveByMemberID(mock.Anything, "member-1").
		Return(accountDomain.TokenPair{AccessToken: "token", ExpiresAt: time.Now().Add(60 * 24 * time.Hour)}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().Schedule(mock.Anything, mock.MatchedBy(func(in scheduleDomain.CreateInput) bool {
		return in.MemberID == "member-1" && in.DraftID == "draft-1"
	})).Return(scheduleDomain.Post{ID: "job-1", MemberID: "member-1", DraftID: "draft-1"}, nil).Once()

	story, err := NewLinkedInScheduleStory(jobs, accounts, drafts, draftMocks.NewMockObjectStore(t), access, linkedinMocks.NewMockGate(t), linkedinMocks.NewMockLinkedInPostStory(t), time.Minute, 31)
	if err != nil {
		t.Fatal(err)
	}
	post, err := story.Schedule(context.Background(), scheduleDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if post.ID != "job-1" {
		t.Fatalf("expected trial member's post to be scheduled, got %#v", post)
	}
}
