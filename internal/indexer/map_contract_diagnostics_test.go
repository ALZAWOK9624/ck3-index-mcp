package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMapContractFile(t *testing.T, root, rel, content string) activeMapFile {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return activeMapFile{Path: path, Rel: rel, Src: Source{Name: "project", Rank: 1}}
}

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

func TestDefinitionSequenceReportsOrderAndDuplicateIndependently(t *testing.T) {
	file := writeMapContractFile(t, t.TempDir(), "map_data/definition.csv", "province;red;green;blue\n1;1;1;1\n3;3;3;3\n2;2;2;2\n2;4;4;4\n")
	ids, diagnostics := auditDefinitionSequence(file)
	if len(ids) != 3 {
		t.Fatalf("defined ids=%v, want 1..3", ids)
	}
	codes := map[string]bool{}
	for _, diagnostic := range diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, code := range []string{"map_definition_out_of_order", "map_definition_duplicate_id"} {
		if !codes[code] {
			t.Fatalf("missing %s in %+v", code, diagnostics)
		}
	}
	if codes["map_definition_non_contiguous_ids"] {
		t.Fatalf("order-only fixture was reported as a gap: %+v", diagnostics)
	}
}

func TestProvinceDefinitionParserRejectsOutOfRangeAndAmbiguousRows(t *testing.T) {
	content := fmt.Sprintf(`province;red;green;blue
0;0;0;0
1;1;2;3
1;4;5;6
2;1;2;3
3;256;0;0
4;-1;0;0
0;1;1;1
%d;7;8;9
`, int64(MaxProvinceID)+1)
	file := writeMapContractFile(t, t.TempDir(), "map_data/definition.csv", content)
	definitions, err := parseProvinceDefinitionsForAudit(file.Path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if definitions.InvalidRows != 4 {
		t.Fatalf("invalid rows=%d samples=%v, want 4", definitions.InvalidRows, definitions.Samples)
	}
	if definitions.DuplicateIDs != 1 || definitions.DuplicateColors != 1 {
		t.Fatalf("duplicate counts ids=%d colors=%d", definitions.DuplicateIDs, definitions.DuplicateColors)
	}
	if len(definitions.IDToColor) != 1 || definitions.IDToColor[1] != 0x010203 {
		t.Fatalf("ambiguous or invalid rows entered ID lookup: %#v", definitions.IDToColor)
	}
	if len(definitions.ColorToID) != 1 || definitions.ColorToID[0x010203] != 1 {
		t.Fatalf("ambiguous or invalid rows entered RGB lookup: %#v", definitions.ColorToID)
	}
	if len(definitions.DuplicateIDSamples) != 1 || !strings.Contains(definitions.DuplicateIDSamples[0], "lines 3 and 4") {
		t.Fatalf("duplicate ID samples=%v", definitions.DuplicateIDSamples)
	}
	if len(definitions.DuplicateColorSamples) != 1 || !strings.Contains(definitions.DuplicateColorSamples[0], "province ids 1 and 2") {
		t.Fatalf("duplicate RGB samples=%v", definitions.DuplicateColorSamples)
	}
}

func TestProvinceDefinitionDuplicateTrackingIncludesRejectedRows(t *testing.T) {
	file := writeMapContractFile(t, t.TempDir(), "map_data/definition.csv", `
1;1;1;1
2;1;1;1
2;2;2;2
3;2;2;2
`)
	definitions, err := parseProvinceDefinitionsForAudit(file.Path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if definitions.DuplicateIDs != 1 || definitions.DuplicateColors != 2 {
		t.Fatalf("cross-duplicate counts ids=%d colors=%d; id/color keys from rejected rows must remain tracked", definitions.DuplicateIDs, definitions.DuplicateColors)
	}
	if len(definitions.IDToColor) != 1 || definitions.IDToColor[1] != 0x010101 {
		t.Fatalf("ambiguous cross-duplicate rows entered first-wins lookup: %#v", definitions.IDToColor)
	}
	if len(definitions.ColorToID) != 1 || definitions.ColorToID[0x010101] != 1 {
		t.Fatalf("ambiguous cross-duplicate rows entered first-wins lookup: %#v", definitions.ColorToID)
	}
}

func TestProvinceDefinitionSentinelDoesNotOverwriteFirstColorOccurrence(t *testing.T) {
	file := writeMapContractFile(t, t.TempDir(), "map_data/definition.csv", `
1;0;0;0
0;0;0;0
2;0;0;0
`)
	definitions, err := parseProvinceDefinitionsForAudit(file.Path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if definitions.DuplicateColors != 2 {
		t.Fatalf("duplicate colors=%d, want sentinel and later province both compared with the first positive black row", definitions.DuplicateColors)
	}
	for _, sample := range definitions.DuplicateColorSamples {
		if !strings.Contains(sample, "province ids 1 and") {
			t.Fatalf("sentinel overwrote first RGB occurrence: samples=%v", definitions.DuplicateColorSamples)
		}
	}
	if definitions.ColorToID[0] != 1 || len(definitions.ColorToID) != 1 {
		t.Fatalf("sentinel changed first-wins RGB lookup: %#v", definitions.ColorToID)
	}
}

func TestDefinitionSequenceCountsHugeSparseGapWithoutMaterializingIt(t *testing.T) {
	content := fmt.Sprintf("province;red;green;blue\n1;1;2;3\n%d;4;5;6\n", MaxProvinceID)
	file := writeMapContractFile(t, t.TempDir(), "map_data/definition.csv", content)
	ids, diagnostics := auditDefinitionSequence(file)
	if len(ids) != 2 || !ids[1] || !ids[MaxProvinceID] {
		t.Fatalf("defined ids=%v", ids)
	}
	var gap *mapContractDiagnostic
	for i := range diagnostics {
		if diagnostics[i].Code == "map_definition_non_contiguous_ids" {
			gap = &diagnostics[i]
			break
		}
	}
	if gap == nil {
		t.Fatalf("missing gap diagnostic: %+v", diagnostics)
	}
	if gap.Occurrences != MaxProvinceID-2 {
		t.Fatalf("missing count=%d, want %d", gap.Occurrences, MaxProvinceID-2)
	}
	if !strings.Contains(gap.Message, "samples: 2, 3, 4, 5, 6, 7, 8, 9") {
		t.Fatalf("gap samples were not bounded to the first eight: %s", gap.Message)
	}
}

func TestProvinceLocatorContractChecksTypedRecordsAndAllowsZeroSentinel(t *testing.T) {
	root := t.TempDir()
	file := writeMapContractFile(t, root, "gfx/map/map_object_data/combat_locators.txt", `
game_object_locator = {
	instances = {
		# { id=99 position={ 1 2 3 } rotation={ 0 0 0 1 } scale={ 1 1 1 } }
		{ id=0 position={ 1 2 3 } rotation={ 0 0 0 1 } scale={ 1 1 1 } }
		{ id=2 position={ 1066 4 5 } rotation={ 0 0 0 1 } scale={ 1 1 1 } }
	}
}
`)
	diagnostics := auditProvinceLocatorContract(map[string]activeMapFile{file.Rel: file}, map[int]bool{1: true})
	if len(diagnostics) != 1 || diagnostics[0].Code != "province_reference_missing_definition" || diagnostics[0].Occurrences != 1 {
		t.Fatalf("locator diagnostics=%+v", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "2") || strings.Contains(diagnostics[0].Message, "1066") || strings.Contains(diagnostics[0].Message, "99") {
		t.Fatalf("locator parser scanned a non-id number: %+v", diagnostics[0])
	}
}

func TestProvinceMappingContractChecksOnlyTypedKeyValuePairs(t *testing.T) {
	root := t.TempDir()
	file := writeMapContractFile(t, root, "history/province_mapping/00.txt", "# 99 = 98\n1 = 2\n3 = 1\n")
	diagnostics := auditProvinceMappingContract(map[string]activeMapFile{file.Rel: file}, map[int]bool{1: true, 2: true})
	if len(diagnostics) != 1 || diagnostics[0].Occurrences != 1 || !strings.Contains(diagnostics[0].Message, "3") {
		t.Fatalf("mapping diagnostics=%+v", diagnostics)
	}
}

func TestInvalidHoldingProvinceRejectsBlockedAssignmentsButAllowsNone(t *testing.T) {
	root := t.TempDir()
	file := writeMapContractFile(t, root, "history/provinces/00.txt", `
1 = { holding = castle_holding }
2 = { holding = none }
3 = { 1066.10.1 = { holding = city_holding } }
`)
	provinces := map[int]*mapProvinceBuild{
		1: {ID: 1, BlockKind: "water", WaterKind: "sea"},
		2: {ID: 2, BlockKind: "water", WaterKind: "river"},
		3: {ID: 3, BlockKind: "impassable_mountain"},
	}
	diagnostics := invalidHoldingProvinceDiagnostics(map[string]activeMapFile{file.Rel: file}, provinces)
	if len(diagnostics) != 1 || diagnostics[0].Code != "invalid_holding_province" || diagnostics[0].Occurrences != 2 {
		t.Fatalf("holding diagnostics=%+v", diagnostics)
	}
}

func TestCountyHistoryAnchorUsesEveryBookmarkEffectiveValue(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, row := range []struct {
		date  int
		field string
		value string
	}{
		{0, "culture", "culture_a"},
		{0, "religion", "faith_a"},
		{0, "holding", "none"},
		{10661002, "holding", "castle_holding"},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO map_province_history(province_id,date_key,field,value) VALUES(1,?,?,?)`, row.date, row.field, row.value); err != nil {
			t.Fatal(err)
		}
	}
	anchors := map[string]countyHistoryAnchor{"c_test": {CountyID: "c_test", BaronyID: "b_test", ProvinceID: 1, Source: "project", Path: "common/landed_titles/00.txt", Line: 1}}
	diagnostics := countyHistoryAnchorDiagnostics(ctx, tx, anchors, []int{10661001, 10661003})
	if len(diagnostics) != 1 || diagnostics[0].Severity != "error" || diagnostics[0].Occurrences != 1 || !strings.Contains(diagnostics[0].Message, "1066.10.1: holding") {
		t.Fatalf("bookmark diagnostics=%+v", diagnostics)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE map_province_history SET date_key=10661001 WHERE province_id=1 AND field='holding' AND value='castle_holding'`); err != nil {
		t.Fatal(err)
	}
	if diagnostics := countyHistoryAnchorDiagnostics(ctx, tx, anchors, []int{10661001, 10661003}); len(diagnostics) != 0 {
		t.Fatalf("effective bookmark values were not accepted: %+v", diagnostics)
	}
}

func TestCollectBookmarkStartDatesFindsNestedBookmarks(t *testing.T) {
	root := t.TempDir()
	file := writeMapContractFile(t, root, "common/bookmarks/bookmarks/00.txt", `group = { bookmark = { start_date = 1066.10.1 } bookmark = { start_date = 867.1.1 } }`)
	dates := collectBookmarkStartDates(map[string]activeMapFile{file.Rel: file})
	if len(dates) != 2 || dates[0] != 8670101 || dates[1] != 10661001 {
		t.Fatalf("bookmark dates=%v", dates)
	}
}

func TestHigherTitleCapitalMustBeCountyInsideDeJureTree(t *testing.T) {
	titles := map[string]*mapTitleBuild{
		"d_test":     {ID: "d_test", Type: "d", CapitalTitle: "c_outside", Children: []string{"c_inside"}, Source: "project", Rel: "common/landed_titles/00.txt", Line: 1},
		"c_inside":   {ID: "c_inside", Type: "c", Parent: "d_test"},
		"c_outside":  {ID: "c_outside", Type: "c"},
		"d_titular":  {ID: "d_titular", Type: "d", CapitalTitle: "c_outside", Source: "project", Rel: "common/landed_titles/00.txt", Line: 2},
		"d_bad_rank": {ID: "d_bad_rank", Type: "d", CapitalTitle: "b_inside", Source: "project", Rel: "common/landed_titles/00.txt", Line: 3},
		"b_inside":   {ID: "b_inside", Type: "b", Parent: "c_inside"},
	}
	diagnostics := titleMapContractDiagnostics(titles, nil, nil, nil)
	if len(diagnostics) != 2 {
		t.Fatalf("capital diagnostics=%+v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Code != "invalid_title_capital_reference" {
			t.Fatalf("unexpected diagnostic=%+v", diagnostic)
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
	blocked, err := parseDefaultMapBlocked(path)
	if err != nil {
		t.Fatal(err)
	}
	if blocked[2].BlockKind != "" || blocked[99].BlockKind != "" || blocked[1].BlockKind != "impassable_mountain" {
		t.Fatalf("commented default.map values entered blocked province cache: %+v", blocked)
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
