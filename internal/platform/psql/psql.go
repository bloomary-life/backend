package psql

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/jmoiron/sqlx"
)

func Connect(ctx context.Context, connString string, opts ...OptionFn) (*sqlx.DB, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	pgxConfig, err := pgx.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	o := newOptions(opts)
	if o.logger != nil {
		pgxConfig.Tracer = &tracelog.TraceLog{
			Logger:   o.logger,
			LogLevel: o.logLevel,
		}
	}

	sqlDB := stdlib.OpenDB(*pgxConfig)
	db := sqlx.NewDb(sqlDB, "pgx")

	if o.maxOpenConnections > 0 {
		db.SetMaxOpenConns(o.maxOpenConnections)
	}

	if o.maxIdleConnections > 0 {
		db.SetMaxIdleConns(o.maxIdleConnections)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}
