package linkedin

import (
	"context"
	"time"
)

type LinkedInPostStory interface {
	CreatePost(ctx context.Context, input CreatePostInput) (CreatePostOutput, error)
}

type CreatePostInput struct {
	AccessToken string
	MemberID    string
	Commentary  string
	Visibility  string
	Attachments []PostAttachment
}

type PostAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type CreatePostOutput struct {
	PostID      string
	PublishedAt time.Time
}
