package packager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Windows DirEntry may cache FileInfo during ReadDir; Unix obtains it lazily.
// Use a lazy stat on every platform so the publication race is deterministic.
type lazyAuditDirEntry struct {
	os.DirEntry
	stat func() (os.FileInfo, error)
}

func (entry lazyAuditDirEntry) Info() (os.FileInfo, error) { return entry.stat() }

func TestCleanupStageDisappearsAfterEnumeration(t *testing.T) {
	root := t.TempDir()
	stage, err := os.MkdirTemp(root, artifactStagePrefix)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("stage entries: %v %v", entries, err)
	}
	published := filepath.Join(root, "published")
	if err := os.Rename(stage, published); err != nil {
		t.Fatal(err)
	}
	entry := lazyAuditDirEntry{DirEntry: entries[0], stat: func() (os.FileInfo, error) { return os.Lstat(stage) }}
	if err := cleanupExpiredStage(root, entry, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("normal concurrent publication rejected: %v", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("publication was touched: %v", err)
	}
	entry.stat = func() (os.FileInfo, error) { return nil, os.ErrPermission }
	if err := cleanupExpiredStage(root, entry, time.Now()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("real inspection error swallowed: %v", err)
	}
}
