package stories

import (
	"context"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func proAccess(t *testing.T) *entitlementMocks.MockAccessStory {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro}, nil).Maybe()
	return access
}

func TestScheduleMCPDraftIsHeldForApproval(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Now().Add(24 * time.Hour).UTC()
	schedules := scheduleMocks.NewMockStory(t)
	schedules.EXPECT().Schedule(mock.Anything, scheduledDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: publishAt, NeedsApproval: true}).
		Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt, AwaitingApproval: true}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().SupersedePending(mock.Anything, "member-1", "draft-1", mock.Anything).Return(nil)
	story, err := NewScheduleMCPDraft(schedules, reviews)
	require.NoError(t, err)

	post, err := story.Schedule(context.Background(), "member-1", "draft-1", publishAt)

	require.NoError(t, err)
	require.True(t, post.AwaitingApproval)
}

func TestRequestApprovalSendsPreviewWithImageAndButtons(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.", Status: "draft",
		Image: &draftDomain.DraftImage{ObjectKey: "key-1", MimeType: "image/png"}}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt, AwaitingApproval: true}}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountApprovalsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().SupersedePending(mock.Anything, "member-1", "draft-1", mock.Anything).Return(nil)
	var created reviewDomain.Approval
	reviews.EXPECT().CreateApproval(mock.Anything, mock.Anything).Run(func(_ context.Context, a reviewDomain.Approval) { created = a }).Return(nil)
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().Read(mock.Anything, "key-1").Return([]byte("png"), nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "Scheduled: Tue 3 Nov 2026, 09:00 UTC") && strings.Contains(m.Text, "Note: From your voice note") &&
			strings.HasSuffix(m.Text, "Proof beats adjectives.") &&
			m.Buttons[0][0].CallbackData == "ap:"+created.ID+":y" && m.Buttons[0][1].CallbackData == "ap:"+created.ID+":n" &&
			m.Buttons[1][0].URL == "https://platform.bloomary.life/editor?draft=draft-1"
	}), &telegramDomain.Photo{Data: []byte("png"), MimeType: "image/png", Caption: "Image for the post below"}).Return(10, nil)
	story, err := NewRequestApproval(proAccess(t), drafts, jobs, reviews, objects, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)

	approval, err := story.Request(context.Background(), "member-1", "draft-1", "From your voice note")

	require.NoError(t, err)
	require.Equal(t, "job-1", approval.ScheduleID)
	require.Equal(t, "Proof beats adjectives.", approval.ProposedText)
	require.Equal(t, reviewDomain.ApprovalPending, created.Status)
}

func TestApproveReleasesHeldScheduleAndSkipCancelsIt(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Now().Add(time.Hour).UTC()
	link := telegramDomain.Link{MemberID: "member-1", ChatID: 100}
	approval := reviewDomain.Approval{ID: "a-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ScheduledAt: publishAt, ProposedText: "Post", Status: reviewDomain.ApprovalPending}
	reviews := reviewMocks.NewMockRepository(t)
	jobs := scheduleMocks.NewMockRepository(t)
	bot := telegramMocks.NewMockGate(t)
	callbacks, err := NewApprovalCallbacks(reviews, jobs, bot)
	require.NoError(t, err)

	reviews.EXPECT().ApprovalByID(mock.Anything, "member-1", "a-1").Return(approval, nil).Once()
	jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{ID: "job-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt}, nil)
	jobs.EXPECT().Approve(mock.Anything, "member-1", "job-1").Return(nil)
	reviews.EXPECT().DecideApproval(mock.Anything, "member-1", "a-1", reviewDomain.ApprovalApproved, mock.Anything).Return(approval, nil)
	bot.EXPECT().EditMessageText(mock.Anything, int64(100), int64(7), mock.MatchedBy(func(text string) bool { return strings.HasPrefix(text, "Approved: posts ") })).Return(nil)

	answer, err := callbacks.HandleCallback(context.Background(), link, telegramDomain.Callback{Data: "ap:a-1:y", ChatID: 100, MessageID: 7})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(answer, "Approved: posts "))

	approval.ID = "a-2"
	reviews.EXPECT().ApprovalByID(mock.Anything, "member-1", "a-2").Return(approval, nil).Once()
	jobs.EXPECT().Cancel(mock.Anything, "member-1", "draft-1").Return(nil)
	reviews.EXPECT().DecideApproval(mock.Anything, "member-1", "a-2", reviewDomain.ApprovalSkipped, mock.Anything).Return(approval, nil)
	bot.EXPECT().EditMessageText(mock.Anything, int64(100), int64(8), mock.MatchedBy(func(text string) bool { return strings.HasPrefix(text, "Skipped:") })).Return(nil)

	answer, err = callbacks.HandleCallback(context.Background(), link, telegramDomain.Callback{Data: "ap:a-2:n", ChatID: 100, MessageID: 8})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(answer, "Skipped:"))
}

