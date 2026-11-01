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

func cyprus(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, postTimeZone)
}

func TestNextPostSlotUsesTheWeekdayTable(t *testing.T) {
	// Verifies: BE-025
	// Sat 31 Oct 2026 evening → Sun 10:00; Sunday taken → Mon 09:00.
	require.Equal(t, cyprus(2026, 11, 1, 10, 0), nextPostSlot(cyprus(2026, 10, 31, 18, 0), nil).In(postTimeZone))
	taken := []scheduledDomain.Post{{DraftID: "other", Status: scheduledDomain.Pending, ScheduledAt: cyprus(2026, 11, 1, 10, 0).UTC()}}
	require.Equal(t, cyprus(2026, 11, 2, 9, 0), nextPostSlot(cyprus(2026, 10, 31, 18, 0), taken).In(postTimeZone))
	// A cancelled post does not take the day.
	cancelled := []scheduledDomain.Post{{DraftID: "other", Status: scheduledDomain.Cancelled, ScheduledAt: cyprus(2026, 11, 1, 10, 0).UTC()}}
	require.Equal(t, cyprus(2026, 11, 1, 10, 0), nextPostSlot(cyprus(2026, 10, 31, 18, 0), cancelled).In(postTimeZone))
}

func TestNextPostSlotInsideTodaysOpenWindowIsNow(t *testing.T) {
	// Verifies: BE-025
	// Before the window: its start. Sunday window 10:00–12:00.
	require.Equal(t, cyprus(2026, 11, 1, 10, 0), nextPostSlot(cyprus(2026, 11, 1, 9, 47), nil).In(postTimeZone))
	// Inside the window: now.
	require.Equal(t, cyprus(2026, 11, 1, 10, 30), nextPostSlot(cyprus(2026, 11, 1, 10, 30), nil).In(postTimeZone))
	require.Equal(t, cyprus(2026, 11, 1, 11, 59), nextPostSlot(cyprus(2026, 11, 1, 11, 59), nil).In(postTimeZone))
	// Window over: the next day's window (Mon 09:00).
	require.Equal(t, cyprus(2026, 11, 2, 9, 0), nextPostSlot(cyprus(2026, 11, 1, 12, 0), nil).In(postTimeZone))
	// Friday's window ends at 10:00.
	require.Equal(t, cyprus(2026, 11, 6, 9, 40), nextPostSlot(cyprus(2026, 11, 6, 9, 40), nil).In(postTimeZone))
	require.Equal(t, cyprus(2026, 11, 7, 10, 0), nextPostSlot(cyprus(2026, 11, 6, 10, 0), nil).In(postTimeZone))
	// Today already has a post: the next free day.
	taken := []scheduledDomain.Post{{DraftID: "other", Status: scheduledDomain.Pending, ScheduledAt: cyprus(2026, 11, 1, 18, 0).UTC()}}
	require.Equal(t, cyprus(2026, 11, 2, 9, 0), nextPostSlot(cyprus(2026, 11, 1, 10, 30), taken).In(postTimeZone))
}

func TestNextPostSlotAcrossTheClockChange(t *testing.T) {
	// Verifies: BE-025
	// Cyprus leaves summer time on Sun 25 Oct 2026: the slot is still 10:00 local (08:00 UTC, not 07:00).
	slot := nextPostSlot(cyprus(2026, 10, 24, 20, 0), nil)
	require.Equal(t, 10, slot.In(postTimeZone).Hour())
	require.Equal(t, time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC), slot.UTC())
}

type savedComments struct{ saved []string }

func (c *savedComments) Save(_ context.Context, memberID, draftID, scheduleID, text string) error {
	c.saved = append(c.saved, memberID+"|"+draftID+"|"+scheduleID+"|"+text)
	return nil
}
func (c *savedComments) Clear(context.Context, string, string) error { return nil }

