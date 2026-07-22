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

func TestScannerErrorsPreservePublishedIndexForFullAndIncrementalScans(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		rel        string
		valid      string
		overlong   string
		kindLabel  string
		entryQuery string
	}{
		{
			name:       "localization",
			rel:        "localization/english/test_l_english.yml",
			valid:      "l_english:\n kept_key:0 \"old\"\n",
			overlong:   "l_english:\n" + strings.Repeat("x", localizationScannerMaxToken+1) + "\n",
			kindLabel:  "localization",
			entryQuery: `SELECT COUNT(*) FROM localization WHERE key='kept_key' AND value='old'`,
		},
		{
			name:       "schema",
			rel:        "common/traits/test.info",
			valid:      "kept_field = value\n",
			overlong:   strings.Repeat("x", schemaScannerMaxToken+1) + "\n",
			kindLabel:  "schema",
			entryQuery: `SELECT COUNT(*) FROM schema_fields WHERE object_type='trait' AND field='kept_field'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			project := filepath.Join(dir, "project")
			writeScanRegressionFile(t, project, tt.rel, tt.valid)
			cfg := Config{
				ConfigPath: filepath.Join(dir, "ck3-index.toml"),
				Database:   "cache/test.sqlite",
				Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
			}
			if _, err := Scan(ctx, cfg); err != nil {
				t.Fatal(err)
			}

			dbPath := filepath.Join(dir, "cache", "test.sqlite")
			readPublishedState := func() (IndexState, string, int) {
				t.Helper()
				db, err := Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				state, err := db.IndexState(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var sha string
				if err := db.sql.QueryRowContext(ctx, `SELECT sha256 FROM files WHERE source_name='project' AND rel_path=?`, tt.rel).Scan(&sha); err != nil {
					t.Fatal(err)
				}
				var entries int
				if err := db.sql.QueryRowContext(ctx, tt.entryQuery).Scan(&entries); err != nil {
					t.Fatal(err)
				}
				return state, sha, entries
			}

			beforeState, beforeSHA, beforeEntries := readPublishedState()
			if !beforeState.Ready() || beforeEntries != 1 {
				t.Fatalf("initial published index = state=%+v entries=%d", beforeState, beforeEntries)
			}
			writeScanRegressionFile(t, project, tt.rel, tt.overlong)

			attempts := []struct {
				name string
				run  func() error
			}{
				{name: "full", run: func() error {
					_, err := Scan(ctx, cfg)
					return err
				}},
				{name: "files", run: func() error {
					_, err := ScanFiles(ctx, cfg, []string{tt.rel})
					return err
				}},
			}
			for _, attempt := range attempts {
				t.Run(attempt.name, func(t *testing.T) {
					err := attempt.run()
					if err == nil || !strings.Contains(err.Error(), "token too long") || !strings.Contains(err.Error(), "scan "+tt.kindLabel) {
						t.Fatalf("%s scanner error = %v, want propagated %s token-too-long error", attempt.name, err, tt.kindLabel)
					}
					afterState, afterSHA, afterEntries := readPublishedState()
					if afterState != beforeState {
						t.Fatalf("%s scan changed published generation: before=%+v after=%+v", attempt.name, beforeState, afterState)
					}
					if afterSHA != beforeSHA || afterEntries != beforeEntries {
						t.Fatalf("%s scan changed indexed records: sha %q -> %q, entries %d -> %d", attempt.name, beforeSHA, afterSHA, beforeEntries, afterEntries)
					}
					db, openErr := Open(dbPath)
					if openErr != nil {
						t.Fatal(openErr)
					}
					status, statusErr := db.RefreshStatus(ctx, cfg)
					closeErr := db.Close()
					if statusErr != nil {
						t.Fatal(statusErr)
					}
					if closeErr != nil {
						t.Fatal(closeErr)
					}
					if status.LastScanError == nil || status.LastScanError.Code != "INTERNAL_ERROR" || status.LastScanError.At == "" {
						t.Fatalf("%s scan failure was not recorded safely for refresh status: %+v", attempt.name, status.LastScanError)
					}
				})
			}
			writeScanRegressionFile(t, project, tt.rel, tt.valid)
			if _, err := ScanFiles(ctx, cfg, []string{tt.rel}); err != nil {
				t.Fatalf("successful incremental repair: %v", err)
			}
			db, err := Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			status, err := db.RefreshStatus(ctx, cfg)
			closeErr := db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if status.LastScanError != nil {
				t.Fatalf("successful refresh did not clear prior failure marker: %+v", status.LastScanError)
			}
		})
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
