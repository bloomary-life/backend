package stories

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	dailyDomain "bloomary-api/internal/domain/daily"
	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	linkedinDomain "bloomary-api/internal/domain/linkedin"
	scheduleDomain "bloomary-api/internal/domain/scheduledpost"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	linkedinMocks "bloomary-api/internal/mocks/domain/linkedin"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestScheduleAllowsTrialMember(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Status: "draft"}, nil)
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

// Verifies: BE-004
func TestScheduleRetryFailsJobOnDailyLimit(t *testing.T) {
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().Update(mock.Anything, "job-1", scheduleDomain.Failed, "", mock.Anything).Return(nil).Once()
	story := &linkedInScheduleStory{jobs: jobs}

	cause := fmt.Errorf("%w: %s", dailyDomain.ErrDailyLimitExceeded, "member-1")
	story.retry(context.Background(), scheduleDomain.Post{ID: "job-1", MemberID: "member-1"}, cause)
}

func TestScheduleRefusesPublishedDraft(t *testing.T) {
	// Verifies: BE-013
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Status: "published"}, nil)
	story, err := NewLinkedInScheduleStory(scheduleMocks.NewMockRepository(t), accountMocks.NewMockRepository(t), drafts, draftMocks.NewMockObjectStore(t), access, linkedinMocks.NewMockGate(t), linkedinMocks.NewMockLinkedInPostStory(t), time.Minute, 31)
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Schedule(context.Background(), scheduleDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: time.Now().Add(time.Hour)})

	if err == nil || !strings.Contains(err.Error(), "unpublished") {
		t.Fatalf("published draft must not be scheduled again, got %v", err)
	}
}

type recordedPublish struct{ posts []scheduleDomain.Post }

func (r *recordedPublish) PostPublished(_ context.Context, post scheduleDomain.Post) {
	r.posts = append(r.posts, post)
}

func TestPublishedPostRunsTheHookWithItsLinkedInID(t *testing.T) {
	// Verifies: BE-024
	job := scheduleDomain.Post{ID: "job-1", MemberID: "member-1", DraftID: "draft-1", Status: scheduleDomain.Processing}
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ClaimDue(mock.Anything, mock.Anything).Return(job, nil)
	jobs.EXPECT().Update(mock.Anything, "job-1", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	accounts := accountMocks.NewMockRepository(t)
	accounts.EXPECT().ActiveByMemberID(mock.Anything, "member-1").Return(accountDomain.TokenPair{AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour)}, nil)
	gate := linkedinMocks.NewMockGate(t)
	gate.EXPECT().UserInfo(mock.Anything, "token").Return(linkedinDomain.Profile{MemberID: "member-1"}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Post", Status: "draft"}, nil)
	drafts.EXPECT().MarkPublished(mock.Anything, "member-1", "draft-1").Return(nil)
	posts := linkedinMocks.NewMockLinkedInPostStory(t)
	posts.EXPECT().CreatePost(mock.Anything, mock.Anything).Return(linkedinDomain.CreatePostOutput{PostID: "urn:li:share:7"}, nil)
	story, err := NewLinkedInScheduleStory(jobs, accounts, drafts, draftMocks.NewMockObjectStore(t), entitlementMocks.NewMockAccessStory(t), gate, posts, time.Minute, 31)
	require.NoError(t, err)
	hook := &recordedPublish{}
	story.(scheduleDomain.PublishedHooks).OnPublished(hook)

	require.True(t, story.(*linkedInScheduleStory).publishOne(context.Background()))

	require.Len(t, hook.posts, 1)
	require.Equal(t, "urn:li:share:7", hook.posts[0].LinkedInPostID)
	require.Equal(t, scheduleDomain.Published, hook.posts[0].Status)
}
