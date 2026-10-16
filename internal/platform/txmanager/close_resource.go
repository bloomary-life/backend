package txmanager

import (
	"context"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

func CloseRows(_ context.Context, rows *sqlx.Rows) {
	if err := rows.Close(); err != nil {
		slog.Error("close rows", "error", err)
	}
}
