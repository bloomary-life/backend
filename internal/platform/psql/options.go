package psql

import (
	"github.com/jackc/pgx/v5/tracelog"
)

const (
	LogLevelTrace = 6
	LogLevelDebug = 5
	LogLevelInfo  = 4
	LogLevelWarn  = 3
	LogLevelError = 2
	LogLevelNone  = 1
)

// options настройки подключения к базе
type options struct {
	logger             tracelog.Logger
	logLevel           tracelog.LogLevel
	maxOpenConnections int
	maxIdleConnections int
}

// OptionFn - тип опции
type OptionFn func(*options)

func newOptions(opts []OptionFn) *options {
	o := &options{}
	for _, option := range opts {
		option(o)
	}

	return o
}

// Logger - tracelog.Logger
func Logger(l tracelog.Logger) OptionFn {
	return func(o *options) {
		o.logger = l
	}
}

// LogLevel - уровень логгирования. Валидные: trace, debug, info, warn, error, none
func LogLevel(l tracelog.LogLevel) OptionFn {
	return func(o *options) {
		o.logLevel = l
	}
}

// MaxOpenConnections устанавливает максимальное число открытых коннектов к БД. Фактически - размер пула
func MaxOpenConnections(n int) OptionFn {
	return func(o *options) {
		o.maxOpenConnections = n
	}
}

// MaxIdleConnections устанавливает максимальное число не используемых коннектов к БД.
func MaxIdleConnections(n int) OptionFn {
	return func(o *options) {
		o.maxIdleConnections = n
	}
}
