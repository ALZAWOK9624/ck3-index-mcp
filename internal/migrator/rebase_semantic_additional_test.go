package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

// These tests deliberately exercise the boundary where a syntactically valid
// file-level diff is not enough evidence for an automatic CK3 rebase.  A
// failed test here means the planner must stop for review rather than publish
// a plausible-looking overlay that changes semantic ownership.

func TestPlanRebaseBlocksDuplicateTopLevelSemanticID(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	rel := "common/culture/cultures/duplicate.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), `culture_a = {
	ethos = old
}
`)
	// This is deliberately non-overlapping at the field level.  If the
	// duplicate ID is ignored, it would otherwise look like a clean merge.
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), `culture_a = {
	ethos = old
}
culture_a = {
	ethos = project
}
`)
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), `culture_a = {
	ethos = old
	traditions = { tradition_target }
}
`)

	transaction := fixture.plan(t)
	if transaction.Status == RebaseStatusReadyToBuild {
		t.Fatalf("duplicate semantic ID was treated as uniquely mergeable: decision=%+v conflicts=%+v", decisionForPath(t, transaction, rel), transaction.Conflicts)
	}
	if !hasRebaseAdditionalConflict(transaction, rel) {
		t.Fatalf("duplicate semantic ID did not produce a blocking conflict: %+v", transaction)
	}
}

func TestPlanRebaseDoesNotSilentlyAcceptAmbiguousCrossFileSemanticMove(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	original := "common/culture/cultures/original.txt"
	moved := "common/culture/cultures/moved.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(original)), `culture_a = {
	ethos = old
}
`)
	// A project author has effectively relocated culture_a into a dedicated
	// file.  The target independently changes the old location.  Keeping both
	// files without a semantic reconciliation would leave two live culture_a
	// definitions in the CK3 loading stack.
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(moved)), `culture_a = {
	ethos = project
}
`)
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(original)), `culture_a = {
	ethos = old
	traditions = { tradition_target }
}
`)

	transaction := fixture.plan(t)
	movedDecision := decisionForPath(t, transaction, moved)
	if movedDecision.Action == "delete_project" || movedDecision.Action == "inherit_target" || !movedDecision.Result.Exists {
		t.Fatalf("cross-file project object was silently dropped: %+v", movedDecision)
	}
	if transaction.Status == RebaseStatusReadyToBuild {
		originalDecision := decisionForPath(t, transaction, original)
		if originalDecision.Action == "inherit_target" && movedDecision.Action == "keep_project" {
			t.Fatalf("ambiguous cross-file semantic move produced an unsafe ready plan: original=%+v moved=%+v", originalDecision, movedDecision)
		}
	}
}

func TestPlanRebaseLocalizationMergePreservesTargetBOMAndCRLF(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	rel := "localization/english/rebase_l_english.yml"
	base := "\ufeffl_english:\r\n key_project:0 \"old project\"\r\n key_target:0 \"old target\"\r\n"
	project := "\ufeffl_english:\r\n key_project:0 \"project value\"\r\n key_target:0 \"old target\"\r\n"
	target := "\ufeffl_english:\r\n key_project:0 \"old project\"\r\n key_target:0 \"target value\"\r\n"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), base)
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), project)
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), target)

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("disjoint localization changes should merge: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "write_candidate" || decision.CandidatePath == "" {
		t.Fatalf("localization merge did not materialize a candidate: %+v", decision)
	}
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	merged := string(data)
	if !strings.HasPrefix(merged, "\ufeff") {
		t.Fatalf("candidate lost target UTF-8 BOM: %q", merged)
	}
	if strings.Contains(strings.ReplaceAll(merged, "\r\n", ""), "\n") {
		t.Fatalf("candidate introduced bare LF line endings: %q", merged)
	}
	if !strings.Contains(merged, "project value") || !strings.Contains(merged, "target value") {
		t.Fatalf("candidate did not retain both disjoint localization changes: %q", merged)
	}
}

func TestParseRebaseLocalizationDocumentTracksBOMAndCRLFValueSpan(t *testing.T) {
	text := "\ufeffl_english:\r\n key_one:0 \"old one\" # keep\r\n"
	document, err := parseRebaseLocalizationDocument(text)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := document.Records["key_one"]
	if !ok {
		t.Fatalf("key was not parsed: %+v", document.Records)
	}
	if document.LineEnding != "\r\n" || document.Raw[record.ValueStart:record.ValueEnd] != "\"old one\"" {
		t.Fatalf("BOM/CRLF source offsets were not preserved: ending=%q value=%q", document.LineEnding, document.Raw[record.ValueStart:record.ValueEnd])
	}
}

func TestPlanRebaseLocalizationMergeChangesOnlyTargetValueSpan(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	rel := "localization/english/rebase_preserve_l_english.yml"
	base := "\ufeffl_english:\r\n key_z:0 \"old z\"\r\n key_a:0 \"old a\"\r\n"
	project := "\ufeffl_english:\r\n key_z:0 \"project z\"\r\n key_a:0 \"old a\"\r\n"
	target := "\ufeffl_english:\r\n# target preamble\r\n  key_z:7 \"old z\" # retain this comment\r\n\r\n  key_a:0 \"target a\"\r\n# target tail\r\n"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), base)
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), project)
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), target)

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("disjoint localization changes should merge without rebuilding target text: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(target, "\"old z\"", "\"project z\"", 1)
	if string(data) != expected {
		t.Fatalf("localization merge rewrote surrounding target content\nwant: %q\n got: %q", expected, string(data))
	}
	if !strings.Contains(string(data), "key_z:7 \"project z\" # retain this comment") {
		t.Fatalf("localization merge did not preserve target version and inline comment: %q", string(data))
	}
}

func TestPlanRebaseLocalizationMergeAppendsNewKeyWithoutReorderingTarget(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	rel := "localization/english/rebase_append_l_english.yml"
	base := "\ufeffl_english:\r\n shared_key:0 \"old\"\r\n"
	project := "\ufeffl_english:\r\n shared_key:0 \"old\"\r\n# project-owned addition\r\n project_added:0 \"new\" # copied as one new record\r\n"
	target := "\ufeffl_english:\r\n# target ordering and comments stay in place\r\n shared_key:0 \"target\"\r\n# target tail\r\n"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), base)
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), project)
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), target)

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("target-only change plus project key addition should merge: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	expected := target + " project_added:0 \"new\" # copied as one new record\r\n"
	if string(data) != expected {
		t.Fatalf("localization addition changed target order or surrounding content\nwant: %q\n got: %q", expected, string(data))
	}
}

func TestPlanRebaseBlocksAmbiguousLocalizationSyntax(t *testing.T) {
	for name, invalidTarget := range map[string]string{
		"duplicate_key":       "l_english:\n key_one:0 \"target one\"\n key_one:0 \"target duplicate\"\n",
		"multiple_headers":    "l_english:\n key_one:0 \"target one\"\nl_french:\n key_two:0 \"bonjour\"\n",
		"header_after_record": "key_one:0 \"target one\"\nl_english:\n",
		"unsupported_line":    "l_english:\n key_one:0 target value without quotes\n",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRebaseSemanticAdditionalFixture(t)
			rel := "localization/english/rebase_invalid_l_english.yml"
			base := "l_english:\n key_one:0 \"old one\"\n"
			project := "l_english:\n key_one:0 \"project one\"\n"
			writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), base)
			writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), project)
			writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), invalidTarget)

			transaction := fixture.plan(t)
			if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "localization_parse_conflict") {
				t.Fatalf("ambiguous localization syntax was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
			}
		})
	}
}

func TestPlanRebaseBlocksRasterAddedOnBothSidesWithoutBase(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	rel := "gfx/interface/rebase_test.png"
	projectData, err := encodeRebasePNG(rebaseRaster{width: 1, height: 1, pixels: []byte{0xff, 0x00, 0x00, 0xff}})
	if err != nil {
		t.Fatal(err)
	}
	targetData, err := encodeRebasePNG(rebaseRaster{width: 1, height: 1, pixels: []byte{0x00, 0x00, 0xff, 0xff}})
	if err != nil {
		t.Fatal(err)
	}
	writeRebaseSemanticAdditionalBytes(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), projectData)
	writeRebaseSemanticAdditionalBytes(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), targetData)

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview {
		t.Fatalf("both-added raster was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" || decision.CandidatePath != "" || !hasRebaseAdditionalConflictCode(transaction, "raster_add_conflict") {
		t.Fatalf("both-added raster was sent to an unsafe pixel merge: decision=%+v conflicts=%+v", decision, transaction.Conflicts)
	}
}

func TestPlanRebaseBlocksTargetAdditionUnderReplacePath(t *testing.T) {
	fixture := newRebaseSemanticAdditionalFixture(t)
	writeText(t, filepath.Join(fixture.project, "descriptor.mod"), `name = "fixture"
