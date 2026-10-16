package stories

import (
	"context"
	"errors"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	draftMocks "bloomary-api/internal/mocks/domain/draft"
	perror "bloomary-api/internal/platform/error"

	"github.com/stretchr/testify/mock"
)

const validChecksum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDraftImagesRejectsTooLargeUpload(t *testing.T) {
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().MaxUploadBytes().Return(int64(10))

	story, err := NewDraftImages(store)
	if err != nil {
		t.Fatal(err)
	}

	input := validPresignInput()
	input.SizeBytes = 11
	_, err = story.PresignUpload(context.Background(), input)
	if !perror.Is(err, draftDomain.ErrCodeImageTooLarge) {
		t.Fatalf("expected image too large error, got %v", err)
	}
}

func TestDraftImagesPresignsPaidUploadWithMetadataHeaders(t *testing.T) {
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().MaxUploadBytes().Return(int64(100))
	store.EXPECT().
		StagingObjectKey("member-1", "draft-1", mock.AnythingOfType("string")).
		RunAndReturn(func(memberID, draftID, imageID string) string {
			return "draft-images/test/staging/" + memberID + "/" + draftID + "/" + imageID
		})
	var presignInput draftDomain.PresignPutInput
	expiresAt := time.Now().Add(time.Minute)
	store.EXPECT().
		PresignPut(mock.Anything, mock.MatchedBy(func(input draftDomain.PresignPutInput) bool {
			presignInput = input
			return input.ObjectKey != "" &&
				input.Headers["Content-Type"] == "image/png" &&
				input.Headers["x-amz-meta-member-id"] == "member-1" &&
				input.Headers["x-amz-meta-draft-id"] == "draft-1" &&
				input.Headers["x-amz-meta-image-id"] != "" &&
				input.Headers["x-amz-meta-checksum-sha256"] == validChecksum &&
				input.Headers["x-amz-meta-size-bytes"] == "42"
		})).
		Return("https://r2.example/upload", expiresAt, nil)

	story, err := NewDraftImages(store)
	if err != nil {
		t.Fatal(err)
	}

	out, err := story.PresignUpload(context.Background(), validPresignInput())
	if err != nil {
		t.Fatal(err)
	}
	if out.ImageID == "" || out.ObjectKey == "" || out.UploadURL == "" {
		t.Fatalf("expected image id, object key, and upload url, got %#v", out)
	}
	if out.Headers["x-amz-meta-member-id"] != "member-1" ||
		out.Headers["x-amz-meta-draft-id"] != "draft-1" ||
		out.Headers["x-amz-meta-image-id"] != out.ImageID ||
		out.Headers["x-amz-meta-checksum-sha256"] != validChecksum ||
		out.Headers["x-amz-meta-size-bytes"] != "42" ||
		out.Headers["Content-Type"] != "image/png" {
		t.Fatalf("unexpected headers: %#v", out.Headers)
	}
	if presignInput.ObjectKey != out.ObjectKey {
		t.Fatalf("expected presign object key %q, got %q", out.ObjectKey, presignInput.ObjectKey)
	}
}

func TestDraftImagesPresignsPaidDownloadForExistingLinkedImage(t *testing.T) {
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().
		LinkedObjectKey("member-1", "draft-1", "image-1").
		Return("draft-images/test/linked/member-1/draft-1/image-1")
	store.EXPECT().
		Stat(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return(draftDomain.ObjectMetadata{ObjectKey: "draft-images/test/linked/member-1/draft-1/image-1"}, nil)
	expiresAt := time.Now().Add(time.Minute)
	store.EXPECT().
		PresignGet(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return("https://r2.example/download", expiresAt, nil)

	story, err := NewDraftImages(store)
	if err != nil {
		t.Fatal(err)
	}

	out, err := story.PresignDownload(
		context.Background(),
		draftDomain.PresignDownloadInput{
			MemberID: "member-1",
			DraftID:  "draft-1",
			ImageID:  "image-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.ImageID != "image-1" || out.DownloadURL != "https://r2.example/download" || !out.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected presign download: %#v", out)
	}
}

func TestDraftImagesPresignDownloadRequiresExistingLinkedImage(t *testing.T) {
	store := draftMocks.NewMockObjectStore(t)
	store.EXPECT().
		LinkedObjectKey("member-1", "draft-1", "image-1").
		Return("draft-images/test/linked/member-1/draft-1/image-1")
	store.EXPECT().
		Stat(mock.Anything, "draft-images/test/linked/member-1/draft-1/image-1").
		Return(draftDomain.ObjectMetadata{}, errors.New("missing"))

	story, err := NewDraftImages(store)
	if err != nil {
		t.Fatal(err)
	}

	_, err = story.PresignDownload(
		context.Background(),
		draftDomain.PresignDownloadInput{
			MemberID: "member-1",
			DraftID:  "draft-1",
			ImageID:  "image-1",
		},
	)
	if !perror.Is(err, draftDomain.ErrCodeImageNotFound) {
		t.Fatalf("expected image not found error, got %v", err)
	}
}

func validPresignInput() draftDomain.PresignUploadInput {
	return draftDomain.PresignUploadInput{
		MemberID:       "member-1",
		DraftID:        "draft-1",
		MimeType:       "image/png",
		SizeBytes:      42,
		ChecksumSHA256: validChecksum,
	}
}
