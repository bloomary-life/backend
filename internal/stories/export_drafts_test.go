package stories

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
	draftMocks "bloomary-api/internal/mocks/domain/draft"

	"github.com/stretchr/testify/mock"
)

func TestExportDraftsWritesMarkdownAndImages(t *testing.T) {
	updated := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	drafts := draftMocks.NewMockRepository(t)
	drafts.EXPECT().ListByMemberID(mock.Anything, "member-1").Return([]draftDomain.Draft{
		{DraftID: "draft-1", Body: "First post", Status: "draft", CreatedAt: updated, UpdatedAt: updated,
			Image: &draftDomain.DraftImage{ImageID: "image-1", ObjectKey: "key-1", MimeType: "image/png"}},
		{DraftID: "draft-2", Body: "Second post", Status: "published", CreatedAt: updated, UpdatedAt: updated},
	}, nil)
	objects := draftMocks.NewMockObjectStore(t)
	objects.EXPECT().Read(mock.Anything, "key-1").Return([]byte("png-bytes"), nil)

	story, err := NewExportDrafts(drafts, objects)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := story.Export(context.Background(), "member-1", &out); err != nil {
		t.Fatal(err)
	}

	archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range archive.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	if len(files) != 3 {
		t.Fatalf("expected 2 drafts + 1 image, got %v", len(files))
	}
	first := files["draft-1.md"]
	if !strings.Contains(first, "First post") || !strings.Contains(first, "![](draft-1.png)") {
		t.Fatalf("unexpected draft-1.md: %q", first)
	}
	if files["draft-1.png"] != "png-bytes" {
		t.Fatalf("unexpected image: %q", files["draft-1.png"])
	}
	if !strings.Contains(files["draft-2.md"], "status: published") {
		t.Fatalf("unexpected draft-2.md: %q", files["draft-2.md"])
	}
}
