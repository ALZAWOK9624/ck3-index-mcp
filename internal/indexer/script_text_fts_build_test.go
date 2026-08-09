package indexer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func scriptTextTriggersPresent(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.sql.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'files_script_text_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func scriptTextFTSRowCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(context.Background(), `SELECT count(*) FROM script_text_fts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func indexedScriptFileCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(context.Background(),
		`SELECT count(*) FROM files WHERE overridden=0 AND kind='script'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A clean bulk load must not maintain script_text_fts row by row: the finalizer
// drops and rebuilds that table from files in one statement, so every trigger
// firing during the load is work that is immediately discarded.
func TestCleanResetLeavesNoScriptTextTriggersForTheBulkLoad(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "cache", "reset.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if present := scriptTextTriggersPresent(t, db); len(present) != len(scriptTextTriggerNames) {
		t.Fatalf("ensureSchema did not install the maintenance triggers: %v", present)
	}
	if err := db.reset(ctx); err != nil {
		t.Fatal(err)
	}
	if present := scriptTextTriggersPresent(t, db); len(present) != 0 {
		t.Fatalf("reset left script-text triggers installed for the bulk load: %v", present)
	}
}

// The saving is only legitimate if the finished generation is indistinguishable
// from one built with the triggers on: complete FTS content, triggers back in
// place, and incremental maintenance working again.
func TestCleanScanRestoresTriggersAndCompleteScriptTextFTS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(game, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/traits/a.txt", "trait_alpha = { value = fts_probe_alpha }\n")
	write("common/traits/b.txt", "trait_beta = { value = fts_probe_beta }\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	present := scriptTextTriggersPresent(t, db)
	sort.Strings(present)
	want := append([]string(nil), scriptTextTriggerNames...)
	sort.Strings(want)
	if len(present) != len(want) {
		t.Fatalf("clean scan did not reinstate the maintenance triggers: %v", present)
	}
	for i := range want {
		if present[i] != want[i] {
			t.Fatalf("triggers after clean scan = %v, want %v", present, want)
		}
	}
	if got, expected := scriptTextFTSRowCount(t, db), indexedScriptFileCount(t, db); got != expected {
		t.Fatalf("script_text_fts holds %d rows for %d indexed script files", got, expected)
	}

	// The content has to be searchable, not merely present in the right count.
	var hits int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM script_text_fts WHERE script_text_fts MATCH 'fts_probe_alpha'`).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Fatal("script text from the clean scan is not searchable")
	}
}

// Once reinstated, the triggers still have to carry an incremental change, or
// the clean-path saving would have broken the path it was borrowed from.
func TestIncrementalScanStillMaintainsScriptTextFTS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "traits", "a.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("trait_alpha = { value = fts_probe_original }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("trait_alpha = { value = fts_probe_rewritten }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for probe, wantHit := range map[string]bool{"fts_probe_rewritten": true, "fts_probe_original": false} {
		var hits int
		if err := db.sql.QueryRowContext(ctx,
			`SELECT count(*) FROM script_text_fts WHERE script_text_fts MATCH ?`, probe).Scan(&hits); err != nil {
			t.Fatal(err)
		}
		if (hits > 0) != wantHit {
			t.Errorf("probe %q: %d hits, wantHit=%v", probe, hits, wantHit)
		}
	}
	if got, expected := scriptTextFTSRowCount(t, db), indexedScriptFileCount(t, db); got != expected {
		t.Fatalf("after incremental scan script_text_fts holds %d rows for %d script files", got, expected)
	}
}
