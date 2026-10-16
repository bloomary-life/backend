package httpclient

import (
	"net/http"
)

type Client interface {
	Do(req *http.Request, opts ...RequestOptionFunc) (*http.Response, error)
}
