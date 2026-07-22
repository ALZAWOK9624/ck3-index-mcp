package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanFilesRefreshesOnlyAffectedSemanticFTSRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/decisions/a.txt", `zzoldneedlealpha = { is_shown = { always = yes } }`)
	write("common/decisions/b.txt", `zzstableneedlebeta = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	write("common/decisions/a.txt", `zznewneedlealpha = { is_shown = { always = yes } }`)
	stats, err := ScanFiles(ctx, cfg, []string{"common/decisions/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts"]; !ok {
		t.Fatalf("semantic FTS timing missing: %+v", stats)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzoldneedlealpha") != 0 {
		t.Fatal("stale FTS term from the replaced file survived incremental scan")
	}
	if semanticFTSCount(t, db, "zznewneedlealpha") == 0 {
		t.Fatal("new FTS term from the changed file was not indexed")
	}
	if semanticFTSCount(t, db, "zzstableneedlebeta") == 0 {
		t.Fatal("unrelated file disappeared from semantic FTS during incremental scan")
	}
}

func TestFullScanRefreshesOnlyAffectedSemanticFTSRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/decisions/a.txt", `zzfulloldneedlealpha = { is_shown = { always = yes } }`)
	write("common/decisions/b.txt", `zzfullstableneedlebeta = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	write("common/decisions/a.txt", `zzfullnewneedlealpha = { is_shown = { always = yes } }`)
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_scoped"]; !ok {
		t.Fatalf("full scan did not use scoped semantic FTS refresh: %+v", stats.TimingsMillis)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; ok {
		t.Fatalf("full scan rebuilt semantic FTS despite unchanged engine rules: %+v", stats.TimingsMillis)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzfulloldneedlealpha") != 0 {
		t.Fatal("stale FTS term from the replaced full-scan file survived")
	}
	if semanticFTSCount(t, db, "zzfullnewneedlealpha") == 0 {
		t.Fatal("new FTS term from the changed full-scan file was not indexed")
	}
	if semanticFTSCount(t, db, "zzfullstableneedlebeta") == 0 {
		t.Fatal("unrelated file disappeared from semantic FTS during full scan")
	}
}

func TestFullScanUsesScopedSemanticFTSWithoutContentChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "project", "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`zzfullunchangedneedle = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Noop {
		t.Fatalf("unchanged full scan did not reuse the published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["semantic_fts"]; ok {
		t.Fatalf("unchanged full scan ran semantic FTS work: %+v", stats.TimingsMillis)
	}
}

func TestFullScanRefreshesChangedSemanticFTSRowsWithoutRebuildingTable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "project", "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`zzmarkeroldneedle = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id) VALUES('datatype','zzengineownedsentinel','zzengineownedsentinel','engine_logs','engine_logs',0)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := storeSearchFTSRowCount(ctx, tx); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`zzmarkernewneedle = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_scoped"]; !ok {
		t.Fatalf("changed full scan did not use scoped semantic FTS refresh: %+v", stats.TimingsMillis)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzengineownedsentinel") == 0 {
		t.Fatal("engine-owned FTS row disappeared, so full scan rebuilt the table instead of refreshing changed files")
	}
}

