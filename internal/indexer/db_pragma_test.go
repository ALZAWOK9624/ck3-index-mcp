package indexer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The read path spent a long time on SQLite's defaults because the DSN builder
// used url.Values.Set for each pragma, and Set replaces the key rather than
// appending to it. Only the last pragma survived, and nothing observed the
// result: the database opened, every query returned correct rows, and the cost
// was invisible. These tests read the settings back off a live connection so
// the same mistake cannot be made silently again.
func TestReadConnectionAppliesEveryTuningPragma(t *testing.T) {
	db := openPragmaProbeDatabase(t)
	for _, expectation := range []struct {
		pragma string
		want   int64
	}{
		// Negative cache_size is a KiB budget rather than a page count.
		{pragma: "cache_size", want: -65536},
		// 2 is SQLITE_TEMP_STORE_MEMORY. Anything less spills every ORDER BY
		// temporary b-tree to disk, and nearly every hot query builds one.
		{pragma: "temp_store", want: 2},
		{pragma: "mmap_size", want: 1073741824},
		{pragma: "busy_timeout", want: 5000},
	} {
		var got int64
		if err := db.sql.QueryRowContext(context.Background(), "PRAGMA "+expectation.pragma).Scan(&got); err != nil {
			t.Fatalf("read PRAGMA %s: %v", expectation.pragma, err)
		}
		if got != expectation.want {
			t.Errorf("PRAGMA %s = %d, want %d", expectation.pragma, got, expectation.want)
		}
	}
}

// TestReadConnectionPragmasApplyToEveryPooledConnection is the half that
// matters under load. Configuring one connection out of the pool would leave
// concurrent tool calls running with different settings, which is exactly the
// failure mode the DSN approach exists to avoid.
func TestReadConnectionPragmasApplyToEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	db := openPragmaProbeDatabase(t)
	// Hold every connection open at once so the pool is forced to create all of
	// them; releasing between checks would let one configured connection serve
	// every query and hide the defect.
	conns := make([]*sql.Conn, 0, maxReadConnections)
	for index := 0; index < maxReadConnections; index++ {
		conn, err := db.sql.Conn(ctx)
		if err != nil {
			t.Fatalf("open pooled connection %d: %v", index, err)
		}
		conns = append(conns, conn)
	}
	for _, held := range conns {
		defer held.Close()
	}
	for index, held := range conns {
		var tempStore int64
		if err := held.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
			t.Fatalf("connection %d: read PRAGMA temp_store: %v", index, err)
		}
		if tempStore != 2 {
			t.Fatalf("pooled connection %d has temp_store=%d, want 2: pragmas are not reaching every connection", index, tempStore)
		}
	}
}

func TestReadConnectionAcquisitionHonorsContextDeadline(t *testing.T) {
	db := openSingleConnectionProbeDatabase(t)
	held, err := db.sql.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelWait()
	started := time.Now()
	blocked, err := db.sql.Conn(waitCtx)
	if blocked != nil {
		blocked.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		held.Close()
		t.Fatalf("blocked connection acquisition error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		held.Close()
		t.Fatalf("blocked connection acquisition ignored its deadline for %s", elapsed)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}

	retryCtx, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	retry, err := db.sql.Conn(retryCtx)
	if err != nil {
		t.Fatalf("connection was not reusable after the timed-out waiter: %v", err)
	}
	if err := retry.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledSQLiteQueryIsInterruptedAndReleasesConnection(t *testing.T) {
	db := openSingleConnectionProbeDatabase(t)
	queryCtx, cancelQuery := context.WithCancel(context.Background())
	queryDone := make(chan error, 1)
	go func() {
		var sum int64
		queryDone <- db.sql.QueryRowContext(queryCtx, `
WITH RECURSIVE count_to_a_billion(value) AS (
    VALUES(1)
    UNION ALL
    SELECT value + 1 FROM count_to_a_billion WHERE value < 1000000000
)
SELECT sum(value) FROM count_to_a_billion`).Scan(&sum)
	}()

	deadline := time.Now().Add(time.Second)
	for db.sql.Stats().InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.sql.Stats().InUse != 1 {
		cancelQuery()
		t.Fatal("long-running SQLite query never acquired the read connection")
	}
	cancelQuery()
	select {
	case err := <-queryDone:
		if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "interrupt")) {
			t.Fatalf("cancelled SQLite query error=%v, want cancellation or interrupt", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SQLite query kept running after its context was cancelled")
	}
	if stats := db.sql.Stats(); stats.InUse != 0 {
		t.Fatalf("cancelled SQLite query retained %d pooled connection(s)", stats.InUse)
	}

	checkCtx, cancelCheck := context.WithTimeout(context.Background(), time.Second)
	defer cancelCheck()
	var one int
	if err := db.sql.QueryRowContext(checkCtx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("connection was not reusable after SQLite interrupt: value=%d error=%v", one, err)
	}
}

func openSingleConnectionProbeDatabase(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cancel.sqlite")
	writer, err := OpenWithOptions(path, SQLiteReadOptions{Connections: 1, CacheMBPerConnection: 8, MMapLimitMB: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.EnsureSchema(context.Background()); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnlyWithOptions(path, SQLiteReadOptions{Connections: 1, CacheMBPerConnection: 8, MMapLimitMB: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader
}

func openPragmaProbeDatabase(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "traits", "probe.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pragma_probe_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/probe.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
