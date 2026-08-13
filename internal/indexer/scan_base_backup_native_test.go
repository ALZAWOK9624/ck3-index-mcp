//go:build ck3_native && cgo && sqlite_fts5

package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The copy loop used to live entirely inside one cgo call, which meant a
// cancelled context could not stop it and a locked source retried forever.
// Cancel after the first successful native step so this exercises cleanup of
// a real, partially copied SQLite destination rather than only preflight.
func TestNativeOnlineBackupHonoursMidCopyCancellation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sqlite")
	writeBackupFixture(t, src, 100000)
	dst := filepath.Join(dir, "snapshot.sqlite")

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	err := onlineBackupDatabaseWithOptionsAndHooks(ctx, src, dst, DefaultSQLiteReadOptions(), nativeBackupTestHooks{
		afterStep: func(rc int) {
			if rc == nativeSQLiteOK {
				cancel()
			}
		},
	})
	cancel()
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

	sleeps := 0
	if err := onlineBackupDatabaseWithOptionsAndHooks(context.Background(), src, dst, DefaultSQLiteReadOptions(), nativeBackupTestHooks{
		sleep: func(ctx context.Context, duration time.Duration) error {
			sleeps++
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("backup produced an empty snapshot")
	}
	if sleeps != 0 {
		t.Fatalf("healthy backup slept %d times while sqlite reported progress", sleeps)
	}
}

func TestNativeBackupClosesHandlesBeforeCleanup(t *testing.T) {
	var order []string
	err := runNativeBackupLoop(context.Background(), nativeBackupOperations{
		step:      func() int { return 999 },
		finish:    func() (int, string) { return nativeSQLiteOK, "" },
		remaining: func() int { return 0 },
		pageCount: func() int { return 0 },
		close:     func() { order = append(order, "close") },
		cleanup:   func() { order = append(order, "cleanup") },
		sleep:     sleepWithContext,
		now:       time.Now,
	})
	if err == nil {
		t.Fatal("invalid backup step unexpectedly succeeded")
	}
	if got := fmt.Sprint(order); got != "[close cleanup]" {
		t.Fatalf("error cleanup order = %s, want handles closed before deletion", got)
	}
}

func TestNativeBackupBusyCancellationClosesBeforeCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	err := runNativeBackupLoop(ctx, nativeBackupOperations{
		step:      func() int { return nativeSQLiteBusy },
		finish:    func() (int, string) { return nativeSQLiteOK, "" },
		remaining: func() int { return 1 },
		pageCount: func() int { return 1 },
		close:     func() { order = append(order, "close") },
		cleanup:   func() { order = append(order, "cleanup") },
		sleep: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
		now: time.Now,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("busy cancellation returned %v", err)
	}
	if got := fmt.Sprint(order); got != "[close cleanup]" {
		t.Fatalf("busy cleanup order = %s, want handles closed before deletion", got)
	}
}

func TestNativeBackupNoProgressWatchdog(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	err := runNativeBackupLoop(context.Background(), nativeBackupOperations{
		step:      func() int { return nativeSQLiteOK },
		finish:    func() (int, string) { return nativeSQLiteOK, "" },
		remaining: func() int { return 10 },
		pageCount: func() int { return 10 },
		close:     func() {},
		cleanup:   func() {},
		sleep:     sleepWithContext,
		now: func() time.Time {
			calls++
			if calls > 2 {
				return now.Add(backupMaxNoProgress + time.Second)
			}
			return now
		},
	})
	if err == nil || !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("stalled SQLITE_OK loop returned %v", err)
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
