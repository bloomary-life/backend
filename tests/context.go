package tests

import (
	"context"
	"io"

	"bloomary-api/internal/platform/logger"
)

// TestContext returns a context with a logger for testing.
// Use this in all tests that require logger in context.
func TestContext() context.Context {
	log, _ := logger.New(logger.Output(io.Discard))
	return log.ToContext(context.Background())
}