replace_path = "common/culture"
`)
	rel := "common/culture/cultures/target_added.txt"
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "culture_target = { ethos = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview {
		t.Fatalf("target addition under replace_path was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" || !hasRebaseAdditionalConflictCode(transaction, "replace_path_target_addition") {
		t.Fatalf("replace_path target addition did not require review: decision=%+v conflicts=%+v", decision, transaction.Conflicts)
	}
}

type rebaseSemanticAdditionalFixture struct {
	base      string
	project   string
	target    string
	profile   string
	artifacts string
	output    string
	cfg       indexer.Config
}

func newRebaseSemanticAdditionalFixture(t *testing.T) rebaseSemanticAdditionalFixture {
	t.Helper()
	root := t.TempDir()
	fixture := rebaseSemanticAdditionalFixture{
		base:      filepath.Join(root, "base"),
		project:   filepath.Join(root, "project"),
		target:    filepath.Join(root, "target"),
		profile:   filepath.Join(root, "profile.toml"),
		artifacts: filepath.Join(root, "artifacts"),
		output:    filepath.Join(root, "migration-copy"),
	}
	for _, directory := range []string{fixture.base, fixture.project, fixture.target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, fixture.profile, `schema_version = 2
name = "semantic-additional-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "disabled"
unknown_policy = "block"
owned_prefixes = []
validation_sources = []
`)
	fixture.cfg = indexer.Config{ArtifactRoot: fixture.artifacts, Sources: []indexer.Source{
		{Name: "project", Path: fixture.project, Rank: 1, Role: indexer.SourceRoleProject},
		{Name: "base", Path: fixture.base, Rank: 2, Role: indexer.SourceRoleDependency},
		{Name: "target", Path: fixture.target, Rank: 3, Role: indexer.SourceRoleDependency},
	}}
	return fixture
}

func (fixture rebaseSemanticAdditionalFixture) plan(t *testing.T) RebaseTransaction {
	t.Helper()
	transaction, err := PlanRebase(context.Background(), fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}

func writeRebaseSemanticAdditionalBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasRebaseAdditionalConflict(transaction RebaseTransaction, path string) bool {
	for _, conflict := range transaction.Conflicts {
		if conflict.Path == path {
			return true
		}
	}
	return false
}

func hasRebaseAdditionalConflictCode(transaction RebaseTransaction, code string) bool {
	for _, conflict := range transaction.Conflicts {
		if conflict.Code == code {
			return true
		}
	}
	return false
}
