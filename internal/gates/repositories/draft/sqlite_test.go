package draft

import (
	"context"
	"database/sql"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSyncMergesDraftsByUpdatedAt(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, repo.Init(context.Background()))

	older := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	_, err = repo.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "newer", UpdatedAt: newer}},
		nil,
		15,
	)
	require.NoError(t, err)

	drafts, err := repo.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "older", UpdatedAt: older}},
		nil,
		15,
	)
	require.NoError(t, err)
	require.Len(t, drafts, 1)
	require.Equal(t, "newer", drafts[0].Body)
	require.True(t, drafts[0].UpdatedAt.Equal(newer))

	drafts, err = repo.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "newer-still", UpdatedAt: newer.Add(time.Hour)}},
		nil,
		15,
	)
	require.NoError(t, err)
	require.Len(t, drafts, 1)
	require.Equal(t, "newer-still", drafts[0].Body)
}

func TestSyncStoresDraftImageMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, repo.Init(context.Background()))

	drafts, err := repo.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{
			{
				DraftID:   "draft-1",
				Body:      "body",
				UpdatedAt: time.Now(),
				Image: &draftDomain.UpsertImage{
					ImageID:        "image-1",
					ObjectKey:      "draft-images/test/staging/member-1/draft-1/image-1",
					MimeType:       "image/png",
					SizeBytes:      42,
					ChecksumSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
			},
		},
		nil,
		15,
	)
	require.NoError(t, err)
	require.Len(t, drafts, 1)
	require.NotNil(t, drafts[0].Image)
	require.Equal(t, "image-1", drafts[0].Image.ImageID)
	require.Equal(t, "image/png", drafts[0].Image.MimeType)
	require.Equal(t, int64(42), drafts[0].Image.SizeBytes)
}

func TestSyncRemovesDraftImage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	require.NoError(t, repo.Init(context.Background()))

	withImage := draftDomain.Upsert{
		DraftID:   "draft-1",
		Body:      "body",
		UpdatedAt: time.Now(),
		Image: &draftDomain.UpsertImage{
			ImageID:        "image-1",
			ObjectKey:      "draft-images/test/linked/member-1/draft-1/image-1",
			MimeType:       "image/png",
			SizeBytes:      42,
			ChecksumSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	_, err = repo.Sync(context.Background(), "member-1", []draftDomain.Upsert{withImage}, nil, 15)
	require.NoError(t, err)

	drafts, err := repo.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "body", UpdatedAt: time.Now(), RemoveImage: true}},
		nil,
		15,
	)
	require.NoError(t, err)
	require.Len(t, drafts, 1)
	require.Nil(t, drafts[0].Image)
}

func TestSetImageLinksImageAndReturnsReplacedObjectKey(t *testing.T) {
	// Verifies: BE-011
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	repo, err := NewSQLiteRepository(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, repo.Init(ctx))
	_, _, err = repo.CreateIfAbsent(ctx, "member-1", "draft-1", "Post", 50, time.Now())
	require.NoError(t, err)

	previous, err := repo.SetImage(ctx, "member-1", "draft-1", draftDomain.UpsertImage{ImageID: "img-1", ObjectKey: "key-1", MimeType: "image/png", SizeBytes: 8, ChecksumSHA256: "abc"})
	require.NoError(t, err)
	require.Empty(t, previous)
	previous, err = repo.SetImage(ctx, "member-1", "draft-1", draftDomain.UpsertImage{ImageID: "img-2", ObjectKey: "key-2", MimeType: "image/jpeg", SizeBytes: 9, ChecksumSHA256: "def"})
	require.NoError(t, err)
	require.Equal(t, "key-1", previous)

	draft, err := repo.ByID(ctx, "member-1", "draft-1")
	require.NoError(t, err)
	require.Equal(t, "img-2", draft.Image.ImageID)
	require.Equal(t, "key-2", draft.Image.ObjectKey)
}
