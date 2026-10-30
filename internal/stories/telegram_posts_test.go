package stories

import (
	"context"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type postsFixture struct {
	botFixture
	reviews  *reviewMocks.MockRepository
	jobs     *scheduleMocks.MockRepository
	drafts   *draftMocks.MockRepository
	notifier *telegramMocks.MockNotifier
}

func newPostsFixture(t *testing.T) postsFixture {
	f := postsFixture{botFixture: newBotFixture(t), reviews: reviewMocks.NewMockRepository(t), jobs: scheduleMocks.NewMockRepository(t),
		drafts: draftMocks.NewMockRepository(t), notifier: telegramMocks.NewMockNotifier(t)}
	notify, err := NewNotifyScheduled(proAccess(t), f.drafts, f.jobs, f.reviews, nil, f.notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	planned, err := NewPlannedPosts(f.jobs, f.drafts, notify, f.gate)
	require.NoError(t, err)
	replies, err := NewEditReplies(f.reviews, f.jobs, f.drafts)
	require.NoError(t, err)
	f.story, err = NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life",
		TelegramBotHandlers{Replies: replies, Callbacks: []telegramDomain.CallbackHandler{planned}, Commands: []telegramDomain.CommandHandler{planned}})
	require.NoError(t, err)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100}, nil)
	return f
}

func TestPlannedPostsButtonListsScheduledPostsInCyprusTimeWithPreviewButtons(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{
		{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)},
		{ID: "job-2", DraftID: "draft-2", Status: scheduledDomain.Processing, ScheduledAt: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)},
	}, nil)
	f.drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.\nSecond line", Status: "draft"}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "1. Tue 3 Nov, 10:00 — Proof beats adjectives.") && !strings.Contains(m.Text, "Second line") &&
			len(m.Buttons) == 1 && m.Buttons[0][0].CallbackData == "pv:job-1"
	})).Return(11, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage(keyPlanned)))
}

func TestPreviewButtonSendsPreviewThatAcceptsReplies(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: time.Now().Add(time.Hour)}, nil)
	f.drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Post", Status: "draft"}, nil)
	f.reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, true).Return(0, nil)
	f.reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool { return n.ScheduleID == "job-1" && n.Requested })).Return(nil)
	f.notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Buttons[0][0].Text == "✏️ How to edit" && m.Buttons[0][1].Text == "Skip"
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 12}, nil)
	f.reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(12), int64(0)).Return(nil)
	f.gate.EXPECT().AnswerCallback(mock.Anything, "cb-2", "", false).Return(nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), telegramDomain.Update{Callback: &telegramDomain.Callback{
		ID: "cb-2", Data: "pv:job-1", ChatID: 100, MessageID: 11, From: telegramDomain.Sender{ID: 100}}}))
}

func TestPlannedPostsWithNothingScheduledPointsToDrafts(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.jobs.EXPECT().ListActive(mock.Anything, "member-1").Return(nil, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: plannedNone,
		Buttons: [][]telegramDomain.Button{{{Text: "Open Drafts", URL: "https://platform.bloomary.life/drafts"}}}}).Return(11, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage(keyPlanned)))
}

func TestHowToEditButtonAnswersWithAnAlert(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.gate.EXPECT().AnswerCallback(mock.Anything, "cb-3", editHowTo, true).Return(nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), telegramDomain.Update{Callback: &telegramDomain.Callback{
		ID: "cb-3", Data: "pv:e", ChatID: 100, MessageID: 11, From: telegramDomain.Sender{ID: 100}}}))
}

func TestReplyToPreviewFromBeforeTheLinkIsNotAnEdit(t *testing.T) {
	// Verifies: BE-017
	f := postsFixture{botFixture: newBotFixture(t), reviews: reviewMocks.NewMockRepository(t), jobs: scheduleMocks.NewMockRepository(t), drafts: draftMocks.NewMockRepository(t)}
	replies, err := NewEditReplies(f.reviews, f.jobs, f.drafts)
	require.NoError(t, err)
	f.story, err = NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life", TelegramBotHandlers{Replies: replies})
	require.NoError(t, err)
	linkedAt := time.Now().UTC()
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100, LinkedAt: linkedAt}, nil)
	f.reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(57)).
		Return(reviewDomain.Notice{ID: "old", DraftID: "draft-1", Status: reviewDomain.NoticeSent, CreatedAt: linkedAt.Add(-time.Hour)}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botReplyHint, Keyboard: botKeyboard}).Return(58, nil)
	update := privateMessage("Make it warmer")
	update.Message.ReplyToMessageID, update.Message.ReplyToBot = 57, true

	require.NoError(t, f.story.HandleUpdate(context.Background(), update))
}

func TestTextReplyToDraftPreviewIsStoredAsEditRequest(t *testing.T) {
	// Verifies: BE-017
	f := newPostsFixture(t)
	notice := reviewDomain.Notice{ID: "n-2", MemberID: "member-1", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 30}
	f.reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(30)).Return(notice, nil)
	f.reviews.EXPECT().NoticeByID(mock.Anything, "member-1", "n-2").Return(notice, nil)
	f.drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Status: "draft"}, nil)
	f.reviews.EXPECT().CountEditRequestsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	f.reviews.EXPECT().CreateEditRequest(mock.Anything, mock.MatchedBy(func(e reviewDomain.EditRequest) bool {
		return e.DraftID == "draft-1" && e.NoticeID == "n-2" && e.Text == "Start with the number" && e.Source == "text"
	})).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: editSaved, Keyboard: botKeyboard}).Return(31, nil)
	update := privateMessage("Start with the number")
	update.Message.ReplyToMessageID = 30

	require.NoError(t, f.story.HandleUpdate(context.Background(), update))
}

func TestReplyToPublishedPostPreviewIsTooLate(t *testing.T) {
	// Verifies: BE-017
	f := newPostsFixture(t)
	f.reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(40)).
		Return(reviewDomain.Notice{ID: "n-3", DraftID: "draft-1", ScheduleID: "job-1", Status: reviewDomain.NoticeSent}, nil)
	f.jobs.EXPECT().ByID(mock.Anything, "member-1", "job-1").Return(scheduledDomain.Post{ID: "job-1", Status: scheduledDomain.Published}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: editTooLate, Keyboard: botKeyboard}).Return(41, nil)
	update := privateMessage("Change the hook")
	update.Message.ReplyToMessageID = 40

	require.NoError(t, f.story.HandleUpdate(context.Background(), update))
}
