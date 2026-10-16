package draft

import (
	"context"
	"io"
	"time"
)

//go:generate go run github.com/vektra/mockery/v2 --config ../../../.mockery.yaml

type Repository interface {
	Sync(ctx context.Context, memberID string, upserts []Upsert, deletes []string, maxActive int) ([]Draft, error)
	ByID(ctx context.Context, memberID, draftID string) (Draft, error)
	ListByMemberID(ctx context.Context, memberID string) ([]Draft, error)
	CreateIfAbsent(ctx context.Context, memberID, draftID, body string, maxActive int, updatedAt time.Time) (Draft, bool, error)
	UpdateUnpublished(ctx context.Context, memberID, draftID, body string, updatedAt time.Time) (Draft, error)
	MarkPublished(ctx context.Context, memberID, draftID string) error
	DeleteByMemberID(ctx context.Context, memberID string) error
}

type SyncStory interface {
	Sync(ctx context.Context, memberID string, upserts []Upsert, deletes []string, images ObjectStore) ([]Draft, error)
}

// ExportStory writes the member's whole draft archive as a zip to w.
type ExportStory interface {
	Export(ctx context.Context, memberID string, w io.Writer) error
}

type ListMCPDraftsStory interface {
	List(ctx context.Context, memberID string) ([]Draft, error)
}

type GetMCPDraftStory interface {
	Get(ctx context.Context, memberID, draftID string) (Draft, error)
}

type CreateMCPDraftStory interface {
	Create(ctx context.Context, memberID, draftID, body string) (Draft, error)
}

type UpdateMCPDraftStory interface {
	Update(ctx context.Context, memberID, draftID, body string) (Draft, error)
}

type ObjectStore interface {
	StagingObjectKey(memberID, draftID, imageID string) string
	LinkedObjectKey(memberID, draftID, imageID string) string
	Stat(ctx context.Context, objectKey string) (ObjectMetadata, error)
	Copy(ctx context.Context, sourceKey, destinationKey string) error
	Delete(ctx context.Context, objectKey string) error
	Read(ctx context.Context, objectKey string) ([]byte, error)
	PresignPut(ctx context.Context, input PresignPutInput) (string, time.Time, error)
	PresignGet(ctx context.Context, objectKey string) (string, time.Time, error)
	MaxUploadBytes() int64
}

type ImageStory interface {
	PresignUpload(ctx context.Context, input PresignUploadInput) (PresignUpload, error)
	PresignDownload(ctx context.Context, input PresignDownloadInput) (PresignDownload, error)
}
