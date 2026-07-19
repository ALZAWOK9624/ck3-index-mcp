package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFullScanReusesPublishedIndexWhenInputsAreUnchanged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`noop_fixture_decision = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.IndexState(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Noop {
		t.Fatalf("unchanged full scan did not reuse the published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["reuse_published_index"]; !ok {
		t.Fatalf("no-op scan did not report reuse timing: %+v", stats.TimingsMillis)
	}
	for _, key := range []string{"load_symbols", "resolve_refs", "validator", "map_context", "semantic_fts"} {
		if _, ok := stats.TimingsMillis[key]; ok {
			t.Fatalf("no-op scan unexpectedly ran %s: %+v", key, stats.TimingsMillis)
		}
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !samePublishedIndexState(before, after) || before.CommittedAt != after.CommittedAt {
		t.Fatalf("no-op scan advanced the published index: before=%+v after=%+v", before, after)
	}
}

func TestFullScanDoesNotReusePublishedIndexAfterContentChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`noop_old_decision = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`noop_new_decision = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatalf("changed full scan incorrectly reused the published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_scoped"]; !ok {
		t.Fatalf("changed full scan did not refresh changed semantic FTS rows: %+v", stats.TimingsMillis)
	}
}
