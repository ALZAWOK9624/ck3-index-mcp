package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func newBaselineFixture(t *testing.T) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO source_layers(name,rank,role,private) VALUES('project',1,'project',0)`); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func addBaselineDiagnostic(t *testing.T, db *DB, ctx context.Context, code, rel string, line int) {
	t.Helper()
	result, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256)
		VALUES('project',1,?,?,'script',0,0,?)`, filepath.Join("project", filepath.FromSlash(rel)), rel, rel)
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col)
		VALUES('validator','error',?,?,?,?,?,1)`, code, "finding in "+rel, fileID, rel, line); err != nil {
		t.Fatal(err)
	}
}

func explainCount(t *testing.T, db *DB, ctx context.Context, filter DiagnosticFilter) int {
	t.Helper()
	found, err := db.ExplainDiagnosticFiltered(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	return len(found)
}

// The point of a baseline: inherited findings stop competing with the ones the
// current work introduced.
func TestDiagnosticBaselineHidesOnlyRecordedFindings(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/inherited.txt", 10)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/also_inherited.txt", 4)

	saved, err := db.SaveDiagnosticBaseline(ctx, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Count != 2 {
		t.Fatalf("baseline recorded %d findings, want 2", saved.Count)
	}
	if got := explainCount(t, db, ctx, DiagnosticFilter{Code: "scope_mismatch", Baseline: "upstream"}); got != 0 {
		t.Fatalf("a freshly recorded baseline still reported %d findings", got)
	}

	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/mine.txt", 7)
	if got := explainCount(t, db, ctx, DiagnosticFilter{Code: "scope_mismatch", Baseline: "upstream"}); got != 1 {
		t.Fatalf("baseline reported %d findings, want the 1 introduced since", got)
	}
	if got := explainCount(t, db, ctx, DiagnosticFilter{Code: "scope_mismatch"}); got != 3 {
		t.Fatalf("unfiltered explain reported %d findings, want all 3", got)
	}
}

// The summary headline and its evidence must describe the same set, so the
// counts are recomputed from the surviving findings rather than reused.
func TestDiagnosticBaselineSummaryCountsMatchFilteredEvidence(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/inherited.txt", 10)
	if _, err := db.SaveDiagnosticBaseline(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/mine.txt", 7)

	report, err := db.CachedValidationSinceBaseline(ctx, "project", "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("filtered report listed %d findings, want 1", len(report.Diagnostics))
	}
	if report.Counts["error"] != 1 {
		t.Fatalf("filtered report counted %d errors, want 1", report.Counts["error"])
	}
}

// Asking to see only new findings against a baseline that was never recorded
// must fail loudly. Returning everything would read as "nothing regressed"
// exactly when the caller is least able to notice.
func TestDiagnosticBaselineRejectsUnknownName(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/a.txt", 3)
	if _, err := db.ExplainDiagnosticFiltered(ctx, DiagnosticFilter{Code: "scope_mismatch", Baseline: "never_recorded"}); err == nil {
		t.Fatal("an unrecorded baseline name was silently accepted")
	}
}

func TestDiagnosticBaselineSaveReplacesEarlierSnapshot(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/a.txt", 3)
	if _, err := db.SaveDiagnosticBaseline(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM diagnostics`); err != nil {
		t.Fatal(err)
	}
	addBaselineDiagnostic(t, db, ctx, "missing_localization", "common/decisions/b.txt", 9)
	saved, err := db.SaveDiagnosticBaseline(ctx, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Count != 1 {
		t.Fatalf("re-recorded baseline holds %d findings, want only the current 1", saved.Count)
	}
	baselines, err := db.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 1 || baselines[0].Name != "upstream" {
		t.Fatalf("listing reported %+v, want a single baseline named upstream", baselines)
	}
}

