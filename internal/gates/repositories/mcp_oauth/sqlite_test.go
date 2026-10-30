package mcpoauth

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mcpDomain "bloomary-api/internal/domain/mcp"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestDeleteByMemberIDRemovesGrantsAndSessions(t *testing.T) {
	// Verifies: BE-015
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStateStore(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.CreateGrant(ctx, mcpDomain.Grant{ID: "g-1", MemberID: "member-1", ClientID: "claude", Scopes: []string{"drafts:read"}, CreatedAt: time.Now()}))
	require.NoError(t, store.CreateGrant(ctx, mcpDomain.Grant{ID: "g-2", MemberID: "member-2", ClientID: "claude", Scopes: []string{"drafts:read"}, CreatedAt: time.Now()}))
	session, err := store.EstablishMemberSession(ctx, "member-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	require.NoError(t, store.DeleteByMemberID(ctx, "member-1"))

	active, err := store.GrantActive(ctx, "g-1")
	require.NoError(t, err)
	require.False(t, active)
	other, err := store.GrantActive(ctx, "g-2")
	require.NoError(t, err)
	require.True(t, other, "other members keep their grants")
	_, err = store.ResolveMember(ctx, session)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
