package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bloomary-api/internal/platform/logger"
)

const (
	requestLogDataLimit = 2048
	redactedLogValue    = "<redacted>"
)

func requestLoggingMiddleware(appLogger *logger.Logger, next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "handler not configured", http.StatusInternalServerError)
		})
	}

	if appLogger == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		r = r.WithContext(logger.ContextWithFields(r.Context(), nil))

		appLogger.WithFields(requestFieldsFromContext(r)).Info("request_accepted")

		recorder := &loggingResponseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
			data:           &limitedLogBuffer{limit: requestLogDataLimit},
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.SetFields(r.Context(), logger.Fields{"panic_type": fmt.Sprintf("%T", recovered)}).
					SetField("outcome", "panic")
				recorder.status = http.StatusInternalServerError
				if !recorder.wroteHeader {
					http.Error(recorder, "internal server error", http.StatusInternalServerError)
				}
			}

			fields := requestFieldsFromContext(r)
			fields["status"] = recorder.logStatus(r)
			fields["duration_ms"] = time.Since(started).Milliseconds()
			fields["data_truncated"] = recorder.data.truncated
			if recorder.data.truncated {
				fields["data"] = redactedLogValue
			} else {
				fields["data"] = logResponseData(recorder.data.String(), recorder.Header().Get("Content-Type"))
			}

			appLogger.WithFields(fields).Info("request_done")
		}()

		next.ServeHTTP(recorder, r)
	})
}

func requestLogFields(r *http.Request) logger.Fields {
	return logger.Fields{
		"method":         r.Method,
		"path":           r.URL.Path,
		"query_params":   redactedQuery(r.URL.Query()),
		"content_length": r.ContentLength,
	}
}

func requestFieldsFromContext(r *http.Request) logger.Fields {
	fields := requestLogFields(r)
	for key, value := range logger.ContextFields(r.Context()) {
		fields[key] = value
	}

	return fields
}

func redactedQuery(values url.Values) url.Values {
	out := url.Values{}
	for key, values := range values {
		for _, value := range values {
			if isSensitiveLogKey(key) {
				value = redactedLogValue
			}

			out.Add(key, value)
		}
	}

	return out
}

func logResponseData(data, contentType string) string {
	if data == "" || !strings.Contains(strings.ToLower(contentType), "json") {
		return data
	}

	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return data
	}

	redacted, err := json.Marshal(redactSensitiveJSON(value))
	if err != nil {
		return data
	}

	return string(redacted)
}

func redactSensitiveJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isSensitiveLogKey(key) {
				out[key] = redactedLogValue
				continue
			}

			out[key] = redactSensitiveJSON(child)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactSensitiveJSON(child)
		}

		return out
	case string:
		return redactSensitiveString(typed)
	default:
		return value
	}
}

func redactSensitiveString(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery == "" {
		return value
	}

	parsed.RawQuery = redactedQuery(parsed.Query()).Encode()
	return parsed.String()
}

func isSensitiveLogKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range []string{"authorization", "token", "secret", "password", "code", "state", "email"} {
		if strings.Contains(key, marker) {
			return true
		}
	}

	return false
}

type limitedLogBuffer struct {
	builder   strings.Builder
	limit     int
	truncated bool
}

func (b *limitedLogBuffer) Write(data []byte) int {
	remaining := b.limit - b.builder.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = b.builder.Write(data[:remaining])
			b.truncated = true
			return len(data)
		}

		_, _ = b.builder.Write(data)
		return len(data)
	}

	if len(data) > 0 {
		b.truncated = true
	}

	return len(data)
}

func (b *limitedLogBuffer) String() string {
	return b.builder.String()
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	data        *limitedLogBuffer
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}

	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	w.wroteHeader = true
	_ = w.data.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *loggingResponseWriter) logStatus(r *http.Request) int {
	if !w.wroteHeader && r.Context().Err() != nil {
		return 499
	}

	return w.status
}
