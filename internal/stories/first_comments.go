package stories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	reviewDomain "bloomary-api/internal/domain/review"
	scheduledDomain "bloomary-api/internal/domain/scheduledpost"
	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/logger"
)

const firstCommentHeader = "First comment for your post that just went out. Paste it under the post:\n\n"

// FirstComments keeps the comment (links allowed) the AI app wrote for a scheduled draft and, when the post
// publishes, sends it to the member in Telegram with Copy and Open post. Posts themselves never carry links.
type FirstComments struct {
	reviews  reviewDomain.Repository
	notifier telegramDomain.Notifier
}

var (
	_ reviewDomain.FirstCommentStory = (*FirstComments)(nil)
	_ scheduledDomain.PublishedHook  = (*FirstComments)(nil)
)

func NewFirstComments(reviews reviewDomain.Repository, notifier telegramDomain.Notifier) (*FirstComments, error) {
	if reviews == nil || notifier == nil {
		return nil, fmt.Errorf("first comments: reviews and notifier are required")
	}
	return &FirstComments{reviews: reviews, notifier: notifier}, nil
}

// Save keeps the comment for that schedule only: a later schedule of the same draft (e.g. from the web app)
// never sends it.
func (s *FirstComments) Save(ctx context.Context, memberID, draftID, scheduleID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > reviewDomain.MaxFirstCommentRunes {
		return fmt.Errorf("first_comment must be 1-%d characters", reviewDomain.MaxFirstCommentRunes)
	}
	return s.reviews.SaveFirstComment(ctx, memberID, draftID, scheduleID, text, time.Now().UTC())
}

func (s *FirstComments) Clear(ctx context.Context, memberID, draftID string) error {
	return s.reviews.DeleteFirstComment(ctx, memberID, draftID)
}

// PostPublished sends the first comment, if the draft has one; failures are logged, the post is already live.
func (s *FirstComments) PostPublished(ctx context.Context, post scheduledDomain.Post) {
	text, scheduleID, err := s.reviews.FirstComment(ctx, post.MemberID, post.DraftID)
	if errors.Is(err, reviewDomain.ErrNotFound) {
		return
	}
	if err != nil {
		logger.FromContext(ctx).WithError(err).Error("first_comment_lookup_failed")
		return
	}
	// Sent once, and only for the schedule it was written for.
	if err := s.reviews.DeleteFirstComment(ctx, post.MemberID, post.DraftID); err != nil {
		logger.FromContext(ctx).WithError(err).Error("first_comment_delete_failed")
	}
	if scheduleID != post.ID {
		return
	}
	var row []telegramDomain.Button
	if utf8.RuneCountInString(text) <= reviewDomain.CopyTextLimit {
		row = append(row, telegramDomain.Button{Text: "Copy comment", CopyText: text})
	}
	if strings.HasPrefix(post.LinkedInPostID, "urn:li:") {
		row = append(row, telegramDomain.Button{Text: "Open post", URL: "https://www.linkedin.com/feed/update/" + post.LinkedInPostID + "/"})
	}
	message := telegramDomain.OutgoingMessage{Text: fitTelegram(firstCommentHeader, text, "")}
	if len(row) > 0 {
		message.Buttons = [][]telegramDomain.Button{row}
	}
	if _, err := s.notifier.Notify(ctx, post.MemberID, message, nil); err != nil {
		logger.FromContext(ctx).WithError(err).Error("first_comment_send_failed")
	}
}