// A baseline is a caller's decision, not derived index data. A full rebuild
// reproduces the same findings, so wiping the baseline with them would silently
// undo the decision.
func TestDiagnosticBaselineSurvivesFullReset(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/a.txt", 3)
	if _, err := db.SaveDiagnosticBaseline(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	if err := db.reset(ctx); err != nil {
		t.Fatal(err)
	}
	baselines, err := db.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 1 || baselines[0].Count != 1 {
		t.Fatalf("after a full reset the baseline reads %+v, want the recorded snapshot intact", baselines)
	}
}

func TestDiagnosticBaselineClearReportsWhatItForgot(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/a.txt", 3)
	if _, err := db.SaveDiagnosticBaseline(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	cleared, err := db.ClearDiagnosticBaseline(ctx, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Count != 1 {
		t.Fatalf("clear reported %d forgotten findings, want 1", cleared.Count)
	}
	missing, err := db.ClearDiagnosticBaseline(ctx, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Count != 0 {
		t.Fatalf("clearing an already-cleared baseline reported %d findings, want 0", missing.Count)
	}
}

// Recording "there is nothing wrong here right now" is the case a baseline
// serves best: every finding that shows up afterwards is a regression by
// construction. Deriving the baseline's existence from its entry rows made that
// exact call report a successful save and then deny the name was ever recorded.
func TestDiagnosticBaselineWithNoFindingsStillExists(t *testing.T) {
	db, ctx := newBaselineFixture(t)

	saved, err := db.SaveDiagnosticBaseline(ctx, "clean")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Count != 0 {
		t.Fatalf("a clean project recorded %d findings, want 0", saved.Count)
	}
	baselines, err := db.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 1 || baselines[0].Name != "clean" || baselines[0].CreatedAt == "" {
		t.Fatalf("listing reported %+v, want the recorded empty baseline", baselines)
	}
	if _, err := db.ExplainDiagnosticFiltered(ctx, DiagnosticFilter{Baseline: "clean"}); err != nil {
		t.Fatalf("a recorded empty baseline was rejected as unknown: %v", err)
	}

	// The first finding to appear afterwards is new, and must be reported.
	addBaselineDiagnostic(t, db, ctx, "scope_mismatch", "common/decisions/mine.txt", 7)
	if got := explainCount(t, db, ctx, DiagnosticFilter{Code: "scope_mismatch", Baseline: "clean"}); got != 1 {
		t.Fatalf("filtering against an empty baseline reported %d findings, want the 1 introduced since", got)
	}
	report, err := db.CachedValidationSinceBaseline(ctx, "project", "clean")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("summary against an empty baseline listed %d findings, want 1", len(report.Diagnostics))
	}
}

// A save must be readable by the next call, and a cleared name must be
// recordable again. Both round trips ran through the entry rows alone, so both
// broke for a baseline that holds none.
func TestDiagnosticBaselineSaveClearSaveRoundTrips(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	named := func(t *testing.T, name string) bool {
		t.Helper()
		baselines, err := db.ListDiagnosticBaselines(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, baseline := range baselines {
			if baseline.Name == name {
				return true
			}
		}
		return false
	}

	if named(t, "foo") {
		t.Fatal("an unrecorded baseline was listed")
	}
	if _, err := db.SaveDiagnosticBaseline(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if !named(t, "foo") {
		t.Fatal("a saved baseline was not listed")
	}
	cleared, err := db.ClearDiagnosticBaseline(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Existed {
		t.Fatal("clearing a recorded baseline reported it as never recorded")
	}
	if named(t, "foo") {
		t.Fatal("a cleared baseline was still listed")
	}
	if _, err := db.SaveDiagnosticBaseline(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if !named(t, "foo") {
		t.Fatal("a baseline re-recorded after a clear was not listed")
	}
	missing, err := db.ClearDiagnosticBaseline(ctx, "never_recorded")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Existed {
		t.Fatal("clearing a name that was never recorded reported a real deletion")
	}
}

// Caches written before the snapshot table existed hold their baselines only as
// entry rows. Opening one must adopt them rather than report every recorded
// name as never created.
func TestDiagnosticBaselinesFromOlderCachesAreAdopted(t *testing.T) {
	db, ctx := newBaselineFixture(t)
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostic_baselines(name,fingerprint,code,severity,created_at)
		VALUES('upstream','validator\x00abc','scope_mismatch','error','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM diagnostic_baseline_snapshots`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baselines, err := db.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 1 || baselines[0].Name != "upstream" || baselines[0].Count != 1 {
		t.Fatalf("an older cache's baseline came back as %+v, want it adopted intact", baselines)
	}
	if baselines[0].CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("adopted baseline reports created_at %q, want the recorded time", baselines[0].CreatedAt)
	}
}
