package indexer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexedInputFingerprintCoversIndexedSemanticsOnly(t *testing.T) {
	base := Config{
		ConfigPath:       filepath.Join(t.TempDir(), "ck3-index.toml"),
		Database:         "cache/index.sqlite",
		BaseDatabase:     "cache/base.sqlite",
		EngineLogs:       "logs",
		GISEnabled:       true,
		GISAnalysis:      "terrain",
		GISSidecarSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sources: []Source{{
			Name: "project", Path: "project", Rank: 1, Role: SourceRoleProject,
		}},
	}
	mutations := map[string]func(Config) Config{
		"source path": func(cfg Config) Config {
			cfg.Sources = append([]Source(nil), cfg.Sources...)
			cfg.Sources[0].Path = "another-project"
			return cfg
		},
		"source rank": func(cfg Config) Config {
			cfg.Sources = append([]Source(nil), cfg.Sources...)
			cfg.Sources[0].Rank = 2
			return cfg
		},
		"source role": func(cfg Config) Config {
			cfg.Sources = append([]Source(nil), cfg.Sources...)
			cfg.Sources[0].Role = SourceRoleDependency
			return cfg
		},
		"source privacy": func(cfg Config) Config {
			cfg.Sources = append([]Source(nil), cfg.Sources...)
			cfg.Sources[0].Private = true
			return cfg
		},
		"resource-only policy": func(cfg Config) Config {
			cfg.Sources = append([]Source(nil), cfg.Sources...)
			cfg.Sources[0].ResourceOnly = true
			return cfg
		},
		"GIS mode": func(cfg Config) Config { cfg.GISAnalysis = "full"; return cfg },
		"GIS implementation": func(cfg Config) Config {
			cfg.GISSidecarSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return cfg
		},
	}
	want := IndexedInputFingerprint(base)
	for name, mutate := range mutations {
		if got := IndexedInputFingerprint(mutate(base)); got == want {
			t.Errorf("changing %s did not change the indexed input fingerprint", name)
		}
	}

	outputsOnly := base
	outputsOnly.Database = "cache/another.sqlite"
	outputsOnly.BaseDatabase = "cache/another-base.sqlite"
	outputsOnly.EngineLogs = "other-logs"
	if got := IndexedInputFingerprint(outputsOnly); got != want {
		t.Fatal("output paths or separately fingerprinted engine logs changed the indexed input identity")
	}
}

func TestScanFilesRejectsAProjectRootChangedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const rel = "common/decisions/root_identity.txt"
	write := func(root, text string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	projectA := filepath.Join(dir, "project-a")
	projectB := filepath.Join(dir, "project-b")
	write(projectA, `root_a_decision = { is_shown = { always = yes } }`)
	write(projectB, `root_b_decision = { is_shown = { always = yes } }`)
	cfgA := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: projectA, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(ctx, cfgA); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.IndexState(ctx)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cfgB := cfgA
	cfgB.Sources = []Source{{Name: "project", Path: projectB, Rank: 1, Role: SourceRoleProject}}
	reader, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	status, err := reader.RefreshStatus(ctx, cfgB)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !status.NeedsFullScan || status.Status != "full_scan_required" {
		t.Fatalf("changed project root still reported incremental-ready: %+v", status)
	}

	_, err = ScanFiles(ctx, cfgB, []string{rel})
	var fullRequired *FullScanRequiredError
	if !errors.As(err, &fullRequired) || fullRequired.Reason != "the configured index input roots changed" {
		t.Fatalf("changed project root returned %v, want FullScanRequiredError", err)
	}

	reader, err = OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	after, err := reader.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !samePublishedIndexState(before, after) {
		t.Fatalf("refused refresh changed the published generation: before=%+v after=%+v", before, after)
	}
	var oldRows, newRows int
	if err := reader.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE name='root_a_decision'`).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if err := reader.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE name='root_b_decision'`).Scan(&newRows); err != nil {
		t.Fatal(err)
	}
	if oldRows == 0 || newRows != 0 {
		t.Fatalf("refused refresh mixed source roots: old=%d new=%d", oldRows, newRows)
	}
}
