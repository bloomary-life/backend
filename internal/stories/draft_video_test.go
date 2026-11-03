package stories

import (
	"testing"

	draftDomain "bloomary-api/internal/domain/draft"

	"github.com/stretchr/testify/require"
)

func TestDraftVideoIsStoredReadAndDeletedWithItsDraft(t *testing.T) {
	// Verifies: BE-064
	f := newCrosspostFixture(t)
	f.draft(t, "draft-1", "Hello")
	for _, key := range []string{"master-1", "captions-1", "cover-1", "master-2"} {
		f.objects.objects[key] = []byte("synthetic")
	}
	video := draftDomain.Video{MasterKey: "master-1", SHA256: "sha-1", CaptionsKey: "captions-1", CoverKey: "cover-1", Bytes: 1000, Seconds: 30, Width: 1080, Height: 1920}
	replaced, err := f.drafts.SetVideo(f.ctx, xMember, "draft-1", video)
	require.NoError(t, err)
	require.Empty(t, replaced)
	extras, err := f.drafts.Extras(f.ctx, xMember, "draft-1")
	require.NoError(t, err)
	require.Equal(t, &video, extras.Video)
	video.MasterKey, video.CaptionsKey, video.CoverKey = "master-2", "", ""
	replaced, err = f.drafts.SetVideo(f.ctx, xMember, "draft-1", video)
	require.NoError(t, err)
	require.Equal(t, []string{"master-1", "captions-1", "cover-1"}, replaced)

	sync, err := NewSyncDrafts(f.access, f.drafts)
	require.NoError(t, err)
	_, err = sync.Sync(f.ctx, xMember, nil, []string{"draft-1"}, f.objects)
	require.NoError(t, err)
	require.NotContains(t, f.objects.objects, "master-2", "deleting a draft deletes its video from R2")
	keys, err := f.drafts.VideoKeys(f.ctx, xMember)
	require.NoError(t, err)
	require.Empty(t, keys)
}
