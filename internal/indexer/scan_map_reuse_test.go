package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFullScanReusesUnchangedMapContext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`map_reuse_decision = { is_shown = { always = yes } }`), 0644); err != nil {
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
	if err := os.WriteFile(path, []byte(`map_reuse_decision_changed = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatal("changed semantic input incorrectly took the no-op scan path")
	}
	if _, ok := stats.TimingsMillis["map_context_reused"]; !ok {
		t.Fatalf("unchanged full scan did not reuse map cache: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["map_context_rebuild"]; ok {
		t.Fatalf("unchanged full scan rebuilt map cache: %+v", stats.TimingsMillis)
	}
}

func TestFullScanRebuildsMapContextForUnindexedMapInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`map_direct_input_decision = { is_shown = { always = yes } }`), 0644); err != nil {
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
	before, err := db.metaValue(ctx, "map_input_fingerprint")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	adjacencyPath := filepath.Join(project, "map_data", "adjacencies.csv")
	if err := os.MkdirAll(filepath.Dir(adjacencyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adjacencyPath, []byte("From;To;Type;Through\n1;2;sea;0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatal("changed unindexed map input incorrectly took the no-op scan path")
	}
	if _, ok := stats.TimingsMillis["map_context_rebuild"]; !ok {
		t.Fatalf("unindexed map input change did not rebuild map cache: %+v", stats.TimingsMillis)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.metaValue(ctx, "map_input_fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("map input fingerprint did not change after adjacencies.csv changed")
	}
}

func TestFullScanRebuildsMapContextWhenMapFingerprintIsMissing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`map_fingerprint_fixture = { is_shown = { always = yes } }`), 0644); err != nil {
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
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DELETE FROM meta WHERE key='map_input_fingerprint'`); err != nil {
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
	if _, ok := stats.TimingsMillis["map_context_rebuild"]; !ok {
		t.Fatalf("missing map fingerprint did not rebuild map cache: %+v", stats.TimingsMillis)
	}
}
