package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
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
