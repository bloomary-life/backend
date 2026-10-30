package stories

import (
	"context"
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

func TestDraftLinkRuleBlocksNetDomainsButNotSentencesOrTechNames(t *testing.T) {
	// Verifies: BE-020
	for _, tc := range []struct {
		body    string
		blocked bool
	}{
		{"see kat.net/call", true},
		{"Book at lumen.net", true},
		{"Book at LUMEN.NET", true},
		{"Book at lumen.observer", true},
		{"Book at kat.So/call", true},
		{"I moved to Cyprus.It was hard and lumen.observer helped", true},
		{"Microsoft.net/Java devs", false},
		{"Microsoft.NET and ASP.NET Core", false},
		{"VB.NET and ADO.NET still run banks", false},
		{"I learned .NET first", false},
		{"I moved from Node.js/Go to Rust.", false},
		{"I moved to Cyprus.It was hard", false},
		{"It ends here.So what", false},
		{"I lived in Berlin.De facto, I worked remotely", false},
		{"Version 2.0.app ships today", false},
		{"e.g. this, i.e. that", false},
		// r4: any letter case, multi-label names, e-mail domains, short links, query strings, dot look-alikes.
		{"Read Kat.Substack.Com today", true},
		{"Write to kat@Gmail.Com today", true},
		{"Watch Youtu.Be", true},
		{"Watch Youtu.Be sure to", true},
		{"Book at Kat.Net", true},
		{"Kat.Net is my site", true},
		{"see lumen.Observer?ref=x", true},
		{"Book at Lumen.Observer.", true},
		{"Book at Lumen.Observer", true},
		{"Book at Lumen.Observer/call", true},
		{"Book at kat.net", true},
		{"Book at LUMEN.OBSERVER", true},
		{"Book at lumen\u3002observer", true},
		{"Book at lumen\uFF61observer", true},
		{"Book at lumen\uFF0Eobserver", true},
		{"Book at lumen\u00B7observer", true},
		{"Book at lumen\u2024observer", true},
		{"Book at Cyprus.It/call", true},
		{"Book at Cyprus.It.", true},
		{"Write to kat@Cyprus.It was fine", true},
		{"Read Kat.Cyprus.It was fine", true},
		{"Cyprus.IT was fine", true},
		{"Microsoft.net and ASP.NET Core", false},
		{"microsoft.NET, asp.net and Socket.IO", false},
		{"ASP.NET VB.NET", false},
		{"I use Node.js and Socket.io daily", false},
		{"Cyprus.It was hard", false},
		{"It ends here.So what", false},
		{"I moved to Cyprus.In 2020 it was cheap", false},
		{"Release 2.0.app is out", false},
	} {
		err := validateMCPDraft("draft-1", tc.body)
		if tc.blocked {
			require.ErrorIs(t, err, ErrMCPDraftHasLink, tc.body)
		} else {
			require.NoError(t, err, tc.body)
		}
	}
}

func TestLaterDraftPreviewKeepsTheFirstCommentAndNote(t *testing.T) {
	// Verifies: BE-019
	comment := "Sources: https://www.anthropic.com/news/x\nBook: https://lumen.observer/call/katerina"
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Shorter now.", Status: "draft"}, nil)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, false).Return(0, nil)
	reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool {
		return n.FirstComment == comment && n.Note == "Edited: shorter"
	})).Return(nil)
	reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(21), int64(0)).Return(nil)
	reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "first", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 20, Note: "Sources: anthropic.com", FirstComment: comment},
	}, nil)
	reviews.EXPECT().ReplaceNotice(mock.Anything, "member-1", "first").Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "Note: Edited: shorter") && strings.HasSuffix(m.Text, firstCommentBlock+comment)
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 21}, nil)
	notifier.EXPECT().Replace(mock.Anything, "member-1", int64(20), replacedPreview).Return(nil)
	story, err := NewNotifyScheduled(proAccess(t), drafts, scheduleMocks.NewMockRepository(t), reviews, nil, notifier, "https://platform.bloomary.life")
	require.NoError(t, err)

	_, err = story.NotifyDraft(context.Background(), "member-1", "draft-1", "Edited: shorter", "", false)

	require.NoError(t, err)
}

