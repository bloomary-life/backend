package stories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
	insightDomain "bloomary-api/internal/domain/insight"
	telegramDomain "bloomary-api/internal/domain/telegram"
	accountMocks "bloomary-api/internal/mocks/domain/account"
	insightMocks "bloomary-api/internal/mocks/domain/insight"
	telegramMocks "bloomary-api/internal/mocks/domain/telegram"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testNonce = "abcdefghijklmnopqrstuvwxyz012345"

type botFixture struct {
	links       *telegramMocks.MockLinkRepository
	gate        *telegramMocks.MockGate
	transcriber *telegramMocks.MockTranscriber
	insights    *insightMocks.MockRepository
	accounts    *accountMocks.MockRepository
	story       telegramDomain.BotStory
}

func newBotFixture(t *testing.T) botFixture {
	f := botFixture{
		links: telegramMocks.NewMockLinkRepository(t), gate: telegramMocks.NewMockGate(t),
		transcriber: telegramMocks.NewMockTranscriber(t), insights: insightMocks.NewMockRepository(t),
		accounts: accountMocks.NewMockRepository(t),
	}
	story, err := NewTelegramBot(f.links, f.gate, f.transcriber, f.insights, f.accounts, "https://platform.bloomary.life", TelegramBotHandlers{
		VoiceMembers: telegramDomain.MemberSet{"member-1": true}, NumbersMembers: telegramDomain.MemberSet{"member-1": true}})
	require.NoError(t, err)
	f.story = story
	return f
}

func privateMessage(text string) telegramDomain.Update {
	return telegramDomain.Update{Message: &telegramDomain.IncomingMessage{ChatID: 100, ChatType: "private", Text: text, From: telegramDomain.Sender{ID: 100, Username: "kat"}}}
}

func TestTelegramLinkAsksForTheNumberShownInBloomaryAndLinksOnMatch(t *testing.T) {
	// Verifies: BE-009
	f := newBotFixture(t)
	attempt := telegramDomain.LinkAttempt{Nonce: testNonce, MemberID: "member-1", Number: 42, ExpiresAt: time.Now().Add(time.Minute)}
	f.links.EXPECT().AttemptByNonce(mock.Anything, testNonce, mock.Anything).Return(attempt, nil)
	f.accounts.EXPECT().ByMemberID(mock.Anything, "member-1").Return(accountDomain.Profile{Name: "Kat"}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		if !strings.Contains(m.Text, "the Bloomary account of Kat") || len(m.Buttons) != 2 || len(m.Buttons[0]) != 4 {
			return false
		}
		for _, b := range m.Buttons[0] {
			if b.CallbackData == "tl:42:"+testNonce {
				return m.Buttons[1][0].CallbackData == "tl:no:"+testNonce
			}
		}
		return false
	})).Return(1, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("/start l_"+testNonce)))

	f.links.EXPECT().ConsumeAttempt(mock.Anything, testNonce, mock.Anything).Return(attempt, nil)
	f.links.EXPECT().SaveLink(mock.Anything, mock.MatchedBy(func(l telegramDomain.Link) bool {
		return l.MemberID == "member-1" && l.TelegramUserID == 100 && l.ChatID == 100 && l.Username == "kat"
	})).Return(nil)
	f.gate.EXPECT().EditMessageText(mock.Anything, int64(100), int64(9), mock.MatchedBy(func(text string) bool { return strings.HasPrefix(text, "Linked to Bloomary.") })).Return(nil)
	f.gate.EXPECT().AnswerCallback(mock.Anything, "cb-1", "", false).Return(nil)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100}, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botHelpVoice, Keyboard: botKeyboard}).Return(10, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), telegramDomain.Update{Callback: &telegramDomain.Callback{
		ID: "cb-1", Data: "tl:42:" + testNonce, ChatID: 100, MessageID: 9, From: telegramDomain.Sender{ID: 100, Username: "kat"}}}))
}

func TestTelegramTextFromLinkedMemberIsSavedAsInsight(t *testing.T) {
	// Verifies: BE-010
	f := newBotFixture(t)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100}, nil)
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(3, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.MatchedBy(func(in insightDomain.Insight) bool {
		return in.MemberID == "member-1" && in.Text == "Recruiters skim the first two lines" && in.Source == insightDomain.SourceText && in.ID != ""
	})).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botSaved, Keyboard: botKeyboard}).Return(2, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("Recruiters skim the first two lines")))
}

func TestTelegramVoiceIsTranscribedAndSavedWithoutKeepingAudio(t *testing.T) {
	// Verifies: BE-010
	f := newBotFixture(t)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100}, nil)
	f.insights.EXPECT().ReserveVoice(mock.Anything, "member-1", 12, mock.Anything, mock.Anything).Return(true, nil)
	audio := []byte("fake-ogg")
	f.gate.EXPECT().DownloadFile(mock.Anything, "voice-1", int64(insightDomain.MaxVoiceFileBytes)).Return(audio, nil)
	f.transcriber.EXPECT().Transcribe(mock.Anything, mock.Anything, "audio/ogg").Return(telegramDomain.Transcript{Text: "Hiring managers want proof, not adjectives.", Language: "en"}, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.MatchedBy(func(in insightDomain.Insight) bool {
		return in.Text == "Hiring managers want proof, not adjectives." && in.Source == insightDomain.SourceVoice
	})).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: "Saved: “Hiring managers want proof, not adjectives.”\n\n" + botSavedTail, Keyboard: botKeyboard}).Return(3, nil)
	update := privateMessage("")
	update.Message.Voice = &telegramDomain.Voice{FileID: "voice-1", MimeType: "audio/ogg", Duration: 12, FileSize: 8}

	require.NoError(t, f.story.HandleUpdate(context.Background(), update))
	require.Equal(t, make([]byte, len(audio)), audio, "audio buffer is cleared after transcription")
}

