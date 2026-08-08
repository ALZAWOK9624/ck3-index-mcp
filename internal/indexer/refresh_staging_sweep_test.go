package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A full refresh that is killed rather than returning an error leaves its
// staging cache behind; nothing used to reclaim it. These tests pin the sweep
// and, just as importantly, the two things it must not touch.

func writeStagingFile(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestOrphanedStagingDatabasesAreReclaimed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ck3_index.sqlite")

	orphan := filepath.Join(dir, ".ck3_index.sqlite.staging-123456.sqlite")
	writeStagingFile(t, orphan, 4096, 3*time.Hour)
	writeStagingFile(t, orphan+"-wal", 512, 3*time.Hour)

	removeOrphanedStagedDatabases(dbPath)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned staging database was not removed: %v", err)
	}
	if _, err := os.Stat(orphan + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("orphaned staging WAL was not removed: %v", err)
	}
}

func TestRecentStagingDatabaseIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ck3_index.sqlite")

	// A stage a live refresh could still be filling.
	active := filepath.Join(dir, ".ck3_index.sqlite.staging-987654.sqlite")
	writeStagingFile(t, active, 4096, time.Minute)

	removeOrphanedStagedDatabases(dbPath)

	if _, err := os.Stat(active); err != nil {
		t.Fatalf("a recent staging database must not be swept: %v", err)
	}
}

func TestSweepLeavesLiveCacheAndUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ck3_index.sqlite")

	keep := []string{
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
		filepath.Join(dir, "base-vanilla.sqlite"),
		// Belongs to a different configured database, so it is not this
		// cache's orphan to reclaim.
		filepath.Join(dir, ".other_index.sqlite.staging-111.sqlite"),
	}
	for _, path := range keep {
		writeStagingFile(t, path, 1024, 5*time.Hour)
	}

	removeOrphanedStagedDatabases(dbPath)

	for _, path := range keep {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("sweep removed %s, which it must never touch: %v", filepath.Base(path), err)
		}
	}
}
