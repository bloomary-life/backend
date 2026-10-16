package platformerror

import (
	"errors"
	"fmt"
)

const (
	ErrCodeRateLimitHit = "rate_limit_hit"
	ErrCodeBadGateway   = "bad_gateway"
	ErrCodeRejected     = "rejected"
	ErrCodeStoreFailed  = "store_failed"
)

type TypedError struct {
	Code string
	Err  error
	Desc string
}

func New(code string, err error, opts ...string) *TypedError {
	typedErr := &TypedError{
		Code: code,
		Err:  err,
	}
	if len(opts) > 0 {
		typedErr.Desc = opts[0]
	}

	return typedErr
}

func Is(err error, code string) bool {
	var typedErr *TypedError
	return errors.As(err, &typedErr) && typedErr.Code == code
}

func (e *TypedError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Desc)
	}

	return fmt.Sprintf("%s: %s: %v", e.Code, e.Desc, e.Err)
}

func (e *TypedError) Unwrap() error {
	return e.Err
}
