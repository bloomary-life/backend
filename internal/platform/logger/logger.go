package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Fields map[string]any

type Field struct {
	Key   string
	Value any
}

type ContextFieldsWriter struct {
	fields *contextFields
}

type Logger struct {
	core          *slog.Logger
	sentry        *sentryAggregator
	sentryEnabled bool
	fatalDelay    time.Duration
	exit          func(int)
}

type Entry struct {
	core   *slog.Logger
	logger *Logger
	fields Fields
}

type Option func(*config)

type config struct {
	json           bool
	output         io.Writer
	fatalDelay     time.Duration
	exit           func(int)
	sentryEnabled  bool
	sentryDSN      string
	appEnvironment string
	appVersion     string
	sentryWindow   time.Duration
}

type contextKey string

type contextFields struct {
	mu     sync.RWMutex
	values Fields
}

const (
	contextFieldsKey contextKey = "fields"
	contextLoggerKey contextKey = "logger"
)

func New(opts ...Option) (*Logger, error) {
	loggerConfig := config{
		output:       os.Stdout,
		fatalDelay:   time.Minute,
		exit:         os.Exit,
		sentryWindow: 10 * time.Minute,
	}
	for _, opt := range opts {
		opt(&loggerConfig)
	}

	handlerOptions := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler
	if loggerConfig.json {
		handler = slog.NewJSONHandler(loggerConfig.output, handlerOptions)
	} else {
		handler = slog.NewTextHandler(loggerConfig.output, handlerOptions)
	}

	log := &Logger{
		core:          slog.New(contextHandler{next: handler}),
		sentryEnabled: loggerConfig.sentryEnabled,
		fatalDelay:    loggerConfig.fatalDelay,
		exit:          loggerConfig.exit,
	}
	if loggerConfig.sentryEnabled {
		reporter, err := newSentryAggregator(loggerConfig)
		if err != nil {
			return nil, err
		}

		log.sentry = reporter
	}

	return log, nil
}

func UseJSONFormat(enabled bool) Option {
	return func(c *config) {
		c.json = enabled
	}
}

func Output(output io.Writer) Option {
	return func(c *config) {
		if output != nil {
			c.output = output
		}
	}
}

func FatalDelay(delay time.Duration) Option {
	return func(c *config) {
		if delay > 0 {
			c.fatalDelay = delay
		}
	}
}

func SentryEnabled(enabled bool) Option {
	return func(c *config) {
		c.sentryEnabled = enabled
	}
}

func SentryDSN(dsn string) Option {
	return func(c *config) {
		c.sentryDSN = dsn
	}
}

func SentryEnvironment(environment string) Option {
	return func(c *config) {
		c.appEnvironment = environment
	}
}

func SentryRelease(release string) Option {
	return func(c *config) {
		c.appVersion = release
	}
}

func AppEnvironment(environment string) Option {
	return SentryEnvironment(environment)
}

func AppVersion(version string) Option {
	return SentryRelease(version)
}

func SentryAggregationWindow(window time.Duration) Option {
	return func(c *config) {
		if window > 0 {
			c.sentryWindow = window
		}
	}
}

func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

func Err(err error) Field {
	return F("error", err)
}

func ContextWithFields(ctx context.Context, fields Fields, extra ...Field) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	store, _ := ctx.Value(contextFieldsKey).(*contextFields)
	if store == nil {
		store = &contextFields{values: Fields{}}
		ctx = context.WithValue(ctx, contextFieldsKey, store)
	}

	SetFields(ctx, fields, extra...)
	return ctx
}

func SetFields(ctx context.Context, fields Fields, extra ...Field) *ContextFieldsWriter {
	if ctx == nil {
		return &ContextFieldsWriter{}
	}

	store, _ := ctx.Value(contextFieldsKey).(*contextFields)
	writer := &ContextFieldsWriter{fields: store}
	return writer.SetFields(fields, extra...)
}

func (w *ContextFieldsWriter) SetField(key string, value any) *ContextFieldsWriter {
	if w == nil || w.fields == nil {
		return w
	}

	w.fields.mu.Lock()
	defer w.fields.mu.Unlock()
	w.fields.values[key] = value
	return w
}

func (w *ContextFieldsWriter) SetFields(fields Fields, extra ...Field) *ContextFieldsWriter {
	if w == nil || w.fields == nil {
		return w
	}

	copied := copyFields(fields)
	w.fields.mu.Lock()
	for key, value := range copied {
		w.fields.values[key] = value
	}

	w.fields.mu.Unlock()

	for _, field := range extra {
		w.SetField(field.Key, field.Value)
	}

	return w
}

func (l *Logger) ToContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	return context.WithValue(ctx, contextLoggerKey, l)
}

func FromContext(ctx context.Context) *Logger {
	log, ok := ctx.Value(contextLoggerKey).(*Logger)
	if !ok {
		panic("logger missing from context")
	}

	return log
}

