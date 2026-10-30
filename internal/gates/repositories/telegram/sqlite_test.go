package telegram

import (
	"context"
	"database/sql"
	"testing"
	"time"

	telegramDomain "bloomary-api/internal/domain/telegram"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestLinkAttemptIsConsumedOnceAndLinkReplacesOlderLink(t *testing.T) {
	// Verifies: BE-009
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.StartAttempt(ctx, telegramDomain.LinkAttempt{Nonce: "nonce-1", MemberID: "member-1", Number: 42, ExpiresAt: now.Add(time.Minute)}))

	peeked, err := repo.AttemptByNonce(ctx, "nonce-1", now)
	require.NoError(t, err)
	require.Equal(t, 42, peeked.Number)
	consumed, err := repo.ConsumeAttempt(ctx, "nonce-1", now)
	require.NoError(t, err)
	require.Equal(t, "member-1", consumed.MemberID)
	_, err = repo.ConsumeAttempt(ctx, "nonce-1", now)
	require.ErrorIs(t, err, telegramDomain.ErrLinkNotFound)

	require.NoError(t, repo.SaveLink(ctx, telegramDomain.Link{TelegramUserID: 100, ChatID: 100, MemberID: "member-1", Username: "old", LinkedAt: now}))
	require.NoError(t, repo.SaveLink(ctx, telegramDomain.Link{TelegramUserID: 200, ChatID: 200, MemberID: "member-1", Username: "new", LinkedAt: now}))
	_, err = repo.ByTelegramUser(ctx, 100)
	require.ErrorIs(t, err, telegramDomain.ErrNotLinked)
	link, err := repo.ByMemberID(ctx, "member-1")
	require.NoError(t, err)
	require.Equal(t, int64(200), link.TelegramUserID)
	require.Equal(t, "new", link.Username)
}

func TestMarkUpdateSeenDropsRepeatsAndForgetsOldIDs(t *testing.T) {
	// Verifies: BE-022
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UTC()

	fresh, err := repo.MarkUpdateSeen(ctx, 7, now)
	require.NoError(t, err)
	require.True(t, fresh)
	fresh, err = repo.MarkUpdateSeen(ctx, 7, now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, fresh)
	fresh, err = repo.MarkUpdateSeen(ctx, 7, now.Add(telegramDomain.UpdateMemory+2*time.Minute))
	require.NoError(t, err)
	require.True(t, fresh, "IDs older than UpdateMemory are forgotten")
}