func TestFullScanRebuildsMissingSemanticFTS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/decisions/a.txt", `zzlostftsoldneedle = { is_shown = { always = yes } }`)
	write("common/decisions/b.txt", `zzlostftsstableneedle = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE search_fts`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	write("common/decisions/a.txt", `zzlostftsnewneedle = { is_shown = { always = yes } }`)
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop {
		t.Fatal("engine-rule drift incorrectly took the no-op scan path")
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; !ok {
		t.Fatalf("missing semantic FTS did not trigger a full rebuild: %+v", stats.TimingsMillis)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzlostftsnewneedle") == 0 {
		t.Fatal("changed file was not restored after semantic FTS loss")
	}
	if semanticFTSCount(t, db, "zzlostftsstableneedle") == 0 {
		t.Fatal("unchanged file was not restored after semantic FTS loss")
	}
}

func TestFullScanRebuildsClearedSemanticFTSBeforeNoop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/decisions/a.txt", `zzclearedftsneedle_a = { is_shown = { always = yes } }`)
	write("common/decisions/b.txt", `zzclearedftsneedle_b = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DELETE FROM search_fts`); err != nil {
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
	if stats.Noop {
		t.Fatalf("cleared semantic FTS incorrectly reused published index: %+v", stats)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; !ok {
		t.Fatalf("cleared semantic FTS did not trigger rebuild: %+v", stats.TimingsMillis)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzclearedftsneedle_a") == 0 || semanticFTSCount(t, db, "zzclearedftsneedle_b") == 0 {
		t.Fatal("cleared semantic FTS was not restored from the published semantic tables")
	}
}

func TestScanFilesRebuildsClearedSemanticFTSBeforePublishing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const changedRel = "common/decisions/changed.txt"
	write(changedRel, `zzscanfilesclearold = { is_shown = { always = yes } }`)
	write("common/decisions/stable.txt", `zzscanfilesclearstable = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
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
	if _, err := db.sql.Exec(`DELETE FROM search_fts`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	write(changedRel, `zzscanfilesclearnew = { is_shown = { always = yes } }`)
	stats, err := ScanFiles(ctx, cfg, []string{changedRel})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; !ok {
		t.Fatalf("scan --files published over cleared semantic FTS: %+v", stats.TimingsMillis)
	}
	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzscanfilesclearnew") == 0 || semanticFTSCount(t, db, "zzscanfilesclearstable") == 0 {
		t.Fatal("scan --files did not restore the full semantic FTS snapshot")
	}
}

func TestFullScanRefreshesSemanticFTSWhenOverrideWinnerChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	const rel = "common/decisions/shared.txt"
	projectFile := filepath.Join(project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(projectFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(game, "common", "decisions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`zzprojectwinnerneedle = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, filepath.FromSlash(rel)), []byte(`zzgamewinnerneedle = { is_shown = { always = yes } }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "game", Path: game, Rank: 2},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectFile); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_scoped"]; !ok {
		t.Fatalf("override transition did not use scoped semantic FTS refresh: %+v", stats.TimingsMillis)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzprojectwinnerneedle") != 0 {
		t.Fatal("removed override winner remained in semantic FTS")
	}
	if semanticFTSCount(t, db, "zzgamewinnerneedle") == 0 {
		t.Fatal("newly active override fallback was not added to semantic FTS")
	}
}

func TestFullScanRebuildsSemanticFTSWhenEngineRulesChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logs := makeEngineLogs(t, "on_fixture:\nExpected Scope: character\n")
	defer func() { _ = ConfigureEngineRules("") }()
	path := filepath.Join(dir, "project", "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture_decision = { is_shown = { always = yes } }"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		EngineLogs: logs,
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "on_actions.log"), []byte("on_fixture:\nExpected Scope: none\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; !ok {
		t.Fatalf("engine-rule drift did not rebuild semantic FTS: %+v", stats.TimingsMillis)
	}
}

func TestFullScanReparsesUnchangedScriptsWhenEngineRulesChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logs := makeEngineLogs(t, "")
	defer func() { _ = ConfigureEngineRules("") }()
	effectsPath := filepath.Join(logs, "effects.log")
	if err := os.WriteFile(effectsPath, []byte("add_trait:\nSupported Scopes: character\n"), 0644); err != nil {
		t.Fatal(err)
	}
	const rel = "events/fixture.txt"
	path := filepath.Join(dir, "project", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`engine_drift.1 = {
	scope = title
	immediate = { add_trait = diligent }
}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		EngineLogs: logs,
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	compilerDiagnosticCount := func(code string) int {
		t.Helper()
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM diagnostics d JOIN files f ON f.id=d.file_id
			WHERE d.source='compiler' AND d.code=? AND f.rel_path=?`, code, rel).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if compilerDiagnosticCount("scope_mismatch") == 0 {
		t.Fatal("fixture did not establish an engine-confirmed scope mismatch")
	}
	if err := os.WriteFile(effectsPath, []byte("add_trait:\nSupported Scopes: title\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_rebuild"]; !ok {
		t.Fatalf("engine-rule drift did not rebuild derived data: %+v", stats.TimingsMillis)
	}
	if compilerDiagnosticCount("scope_mismatch") != 0 || compilerDiagnosticCount("scope_uncertain") == 0 {
		t.Fatal("unchanged script retained diagnostics from the previous engine-rule snapshot")
	}
}

func TestFullScanBatchesScopedSemanticFTSRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < ftsRefreshBatchSize+8; i++ {
		write(fmt.Sprintf("common/decisions/batch_%03d.txt", i), fmt.Sprintf("zzbatchold%03d = { is_shown = { always = yes } }", i))
	}
	write("common/decisions/stable.txt", `zzbatchstable = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ftsRefreshBatchSize+8; i++ {
		write(fmt.Sprintf("common/decisions/batch_%03d.txt", i), fmt.Sprintf("zzbatchnew%03d = { is_shown = { always = yes } }", i))
	}
	stats, err := Scan(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts_scoped"]; !ok {
		t.Fatalf("large changed full scan did not use batched scoped FTS refresh: %+v", stats.TimingsMillis)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if semanticFTSCount(t, db, "zzbatchold519") != 0 {
		t.Fatal("old term survived batched scoped FTS refresh")
	}
	if semanticFTSCount(t, db, "zzbatchnew519") == 0 {
		t.Fatal("new term from the final FTS batch was not indexed")
	}
	if semanticFTSCount(t, db, "zzbatchstable") == 0 {
		t.Fatal("unrelated semantic FTS row disappeared during batched refresh")
	}
}

func TestScanFilesRejectsEngineRuleDrift(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logs := makeEngineLogs(t, "on_fixture:\nExpected Scope: character\n")
	defer func() { _ = ConfigureEngineRules("") }()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "fixture.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture_decision = { is_shown = { always = yes } }"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		EngineLogs: logs,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "on_actions.log"), []byte("on_fixture:\nExpected Scope: none\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ScanFiles(ctx, cfg, []string{"common/decisions/fixture.txt"})
	var fullRequired *FullScanRequiredError
	if !errors.As(err, &fullRequired) || fullRequired.Reason != "engine log rules changed" {
		t.Fatalf("engine rule drift did not force full scan: %v", err)
	}
}

func semanticFTSCount(t *testing.T, db *DB, term string) int {
	t.Helper()
	var count int
	if err := db.sql.QueryRow(`SELECT count(*) FROM search_fts WHERE search_fts MATCH ?`, `"`+term+`"`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
