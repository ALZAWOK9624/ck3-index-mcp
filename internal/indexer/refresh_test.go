package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScanFilesReportsRemovalAndRequiresFullForHiddenFallback(t *testing.T) {
	ctx := context.Background()
	makeConfig := func(t *testing.T, withFallback bool) (Config, string, string) {
		t.Helper()
		dir := t.TempDir()
		project := filepath.Join(dir, "project")
		fallback := filepath.Join(dir, "fallback")
		rel := "common/traits/refresh_remove.txt"
		write := func(root, contents string) string {
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		projectPath := write(project, "refresh_remove_project = {}\n")
		sources := []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true}}
		if withFallback {
			write(fallback, "refresh_remove_fallback = {}\n")
			sources = append(sources, Source{Name: "base", Path: fallback, Rank: 2, Role: SourceRoleDependency})
		}
		cfg := Config{ConfigPath: filepath.Join(dir, "ck3-index.toml"), Database: "cache/test.sqlite", Sources: sources}
		if _, err := Scan(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		return cfg, projectPath, rel
	}

	t.Run("removal deletes an active project file", func(t *testing.T) {
		cfg, path, rel := makeConfig(t, false)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		stats, err := ScanFiles(ctx, cfg, []string{rel})
		if err != nil {
			t.Fatal(err)
		}
		if stats.ChangedFiles != 1 || stats.RemovedFiles != 1 {
			t.Fatalf("removal stats = %+v, want one changed and removed file", stats)
		}
		db, err := Open(filepath.Join(filepath.Dir(cfg.ConfigPath), "cache", "test.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		object, err := db.QueryObject(ctx, "refresh_remove_project")
		if err != nil {
			t.Fatal(err)
		}
		if len(object.Definitions) != 0 {
			t.Fatalf("removed file still has active definition: %+v", object.Definitions)
		}
	})

	t.Run("removal exposing a lower-precedence file requires full scan", func(t *testing.T) {
		cfg, path, rel := makeConfig(t, true)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		_, err := ScanFiles(ctx, cfg, []string{rel})
		var fullRequired *FullScanRequiredError
		if !errors.As(err, &fullRequired) {
			t.Fatalf("removal with fallback error = %v, want FullScanRequiredError", err)
		}
	})

	t.Run("never-indexed missing path is explicit no-op", func(t *testing.T) {
		cfg, _, _ := makeConfig(t, false)
		stats, err := ScanFiles(ctx, cfg, []string{"common/traits/not_indexed.txt"})
		if err != nil {
			t.Fatal(err)
		}
		if len(stats.MissingFiles) != 1 || len(stats.PathOutcomes) != 1 || stats.PathOutcomes[0].Status != "not_indexed" {
			t.Fatalf("missing refresh path was not explicit: %+v", stats)
		}
	})
}

func TestRefreshStatusDoesNotTreatUnpublishedIndexAsIncrementalReady(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	status, err := db.RefreshStatus(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "full_scan_required" || !status.NeedsFullScan || status.Index.Ready() {
		t.Fatalf("unpublished refresh status = %+v", status)
	}
}
