package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestStagedFullPublishRejectsChangedBaseGeneration(t *testing.T) {
	ctx := context.Background()
	cfg, reader, sourcePath, dbPath := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("staged_conflict = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagePath, err := stagedFullScanPath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeStagedDatabase(stagePath)
	stageConfig := cfg
	stageConfig.Database = stagePath
	stageConfig.ForceClean = true
	if _, err := scanWithMode(ctx, stageConfig, true); err != nil {
		t.Fatal(err)
	}

	writer, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := writer.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bumpScanGeneration(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	err = publishStagedFullScan(ctx, cfg, stagePath, publicationBaseFromState(before))
	if !errors.Is(err, ErrConflictingGeneration) {
		t.Fatalf("publish error = %v, want ErrConflictingGeneration", err)
	}
	oldObject, err := reader.QueryObject(ctx, "staged_before")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldObject.Definitions) != 1 {
		t.Fatalf("conflicting publish replaced live snapshot: %+v", oldObject)
	}
	conflictingObject, err := reader.QueryObject(ctx, "staged_conflict")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflictingObject.Definitions) != 0 {
		t.Fatalf("conflicting staged object leaked into live snapshot: %+v", conflictingObject)
	}
}

func TestConcurrentFullRefreshesPublishDistinctGenerations(t *testing.T) {
	cfg, reader, sourcePath, _ := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("concurrent_full = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsCh := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := ScanFullStaged(context.Background(), cfg)
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := reader.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation+2 {
		t.Fatalf("generation after two serialized full refreshes = %d, want %d", after.Generation, before.Generation+2)
	}
}

func TestConcurrentFullAndFilesRefreshDoNotLoseEitherSourceUpdate(t *testing.T) {
	cfg, reader, fullPath, _ := stagedFullRefreshFixture(t)
	filesRel := "common/traits/concurrent_files.txt"
	filesPath := filepath.Join(cfg.Sources[0].Path, filepath.FromSlash(filesRel))
	if err := os.WriteFile(fullPath, []byte("concurrent_full_value = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filesPath, []byte("concurrent_files_value = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	go func() {
		<-start
		_, err := ScanFullStaged(context.Background(), cfg)
		errorsCh <- err
	}()
	go func() {
		<-start
		_, err := ScanFiles(context.Background(), cfg, []string{filesRel})
		errorsCh <- err
	}()
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-errorsCh; err != nil {
			t.Fatalf("concurrent full/files refresh %d failed: %v", index+1, err)
		}
	}

	for _, id := range []string{"concurrent_full_value", "concurrent_files_value"} {
		object, err := reader.QueryObject(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if len(object.Definitions) != 1 {
			t.Fatalf("concurrent full/files refresh lost %q: %+v", id, object)
		}
	}
	oldObject, err := reader.QueryObject(context.Background(), "staged_before")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldObject.Definitions) != 0 {
		t.Fatalf("concurrent full/files refresh retained replaced definition: %+v", oldObject)
	}
}

func TestPublicationLockIsSharedAndContextCancelable(t *testing.T) {
	_, _, _, dbPath := stagedFullRefreshFixture(t)
	first, err := acquirePublicationLock(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := acquirePublicationLock(ctx, dbPath); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second publication lock error = %v, want deadline exceeded", err)
	}
}
