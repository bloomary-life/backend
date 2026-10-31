package stories

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	insightDomain "bloomary-api/internal/domain/insight"
	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	reviewMocks "bloomary-api/internal/mocks/domain/review"
	scheduleMocks "bloomary-api/internal/mocks/domain/scheduledpost"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"
	"bloomary-api/internal/platform/logger"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDraftLinkRuleBlocksBareDomainsAndTricksButNotTechNames(t *testing.T) {
	// Verifies: BE-020
	blocked := []string{
		"Book a call: lumen.observer", "Join at bloomary.life", "kat.substack.com", "youtu.be/abc123", "linktr.ee/kat",
		"forms.gle/abc", "rb.gy/abc", "example.nl/x", "example.es/x", "lnkd.in/abc", "URLhttps://evil.com", "[book](//lumen.observer)",
		"hxxps://evil.com", "https ://evil.com", "ｈｔｔｐｓ://evil.com", "lumen․observer/call", "www.example",
	}
	for _, body := range blocked {
		require.ErrorIs(t, validateMCPDraft("draft-1", body), ErrMCPDraftHasLink, body)
	}
	for _, body := range []string{"Microsoft.net/Java devs", "I moved from Node.js/Go to Rust.", "Next.js and Vue.js", "Socket.io is fine", "e.g. this, i.e. that", "ASP.NET Core"} {
		require.NoError(t, validateMCPDraft("draft-1", body), body)
	}
}

func TestVoiceFromMemberNotOnTheAllowlistNeverReachesDeepgram(t *testing.T) {
	// Verifies: BE-021
	f := newBotFixture(t)
	story, err := NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life", TelegramBotHandlers{})
	require.NoError(t, err)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-2", ChatID: 100, TelegramUserID: 100}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botNoVoice, Keyboard: botKeyboard}).Return(5, nil)

	require.NoError(t, story.HandleUpdate(context.Background(), voiceMessage(12)))
}

func TestHelpSaysTextOnlyWithoutVoice(t *testing.T) {
	// Verifies: BE-021
	f := newBotFixture(t)
	story, err := NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life", TelegramBotHandlers{})
	require.NoError(t, err)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-2", ChatID: 100, TelegramUserID: 100}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botHelpText, Keyboard: botKeyboard}).Return(5, nil)

	require.NoError(t, story.HandleUpdate(context.Background(), privateMessage(keyHelp)))
	require.NotContains(t, botHelpText, "voice")
}

func TestVoiceReservationCountsTheFileSizeWhenTheClaimedDurationIsShort(t *testing.T) {
	// Verifies: BE-021
	require.Equal(t, 10_000, reservedSeconds(&telegramDomain.Voice{Duration: 1, FileSize: 20_000_000}))
	require.Equal(t, 12, reservedSeconds(&telegramDomain.Voice{Duration: 12, FileSize: 8}))
	require.Equal(t, 1, reservedSeconds(&telegramDomain.Voice{}))
}

func TestNumbersNeedAColonOrCommaAndAnAllowListedMember(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.Anything).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botSaved, Keyboard: botKeyboard}).Return(7, nil)
	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("Numbers show that juniors wait 9 months")))

	g := newBotFixture(t)
	other, err := NewTelegramBot(g.links, g.gate, g.transcriber, g.insights, g.accounts, "https://platform.bloomary.life", TelegramBotHandlers{})
	require.NoError(t, err)
	g.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-2", ChatID: 100, TelegramUserID: 100}, nil)
	g.insights.EXPECT().CountSince(mock.Anything, "member-2", insightDomain.Source(""), mock.Anything).Return(0, nil)
	g.insights.EXPECT().Create(mock.Anything, mock.Anything).Return(nil)
	g.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botSaved, Keyboard: botKeyboard}).Return(8, nil)
	require.NoError(t, other.HandleUpdate(context.Background(), privateMessage("numbers: views 120")))
}

func TestSavedIdeaIsNotReportedAsLostWhenTheConfirmationFails(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	f.gate.EXPECT().SendMessage(mock.Anything, mock.Anything).Return(0, errors.New("429 too many requests")).Once()

	log, err := logger.New(logger.Output(io.Discard))
	require.NoError(t, err)

	require.NoError(t, f.story.HandleUpdate(log.ToContext(context.Background()), privateMessage("an idea")))
}

