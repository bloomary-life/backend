package resend

import "context"

type Gate interface {
	SendFeedback(ctx context.Context, feedback Feedback) error
}
