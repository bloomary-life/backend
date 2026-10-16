package feedback

import "context"

type FeedbackStory interface {
	SubmitFeedback(ctx context.Context, input Input) error
}
