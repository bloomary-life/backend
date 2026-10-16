package linkedin

import (
	"context"
	"io"
)

type Gate interface {
	ExchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (Token, error)
	UserInfo(ctx context.Context, accessToken string) (Profile, error)
	PublishPost(ctx context.Context, accessToken string, post Post) (string, error)
	UploadMedia(ctx context.Context, accessToken string, upload Upload, contentType string, body io.Reader) (string, error)
}
