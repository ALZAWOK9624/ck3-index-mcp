package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditOnActionEvidenceAggregatesPublishedLayers(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = character
on_army_monthly = { }

# Root does not exist
on_alliance_added = { }

# Root = character
on_none_documented_fixture = { }

# Root = character
on_conflict_documented_fixture = { }

# Root = character
on_ambiguous_documented_fixture = { }

# Root = province
on_ambiguous_documented_fixture = { }

# Root = character
# Root = province
on_duplicate_root_documented_fixture = { }
`)

	db := newOnActionEvidenceAuditDB(t, "ready")
	defer db.Close()
	insertOnActionEvidenceEngineRule(t, db, "on_army_monthly", "character")
	insertOnActionEvidenceEngineRule(t, db, "on_alliance_added", "none")
	insertOnActionEvidenceEngineRule(t, db, "on_none_documented_fixture", "none")
	insertOnActionEvidenceEngineRule(t, db, "on_conflict_documented_fixture", "province")
	insertOnActionEvidenceEngineRule(t, db, "on_ambiguous_documented_fixture", "character")
	insertOnActionEvidenceEngineRule(t, db, "on_duplicate_root_documented_fixture", "character")
	indexOnActionEvidenceDocumentationSnapshot(t, db, game)

	report, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || !report.EngineEvidenceAvailable || !report.DocumentationEvidenceAvailable || report.TigerSourceVersion != "1.15.0" || report.HookCount < len(tigerOnActions) {
		t.Fatalf("unexpected audit summary: %+v", report)
	}

	army := onActionEvidenceFinding(t, report.Findings, "on_army_monthly")
	if army.Status != "match" || army.Engine.Type != "character" || army.Tiger.Root.Type != "character" || army.Documentation.Status != "documented" || len(army.Documentation.Roots) != 1 || army.Documentation.Roots[0].Root.Type != "character" || army.EngineTigerComparison != "match" || army.EngineDocumentationComparison != "match" || army.TigerDocumentationComparison != "match" {
		t.Fatalf("published matching evidence did not remain layered: %+v", army)
	}
	alliance := onActionEvidenceFinding(t, report.Findings, "on_alliance_added")
	if alliance.Status != "match" || alliance.Engine.Status != "none" || alliance.Tiger.Root.Status != "none" || alliance.Documentation.Roots[0].Root.Status != "none" {
		t.Fatalf("none root was not preserved through all evidence layers: %+v", alliance)
	}
	noneDocumented := onActionEvidenceFinding(t, report.Findings, "on_none_documented_fixture")
	if noneDocumented.Status != "engine_none_with_documented_root" || noneDocumented.EngineDocumentationComparison != "engine_none_with_documented_root" || noneDocumented.Tiger.Found {
		t.Fatalf("engine none/documented root review was not explicit: %+v", noneDocumented)
	}
	conflict := onActionEvidenceFinding(t, report.Findings, "on_conflict_documented_fixture")
	if conflict.Status != "engine_scope_conflicts_documented_root" || conflict.EngineDocumentationComparison != "engine_scope_conflicts_documented_root" {
		t.Fatalf("engine/documented root conflict was not explicit: %+v", conflict)
	}
	ambiguous := onActionEvidenceFinding(t, report.Findings, "on_ambiguous_documented_fixture")
	if ambiguous.Status != "ambiguous" || ambiguous.Documentation.Status != "ambiguous" || len(ambiguous.Documentation.Roots) != 2 || ambiguous.EngineDocumentationComparison != "ambiguous" {
		t.Fatalf("ambiguous documentation roots were not retained: %+v", ambiguous)
	}
	duplicateRoot := onActionEvidenceFinding(t, report.Findings, "on_duplicate_root_documented_fixture")
	if duplicateRoot.Status != "ambiguous" || duplicateRoot.Documentation.Status != "ambiguous" || len(duplicateRoot.Documentation.Roots) != 1 || duplicateRoot.Documentation.Roots[0].Root.Status != "ambiguous" {
		t.Fatalf("multiple roots in one comment contract were not retained as ambiguous: %+v", duplicateRoot)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), filepath.ToSlash(game)) || strings.Contains(string(encoded), "Root = character") {
		t.Fatalf("unified evidence audit leaked a physical source root or raw comment prose: %s", encoded)
	}
}

func TestAuditOnActionEvidenceDoesNotUseUnpublishedEngineRowsAndBoundsOutput(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = character
on_army_monthly = { }
`)

	db := newOnActionEvidenceAuditDB(t, "finalizing")
	defer db.Close()
	insertOnActionEvidenceEngineRule(t, db, "on_army_monthly", "none")

	report, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if report.EngineEvidenceAvailable {
		t.Fatalf("finalizing engine rows became published evidence: %+v", report)
	}
	army := onActionEvidenceFinding(t, report.Findings, "on_army_monthly")
	if army.Status != "evidence_unavailable" || army.Engine.Status != "unavailable" || army.Engine.Type != "" || army.EngineDocumentationComparison != "evidence_unavailable" || army.TigerDocumentationComparison != "match" {
		t.Fatalf("unpublished engine evidence leaked into unified result: %+v", army)
	}

	bounded, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bounded.Truncated || len(bounded.Findings) != 1 || bounded.HookCount <= len(bounded.Findings) {
		t.Fatalf("bounded audit output was not enforced: %+v", bounded)
	}
}

