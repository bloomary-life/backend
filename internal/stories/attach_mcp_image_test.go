package stories

import (
	"context"
	"encoding/base64"
	"testing"

	draftDomain "bloomary-api/internal/domain/draft"
	entitlementDomain "bloomary-api/internal/domain/entitlement"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	entitlementMocks "bloomary-api/internal/mocks/domain/entitlement"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// pngBytes is a PNG signature followed by an IHDR chunk header, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestAttachMCPImageStoresSniffedPNGAndReplacesOldImage(t *testing.T) {
	// Verifies: BE-011
	access := entitlementMocks.NewMockAccessStory(t)
	access.EXPECT().Resolve(mock.Anything, "member-1", mock.Anything).Return(entitlementDomain.Entitlement{Tier: entitlementDomain.TierPro}, nil)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Status: "draft"}, nil).Once()
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().MaxUploadBytes().Return(5 << 20)
	objects.EXPECT().LinkedObjectKey("member-1", "draft-1", mock.Anything).Return("draft-images/production/linked/member-1/draft-1/new")
	objects.EXPECT().Put(mock.Anything, "draft-images/production/linked/member-1/draft-1/new", "image/png", pngBytes, mock.MatchedBy(func(meta map[string]string) bool {
		return meta["member-id"] == "member-1" && meta["draft-id"] == "draft-1" && meta["size-bytes"] == "16"
	})).Return(nil)
	drafts.EXPECT().SetImage(mock.Anything, "member-1", "draft-1", mock.MatchedBy(func(image draftDomain.UpsertImage) bool {
		return image.MimeType == "image/png" && image.SizeBytes == 16 && len(image.ChecksumSHA256) == 64
	})).Return("old-key", nil)
	objects.EXPECT().Delete(mock.Anything, "old-key").Return(nil)
	drafts.EXPECT().ByID(mock.Anything, "member-1", "draft-1").Return(draftDomain.Draft{DraftID: "draft-1", Status: "draft", Image: &draftDomain.DraftImage{ImageID: "new", MimeType: "image/png"}}, nil).Once()
	story, err := NewAttachMCPImage(access, drafts, objects, draftMocks.NewMockRemoteImageFetcher(t))
	require.NoError(t, err)

	draft, err := story.Attach(context.Background(), draftDomain.AttachImageInput{MemberID: "member-1", DraftID: "draft-1",
		DataBase64: base64.StdEncoding.EncodeToString(pngBytes), MimeType: "image/png"})

	require.NoError(t, err)
	require.Equal(t, "image/png", draft.Image.MimeType)
}
