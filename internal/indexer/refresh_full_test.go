package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func stagedFullRefreshFixture(t *testing.T) (Config, *DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	rel := "common/traits/staged_refresh.txt"
	path := filepath.Join(project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("staged_before = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return cfg, reader, path, dbPath
}

func TestScanFullStagedPublishesOnlyCompletedGeneration(t *testing.T) {
	ctx := context.Background()
	cfg, reader, sourcePath, dbPath := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Ready() {
		t.Fatalf("initial index state = %+v, want ready", before)
	}
	if err := os.WriteFile(sourcePath, []byte("staged_after = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanFullStaged(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Database != dbPath {
		t.Fatalf("published database = %q, want %q", stats.Database, dbPath)
	}
	after, err := reader.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready() || after.Generation != before.Generation+1 || after.Revision == before.Revision {
		t.Fatalf("staged publication state before=%+v after=%+v", before, after)
	}
	oldObject, err := reader.QueryObject(ctx, "staged_before")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldObject.Definitions) != 0 {
		t.Fatalf("old definition survived staged publication: %+v", oldObject.Definitions)
	}
	newObject, err := reader.QueryObject(ctx, "staged_after")
	if err != nil {
		t.Fatal(err)
	}
	if len(newObject.Definitions) != 1 {
		t.Fatalf("new definition not visible through existing reader: %+v", newObject)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), ".test.sqlite.staging-*.sqlite*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staged database was not cleaned up: %v", leftovers)
	}
}

func TestScanFullStagedCancellationRetainsPublishedGeneration(t *testing.T) {
	cfg, reader, _, _ := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanFullStaged(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled staged scan error = %v, want context.Canceled", err)
	}
	after, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !samePublishedIndexState(before, after) || !after.Ready() {
		t.Fatalf("cancellation changed published generation: before=%+v after=%+v", before, after)
	}
	object, err := reader.QueryObject(context.Background(), "staged_before")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.Definitions) != 1 {
		t.Fatalf("cancellation lost prior published definition: %+v", object)
	}
	status, err := reader.RefreshStatus(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastScanError == nil || status.LastScanError.Code != "OPERATION_CANCELLED" {
		t.Fatalf("cancellation failure was not propagated into refresh status: %+v", status)
	}
}

func TestScanFullStagedFailureRetainsPublishedGenerationAndRecordsStatus(t *testing.T) {
	cfg, reader, _, _ := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	broken := cfg
	broken.Sources = append([]Source(nil), cfg.Sources...)
	broken.Sources[0].Path = filepath.Join(filepath.Dir(cfg.ConfigPath), "missing-project-root")
	if _, err := ScanFullStaged(context.Background(), broken); err == nil {
		t.Fatal("staged full scan unexpectedly accepted a missing source root")
	}
	after, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !samePublishedIndexState(before, after) || !after.Ready() {
		t.Fatalf("failed staged scan changed published generation: before=%+v after=%+v", before, after)
	}
	status, err := reader.RefreshStatus(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastScanError == nil || status.LastScanError.Code != "INTERNAL_ERROR" {
		t.Fatalf("failed staged scan was not propagated into refresh status: %+v", status)
	}
}
