package stories

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	draftDomain "bloomary-api/internal/domain/draft"
)

// ExportDrafts writes a member's whole server-side archive as a zip: one
// Markdown file per draft plus its image. It is open in every access state,
// because it is the member's copy of their own data (GDPR Art. 15(3) / 20).
type ExportDrafts struct {
	drafts  draftDomain.Repository
	objects draftDomain.ObjectStore
}

// NewExportDrafts builds the export story. objects may be nil when draft images
// are not configured; drafts are then exported without images.
func NewExportDrafts(drafts draftDomain.Repository, objects draftDomain.ObjectStore) (*ExportDrafts, error) {
	if drafts == nil {
		return nil, fmt.Errorf("export drafts: repository is required")
	}
	return &ExportDrafts{drafts: drafts, objects: objects}, nil
}

func (s *ExportDrafts) Export(ctx context.Context, memberID string, w io.Writer) error {
	drafts, err := s.drafts.ListByMemberID(ctx, memberID)
	if err != nil {
		return fmt.Errorf("list drafts for export: %w", err)
	}
	archive := zip.NewWriter(w)
	for _, draft := range drafts {
		name := exportFileName(draft.DraftID)
		imageName := ""
		if draft.Image != nil && s.objects != nil {
			data, err := s.objects.Read(ctx, draft.Image.ObjectKey)
			if err != nil {
				return fmt.Errorf("read draft image for export: %w", err)
			}
			imageName = name + imageExtension(draft.Image.MimeType)
			if err := writeZipFile(archive, imageName, data); err != nil {
				return err
			}
		}
		if err := writeZipFile(archive, name+".md", []byte(draftMarkdown(draft, imageName))); err != nil {
			return err
		}
	}
	return archive.Close()
}

func draftMarkdown(draft draftDomain.Draft, imageName string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "draftId: %q\n", draft.DraftID)
	fmt.Fprintf(&b, "status: %s\n", draft.Status)
	fmt.Fprintf(&b, "createdAt: %s\n", draft.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "updatedAt: %s\n", draft.UpdatedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString(draft.Body)
	b.WriteString("\n")
	if imageName != "" {
		fmt.Fprintf(&b, "\n![](%s)\n", imageName)
	}
	return b.String()
}

func writeZipFile(archive *zip.Writer, name string, data []byte) error {
	f, err := archive.Create(name)
	if err != nil {
		return fmt.Errorf("create %s in export: %w", name, err)
	}
	_, err = f.Write(data)
	return err
}

// exportFileName keeps client-chosen draft IDs from escaping the archive root.
func exportFileName(draftID string) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < ' ' {
			return '_'
		}
		return r
	}, draftID)
	name = strings.ReplaceAll(name, "..", "_")
	if name == "" {
		return "draft"
	}
	return name
}

func imageExtension(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	default:
		return ".img"
	}
}
