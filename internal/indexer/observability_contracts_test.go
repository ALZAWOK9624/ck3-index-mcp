package indexer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// --- P1-1: a scan failure must stay diagnosable -----------------------------

func TestRedactHostPathsKeepsMessageAndDropsRoots(t *testing.T) {
	text := `open D:/ck3 tools/项目甲/22/common/traits/a.txt: permission denied`
	got := redactHostPaths(text, []string{`D:/ck3 tools/项目甲/22`})
	if strings.Contains(got, "D:/ck3 tools") {
		t.Fatalf("host root survived redaction: %s", got)
	}
	if !strings.Contains(got, "项目甲/22") {
		t.Fatalf("redaction dropped the identifying tail: %s", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("redaction destroyed the diagnostic text: %s", got)
	}
}

func TestRedactHostPathsLeavesOrdinaryTextAlone(t *testing.T) {
	text := "detail_index.tga: only uncompressed true-color TGA is supported"
	if got := redactHostPaths(text, []string{`D:/cache/i.sqlite`}); got != text {
		t.Fatalf("redaction altered a path-free message: %s", got)
	}
}

// TestStagedFullScanFailureReportsCause is the regression for the failure that
// cost the most time to diagnose: the wrapper reported only "did not complete",
// and an MCP caller — which sees nothing but the tool result — had no way to
// learn that a texture could not be decoded.
func TestStagedFullScanFailureReportsCause(t *testing.T) {
	cause := fmt.Errorf("detail_index.tga: %w", errors.New("only uncompressed true-color TGA is supported"))
	err := sanitizeStagedFullScanFailure(cause, []string{`D:/ck3 tools/cache/i.sqlite`})
	if err == nil {
		t.Fatal("expected a wrapped failure")
	}
	if !strings.Contains(err.Error(), "only uncompressed true-color TGA is supported") {
		t.Fatalf("cause was discarded: %s", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("unwrap chain no longer reaches the original cause")
	}
	if detail := scanFailureDetail(err); detail == "" {
		t.Fatal("no durable detail recorded for a sanitized failure")
	}
}

func TestScanFailureDetailOmittedForUnsanitizedErrors(t *testing.T) {
	// A raw error may still embed a host path, so it contributes a code only.
	if detail := scanFailureDetail(errors.New(`open D:/secret/x: denied`)); detail != "" {
		t.Fatalf("unsanitized error leaked a detail: %s", detail)
	}
}

func TestScanFailureDetailSurvivesRestart(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf("detail_index.tga: %w", errors.New("only uncompressed true-color TGA is supported"))
	db.recordScanFailure(ctx, sanitizeStagedFullScanFailure(cause, nil))

	stored, err := db.lastScanError(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || !strings.Contains(stored.Detail, "only uncompressed true-color TGA") {
		t.Fatalf("detail did not persist: %+v", stored)
	}
	db.clearScanFailure(ctx)
	cleared, err := db.lastScanError(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != nil {
		t.Fatalf("a successful scan left the previous failure behind: %+v", cleared)
	}
}

// --- P1-2: an unreadable terrain asset must not void the publication --------

func TestDegradeSurfaceMaterialCacheRecordsIssueInsteadOfFailing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "x.sqlite"))
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

	active := map[string]activeMapFile{
		"gfx/map/terrain/detail_index.tga": {
			Path: `D:/ck3 tools/godherja/gfx/map/terrain/detail_index.tga`,
			Rel:  "gfx/map/terrain/detail_index.tga",
			Src:  Source{Name: "godherja"},
		},
	}
	cause := errors.New("detail_index.tga: only uncompressed true-color TGA is supported")
	if err := degradeSurfaceMaterialCache(ctx, tx, active, cause); err != nil {
		t.Fatalf("degradation itself failed: %v", err)
	}

	var code, message, path string
	row := tx.QueryRowContext(ctx, `SELECT code,message,COALESCE(path,'') FROM map_integrity_issues`)
	if err := row.Scan(&code, &message, &path); err != nil {
		t.Fatalf("no integrity issue was recorded: %v", err)
	}
	if code != "surface_material_unavailable" {
		t.Fatalf("unexpected issue code %q", code)
	}
	if !strings.Contains(message, "only uncompressed true-color TGA") {
		t.Fatalf("issue lost the cause: %s", message)
	}
	if strings.Contains(message, "D:/ck3 tools") {
		t.Fatalf("issue leaked a host path: %s", message)
	}
	if path != "gfx/map/terrain/detail_index.tga" {
		t.Fatalf("issue should point at the source-relative asset, got %q", path)
	}
}

// --- P2: findings must carry their provenance and actionable advice ---------

func analyzeBuilding(t *testing.T, body string) []Diagnostic {
	t.Helper()
	analysis, err := AnalyzeVirtualFile("common/buildings/AX/test_buildings.txt", "patch", 1, body)
	if err != nil {
		t.Fatal(err)
	}
	return analysis.Diagnostics
}

func findDiagnostic(diagnostics []Diagnostic, code string) *Diagnostic {
	for i := range diagnostics {
		if diagnostics[i].Code == code {
			return &diagnostics[i]
		}
	}
	return nil
}

// TestInvalidModifierContextStatesEngineProvenance keeps the finding credible.
// The use areas come from modifiers.log, not a heuristic; when the message
// omits that, a correct finding gets waved off as a false positive because the
// field name reads like it belongs in the container.
func TestInvalidModifierContextStatesEngineProvenance(t *testing.T) {
	diagnostics := analyzeBuilding(t, "test_building = {\n\tprovince_modifier = {\n\t\tmonthly_income_mult = 0.1\n\t}\n}\n")
	found := findDiagnostic(diagnostics, "invalid_modifier_context")
	if found == nil {
		t.Fatalf("expected invalid_modifier_context, got %+v", diagnostics)
	}
	if !strings.Contains(found.Message, "engine_log") {
		t.Fatalf("message does not state its engine provenance: %s", found.Message)
	}
	if !strings.Contains(found.Message, "character") {
		t.Fatalf("message does not state the valid use area: %s", found.Message)
	}
}

// TestBuildingCultureModifierReportedAsUnreachable covers the advice that was
// actively misleading: buildings declare no culture-scope container, so telling
// a reader to "move it to a compatible container" produces edits that change
// nothing.
func TestBuildingCultureModifierReportedAsUnreachable(t *testing.T) {
	diagnostics := analyzeBuilding(t, "test_building = {\n\tcharacter_modifier = {\n\t\tmercenary_count_mult = 0.1\n\t}\n}\n")
	found := findDiagnostic(diagnostics, "invalid_modifier_context")
	if found == nil {
		t.Fatalf("expected invalid_modifier_context, got %+v", diagnostics)
	}
	if !strings.Contains(found.Message, "no container reaching") {
		t.Fatalf("message still implies the field can be relocated: %s", found.Message)
	}
	if !strings.Contains(found.Message, "culture") {
		t.Fatalf("message does not name the unreachable scope: %s", found.Message)
	}
}

// --- P3: a hotspot count must say how much of it is the project's ----------

// TestDiagnosticHotspotsSeparateProjectFromUpstream covers the mis-read that
// started this work: an overview hotspot counting every layer looks like the
// project owns all of it, sending the reader to fix files it does not control.
func TestDiagnosticHotspotsSeparateProjectFromUpstream(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.sql.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO source_layers(name,rank,role,private) VALUES('project',1,?,1)`, string(SourceRoleProject))
	exec(`INSERT INTO source_layers(name,rank,role,private) VALUES('godherja',2,?,0)`, string(SourceRoleDependency))
	exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES(1,'project',1,'p','a.txt','script',0,'x')`)
	exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES(2,'godherja',2,'g','b.txt','script',0,'y')`)
	for _, fileID := range []int{2, 2, 2, 1} {
		exec(`INSERT INTO diagnostics(source,severity,code,message,file_id,path) VALUES('ck3-index','error','localization_entry_syntax','m',?,'x.yml')`, fileID)
	}

	evidence, err := diagnosticHotspots(ctx, db.sql, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected one hotspot, got %+v", evidence)
	}
	if !strings.Contains(evidence[0].Detail, "count=4") {
		t.Fatalf("hotspot lost the workspace-wide total: %s", evidence[0].Detail)
	}
	if !strings.Contains(evidence[0].Detail, "project=1") {
		t.Fatalf("hotspot does not report the project's own share: %s", evidence[0].Detail)
	}
}

// A field that genuinely can be relocated must keep the actionable advice.
func TestRelocatableModifierKeepsMoveAdvice(t *testing.T) {
	diagnostics := analyzeBuilding(t, "test_building = {\n\tcounty_modifier = {\n\t\thostile_county_attrition = 1\n\t}\n}\n")
	found := findDiagnostic(diagnostics, "invalid_modifier_context")
	if found == nil {
		t.Fatalf("expected invalid_modifier_context, got %+v", diagnostics)
	}
	if !strings.Contains(found.Message, "move it to a compatible modifier container") {
		t.Fatalf("a relocatable field lost its actionable advice: %s", found.Message)
	}
	if strings.Contains(found.Message, "no container reaching") {
		t.Fatalf("a relocatable field was reported as unreachable: %s", found.Message)
	}
}
