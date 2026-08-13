package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

// A refresh where every file hashes the same publishes nothing. Bumping the
// generation anyway tells every client the opposite: the MCP read cache throws
// away entries that are still valid, and a caller watching for new snapshots
// sees one that does not exist.
func TestScanFilesNoOpKeepsTheGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rel := "common/decisions/zz_noop.txt"
	writeProjectFile(t, dir, rel, `zz_noop_decision = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	generation := func(t *testing.T) int64 {
		t.Helper()
		db, err := OpenReadOnly(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		state, err := db.IndexState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state.Generation
	}

	before := generation(t)
	stats, err := ScanFiles(ctx, cfg, []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChangedFiles != 0 {
		t.Fatalf("refreshing an unchanged file reported %d changed files", stats.ChangedFiles)
	}
	if !stats.Noop {
		t.Fatal("a refresh that changed nothing did not report itself as a no-op")
	}
	if stats.WALCheckpoint != nil {
		t.Fatal("a true no-op opened the write path and checkpointed the WAL")
	}
	if _, wrote := stats.TimingsMillis["sqlite_write"]; wrote {
		t.Fatalf("a true no-op reported sqlite write timing: %+v", stats.TimingsMillis)
	}
	if after := generation(t); after != before {
		t.Fatalf("no-op refresh moved the generation %d -> %d", before, after)
	}

	// A real edit must still publish.
	writeProjectFile(t, dir, rel, `zz_noop_decision = { is_shown = { always = no } }`)
	stats, err = ScanFiles(ctx, cfg, []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatal("a refresh that changed a file reported itself as a no-op")
	}
	if after := generation(t); after == before {
		t.Fatalf("an edited file left the generation at %d", after)
	}
}
