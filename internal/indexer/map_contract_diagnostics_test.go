package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectBaseMapContractDiagnosticsFindsCrossFileFailures(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) activeMapFile {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		return activeMapFile{Path: path, Rel: rel, Src: Source{Name: "project", Rank: 1}}
	}
	active := map[string]activeMapFile{}
	active["map_data/definition.csv"] = write("map_data/definition.csv", "province;red;green;blue\n1;255;0;0\n3;0;0;255\n")
	active["map_data/default.map"] = write("map_data/default.map", "impassable_mountains = LIST { 3 }\nimpassable_mountains = LIST { 4 }\n")
	terrain := write("common/province_terrain/00.txt", "1 = hills\n1 = plains\n4 = forest\n")
	active[terrain.Rel] = terrain
	history := write("history/provinces/00.txt", "1 = { culture = a }\n1 = { culture = b }\n4 = { holding = castle_holding }\n")
	active[history.Rel] = history

	diagnostics, _ := collectBaseMapContractDiagnostics(context.Background(), active)
	codes := map[string]bool{}
	for _, diagnostic := range diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, code := range []string{
		"map_definition_non_contiguous_ids",
		"duplicate_default_map_field",
		"conflicting_province_terrain_assignment",
		"duplicate_province_history_block",
		"conflicting_province_history_field",
		"province_reference_missing_definition",
	} {
		if !codes[code] {
			t.Errorf("expected diagnostic %s, got %+v", code, codes)
		}
	}
}

func TestParseActiveLandedTitlesUsesFirstDirectBaronyAsHistoryAnchor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "common", "landed_titles", "00.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`
e_test = {
	k_test = {
		d_test = {
			c_test = {
				capital = b_second
				b_first = { province = 1 }
				b_second = { province = 2 }
			}
		}
	}
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	active := map[string]activeMapFile{
		"common/landed_titles/00.txt": {Path: path, Rel: "common/landed_titles/00.txt", Src: Source{Name: "project", Rank: 1}},
	}
	titles, _, _, anchors, issues, err := parseActiveLandedTitles(active)
	if err != nil {
		t.Fatal(err)
	}
	anchor := anchors["c_test"]
	if anchor.BaronyID != "b_first" || anchor.ProvinceID != 1 || anchor.DeclaredCapital != "b_second" {
		t.Fatalf("expected first direct barony as history anchor, got %+v", anchor)
	}
	diagnostics := titleMapContractDiagnostics(titles, map[int]*mapProvinceBuild{
		1: {ID: 1, Area: 1}, 2: {ID: 2, Area: 1},
	}, map[int]bool{1: true, 2: true}, issues)
	foundMismatch := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "county_history_anchor_mismatch" {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatalf("expected county_history_anchor_mismatch, got %+v", diagnostics)
	}
}

func TestReplaceMapContractDiagnosticsFeedsOrdinaryValidation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden)
		VALUES('project',1,'definition.csv','map_data/definition.csv','map',0,0,'fixture',0)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceMapContractDiagnostics(ctx, tx, []mapContractDiagnostic{{
		Severity: "error", Code: "map_definition_non_contiguous_ids", Message: "fixture gap",
		Source: "project", Path: "map_data/definition.csv", Line: 2, Occurrences: 1,
	}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	report, err := db.CachedValidation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == "map_definition_non_contiguous_ids" && diagnostic.Source == "map-integrity" {
			found = true
		}
	}
	if !found || report.Counts["error"] == 0 {
		t.Fatalf("ordinary validation did not receive map-integrity diagnostics: %+v", report)
	}
}

func TestDefaultMapContractIgnoresCommentedListsAndIDs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "default.map")
	text := `
impassable_mountains = LIST { 1 # 99 is documentation only
}
# impassable_mountains = LIST { 2 }
`
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	diagnostics := auditDefaultMapContract(activeMapFile{Path: path, Rel: "map_data/default.map", Src: Source{Name: "project"}}, map[int]bool{1: true})
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "duplicate_default_map_field" || diagnostic.Code == "province_reference_missing_definition" {
			t.Fatalf("commented default.map content produced a hard diagnostic: %+v", diagnostics)
		}
	}
}

func TestMapPackageDiagnosticsRequiresCurrentIndexedBytes(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	definitionPath := filepath.Join(dir, "project", "map_data", "definition.csv")
	data, err := os.ReadFile(definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,path,line,source_layer,confidence,fingerprint,occurrences)
		VALUES('map-integrity','error','fixture_map_error','fixture','map_data/definition.csv',1,'project','high','fixture-map',1)`); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := db.MapPackageDiagnostics(ctx, []PatchFileInput{{Path: "map_data/definition.csv", Content: string(data)}})
	if err != nil {
		t.Fatal(err)
	}
	foundFixture := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "fixture_map_error" {
			foundFixture = true
		}
		if diagnostic.Code == "map_package_index_stale" {
			t.Fatalf("matching indexed bytes were marked stale: %+v", diagnostics)
		}
	}
	if !foundFixture {
		t.Fatalf("persisted map diagnostic was not included: %+v", diagnostics)
	}

	diagnostics, err = db.MapPackageDiagnostics(ctx, []PatchFileInput{{Path: "map_data/definition.csv", Content: string(data) + "\n6;1;2;3"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Code != "map_package_index_stale" {
		t.Fatalf("changed unscanned map bytes were not blocked: %+v", diagnostics)
	}
}