func TestScheduleNextSchedulesTheDraftAtTheNextSlotAndSendsTheScheduledPreview(t *testing.T) {
	// Verifies: BE-025
	now := cyprus(2026, 10, 31, 18, 0)
	slot := cyprus(2026, 11, 1, 10, 0).UTC()
	notice := reviewDomain.Notice{ID: "n-1", MemberID: "member-1", DraftID: "draft-1", ProposedText: "Post", FirstComment: "Sources: https://x.example", Status: reviewDomain.NoticeSent}
	draft := draftDomain.Draft{DraftID: "draft-1", Body: "Post", Status: "draft"}
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().NoticeByID(mock.Anything, "member-1", "n-1").Return(notice, nil)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool { return n.ScheduleID == "job-1" })).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return(nil, nil).Maybe()
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(31), int64(0)).Return(nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draft, nil)
	schedule := scheduleMocks.NewMockStory(t)
	schedule.EXPECT().ListActive(mock.Anything, "member-1").Return(nil, nil)
	schedule.EXPECT().Schedule(mock.Anything, scheduledDomain.CreateInput{MemberID: "member-1", DraftID: "draft-1", ScheduledAt: slot}).
		Return(scheduledDomain.Post{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: slot}, nil)
	jobs := scheduleMocks.NewMockRepository(t)
	jobs.EXPECT().ListActive(mock.Anything, "member-1").Return([]scheduledDomain.Post{{ID: "job-1", DraftID: "draft-1", Status: scheduledDomain.Pending, ScheduledAt: slot}}, nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.HasPrefix(m.Text, noticeHeader) && strings.Contains(m.Text, "Goes out: Sun 1 Nov, 10:00") && strings.Contains(m.Text, "Note: Scheduled from Telegram.")
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 30}, nil).Once()
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Text == "Post" && m.Buttons[0][0].Text == "Skip"
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 31}, nil).Once()
	notify, err := NewNotifyScheduled(proAccess(t), drafts, jobs, reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)
	comments := &savedComments{}
	callbacks, err := NewScheduleNextCallbacks(reviews, drafts, schedule, comments, notify)
	require.NoError(t, err)
	callbacks.now = func() time.Time { return now }

	answer, err := callbacks.HandleCallback(context.Background(), telegramDomain.Link{MemberID: "member-1", ChatID: 100}, telegramDomain.Callback{Data: "sn:n-1", ChatID: 100, MessageID: 9})

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(answer, "Scheduled: Sun 1 Nov, 10:00"), answer)
	require.Equal(t, []string{"member-1|draft-1|job-1|Sources: https://x.example"}, comments.saved, "the preview's first comment goes with the schedule")
}

func TestLongPostWithImageSendsTheImageThenThePostText(t *testing.T) {
	// Verifies: BE-026
	long := strings.Repeat("Proof beats adjectives. ", 50) // over the 1024-unit caption limit
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: long, Status: "draft",
		Image: &draftDomain.DraftImage{ObjectKey: "key-1", MimeType: "image/png"}}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.Anything).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return(nil, nil).Maybe()
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(12), int64(11)).Return(nil)
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().Read(mock.Anything, "key-1").Return([]byte("png"), nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool { return m.Text == draftHeader }),
		(*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 10}, nil).Once()
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Text == long && m.Buttons[0][0].Text == "📅 Schedule (next slot)"
	}), &telegramDomain.Photo{Data: []byte("png"), MimeType: "image/png"}).Return(telegramDomain.Delivery{MessageID: 12, PhotoMessageID: 11}, nil).Once()
	story, err := NewNotifyScheduled(proAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, objects, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)

	notice, err := story.NotifyDraft(context.Background(), "member-1", "draft-1", "", "", false)

	require.NoError(t, err)
	require.Equal(t, int64(12), notice.MessageID)
	require.Equal(t, int64(11), notice.PhotoMessageID, "a reply to the image reaches the same notice")
}
