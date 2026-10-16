package recaptcha

import "context"

type Captcha interface {
	Verify(ctx context.Context, response, remoteAddr string) error
}