func TestUnlinkTakesTheKeyboardAway(t *testing.T) {
	// Verifies: BE-009
	links := telegramMocks.NewMockLinkRepository(t)
	links.EXPECT().ByMemberID(mock.Anything, "member-1").Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100}, nil)
	links.EXPECT().DeleteByMemberID(mock.Anything, "member-1").Return(nil)
	gate := telegramMocks.NewMockGate(t)
	gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botUnlinked, RemoveKeyboard: true}).Return(9, nil)
	story, err := NewTelegramLink(links, "bloomary_app", gate, telegramDomain.MemberSet{"member-1": true})
	require.NoError(t, err)

	require.NoError(t, story.Unlink(context.Background(), "member-1"))
	require.True(t, story.VoiceAllowed("member-1"))
	require.False(t, story.VoiceAllowed("member-2"))
	require.Empty(t, BotCommands, "buttons, not a command menu")
}

func TestDraftPreviewShowsFirstCommentWithCopyAndReplacesTheOlderPreview(t *testing.T) {
	// Verifies: BE-019
	comment := "Sources: https://www.anthropic.com/news/x"
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.", Status: "draft"}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.Anything).Return(nil)
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(20), int64(0)).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "old", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 11},
		{ID: "other", DraftID: "draft-2", Status: reviewDomain.NoticeSent, MessageID: 12},
	}, nil)
	reviews.EXPECT().ReplaceNotice(mock.Anything, "member-1", "old").Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.HasSuffix(m.Text, "\n\nFirst comment (paste under the post after you publish):\n"+comment) &&
			m.Buttons[0][0] == telegramDomain.Button{Text: "Copy first comment", CopyText: comment}
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 19}, nil).Once()
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return m.Text == "Proof beats adjectives."
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 20}, nil).Once()
	notifier.EXPECT().Replace(mock.Anything, "member-1", int64(11), replacedPreview).Return(nil)
	story, err := NewNotifyScheduled(proAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)

	_, err = story.NotifyDraft(context.Background(), "member-1", "draft-1", "", comment, false)

	require.NoError(t, err)
}

func TestFirstCommentIsNotSentForAnotherScheduleOfTheDraft(t *testing.T) {
	// Verifies: BE-024
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().FirstComment(mock.Anything, "member-1", "draft-1").Return("old links", "job-1", nil)
	reviews.EXPECT().DeleteFirstComment(mock.Anything, "member-1", "draft-1").Return(nil)
	comments, err := NewFirstComments(reviews, telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)

	comments.PostPublished(context.Background(), scheduledDomain.Post{ID: "job-2", MemberID: "member-1", DraftID: "draft-1", LinkedInPostID: "urn:li:share:7"})
}

func TestMyPostsListsRecentDraftPreviews(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.jobs.EXPECT().ListActive(mock.Anything, "member-1").Return(nil, nil)
	f.reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "n-new", DraftID: "draft-1", Status: reviewDomain.NoticeSent},
		{ID: "n-old", DraftID: "draft-1", Status: reviewDomain.NoticeReplaced},
		{ID: "n-skip", DraftID: "draft-2", Status: reviewDomain.NoticeSkipped},
	}, nil)
	f.drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.", Status: "draft"}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.HasPrefix(m.Text, draftsHeader) && strings.Contains(m.Text, "1. Proof beats adjectives.") &&
			len(m.Buttons) == 1 && m.Buttons[0][0].CallbackData == "pv:d:n-new"
	})).Return(11, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage(keyPlannedOld)))
}

func TestTrendRefusesIPLiteralSourcesAndPassesSilent(t *testing.T) {
	// Verifies: BE-023
	reviews := reviewMocks.NewMockRepository(t)
	story, err := NewSendSuggestion(proAccess(t), reviews, telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)
	_, err = story.Send(context.Background(), reviewDomain.SuggestionInput{MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "https://203.0.113.9/news", Text: trendSummary})
	require.ErrorIs(t, err, reviewDomain.ErrInvalidSource)

	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.Anything).Return(nil)
	reviews.EXPECT().SetSuggestionMessage(mock.Anything, "member-1", mock.Anything, int64(3)).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool { return m.Silent }), (*telegramDomain.Photo)(nil)).
		Return(telegramDomain.Delivery{MessageID: 3}, nil)
	story, err = NewSendSuggestion(proAccess(t), reviews, notifier)
	require.NoError(t, err)
	_, err = story.Send(context.Background(), reviewDomain.SuggestionInput{MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "https://www.anthropic.com/news/x", Text: trendSummary, Silent: true})
	require.NoError(t, err)
}

func TestPostTimeShowsTheUTCOffset(t *testing.T) {
	// Verifies: BE-013
	require.Equal(t, "Tue 3 Nov, 10:00 (UTC+2)", formatPostTime(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)))
	require.Equal(t, "Wed 1 Jul, 10:00 (UTC+3)", formatPostTime(time.Date(2026, 7, 1, 7, 0, 0, 0, time.UTC)))
}
