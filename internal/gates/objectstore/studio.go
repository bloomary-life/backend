package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"time"

	videostudioDomain "bloomary-api/internal/domain/videostudio"
)

// studioUploadTTL: a producer upload URL lives 15 minutes (security.md §3: short TTL, content type fixed).
const studioUploadTTL = 15 * time.Minute

// StudioMedia is videostudio.MediaStore on R2: keys "video-studio/<env>/<member>/<request>/<name>".
type StudioMedia struct {
	store *R2ObjectStore
}

var _ videostudioDomain.MediaStore = (*StudioMedia)(nil)

func NewStudioMedia(store *R2ObjectStore) *StudioMedia { return &StudioMedia{store: store} }

func (m *StudioMedia) Key(memberID, requestID, name string) string {
	return strings.Join([]string{"video-studio", m.store.environment, pathPart(memberID), pathPart(requestID), pathPart(name)}, "/")
}

// UploadURL is a presigned PUT for exactly this key with this Content-Type.
func (m *StudioMedia) UploadURL(_ context.Context, key, contentType string) (string, time.Time, error) {
	return m.store.presignFor(httpMethodPut, key, map[string]string{"Content-Type": contentType}, studioUploadTTL)
}

func (m *StudioMedia) DownloadURL(ctx context.Context, key string) (string, time.Time, error) {
	return m.store.PresignGet(ctx, key)
}

func (m *StudioMedia) Size(ctx context.Context, key string) (int64, error) {
	meta, err := m.store.Stat(ctx, key)
	return meta.SizeBytes, err
}

// SHA256 streams the object through the hash; nothing is held in memory.
func (m *StudioMedia) SHA256(ctx context.Context, key string) (string, error) {
	body, _, _, err := m.store.Open(ctx, key)
	if err != nil {
		return "", err
	}
	defer body.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, body); err != nil {
		return "", stripURL(err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *StudioMedia) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	body, _, _, err := m.store.Open(ctx, key)
	return body, err
}

func (m *StudioMedia) Read(ctx context.Context, key string) ([]byte, error) {
	return m.store.Read(ctx, key)
}

func (m *StudioMedia) Put(ctx context.Context, key, contentType string, data []byte) error {
	return m.store.Put(ctx, key, contentType, data, nil)
}

func (m *StudioMedia) Copy(ctx context.Context, sourceKey, destinationKey string) error {
	return m.store.Copy(ctx, sourceKey, destinationKey)
}

func (m *StudioMedia) Delete(ctx context.Context, key string) error { return m.store.Delete(ctx, key) }
