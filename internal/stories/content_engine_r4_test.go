package stories

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	reviewDomain "bloomary-api/internal/domain/review"
	telegramDomain "bloomary-api/internal/domain/telegram"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestUnlinkedTelegramMessageIsNotKeptByTheForgetTimer(t *testing.T) {
	// Verifies: BE-010
	f := newBotFixture(t)
	bot := f.story.(*telegramBot)
	bot.notLinkedTTL = time.Hour
	f.gate.EXPECT().SendMessage(mock.Anything, mock.Anything).Return(1, nil).Once()
	collected := make(chan struct{})
	message := &telegramDomain.IncomingMessage{ChatID: 100, ChatType: "private", Text: "my phone is +357 99 000000, call me",
		From: telegramDomain.Sender{ID: 100, Username: "stranger"}}
	runtime.SetFinalizer(message, func(*telegramDomain.IncomingMessage) { close(collected) })

	require.NoError(t, bot.replyNotLinked(context.Background(), message))
	message = nil //nolint:ineffassign,wastedassign // drop the test's own reference

	require.Eventually(t, func() bool {
		runtime.GC()
		select {
		case <-collected:
			return true
		default:
			return false
		}
	}, 2*time.Second, 10*time.Millisecond, "the hour timer keeps the whole message reachable")
	bot.notLinkedMu.Lock()
	_, kept := bot.notLinkedAt[100]
	bot.notLinkedMu.Unlock()
	require.True(t, kept, "the user ID is kept for its hour")
}

// clearFixture is a draft preview whose earlier previews (newest first) are given.
func clearFixture(t *testing.T, earlier []reviewDomain.Notice, created func(reviewDomain.Notice) bool, sent func(telegramDomain.OutgoingMessage) bool) *NotifyScheduled {
	t.Helper()
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Shorter now.", Status: "draft"}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return(earlier, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(created)).Return(nil)
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(21), int64(0)).Return(nil)
	reviews.EXPECT().ReplaceNotice(mock.Anything, "member-1", mock.Anything).Return(nil).Maybe()
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(sent), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 21}, nil)
	notifier.EXPECT().Replace(mock.Anything, "member-1", mock.Anything, replacedPreview).Return(nil).Maybe()
	story, err := NewNotifyScheduled(proAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	return story
}

func TestEditCanClearTheStoredFirstComment(t *testing.T) {
	// Verifies: BE-019
	booking := "Sources: https://www.anthropic.com/news/x\nBook: https://lumen.observer/call/katerina"
	story := clearFixture(t,
		[]reviewDomain.Notice{{ID: "first", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, FirstComment: booking}},
		func(n reviewDomain.Notice) bool {
			return n.FirstComment == "" && n.Note == "Edited: removed the booking link"
		},
		func(m telegramDomain.OutgoingMessage) bool {
			return !strings.Contains(m.Text, firstCommentBlock) && !strings.Contains(m.Text, "lumen.observer") && len(m.Buttons) == 2
		})

	_, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "Edited: removed the booking link", "", true)

	require.NoError(t, err)
}

func TestEditCanReplaceTheStoredFirstComment(t *testing.T) {
	// Verifies: BE-019
	story := clearFixture(t,
		[]reviewDomain.Notice{{ID: "first", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, FirstComment: "Book: https://lumen.observer/call/katerina"}},
		func(n reviewDomain.Notice) bool { return n.FirstComment == "Sources: https://www.anthropic.com/news/x" },
		func(m telegramDomain.OutgoingMessage) bool {
			return strings.HasSuffix(m.Text, firstCommentBlock+"Sources: https://www.anthropic.com/news/x") && !strings.Contains(m.Text, "call/katerina")
		})

	_, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "Edited: new sources", "Sources: https://www.anthropic.com/news/x", false)

	require.NoError(t, err)
}

func TestClearedFirstCommentDoesNotComeBackOnALaterPreview(t *testing.T) {
	// Verifies: BE-019
	story := clearFixture(t,
		[]reviewDomain.Notice{
			{ID: "cleared", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, Note: "Edited: removed the booking link", CreatedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)},
			{ID: "first", DraftID: "draft-1", Status: reviewDomain.NoticeReplaced, MessageID: 19, FirstComment: "Book: https://lumen.observer/call/katerina"},
		},
		func(n reviewDomain.Notice) bool { return n.FirstComment == "" },
		func(m telegramDomain.OutgoingMessage) bool { return !strings.Contains(m.Text, "lumen.observer") })

	_, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "Edited: shorter", "", false)

	require.NoError(t, err)
}

func TestReusedNoteIsLabelledWithItsPreviewDate(t *testing.T) {
	// Verifies: BE-019
	story := clearFixture(t,
		[]reviewDomain.Notice{{ID: "first", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, Note: "Edited: shorter",
			CreatedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}},
		func(n reviewDomain.Notice) bool { return n.Note == "(from the preview of Oct 30) Edited: shorter" },
		func(m telegramDomain.OutgoingMessage) bool {
			return strings.Contains(m.Text, "Note: (from the preview of Oct 30) Edited: shorter")
		})

	_, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "", "", false)

	require.NoError(t, err)
}

func TestLabelledNoteKeepsItsFirstDateWhenReusedAgain(t *testing.T) {
	// Verifies: BE-019
	story := clearFixture(t,
		[]reviewDomain.Notice{{ID: "second", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, Note: "(from the preview of Oct 30) Edited: shorter",
			CreatedAt: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)}},
		func(n reviewDomain.Notice) bool { return n.Note == "(from the preview of Oct 30) Edited: shorter" },
		func(telegramDomain.OutgoingMessage) bool { return true })

	_, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "", "", false)

	require.NoError(t, err)
}
