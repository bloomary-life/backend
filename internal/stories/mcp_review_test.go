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

func TestNotifyScheduledSendsPreviewWithEditAndSkip(t *testing.T) {
	// Verifies: BE-013
	publishAt := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.", Status: "draft",
		Image: &draftDomain.DraftImage{ObjectKey: "key-1", MimeType: "image/png"}}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: publishAt}}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	var created reviewDomain.Notice
	reviews.EXPECT().CreateNotice(mock.Anything, mock.Anything).Run(func(_ context.Context, n reviewDomain.Notice) { created = n }).Return(nil)
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().Read(mock.Anything, "key-1").Return([]byte("png"), nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "Scheduled: Tue 3 Nov 2026, 09:00 UTC") && strings.Contains(m.Text, "Note: Sources: Eurostat 2025") &&
			strings.Contains(m.Text, "reply to this message with a voice note or text") &&
			strings.HasSuffix(m.Text, "Proof beats adjectives.") && len(m.Buttons) == 2 &&
			m.Buttons[0][0].CallbackData == "pv:e" && m.Buttons[0][1].CallbackData == "sk:"+created.ID &&
			m.Buttons[1][0].URL == "https://platform.bloomary.life/editor?draft=draft-1"
	}), &telegramDomain.Photo{Data: []byte("png"), MimeType: "image/png", Caption: "Image for the post below"}).Return(10, nil)
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(10)).Return(nil)
	story, err := NewNotifyScheduled(proAccess(t), drafts, jobs, reviews, objects, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)

	notice, err := story.Notify(context.Background(), "member-1", "draft-1", "Sources: Eurostat 2025")

	require.NoError(t, err)
	require.Equal(t, "job-1", notice.ScheduleID)
	require.Equal(t, "Proof beats adjectives.", notice.ProposedText)
	require.Equal(t, reviewDomain.NoticeSent, created.Status)
	require.Equal(t, int64(10), notice.MessageID)
}

func TestSkipCancelsScheduledPost(t *testing.T) {
	// Verifies: BE-013
	notice := reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", ScheduledAt: time.Now().Add(time.Hour), ProposedText: "Post", Status: reviewDomain.NoticeSent}
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().NoticeByID(mock.Anything, "member-1", "n-1").Return(notice, nil)
	reviews.EXPECT().SkipNotice(mock.Anything, "member-1", "n-1", mock.Anything).Return(notice, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{ID: "job-1", Status: scheduledDomain.Pending}, nil)
	jobs.EXPECT().Cancel(mock.Anything, "member-1", "draft-1").Return(nil)
	bot := telegramMocks.NewMockGate(t)
	bot.EXPECT().EditMessageText(mock.Anything, int64(100), int64(8), mock.MatchedBy(func(text string) bool { return strings.HasPrefix(text, "Skipped:") })).Return(nil)
	callbacks, err := NewSkipCallbacks(reviews, jobs, bot)
	require.NoError(t, err)

	answer, err := callbacks.HandleCallback(context.Background(), telegramDomain.Link{MemberID: "member-1", ChatID: 100}, telegramDomain.Callback{Data: "sk:n-1", ChatID: 100, MessageID: 8})

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(answer, "Skipped:"))
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

func TestListFeedbackReportsOutcomesAndMemberEdits(t *testing.T) {
	// Verifies: BE-014
	since := time.Now().Add(-time.Hour)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", since).Return([]reviewDomain.Notice{
		{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ScheduleID: "job-1", Status: reviewDomain.NoticeSent, ProposedText: "Hiring is hard"},
		{ID: "n-2", MemberID: "member-1", DraftID: "draft-2", ScheduleID: "job-2", Status: reviewDomain.NoticeSkipped, ProposedText: "Skip me"},
	}, nil)
	reviews.EXPECT().ListSuggestions(mock.Anything, "member-1", since).Return([]reviewDomain.Suggestion{{ID: "s-1", Rating: 5}}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{Body: "Hiring is slow"}, nil)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-2").Return(draftDomain.Draft{Body: "Skip me"}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{Status: scheduledDomain.Published}, nil)
	story, err := NewListFeedback(proAccess(t), reviews, drafts, jobs)
	require.NoError(t, err)

	feedback, err := story.List(context.Background(), "member-1", since)

	require.NoError(t, err)
	require.Len(t, feedback.Posts, 2)
	require.Equal(t, "published", feedback.Posts[0].PostStatus)
	require.True(t, feedback.Posts[0].Edited)
	require.Equal(t, "Hiring is [-hard-]{+slow+}", feedback.Posts[0].Diff)
	require.Equal(t, "skipped", feedback.Posts[1].PostStatus)
	require.False(t, feedback.Posts[1].Edited)
	require.Equal(t, 5, feedback.Suggestions[0].Rating)
}