func TestMCPUpdateOfScheduledDraftHoldsItAgain(t *testing.T) {
	// Verifies: BE-013
	repository := draftMocks.NewMockRepository(t)
	repository.EXPECT().UpdateUnpublished(mock.Anything, "member-1", "draft-1", "New text", mock.Anything).Return(draftDomain.Draft{DraftID: "draft-1", Body: "New text", Status: "draft"}, nil)
	update, err := NewUpdateMCPDraft(proAccess(t), repository)
	require.NoError(t, err)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().HoldForApproval(mock.Anything, "member-1", "draft-1").Return(nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().SupersedePending(mock.Anything, "member-1", "draft-1", mock.Anything).Return(nil)
	hold, err := NewMCPDraftChangeHold(jobs, reviews)
	require.NoError(t, err)
	story, err := NewHoldOnMCPUpdate(update, hold)
	require.NoError(t, err)

	_, err = story.Update(context.Background(), "member-1", "draft-1", "New text")

	require.NoError(t, err)
}

func TestSendSuggestionSendsCopyRatingAndPostedButtons(t *testing.T) {
	// Verifies: BE-012
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.Anything).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Text == "Comment idea\nThey asked about EU visas\n\nThe Blue Card salary threshold changed this year." &&
			m.Buttons[0][0] == telegramDomain.Button{Text: "Open post", URL: "https://www.linkedin.com/feed/update/urn:li:activity:1"} &&
			m.Buttons[0][1] == telegramDomain.Button{Text: "Copy comment", CopyText: "The Blue Card salary threshold changed this year."} &&
			len(m.Buttons[1]) == 5 && strings.HasSuffix(m.Buttons[1][4].CallbackData, ":5") && strings.HasSuffix(m.Buttons[2][0].CallbackData, ":p")
	}), (*telegramDomain.Photo)(nil)).Return(11, nil)
	story, err := NewSendSuggestion(proAccess(t), reviews, notifier)
	require.NoError(t, err)

	suggestion, err := story.Send(context.Background(), reviewDomain.SuggestionInput{MemberID: "member-1", Kind: "comment",
		TargetURL: "https://www.linkedin.com/feed/update/urn:li:activity:1", Text: "The Blue Card salary threshold changed this year.", Context: "They asked about EU visas"})

	require.NoError(t, err)
	require.Len(t, suggestion.ID, 32)
}

func TestSuggestionRatingIsStored(t *testing.T) {
	// Verifies: BE-012
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().RateSuggestion(mock.Anything, "member-1", "s-1", 4, mock.Anything).Return(nil)
	callbacks, err := NewSuggestionCallbacks(reviews)
	require.NoError(t, err)

	answer, err := callbacks.HandleCallback(context.Background(), telegramDomain.Link{MemberID: "member-1"}, telegramDomain.Callback{Data: "sg:s-1:4"})

	require.NoError(t, err)
	require.Equal(t, "Rated 4 of 5.", answer)
}

func TestListFeedbackDiffsMemberEditsOfApprovedDrafts(t *testing.T) {
	// Verifies: BE-014
	since := time.Now().Add(-time.Hour)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().ListApprovals(mock.Anything, "member-1", since).Return([]reviewDomain.Approval{
		{ID: "a-1", DraftID: "draft-1", Status: reviewDomain.ApprovalApproved, ProposedText: "Hiring is hard"},
		{ID: "a-2", DraftID: "draft-2", Status: reviewDomain.ApprovalSkipped, ProposedText: "Skip me"},
	}, nil)
	reviews.EXPECT().ListSuggestions(mock.Anything, "member-1", since).Return([]reviewDomain.Suggestion{{ID: "s-1", Rating: 5}}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{Body: "Hiring is slow"}, nil)
	story, err := NewListFeedback(proAccess(t), reviews, drafts)
	require.NoError(t, err)

	feedback, err := story.List(context.Background(), "member-1", since)

	require.NoError(t, err)
	require.Len(t, feedback.Approvals, 2)
	require.True(t, feedback.Approvals[0].Edited)
	require.Equal(t, "Hiring is [-hard-]{+slow+}", feedback.Approvals[0].Diff)
	require.Equal(t, "Hiring is slow", feedback.Approvals[0].FinalText)
	require.Empty(t, feedback.Approvals[1].Diff)
	require.Equal(t, 5, feedback.Suggestions[0].Rating)
}
