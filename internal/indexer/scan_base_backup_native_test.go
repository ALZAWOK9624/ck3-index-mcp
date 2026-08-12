//go:build ck3_native && cgo && sqlite_fts5

package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The copy loop used to live entirely inside one cgo call, which meant a
// cancelled context could not stop it and a locked source retried forever.
func TestNativeOnlineBackupHonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sqlite")
	writeBackupFixture(t, src, 20000)
	dst := filepath.Join(dir, "snapshot.sqlite")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := onlineBackupDatabase(ctx, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backup returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled backup took %s to return", elapsed)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled backup left a destination behind: %v", statErr)
	}
}

// A healthy step must not wait. The old loop slept 10ms after every 256 pages,
// which is a fixed tax of roughly ten seconds per gigabyte.
func TestNativeOnlineBackupDoesNotSleepOnProgress(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sqlite")
	// Comfortably more than one 256-page batch, so a per-batch sleep would
	// dominate the measurement.
	writeBackupFixture(t, src, 40000)
	dst := filepath.Join(dir, "snapshot.sqlite")

	start := time.Now()
	if err := onlineBackupDatabase(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("backup produced an empty snapshot")
	}
	// The fixture is several thousand pages. At 10ms per 256-page batch the
	// old loop needed well over a second for it; a loop that only waits on
	// contention finishes in a fraction of that.
	if elapsed > time.Second {
		t.Fatalf("backup of %d bytes took %s, which is the shape of a per-batch sleep", info.Size(), elapsed)
	}
}

func writeBackupFixture(t *testing.T, path string, rows int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE payload(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO payload(id, body) VALUES(?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := statement.Exec(i, fmt.Sprintf("row-%d-%s", i, "padding padding padding padding padding")); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
