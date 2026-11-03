// Package netcalltest holds test doubles for the network gates.
package netcalltest

import (
	"bytes"
	"context"
	"io"
	"time"
)

// FakeMedia is an in-memory MediaSource for gate tests: synthetic bytes, a fixed signed-URL base.
type FakeMedia struct {
	Objects map[string][]byte
	Types   map[string]string
	URLBase string
}

func (f FakeMedia) Open(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	data := f.Objects[key]
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), f.Types[key], nil
}

func (f FakeMedia) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return f.URLBase + "/" + key + "?X-Amz-Signature=synthetic", nil
}
