package stories

import (
	"context"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"
	perror "bloomary-api/internal/platform/error"

	"github.com/stretchr/testify/mock"
)

func TestSyncDraftsAllowsPaidEntitlement(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit, PostCount: 1}, nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.ProDraftLimit).
		Return([]draftDomain.Draft{{DraftID: "draft-1", Body: "body"}}, nil)
	story, err := NewSyncDrafts(
		access,
		repo,
	)
	if err != nil {
		t.Fatal(err)
	}

	drafts, err := story.Sync(context.Background(), "member-1", []draftDomain.Upsert{{DraftID: "draft-1", Body: "body", UpdatedAt: time.Now()}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].DraftID != "draft-1" {
		t.Fatalf("expected synced draft, got %#v", drafts)
	}
}

// The repo gets no Sync expectation here: an edit of a lapsed member's existing
// draft must be refused before it reaches storage.
func TestSyncDraftsRejectsLapsedUpsert(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierLapsed, DraftLimit: entitlementDomain.FreeDraftLimit, PostCount: 1}, nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().ListByMemberID(mock.Anything, "member-1").
		Return([]draftDomain.Draft{{DraftID: "draft-1", Body: "body"}}, nil)
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Sync(
		context.Background(), "member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "body", UpdatedAt: time.Now()}}, nil, nil,
	)
	if !perror.Is(err, draftDomain.ErrCodeProRequired) {
		t.Fatalf("expected pro_required, got %v", err)
	}
}

func TestSyncDraftsRejectsLapsedDelete(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierLapsed, DraftLimit: entitlementDomain.FreeDraftLimit, PostCount: 1}, nil)
	story, err := NewSyncDrafts(access, draftMocks.NewMockRepository(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Sync(context.Background(), "member-1", nil, []string{"draft-1"}, nil)
	if !perror.Is(err, draftDomain.ErrCodeProRequired) {
		t.Fatalf("expected pro_required, got %v", err)
	}
}

// Pull stays open for a lapsed member and returns the whole archive, not just
// the free limit, so a reinstall or a second device still shows every draft.
func TestSyncDraftsAllowsLapsedRead(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierLapsed, DraftLimit: entitlementDomain.FreeDraftLimit, PostCount: 1}, nil)
	archive := make([]draftDomain.Draft, 7)
	for index := range archive {
		archive[index] = draftDomain.Draft{DraftID: "draft-" + string(rune('a'+index)), Body: "body"}
	}
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().Sync(mock.Anything, "member-1", []draftDomain.Upsert(nil), []string(nil), entitlementDomain.FreeDraftLimit).
		Return(archive, nil)
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	drafts, err := story.Sync(context.Background(), "member-1", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != len(archive) {
		t.Fatalf("expected whole archive of %d drafts, got %d", len(archive), len(drafts))
	}
}

// A lapsed member may still save a brand-new draft; the repo enforces the
// free limit on the total.
func TestSyncDraftsAllowsLapsedNewDraft(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierLapsed, DraftLimit: entitlementDomain.FreeDraftLimit, PostCount: 1}, nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().ListByMemberID(mock.Anything, "member-1").
		Return([]draftDomain.Draft{{DraftID: "draft-1", Body: "old"}}, nil)
	repo.EXPECT().Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.FreeDraftLimit).
		Return([]draftDomain.Draft{{DraftID: "draft-2", Body: "new"}, {DraftID: "draft-1", Body: "old"}}, nil)
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	drafts, err := story.Sync(
		context.Background(), "member-1",
		[]draftDomain.Upsert{{DraftID: "draft-2", Body: "new", UpdatedAt: time.Now()}}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 || drafts[0].DraftID != "draft-2" {
		t.Fatalf("expected new draft saved, got %#v", drafts)
	}
}

func TestSyncDraftsPreparesImageMetadata(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit, PostCount: 1}, nil)
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().MaxUploadBytes().Return(int64(100))
	store.EXPECT().
		StagingObjectKey("member-1", "draft-1", "image-1").
		Return("draft-images/test/staging/member-1/draft-1/image-1")
	store.EXPECT().
		Stat(mock.Anything, "draft-images/test/staging/member-1/draft-1/image-1").
		Return(draftDomain.ObjectMetadata{
			ContentType: "image/png",
			SizeBytes:   42,
			Metadata: map[string]string{
				"member-id":       "member-1",
				"draft-id":        "draft-1",
				"image-id":        "image-1",
				"checksum-sha256": validChecksum,
				"size-bytes":      "42",
			},
		}, nil)
	store.EXPECT().
		LinkedObjectKey("member-1", "draft-1", "image-1").
		Return("draft-images/test/linked/member-1/draft-1/image-1")
	store.EXPECT().
		Copy(mock.Anything, "draft-images/test/staging/member-1/draft-1/image-1", "draft-images/test/linked/member-1/draft-1/image-1").
		Return(nil)
	store.EXPECT().
		PresignGet(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return("https://r2.example/download", time.Now().Add(time.Minute), nil)
	store.EXPECT().
		Delete(mock.Anything, "draft-images/test/staging/member-1/draft-1/image-1").
		Return(nil)
	repo := draftMocks.NewMockRepository(t)
	var syncedUpserts []draftDomain.Upsert
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.ProDraftLimit).
		Run(func(_ context.Context, _ string, upserts []draftDomain.Upsert, _ []string, _ int) {
			syncedUpserts = upserts
		}).
		Return(
			[]draftDomain.Draft{
				{
					DraftID: "draft-1",
					Body:    "body",
					Image: &draftDomain.DraftImage{
						ImageID:        "image-1",
						ObjectKey:      "draft-images/test/linked/member-1/draft-1/image-1",
						MimeType:       "image/png",
						SizeBytes:      42,
						ChecksumSHA256: validChecksum,
					},
				},
			},
			nil,
		)
	story, err := NewSyncDrafts(
		access,
		repo,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{
			{
				DraftID:   "draft-1",
				Body:      "body",
				UpdatedAt: time.Now(),
				Image: &draftDomain.UpsertImage{
					ImageID:        "image-1",
					MimeType:       "image/png",
					SizeBytes:      42,
					ChecksumSHA256: validChecksum,
				},
			},
		},
		nil,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(syncedUpserts) != 1 || syncedUpserts[0].Image == nil {
		t.Fatalf("expected image upsert, got %#v", syncedUpserts)
	}
	if syncedUpserts[0].Image.ObjectKey != "draft-images/test/linked/member-1/draft-1/image-1" {
		t.Fatalf("unexpected object key %q", syncedUpserts[0].Image.ObjectKey)
	}
}

