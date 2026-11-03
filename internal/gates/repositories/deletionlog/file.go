// Package deletionlog appends deleted member IDs to <db path>.deletion-log: one "<RFC3339 UTC> <member ID>" line per
// deleted account, file mode 0600, append-only.
package deletionlog

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	accountDomain "bloomary-api/internal/domain/account"
)

type File struct {
	path string
	mu   sync.Mutex
}

var _ accountDomain.DeletionLog = (*File)(nil)

func NewFile(path string) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("deletion log: path is required")
	}
	return &File{path: path}, nil
}

func (f *File) Append(_ context.Context, memberID string, at time.Time) error {
	if memberID == "" || strings.ContainsAny(memberID, " \n\r") {
		return fmt.Errorf("deletion log: invalid member ID")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open deletion log: %w", err)
	}
	if _, err := fmt.Fprintf(file, "%s %s\n", at.UTC().Format(time.RFC3339), memberID); err != nil {
		_ = file.Close()
		return fmt.Errorf("write deletion log: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync deletion log: %w", err)
	}
	return file.Close()
}

func (f *File) Since(_ context.Context, since time.Time) ([]accountDomain.DeletedMember, error) {
	file, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open deletion log: %w", err)
	}
	defer file.Close()
	var deleted []accountDomain.DeletedMember
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		at, memberID, ok := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if !ok {
			continue
		}
		when, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, fmt.Errorf("deletion log line %d: bad time", line)
		}
		if when.After(since) {
			deleted = append(deleted, accountDomain.DeletedMember{At: when, MemberID: memberID})
		}
	}
	return deleted, scanner.Err()
}
