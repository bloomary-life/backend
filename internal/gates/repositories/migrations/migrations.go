// Package migrations holds Goose schema migrations for tables added since the cross-posting work.
// Baseline = the schema the repositories' inline Init creates; Up runs after those, at start and in tests.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed sql/*.sql
var files embed.FS

// TableName is Goose's version table, with the project prefix.
const TableName = "bloomary_goose_db_version"

// Up applies every migration not applied yet.
func Up(ctx context.Context, db *sql.DB) error {
	migrations, err := fs.Sub(files, "sql")
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations,
		goose.WithTableName(TableName), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrations up: %w", err)
	}
	return nil
}