func TestSyncDraftsRejectsChecksumMismatch(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit, PostCount: 1}, nil)
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().MaxUploadBytes().Return(int64(100))
	store.EXPECT().
		StagingObjectKey("member-1", "draft-1", "image-1").
		Return("draft-images/test/staging/member-1/draft-1/image-1")
	store.EXPECT().
		Stat(mock.Anything, "draft-images/test/staging/member-1/draft-1/image-1").
		Return(draftDomain.ObjectMetadata{
			ContentType: "image/png",
			SizeBytes:   42,
			Metadata: map[string]string{
				"member-id":       "member-1",
				"draft-id":        "draft-1",
				"image-id":        "image-1",
				"checksum-sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"size-bytes":      "42",
			},
		}, nil)
	story, err := NewSyncDrafts(access, draftMocks.NewMockRepository(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Sync(
		context.Background(),
		"member-1",
		[]draftDomain.Upsert{
			{
				DraftID:   "draft-1",
				Body:      "body",
				UpdatedAt: time.Now(),
				Image: &draftDomain.UpsertImage{
					ImageID:        "image-1",
					MimeType:       "image/png",
					SizeBytes:      42,
					ChecksumSHA256: validChecksum,
				},
			},
		},
		nil,
		store,
	)
	if !perror.Is(err, draftDomain.ErrCodeImageChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestSyncDraftsDeletesLinkedImageBeforeDraftMetadata(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit, PostCount: 1}, nil)
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().
		Delete(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return(nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.ProDraftLimit).
		Return([]draftDomain.Draft{
			{
				DraftID: "draft-1",
				Image: &draftDomain.DraftImage{
					ObjectKey: "draft-images/test/linked/member-1/draft-1/image-1",
				},
			},
			{DraftID: "draft-2"},
		}, nil).
		Once()
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, []string{"draft-1"}, entitlementDomain.ProDraftLimit).
		Return([]draftDomain.Draft{{DraftID: "draft-2"}}, nil).
		Once()
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	drafts, err := story.Sync(context.Background(), "member-1", nil, []string{"draft-1"}, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].DraftID != "draft-2" {
		t.Fatalf("expected remaining draft, got %#v", drafts)
	}
}

func TestSyncDraftsDeletesRemovedImageObject(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierTrial, DraftLimit: entitlementDomain.FreeDraftLimit}, nil)
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().
		Delete(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return(nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.FreeDraftLimit).
		Return([]draftDomain.Draft{{
			DraftID: "draft-1",
			Image:   &draftDomain.DraftImage{ObjectKey: "draft-images/test/linked/member-1/draft-1/image-1"},
		}}, nil).
		Once()
	repo.EXPECT().
		Sync(mock.Anything, "member-1", mock.Anything, mock.Anything, entitlementDomain.FreeDraftLimit).
		Return([]draftDomain.Draft{{DraftID: "draft-1"}}, nil).
		Once()
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	drafts, err := story.Sync(
		context.Background(), "member-1",
		[]draftDomain.Upsert{{DraftID: "draft-1", Body: "body", RemoveImage: true}}, nil, store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Image != nil {
		t.Fatalf("expected draft without image, got %#v", drafts)
	}
}

// Clients sent the same UUID upper- and lowercase; the store must see one canonical ID.
func TestSyncDraftsLowercasesDraftIDs(t *testing.T) {
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).
		Return(entitlementDomain.Entitlement{IsPro: true, Tier: entitlementDomain.TierPro, DraftLimit: entitlementDomain.ProDraftLimit}, nil)
	repo := draftMocks.NewMockRepository(t)
	repo.EXPECT().Sync(mock.Anything, "member-1",
		mock.MatchedBy(func(upserts []draftDomain.Upsert) bool {
			return len(upserts) == 1 && upserts[0].DraftID == "460e20cb-2ef4-47d9-8a22-232818876297"
		}),
		[]string{"b1061f5e-1c50-4df9-ad4d-f3cd496a5643"},
		entitlementDomain.ProDraftLimit,
	).Return(nil, nil)
	story, err := NewSyncDrafts(access, repo)
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.Sync(
		context.Background(), "member-1",
		[]draftDomain.Upsert{{DraftID: " 460E20CB-2EF4-47D9-8A22-232818876297 ", Body: "body", UpdatedAt: time.Now()}},
		[]string{"B1061F5E-1C50-4DF9-AD4D-F3CD496A5643"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
}