func TestMyPostsPreviewWorksAgainAfterItWasReplaced(t *testing.T) {
	// Verifies: BE-018
	f := newPostsFixture(t)
	f.reviews.EXPECT().NoticeByID(mock.Anything, "member-1", "n-1").Return(reviewDomain.Notice{ID: "n-1", DraftID: "draft-1", Status: reviewDomain.NoticeReplaced}, nil)
	f.drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Body: "Proof beats adjectives.", Status: "draft"}, nil)
	f.reviews.EXPECT().CountNoticesSince(mock.Anything, "member-1", mock.Anything, true).Return(0, nil)
	f.reviews.EXPECT().ListNotices(mock.Anything, "member-1", mock.Anything).Return([]reviewDomain.Notice{
		{ID: "n-2", DraftID: "draft-1", Status: reviewDomain.NoticeSent, MessageID: 30, Note: "Sources: your note", FirstComment: "Book: https://lumen.observer",
			CreatedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)},
	}, nil)
	f.reviews.EXPECT().CreateNotice(mock.Anything, mock.MatchedBy(func(n reviewDomain.Notice) bool {
		return n.Requested && n.FirstComment == "Book: https://lumen.observer" && n.Note == "(from the preview of Oct 30) Sources: your note"
	})).Return(nil)
	f.notifier.EXPECT().Notify(mock.Anything, "member-1", mock.Anything, (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 31}, nil)
	f.reviews.EXPECT().SetNoticeMessage(mock.Anything, "member-1", mock.Anything, int64(31), int64(0)).Return(nil)
	f.reviews.EXPECT().ReplaceNotice(mock.Anything, "member-1", "n-2").Return(nil)
	f.notifier.EXPECT().Replace(mock.Anything, "member-1", int64(30), replacedPreview).Return(nil)
	f.gate.EXPECT().AnswerCallback(mock.Anything, "cb-9", "", false).Return(nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), telegramDomain.Update{Callback: &telegramDomain.Callback{
		ID: "cb-9", ChatID: 100, MessageID: 5, Data: "pv:d:n-1", From: telegramDomain.Sender{ID: 100}}}))
}

func TestListFeedbackReportsReplacedPreviews(t *testing.T) {
	// Verifies: BE-014
	story, err := NewListFeedback(proAccess(t), reviewMocks.NewMockRepository(t), draftMocks.NewMockRepository(t), scheduleMocks.NewMockRepository(t))
	require.NoError(t, err)

	require.Equal(t, "replaced", story.postStatus(context.Background(), reviewDomain.Notice{Status: reviewDomain.NoticeReplaced}))
	require.Equal(t, "draft", story.postStatus(context.Background(), reviewDomain.Notice{Status: reviewDomain.NoticeSent}))
}

func TestReplyToACommentIdeaIsRefusedNotSavedAsAnIdea(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(80)).Return(reviewDomain.Notice{}, reviewDomain.ErrNotFound)
	reviews.EXPECT().SuggestionByMessage(mock.Anything, "member-1", int64(80)).Return(reviewDomain.Suggestion{
		ID: "s-1", MemberID: "member-1", Kind: reviewDomain.KindComment, TargetURL: "https://www.linkedin.com/posts/x", CreatedAt: time.Now(), MessageID: 80}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: commentReply, Keyboard: botKeyboard}).Return(81, nil)
	edits, err := NewEditReplies(reviews, scheduleMocks.NewMockRepository(t), draftMocks.NewMockRepository(t))
	require.NoError(t, err)
	trends, err := NewTrendReplies(reviews, f.insights)
	require.NoError(t, err)
	story, err := NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life",
		TelegramBotHandlers{Replies: []telegramDomain.ReplyHandler{edits, trends}})
	require.NoError(t, err)
	update := privateMessage("shorter, drop the question")
	update.Message.ReplyToMessageID, update.Message.ReplyToBot = 80, true

	require.NoError(t, story.HandleUpdate(context.Background(), update))
}

func TestUnlinkedTelegramIDIsForgottenAfterItsHour(t *testing.T) {
	// Verifies: BE-010
	f := newBotFixture(t)
	bot := f.story.(*telegramBot)
	bot.notLinkedTTL = 20 * time.Millisecond
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{}, telegramDomain.ErrNotLinked)
	f.gate.EXPECT().SendMessage(mock.Anything, mock.Anything).Return(1, nil).Once()

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("hello")))
	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("hello again")))

	require.Eventually(t, func() bool {
		bot.notLinkedMu.Lock()
		defer bot.notLinkedMu.Unlock()
		return len(bot.notLinkedAt) == 0
	}, time.Second, 5*time.Millisecond)
}
