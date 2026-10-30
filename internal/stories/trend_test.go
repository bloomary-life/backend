package stories

import (
	"context"
	"strings"
	"testing"
	"time"

	insightDomain "bloomary-api/internal/domain/insight"
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

const trendSummary = "Anthropic released a new model today.\nIt targets long coding tasks."

func TestSendTrendShowsOpenSourceAndKeepsTheMessageForReplies(t *testing.T) {
	// Verifies: BE-023
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().CountSuggestionsSince(mock.Anything, "member-1", mock.Anything).Return(0, nil)
	reviews.EXPECT().CreateSuggestion(mock.Anything, mock.MatchedBy(func(s reviewDomain.Suggestion) bool { return s.Kind == reviewDomain.KindTrend })).Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", telegramDomain.OutgoingMessage{
		Text:    "Story for today\n" + trendSummary + trendFooter,
		Buttons: [][]telegramDomain.Button{{{Text: "Open source", URL: "https://www.anthropic.com/news/x"}}},
	}, (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 70}, nil)
	reviews.EXPECT().SetSuggestionMessage(mock.Anything, "member-1", mock.Anything, int64(70)).Return(nil)
	story, err := NewSendSuggestion(proAccess(t), reviews, notifier)
	require.NoError(t, err)

	sent, err := story.Send(context.Background(), reviewDomain.SuggestionInput{MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "https://www.anthropic.com/news/x", Text: trendSummary})

	require.NoError(t, err)
	require.Equal(t, int64(70), sent.MessageID)
}

func TestSendTrendNeedsAnHTTPSSource(t *testing.T) {
	// Verifies: BE-023
	story, err := NewSendSuggestion(proAccess(t), reviewMocks.NewMockRepository(t), telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)

	_, err = story.Send(context.Background(), reviewDomain.SuggestionInput{MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "http://example.com/x", Text: trendSummary})

	require.ErrorIs(t, err, reviewDomain.ErrInvalidSource)
}

func TestVoiceReplyToTrendBecomesInsightWithTheStory(t *testing.T) {
	// Verifies: BE-023
	f := linkedBot(t)
	reviews := reviewMocks.NewMockRepository(t)
	trend := reviewDomain.Suggestion{ID: "s-9", MemberID: "member-1", Kind: reviewDomain.KindTrend, TargetURL: "https://www.anthropic.com/news/x", Text: trendSummary, CreatedAt: time.Now(), MessageID: 70}
	reviews.EXPECT().NoticeByMessage(mock.Anything, "member-1", int64(70)).Return(reviewDomain.Notice{}, reviewDomain.ErrNotFound)
	reviews.EXPECT().SuggestionByMessage(mock.Anything, "member-1", int64(70)).Return(trend, nil)
	reviews.EXPECT().SuggestionByID(mock.Anything, "member-1", "s-9").Return(trend, nil)
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, nil)
	f.insights.EXPECT().ReserveVoice(mock.Anything, "member-1", 9, mock.Anything, mock.Anything).Return(true, nil)
	f.gate.EXPECT().DownloadFile(mock.Anything, "voice-1", mock.Anything).Return([]byte("ogg"), nil)
	f.transcriber.EXPECT().Transcribe(mock.Anything, mock.Anything, "audio/ogg").Return(telegramDomain.Transcript{Text: "Juniors should learn to review AI code."}, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.MatchedBy(func(in insightDomain.Insight) bool {
		return in.Text == "Juniors should learn to review AI code." && in.Source == insightDomain.SourceVoice &&
			in.Trend != nil && *in.Trend == insightDomain.Trend{SourceURL: "https://www.anthropic.com/news/x", Summary: trendSummary}
	})).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: "Saved your take: “Juniors should learn to review AI code.”\n\n" + trendSavedTail, Keyboard: botKeyboard}).Return(71, nil)
	edits, err := NewEditReplies(reviews, scheduleMocks.NewMockRepository(t), draftMocks.NewMockRepository(t))
	require.NoError(t, err)
	trends, err := NewTrendReplies(reviews, f.insights)
	require.NoError(t, err)
	story, err := NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life",
		TelegramBotHandlers{Replies: []telegramDomain.ReplyHandler{edits, trends}, VoiceMembers: telegramDomain.MemberSet{"member-1": true}})
	require.NoError(t, err)
	update := voiceMessage(9)
	update.Message.ReplyToMessageID, update.Message.ReplyToBot = 70, true

	require.NoError(t, story.HandleUpdate(context.Background(), update))
}

func TestFirstCommentIsSentWithCopyAndOpenPostWhenThePostPublishes(t *testing.T) {
	// Verifies: BE-024
	comment := "Book a 45-minute call: https://lumen.observer/call/katerina"
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().FirstComment(mock.Anything, "member-1", "draft-1").Return(comment, "job-1", nil)
	reviews.EXPECT().DeleteFirstComment(mock.Anything, "member-1", "draft-1").Return(nil)
	notifier := telegramMocks.NewMockNotifier(t)
	notifier.EXPECT().Notify(mock.Anything, "member-1", mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.HasPrefix(m.Text, "First comment for your post") && strings.HasSuffix(m.Text, comment) && len(m.Buttons) == 1 &&
			m.Buttons[0][0] == telegramDomain.Button{Text: "Copy comment", CopyText: comment} &&
			m.Buttons[0][1] == telegramDomain.Button{Text: "Open post", URL: "https://www.linkedin.com/feed/update/urn:li:share:7/"}
	}), (*telegramDomain.Photo)(nil)).Return(telegramDomain.Delivery{MessageID: 80}, nil)
	comments, err := NewFirstComments(reviews, notifier)
	require.NoError(t, err)

	comments.PostPublished(context.Background(), scheduledDomain.Post{ID: "job-1", MemberID: "member-1", DraftID: "draft-1", LinkedInPostID: "urn:li:share:7", Status: scheduledDomain.Published})
}

func TestNoFirstCommentSendsNothing(t *testing.T) {
	// Verifies: BE-024
	reviews := reviewMocks.NewMockRepository(t)
	reviews.EXPECT().FirstComment(mock.Anything, "member-1", "draft-1").Return("", "", reviewDomain.ErrNotFound)
	comments, err := NewFirstComments(reviews, telegramMocks.NewMockNotifier(t))
	require.NoError(t, err)

	comments.PostPublished(context.Background(), scheduledDomain.Post{MemberID: "member-1", DraftID: "draft-1", LinkedInPostID: "urn:li:share:7"})
}
