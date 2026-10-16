package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSentryDisabledDoesNotRequireDSN(t *testing.T) {
	log, err := New(SentryEnabled(false))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if !log.Flush(0) {
		t.Fatal("disabled sentry flush returned false")
	}
}

func TestSentryEnabledRequiresDSN(t *testing.T) {
	if _, err := New(SentryEnabled(true)); err == nil {
		t.Fatal("expected error")
	}
}

func TestSentryEnabledInstallsHook(t *testing.T) {
	log, err := New(
		SentryEnabled(true),
		SentryDSN("https://public@example.com/1"),
		SentryEnvironment("test"),
		SentryRelease("test-release"),
		SentryAggregationWindow(3*time.Minute),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if log.sentry == nil {
		t.Fatal("sentry aggregator is nil")
	}
	if log.sentry.window != 3*time.Minute {
		t.Fatalf("sentry window = %s", log.sentry.window)
	}
}

func TestTextFormatIncludesContextFields(t *testing.T) {
	var out bytes.Buffer
	log, err := New(Output(&out))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	ctx := ContextWithFields(context.Background(), Fields{"request_id": "req-1"})

	log.WithField("route", "/health").InfoContext(ctx, "ready")

	line := out.String()
	for _, want := range []string{"msg=ready", "route=/health", "request_id=req-1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}
}

func TestJSONFormatToggle(t *testing.T) {
	var out bytes.Buffer
	log, err := New(Output(&out), UseJSONFormat(true))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	log.WithField("component", "api").Info("ready")

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("log json: %v", err)
	}
	if line["msg"] != "ready" || line["component"] != "api" {
		t.Fatalf("log line = %#v", line)
	}
}

func TestConcurrentLogging(t *testing.T) {
	var out bytes.Buffer
	log, err := New(Output(&out), UseJSONFormat(true))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	entry := log.WithField("component", "api")

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := ContextWithFields(context.Background(), Fields{"request_id": i})
			entry.WithField("worker", i).InfoContext(ctx, "ready")
		}(i)
	}
	wg.Wait()

	if count := bytes.Count(out.Bytes(), []byte("\n")); count != 100 {
		t.Fatalf("log line count = %d", count)
	}
}
