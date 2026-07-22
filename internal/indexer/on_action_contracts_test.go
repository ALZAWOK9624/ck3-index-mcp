package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanVanillaOnActionCommentsIsTopLevelAndConservative(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	dir := filepath.Join(game, "common", "on_action", "dlc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `# General header that is not itself a binding

# Root = character
# scope:target is character

on_direct = {
	text = "{ # braces and comments in a string are not syntax"
}

# Root does not exist
# scope:first = character
# scope:reason = flag:inheritance or none
# scope:<flag_type> - character
on_none = { }

# Root is the owner of the army
# scope:army is the army
on_narrative = { }

wrapper = {
	# Root = province
	nested = { }
}

# scope:invalid.name = character
# old_scope = character
on_rejected = { }
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	scan, err := scanVanillaOnActionComments(ctx, Source{Name: "game", Path: game, Rank: 3})
	if err != nil {
		t.Fatal(err)
	}
	if scan.Files != 1 || scan.OnActions != 5 || len(scan.Contracts) != 3 {
		t.Fatalf("unexpected scan summary: %+v", scan)
	}
	direct := contractByName(t, scan.Contracts, "on_direct")
	if direct.Path != "common/on_action/dlc/fixture.txt" || direct.Line != 6 {
		t.Fatalf("contract leaked or lost source-relative coordinates: %+v", direct)
	}
	root, ok := contractRootBinding(direct.Bindings)
	if !ok || root.Kind != "scope" || root.Scope != "character" || root.Line != 3 {
		t.Fatalf("direct root was not parsed conservatively: %+v", direct.Bindings)
	}
	none := contractByName(t, scan.Contracts, "on_none")
	noneRoot, ok := contractRootBinding(none.Bindings)
	if !ok || noneRoot.Kind != "none" || noneRoot.Scope != "none" {
		t.Fatalf("none root was normalized incorrectly: %+v", none.Bindings)
	}
	if len(none.Bindings) != 4 || none.Bindings[2].Kind != "flag" || none.Bindings[3].Target != "<flag_type>" {
		t.Fatalf("named scope evidence was lost: %+v", none.Bindings)
	}
	narrative := contractByName(t, scan.Contracts, "on_narrative")
	narrativeRoot, ok := contractRootBinding(narrative.Bindings)
	if !ok || narrativeRoot.Kind != "unknown" || narrativeRoot.Scope != "" {
		t.Fatalf("narrative comment was guessed as a scope: %+v", narrative.Bindings)
	}
	if _, ok := contractByNameOptional(scan.Contracts, "on_rejected"); ok {
		t.Fatalf("unsupported dotted or legacy annotations produced a contract: %+v", scan.Contracts)
	}
}

func TestAuditOnActionScopeContractsComparesOnlyDirectRootEvidence(t *testing.T) {
	ctx := context.Background()
	game := t.TempDir()
	dir := filepath.Join(game, "common", "on_action")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `# Root = character
on_match = { }

# Root does not exist
on_none = { }

# Root = province
on_review = { }

# Root is the owner of an army
on_narrative = { }
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]string{"on_match": "character", "on_none": "none", "on_review": "none"} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
			VALUES(?, 'on_action', ?, '', '', 'on_actions.log')`, name, scope); err != nil {
			t.Fatal(err)
		}
	}
	report, err := db.AuditOnActionScopeContracts(ctx, Config{Sources: []Source{{Name: "game", Path: game, Rank: 3}}}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !report.EngineEvidenceAvailable || report.DocumentedOnActions != 4 || report.ComparableRootBindings != 3 || report.RootMatches != 2 || report.ReviewCount != 1 {
		t.Fatalf("unexpected audit summary: %+v", report)
	}
	review := contractByName(t, report.Contracts, "on_review")
	if review.RootComparison != "engine_none_with_documented_root" || review.Confidence != "medium" {
		t.Fatalf("engine none/documented root review was not retained: %+v", review)
	}
	narrative := contractByName(t, report.Contracts, "on_narrative")
	if narrative.RootComparison != "unresolved_documentation" || narrative.Confidence != "low" {
		t.Fatalf("narrative comment became a hard rule: %+v", narrative)
	}
	if len(report.Findings) != 1 || report.Findings[0].Severity != "review" || report.Findings[0].Path != "common/on_action/fixture.txt" {
		t.Fatalf("review finding was not bounded/source-relative: %+v", report.Findings)
	}
}

func TestAuditOnActionRulesDoesNotTreatMissingOptionalLogAsLiveEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','other-logs-only')`); err != nil {
		t.Fatal(err)
	}
	report, err := db.AuditOnActionRules(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveEvidenceAvailable || report.LiveCount != 0 || report.EngineOnlyCount != 0 || report.SnapshotOnlyCount != 0 {
		t.Fatalf("missing optional log became false drift: %+v", report)
	}
}

