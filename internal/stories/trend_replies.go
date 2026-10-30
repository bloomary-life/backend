package stories

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	insightDomain "bloomary-api/internal/domain/insight"
	reviewDomain "bloomary-api/internal/domain/review"
	telegramDomain "bloomary-api/internal/domain/telegram"
)

const (
	trendSavedTail = "When your AI app turns it into a post, the preview comes here. Each reply becomes its own post: to add to this one, wait for its preview and reply there."
	trendSaved     = "Saved your take on this story. " + trendSavedTail
	trendLimit     = "That's 60 ideas in 24 hours, the limit. Send more in a few hours."
)

// TrendReplies turns a member's reply to a trend message into an insight that carries the story (source and summary).
type TrendReplies struct {
	reviews  reviewDomain.Repository
	insights insightDomain.Repository
}

var _ telegramDomain.ReplyHandler = (*TrendReplies)(nil)

func NewTrendReplies(reviews reviewDomain.Repository, insights insightDomain.Repository) (*TrendReplies, error) {
	if reviews == nil || insights == nil {
		return nil, fmt.Errorf("trend replies: reviews and insights are required")
	}
	return &TrendReplies{reviews: reviews, insights: insights}, nil
}

// Claim owns replies to the member's own trend messages sent since the current link; it refuses at the daily idea limit.
func (s *TrendReplies) Claim(ctx context.Context, link telegramDomain.Link, replyToMessageID int64) (telegramDomain.ReplyClaim, bool, error) {
	suggestion, err := s.reviews.SuggestionByMessage(ctx, link.MemberID, replyToMessageID)
	if errors.Is(err, reviewDomain.ErrNotFound) {
		return telegramDomain.ReplyClaim{}, false, nil
	}
	if err != nil {
		return telegramDomain.ReplyClaim{}, false, err
	}
	if suggestion.Kind != reviewDomain.KindTrend || suggestion.CreatedAt.Before(link.LinkedAt) {
		return telegramDomain.ReplyClaim{}, false, nil
	}
	claim := telegramDomain.ReplyClaim{ID: suggestion.ID}
	count, err := s.insights.CountSince(ctx, link.MemberID, "", time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		return telegramDomain.ReplyClaim{}, false, err
	}
	if count >= insightDomain.DailyLimit {
		claim.Refusal = trendLimit
	}
	return claim, true, nil
}

func (s *TrendReplies) Save(ctx context.Context, link telegramDomain.Link, claim telegramDomain.ReplyClaim, text, source string) (string, error) {
	suggestion, err := s.reviews.SuggestionByID(ctx, link.MemberID, claim.ID)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(text) > insightDomain.MaxTextRunes {
		text = string([]rune(text)[:insightDomain.MaxTextRunes])
	}
	err = s.insights.Create(ctx, insightDomain.Insight{ID: uuid.NewString(), MemberID: link.MemberID, Text: text, Source: insightDomain.Source(source),
		CreatedAt: time.Now().UTC(), Trend: &insightDomain.Trend{SourceURL: suggestion.TargetURL, Summary: suggestion.Text}})
	if err != nil {
		return "", err
	}
	if source == string(insightDomain.SourceVoice) {
		return "Saved your take: “" + snippet(text) + "”\n\n" + trendSavedTail, nil
	}
	return trendSaved, nil
}
