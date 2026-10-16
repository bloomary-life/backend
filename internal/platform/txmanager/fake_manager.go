package txmanager

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type fakeManager struct {
}

func FakeManager() {
	once.Do(
		func() {
			managerOnce = &fakeManager{}
		},
	)
}

func (m *fakeManager) Conn(_ context.Context) sqlx.ExtContext {
	return nil
}

func (m *fakeManager) Do(
	ctx context.Context, txFunc func(ctx context.Context) error, _ ...sql.TxOptions,
) error {
	return txFunc(ctx)
}