func TestLookupOnActionDocumentationContractUsesRankThreeSafeProjection(t *testing.T) {
	ctx := context.Background()
	private := t.TempDir()
	vanilla := t.TempDir()
	writeOnActionDocumentationFixture(t, private, "fixture.txt", `# Root = county
on_projection_fixture = { }
`)
	writeOnActionDocumentationFixture(t, vanilla, "fixture.txt", `# Root = character
# scope:target = character
# scope:reason = flag:PRIVATE_DOC_SENTINEL
# scope:<flag_type> = character
on_projection_fixture = { }
`)
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('on_projection_fixture','on_action','none','','fixture','on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col)
		VALUES('on_action','on_projection_fixture','',0,0,'vanilla',3,'common/on_action/fixture.txt',5,1)`); err != nil {
		t.Fatal(err)
	}

	documentation, err := db.LookupOnActionDocumentationContract(ctx, Config{Sources: []Source{
		{Name: "game", Path: private, Rank: 1},
		{Name: "vanilla", Path: vanilla, Rank: 3},
	}}, "ON_PROJECTION_FIXTURE", 8)
	if err != nil {
		t.Fatal(err)
	}
	if documentation.Status != "documented" || documentation.Selection != "unique" || !documentation.ReviewOnly || !documentation.EngineEvidenceAvailable || len(documentation.Candidates) != 1 {
		t.Fatalf("unexpected documentation envelope: %+v", documentation)
	}
	candidate := documentation.Candidates[0]
	if candidate.SourceRef == nil || candidate.SourceRef.Path != "common/on_action/fixture.txt" || candidate.Root.Status != "explicit" || candidate.Root.DocumentedType != "character" || !candidate.EngineRuleFound || candidate.ReviewStatus != "engine_none_with_documented_root" || candidate.Confidence != "medium" {
		t.Fatalf("safe projected root evidence was not retained: %+v", candidate)
	}
	if len(candidate.Bindings) != 2 || candidate.Bindings[0].Name != "target" || candidate.Bindings[0].ValueKind != "documented_type" || candidate.Bindings[0].DocumentedType != "character" || candidate.Bindings[1].Name != "reason" || candidate.Bindings[1].ValueKind != "flag" || candidate.DynamicBindingCount != 1 {
		t.Fatalf("projection leaked or lost binding classification: %+v", candidate)
	}
	encoded, err := json.Marshal(documentation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_DOC_SENTINEL") || strings.Contains(string(encoded), "<flag_type>") || strings.Contains(string(encoded), filepath.Base(private)) || strings.Contains(string(encoded), `"source":"`) {
		t.Fatalf("documentation projection leaked comment prose, dynamic target, or source identity: %s", encoded)
	}
}

func TestLookupOnActionDocumentationContractDoesNotMixUnpublishedEngineRows(t *testing.T) {
	ctx := context.Background()
	vanilla := t.TempDir()
	writeOnActionDocumentationFixture(t, vanilla, "fixture.txt", `# Root = character
on_unpublished_fixture = { }
`)
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','finalizing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('on_unpublished_fixture','on_action','none','','fixture','on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	documentation, err := db.LookupOnActionDocumentationContract(ctx, Config{Sources: []Source{{Name: "vanilla", Path: vanilla, Rank: 3}}}, "on_unpublished_fixture", 8)
	if err != nil {
		t.Fatal(err)
	}
	if documentation.Status != "documented" || documentation.EngineEvidenceAvailable || len(documentation.Candidates) != 1 || documentation.Candidates[0].EngineRuleFound || documentation.Candidates[0].ReviewStatus != "engine_evidence_unavailable" {
		t.Fatalf("unpublished rows leaked into documentation comparison: %+v", documentation)
	}
}

func TestLookupOnActionDocumentationContractUsesReadyLocatorMissWithoutFullSourceWalk(t *testing.T) {
	ctx := context.Background()
	vanilla := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','ready')`); err != nil {
		t.Fatal(err)
	}

	// There is no common/on_action directory to walk. A ready index that has no
	// declaration locator for this key should return the bounded locator miss,
	// not repeatedly scan the full source tree for every unknown query.
	documentation, err := db.LookupOnActionDocumentationContract(ctx, Config{Sources: []Source{{Name: "vanilla", Path: vanilla, Rank: 3}}}, "on_absent_fixture", 8)
	if err != nil {
		t.Fatal(err)
	}
	if documentation.Status != "not_documented" || documentation.Selection != "none" || len(documentation.Candidates) != 0 {
		t.Fatalf("ready locator miss did not stay bounded: %+v", documentation)
	}
}

func TestLookupOnActionDocumentationContractBoundsAmbiguousCandidates(t *testing.T) {
	ctx := context.Background()
	vanilla := t.TempDir()
	writeOnActionDocumentationFixture(t, vanilla, "a.txt", `# Root = character
on_ambiguous_fixture = { }
`)
	writeOnActionDocumentationFixture(t, vanilla, "b.txt", `# Root = county
on_ambiguous_fixture = { }
`)
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	documentation, err := db.LookupOnActionDocumentationContract(ctx, Config{Sources: []Source{{Name: "vanilla", Path: vanilla, Rank: 3}}}, "on_ambiguous_fixture", 1)
	if err != nil {
		t.Fatal(err)
	}
	if documentation.Status != "ambiguous" || documentation.Selection != "multiple" || !documentation.Truncated || len(documentation.Candidates) != 1 || documentation.Candidates[0].SourceRef == nil || documentation.Candidates[0].SourceRef.Path != "common/on_action/a.txt" {
		t.Fatalf("ambiguous documentation was not bounded deterministically: %+v", documentation)
	}
}

func writeOnActionDocumentationFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "common", "on_action")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func contractByName(t *testing.T, contracts []OnActionScopeContract, name string) OnActionScopeContract {
	t.Helper()
	contract, ok := contractByNameOptional(contracts, name)
	if !ok {
		t.Fatalf("missing %s in %+v", name, contracts)
	}
	return contract
}

func contractByNameOptional(contracts []OnActionScopeContract, name string) (OnActionScopeContract, bool) {
	for _, contract := range contracts {
		if contract.Name == name {
			return contract, true
		}
	}
	return OnActionScopeContract{}, false
}
