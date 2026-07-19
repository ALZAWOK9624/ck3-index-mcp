package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleIntegrityRefreshesAcrossScanValidateInspectAndScanFiles(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	titlePath := filepath.Join(project, "common", "landed_titles", "00_landed_titles.txt")
	if err := os.MkdirAll(filepath.Dir(titlePath), 0755); err != nil {
		t.Fatal(err)
	}
	broken := `e_test = {
	k_test = {
		d_test = {
			c_c100 = {
				b_b538 = { province = 8930 }
				b_b538 = { province = 8922 }
				b_b538 = { province = 8921 }
			}
			c_c155 = {
				b_b567 = { province = 8922 }
				b_b568 = { province = 8930 }
			}
		}
	}
}`
	if err := os.WriteFile(titlePath, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
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

	duplicates, err := db.ExplainDiagnostic(context.Background(), "duplicate_title_id")
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 3 {
		t.Fatalf("duplicate title diagnostics=%d want=3: %+v", len(duplicates), duplicates)
	}
	for _, line := range []int{5, 6, 7} {
		found := false
		for _, diagnostic := range duplicates {
			found = found || diagnostic.Line == line
		}
		if !found {
			t.Fatalf("missing duplicate b_b538 line %d in %+v", line, duplicates)
		}
	}
	provinceConflicts, err := db.ExplainDiagnostic(context.Background(), "duplicate_barony_province")
	if err != nil || len(provinceConflicts) < 4 {
		t.Fatalf("expected both 8922 and 8930 conflicts, err=%v diagnostics=%+v", err, provinceConflicts)
	}
	inspect, err := db.LLMQueryObject(context.Background(), "b_b538", LLMOptions{AllowProject: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Counts["diagnostics"] != 3 || !strings.Contains(inspect.Summary, "resolution=ambiguous") {
		t.Fatalf("inspect did not expose live ambiguity: %+v", inspect)
	}
	if _, err := db.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.ExplainDiagnostic(context.Background(), "duplicate_object")
	if err != nil || len(legacy) != 0 {
		t.Fatalf("generic duplicate noise survived validate: err=%v diagnostics=%+v", err, legacy)
	}
	before, err := db.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	fixed := strings.Replace(broken, "\t\t\t\tb_b538 = { province = 8930 }\n\t\t\t\tb_b538 = { province = 8922 }\n", "", 1)
	if err := os.WriteFile(titlePath, []byte(fixed), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFiles(context.Background(), cfg, []string{"common/landed_titles/00_landed_titles.txt"}); err != nil {
		t.Fatal(err)
	}
	after, err := db.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("scan generation did not advance: before=%+v after=%+v", before, after)
	}
	duplicates, err = db.ExplainDiagnostic(context.Background(), "duplicate_title_id")
	if err != nil || len(duplicates) != 0 {
		t.Fatalf("incremental scan did not clear repaired duplicate: err=%v diagnostics=%+v", err, duplicates)
	}
}

func TestInspectDoesNotTreatLandedTitleAndTitleHistoryAsDuplicates(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	landedPath := filepath.Join(project, "common", "landed_titles", "00_landed_titles.txt")
	historyPath := filepath.Join(project, "history", "titles", "titles.txt")
	for _, path := range []string{landedPath, historyPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(landedPath, []byte("c_test = { b_test = { province = 1 } }"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, []byte("c_test = { 1.1.1 = { holder = 1 } }"), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
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
	inspect, err := db.LLMQueryObject(context.Background(), "c_test", LLMOptions{AllowProject: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Counts["diagnostics"] != 0 || strings.Contains(inspect.Summary, "resolution=ambiguous") {
		t.Fatalf("landed title and title history were reported as duplicates: %+v", inspect)
	}
}

func TestPatchPreflightReportsTitleAndProvinceIntegrityWarnings(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := db.LLMPreflightPatch(context.Background(), []PatchFileInput{{
		Path: "common/landed_titles/test.txt",
		Content: `e_test = { k_test = { d_test = { c_test = {
			b_b538 = { province = 8921 }
			b_b538 = { province = 8922 }
			b_other = { province = 8921 }
		} } } }`,
	}}, LLMOptions{AllowProject: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["integrity_warnings"] < 2 || result.Counts["nonblocking_risks"] < 2 || result.Counts["blocking_risks"] != 0 {
		t.Fatalf("preflight integrity classification is wrong: %+v", result.Counts)
	}
	text := ""
	for _, evidence := range result.Evidence {
		text += evidence.Detail + "\n"
	}
	if !strings.Contains(text, "duplicate_title_id") || !strings.Contains(text, "duplicate_barony_province") {
		t.Fatalf("missing title integrity evidence: %s", text)
	}
}
