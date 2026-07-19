package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScanRegressionFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanReportsMissingConfiguredSourceWithoutDestroyingIndex(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	writeScanRegressionFile(t, project, "common/traits/test.txt", "kept_trait = {}\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	initial, err := Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Objects == 0 || unchanged.Objects != initial.Objects || unchanged.Files != initial.Files {
		t.Fatalf("incremental scan reported change counts instead of database totals: initial=%+v unchanged=%+v", initial, unchanged)
	}
	if err := os.Rename(project, filepath.Join(dir, "project-away")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), `scan source "project"`) {
		t.Fatalf("Scan error=%v, want an explicit missing-source error", err)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.QueryObject(context.Background(), "kept_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Definitions) != 1 {
		t.Fatalf("failed scan destroyed the last valid index: %+v", got)
	}
}

func TestScanPromotesPreviouslyOverriddenFileWhenWinnerIsRemoved(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	const rel = "common/traits/shared.txt"
	projectFile := filepath.Join(project, filepath.FromSlash(rel))
	writeScanRegressionFile(t, project, rel, "project_trait = {}\n")
	writeScanRegressionFile(t, game, rel, "game_trait = {}\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "game", Path: game, Rank: 2},
		},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectFile); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.QueryObject(context.Background(), "game_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Definitions) != 1 || got.Definitions[0].Source != "game" {
		t.Fatalf("lower source was not promoted after the winner disappeared: %+v", got)
	}
}
