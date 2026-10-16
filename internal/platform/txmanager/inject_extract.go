package txmanager

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type txCtxKey uint8

const txKey txCtxKey = 1 << 7

func extractTx(ctx context.Context) (*sqlx.Tx, error) {
	tx, ok := ctx.Value(txKey).(*sqlx.Tx)
	if !ok {
		return nil, ErrTxNotFound
	}

	return tx, nil
}

func injectTx(ctx context.Context, tx *sqlx.Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}