func TestAuditOnActionEvidenceTreatsMissingPublishedHookAsStructuralDrift(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = character
on_army_monthly = { }
`)

	db := newOnActionEvidenceAuditDB(t, "ready")
	defer db.Close()
	// One published engine row establishes a complete live on_actions.log
	// layer for this fixture; on_army_monthly is deliberately absent from it.
	insertOnActionEvidenceEngineRule(t, db, "on_engine_anchor_fixture", "character")
	indexOnActionEvidenceDocumentationSnapshot(t, db, game)

	report, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	army := onActionEvidenceFinding(t, report.Findings, "on_army_monthly")
	if !report.EngineEvidenceAvailable || army.Status != "engine_missing_with_static_root" || army.Engine.Status != "not_found" || army.Engine.Confidence != "high" || army.EngineTigerComparison != "engine_missing_with_static_root" || army.EngineDocumentationComparison != "engine_missing_with_documented_root" {
		t.Fatalf("published engine omission was not reported as structural drift: %+v", army)
	}
}

func TestAuditOnActionEvidenceDoesNotHideThreeWayConflictBehindMatch(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = character
on_alliance_added = { }
`)

	db := newOnActionEvidenceAuditDB(t, "ready")
	defer db.Close()
	// Tiger declares this hook as none. Engine and comments agree with each
	// other, which used to incorrectly collapse the three-way result to match.
	insertOnActionEvidenceEngineRule(t, db, "on_alliance_added", "character")
	indexOnActionEvidenceDocumentationSnapshot(t, db, game)

	report, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	alliance := onActionEvidenceFinding(t, report.Findings, "on_alliance_added")
	if alliance.EngineDocumentationComparison != "match" || alliance.EngineTigerComparison != "engine_scope_conflicts_static_root" || alliance.TigerDocumentationComparison != "static_scope_conflicts_documented_root" || alliance.Status != "engine_scope_conflicts_static_root" {
		t.Fatalf("three-way evidence disagreement was hidden by a pairwise match: %+v", alliance)
	}
}

func TestAuditOnActionEvidenceWithholdsChangedVanillaComments(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = character
on_army_monthly = { }
`)

	db := newOnActionEvidenceAuditDB(t, "ready")
	defer db.Close()
	insertOnActionEvidenceEngineRule(t, db, "on_army_monthly", "character")
	indexOnActionEvidenceDocumentationSnapshot(t, db, game)
	// This edit happened after the published index snapshot. The audit must
	// not present it as though it were contemporaneous engine evidence.
	writeOnActionEvidenceFixture(t, game, "fixture.txt", `# Root = province
on_army_monthly = { }
`)

	report, err := db.AuditOnActionEvidence(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 500)
	if err != nil {
		t.Fatal(err)
	}
	army := onActionEvidenceFinding(t, report.Findings, "on_army_monthly")
	if report.Status != "stale" || report.DocumentationEvidenceAvailable || report.DocumentationEvidenceStatus != "stale" || army.Documentation.Status != "stale" || army.Status != "documentation_stale" || army.EngineDocumentationComparison != "evidence_unavailable" {
		t.Fatalf("changed vanilla comments were mixed with published engine evidence: report=%+v finding=%+v", report, army)
	}
}

func newOnActionEvidenceAuditDB(t *testing.T, status string) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "audit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_status',?),('engine_data_fingerprint','fixture')`, status); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func insertOnActionEvidenceEngineRule(t *testing.T, db *DB, name, scopes string) {
	t.Helper()
	if _, err := db.sql.ExecContext(context.Background(), `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES(?, 'on_action', ?, '', '', 'on_actions.log')`, name, scopes); err != nil {
		t.Fatal(err)
	}
}

func indexOnActionEvidenceDocumentationSnapshot(t *testing.T, db *DB, root string) {
	t.Helper()
	source := Source{Name: "game", Path: root, Rank: 3}
	hashes, available, err := currentOnActionDocumentationHashes(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("test fixture did not create a readable common/on_action directory")
	}
	for rel, hash := range hashes {
		physical := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := db.sql.ExecContext(context.Background(), `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256)
			VALUES(?,?,?,?,?,?,?)`, source.Name, source.Rank, physical, rel, "script", 0, hash); err != nil {
			t.Fatal(err)
		}
	}
}

func writeOnActionEvidenceFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "common", "on_action")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func onActionEvidenceFinding(t *testing.T, findings []OnActionEvidenceHook, hook string) OnActionEvidenceHook {
	t.Helper()
	for _, finding := range findings {
		if finding.Hook == hook {
			return finding
		}
	}
	t.Fatalf("missing %s in %+v", hook, findings)
	return OnActionEvidenceHook{}
}