func TestTelegramUnlinkedUserIsAskedToLinkBloomary(t *testing.T) {
	// Verifies: BE-010
	f := newBotFixture(t)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{}, telegramDomain.ErrNotLinked)
	f.gate.EXPECT().SendMessage(mock.Anything, mock.MatchedBy(func(m telegramDomain.OutgoingMessage) bool {
		return strings.Contains(m.Text, "isn't linked to Bloomary") && strings.Contains(m.Text, "Sign in to Bloomary with LinkedIn") &&
			strings.Contains(m.Text, "https://platform.bloomary.life/settings") && m.Keyboard == nil
	})).Return(4, nil).Once()

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("an idea")))
	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("another idea")), "answered at most once an hour")
}

func linkedBot(t *testing.T) botFixture {
	f := newBotFixture(t)
	f.links.EXPECT().ByTelegramUser(mock.Anything, int64(100)).Return(telegramDomain.Link{MemberID: "member-1", ChatID: 100, TelegramUserID: 100}, nil)
	return f
}

func voiceMessage(seconds int) telegramDomain.Update {
	update := privateMessage("")
	update.Message.Voice = &telegramDomain.Voice{FileID: "voice-1", MimeType: "audio/ogg", Duration: seconds, FileSize: 8}
	return update
}

func TestTelegramVoiceOverTheDailyCapIsNotSentToDeepgram(t *testing.T) {
	// Verifies: BE-021
	f := linkedBot(t)
	f.insights.EXPECT().ReserveVoice(mock.Anything, "member-1", 600, mock.Anything, mock.Anything).Return(false, nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botVoiceLimit, Keyboard: botKeyboard}).Return(5, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), voiceMessage(600)))
}

func TestTelegramEmptyTranscriptIsNotSaved(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	f.insights.EXPECT().ReserveVoice(mock.Anything, "member-1", 3, mock.Anything, mock.Anything).Return(true, nil)
	f.gate.EXPECT().DownloadFile(mock.Anything, "voice-1", mock.Anything).Return([]byte("ogg"), nil)
	f.transcriber.EXPECT().Transcribe(mock.Anything, mock.Anything, "audio/ogg").Return(telegramDomain.Transcript{}, telegramDomain.ErrTranscriptEmpty)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botVoiceEmpty, Keyboard: botKeyboard}).Return(6, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), voiceMessage(3)))
}

func TestTelegramWeeklyNumbersGetTheirOwnConfirmation(t *testing.T) {
	// Verifies: BE-010
	for _, text := range []string{"numbers: views 120", "Numbers: views 120", "Numbers, views 120"} {
		f := linkedBot(t)
		f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, nil)
		f.insights.EXPECT().Create(mock.Anything, mock.MatchedBy(func(in insightDomain.Insight) bool { return in.Text == text })).Return(nil)
		f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botNumbers, Keyboard: botKeyboard}).Return(7, nil)

		require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage(text)), text)
	}
}

func TestTelegramSlashTextThatIsNotACommandIsSavedAsIdea(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	text := "/r/cscareerquestions thread says juniors can't find jobs"
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, nil)
	f.insights.EXPECT().Create(mock.Anything, mock.MatchedBy(func(in insightDomain.Insight) bool { return in.Text == text })).Return(nil)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botSaved, Keyboard: botKeyboard}).Return(8, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage(text)))
}

func TestTelegramUnsupportedMessageSaysVoiceOrTextOnly(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botUnsupported, Keyboard: botKeyboard}).Return(9, nil)

	require.NoError(t, f.story.HandleUpdate(context.Background(), privateMessage("")))
}

func TestTelegramSaveFailureTellsTheMember(t *testing.T) {
	// Verifies: BE-010
	f := linkedBot(t)
	f.insights.EXPECT().CountSince(mock.Anything, "member-1", insightDomain.Source(""), mock.Anything).Return(0, errors.New("database is locked"))
	f.gate.EXPECT().SendMessage(mock.Anything, telegramDomain.OutgoingMessage{ChatID: 100, Text: botFailed, Keyboard: botKeyboard}).Return(10, nil)

	require.Error(t, f.story.HandleUpdate(context.Background(), privateMessage("an idea")))
}

func TestTelegramRepeatedUpdateIsIgnored(t *testing.T) {
	// Verifies: BE-022
	f := newBotFixture(t)
	f.links.EXPECT().MarkUpdateSeen(mock.Anything, int64(77), mock.Anything).Return(false, nil)
	update := privateMessage("an idea")
	update.ID = 77

	require.NoError(t, f.story.HandleUpdate(context.Background(), update))
}

func TestTelegramLinkStartReturnsDeepLinkAndNumber(t *testing.T) {
	// Verifies: BE-009
	links := telegramMocks.NewMockLinkRepository(t)
	var stored telegramDomain.LinkAttempt
	links.EXPECT().StartAttempt(mock.Anything, mock.Anything).Run(func(_ context.Context, attempt telegramDomain.LinkAttempt) { stored = attempt }).Return(nil)
	story, err := NewTelegramLink(links, "@BloomaryBot", nil, nil)
	require.NoError(t, err)

	start, err := story.Start(context.Background(), "member-1")

	require.NoError(t, err)
	require.Equal(t, "https://t.me/BloomaryBot?start=l_"+stored.Nonce, start.DeepLink)
	require.Regexp(t, `^[A-Za-z0-9_-]{32}$`, stored.Nonce)
	require.Equal(t, stored.Number, start.Number)
	require.GreaterOrEqual(t, start.Number, 10)
	require.LessOrEqual(t, start.Number, 99)
	require.Equal(t, "member-1", stored.MemberID)
}
