package notification

import (
	domain "bloomary-api/internal/domain/notification"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Device registrations live beside the SQLite database on the persistent volume.
// Atomic replacement prevents partial files; one registration owns each APNs token.
type Files struct {
	dir string
	mu  sync.Mutex
}

func NewFiles(dir string) (*Files, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Files{dir: dir}, nil
}
func (f *Files) path(d domain.Device) string {
	sum := sha256.Sum256([]byte(d.Environment + ":" + d.Token))
	return filepath.Join(f.dir, hex.EncodeToString(sum[:])+".json")
}
func (f *Files) Save(ctx context.Context, d domain.Device, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.path(d)
	if !enabled {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var current domain.Device
		if err := json.Unmarshal(data, &current); err != nil {
			return err
		}
		if current.MemberID != d.MemberID {
			return nil
		}
		return os.Remove(path)
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".registration-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
func (f *Files) list(memberID string) ([]domain.Device, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	var devices []domain.Device
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var d domain.Device
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, fmt.Errorf("decode device registration: %w", err)
		}
		if d.MemberID == memberID {
			devices = append(devices, d)
		}
	}
	return devices, nil
}
func (f *Files) List(ctx context.Context, memberID string) ([]domain.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list(memberID)
}
func (f *Files) DeleteMember(ctx context.Context, memberID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	devices, err := f.list(memberID)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if err := os.Remove(f.path(d)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