func LookupContext(ctx context.Context) (*Logger, bool) {
	if ctx == nil {
		return nil, false
	}

	log, ok := ctx.Value(contextLoggerKey).(*Logger)
	return log, ok
}

func ContextFields(ctx context.Context) Fields {
	if ctx == nil {
		return Fields{}
	}

	existing, _ := ctx.Value(contextFieldsKey).(*contextFields)
	if existing == nil {
		return Fields{}
	}

	existing.mu.RLock()
	defer existing.mu.RUnlock()
	return copyFields(existing.values)
}

func (l *Logger) Flush(timeout time.Duration) bool {
	if !l.sentryEnabled {
		return true
	}

	return flushSentry(l.sentry, timeout)
}

func (l *Logger) WithFields(fields Fields) *Entry {
	copied := copyFields(fields)
	return &Entry{core: l.core.With(attrs(copied)...), logger: l, fields: copied}
}

func (l *Logger) WithField(key string, value any) *Entry {
	return l.WithFields(Fields{key: value})
}

func (l *Logger) WithError(err error) *Entry {
	return l.WithField("error", err)
}

func (l *Logger) Info(args ...any) {
	l.InfoContext(context.Background(), args...)
}

func (l *Logger) InfoContext(ctx context.Context, args ...any) {
	l.core.InfoContext(ctx, message(args...))
}

func (l *Logger) Error(args ...any) {
	l.ErrorContext(context.Background(), args...)
}

func (l *Logger) ErrorContext(ctx context.Context, args ...any) {
	message := message(args...)
	l.core.ErrorContext(ctx, message)
	l.report(ctx, "error", message, nil)
}

func (l *Logger) Fatal(args ...any) {
	message := message(args...)
	l.core.Log(context.Background(), slog.LevelError, message, slog.String("level", "fatal"))
	l.report(context.Background(), "fatal", message, nil)
	l.Flush(l.fatalDelay)
	l.exit(1)
}

func (l *Logger) Panic(args ...any) {
	message := message(args...)
	l.core.Log(context.Background(), slog.LevelError, message, slog.String("level", "panic"))
	l.report(context.Background(), "panic", message, nil)
	l.Flush(l.fatalDelay)
	panic(message)
}

func (l *Logger) report(ctx context.Context, level, message string, fields Fields) {
	if l.sentryEnabled {
		l.sentry.report(ctx, level, message, fields)
	}
}

func (e *Entry) WithFields(fields Fields) *Entry {
	merged := copyFields(e.fields)
	copied := copyFields(fields)
	for key, value := range copied {
		merged[key] = value
	}

	return &Entry{core: e.core.With(attrs(copied)...), logger: e.logger, fields: merged}
}

func (e *Entry) WithField(key string, value any) *Entry {
	return e.WithFields(Fields{key: value})
}

func (e *Entry) WithError(err error) *Entry {
	return e.WithField("error", err)
}

func (e *Entry) Info(args ...any) {
	e.InfoContext(context.Background(), args...)
}

func (e *Entry) InfoContext(ctx context.Context, args ...any) {
	e.core.InfoContext(ctx, message(args...))
}

func (e *Entry) Error(args ...any) {
	e.ErrorContext(context.Background(), args...)
}

func (e *Entry) ErrorContext(ctx context.Context, args ...any) {
	message := message(args...)
	e.core.ErrorContext(ctx, message)
	e.logger.report(ctx, "error", message, e.fields)
}

func (e *Entry) Fatal(args ...any) {
	message := message(args...)
	e.core.Log(context.Background(), slog.LevelError, message, slog.String("level", "fatal"))
	e.logger.report(context.Background(), "fatal", message, e.fields)
	e.logger.Flush(e.logger.fatalDelay)
	e.logger.exit(1)
}

func (e *Entry) Fatalf(format string, args ...any) {
	e.Fatal(fmt.Sprintf(format, args...))
}

func (e *Entry) Panic(args ...any) {
	message := message(args...)
	e.core.Log(context.Background(), slog.LevelError, message, slog.String("level", "panic"))
	e.logger.report(context.Background(), "panic", message, e.fields)
	e.logger.Flush(e.logger.fatalDelay)
	panic(message)
}

type contextHandler struct {
	next slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	for key, value := range ContextFields(ctx) {
		record.AddAttrs(slog.Any(key, value))
	}

	return h.next.Handle(ctx, record)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}

func attrs(fields Fields) []any {
	if len(fields) == 0 {
		return nil
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	out := make([]any, 0, len(fields)*2)
	for _, key := range keys {
		out = append(out, key, fields[key])
	}

	return out
}

func copyFields(fields Fields) Fields {
	out := make(Fields, len(fields))
	for key, value := range fields {
		out[key] = value
	}

	return out
}

func mergeFields(first, second Fields) Fields {
	merged := copyFields(first)
	for key, value := range second {
		merged[key] = value
	}

	return merged
}

func message(args ...any) string {
	return strings.TrimSpace(fmt.Sprint(args...))
}
