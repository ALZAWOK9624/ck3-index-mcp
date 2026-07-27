package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestPlanRebaseProjectMapAuthorityRewritesInheritedProvinceHistory(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "10;1;2;3;project;x\n", "20;1;2;3;target;x\n")
	rel := "history/provinces/00_test.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = old }\n")
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "20 = { culture = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("project-map plan blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "write_candidate" || decision.Adapter != "exact_rgb_province_mapping" {
		t.Fatalf("inherited province history was not replayed through RGB mapping: %+v", decision)
	}
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "10 =") || strings.Contains(got, "20 =") {
		t.Fatalf("history candidate did not use project province ID: %q", got)
	}
}

func TestPlanRebaseProjectMapAuthorityMergesMappedProvinceHistoryFields(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "10;1;2;3;project;x\n", "20;1;2;3;target;x\n")
	rel := "history/provinces/00_test.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = old faith = old }\n")
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), "10 = { culture = project faith = old }\n")
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "20 = { culture = old faith = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("mapped province merge blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "write_candidate" {
		t.Fatalf("mapped province history did not make a semantic candidate: %+v", decision)
	}
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	merged := string(data)
	for _, want := range []string{"10 =", "culture = project", "faith = target"} {
		if !strings.Contains(merged, want) {
			t.Fatalf("mapped merge is missing %q: %q", want, merged)
		}
	}
}

func TestPlanRebaseProjectMapAuthorityBlocksUnmappedTargetProvinceReference(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "10;1;2;3;project;x\n", "20;1;2;3;target;x\n30;9;9;9;new;x\n")
	rel := "history/provinces/00_test.txt"
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "30 = { culture = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "exact_rgb_province_mapping_unresolved") {
		t.Fatalf("unmapped target province reference was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
}

func TestPlanRebaseProjectMapAuthorityBlocksTargetOnlyCoreMapAsset(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "10;1;2;3;project;x\n", "20;1;2;3;target;x\n")
	asset := "map_data/positions.txt"
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(asset)), "province = 20\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "map_project_asset_unproven") {
		t.Fatalf("target-only project-authority map asset was inherited: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, asset)
	if decision.Action != "conflict" || decision.Action == "inherit_target" {
		t.Fatalf("target-only map asset decision=%+v", decision)
	}
}

func TestPlanRebaseProjectMapAuthorityPreservesBaseReferenceWhenTargetDeletesIt(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "10;1;2;3;project;x\n", "20;1;2;3;target;x\n")
	rel := "history/provinces/00_deleted_upstream.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = base }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("project map authority blocked target deletion without reason: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "write_candidate" || decision.CandidatePath == "" {
		t.Fatalf("target deletion silently removed project-authority history: %+v", decision)
	}
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil || !strings.Contains(string(data), "10 =") {
		t.Fatalf("preserved history was not rewritten to project ID: %q err=%v", data, err)
	}
}

func TestPlanRebaseTargetMapAuthorityReplaysCustomizedReferenceWithExactRGB(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeText(t, fixture.profile, `schema_version = 2
name = "target-map-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "target"
unknown_policy = "block"
validation_sources = []
`)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "1;1;2;3;project;x\n", "2;1;2;3;target;x\n")
	rel := "history/provinces/00_custom.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = base }\n")
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), "1 = { culture = project }\n")
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "2 = { culture = base faith = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("target authority did not safely replay exact-RGB project reference: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "write_candidate" {
		t.Fatalf("target authority did not materialize rewritten reference: %+v", decision)
	}
	data, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.CandidatePath)))
	if err != nil || !strings.Contains(string(data), "2 =") || strings.Contains(string(data), "1 =") {
		t.Fatalf("target-authority history was not rewritten to target ID: %q err=%v", data, err)
	}
}

func TestPlanRebaseTargetMapAuthorityBlocksUnmappedProjectReference(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeText(t, fixture.profile, `schema_version = 2
name = "target-map-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "target"
unknown_policy = "block"
validation_sources = []
`)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "3;9;9;9;project-only;x\n", "2;1;2;3;target;x\n")
	rel := "history/provinces/00_custom.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = base }\n")
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), "3 = { culture = project }\n")
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "2 = { culture = base faith = target }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "exact_rgb_province_mapping_unresolved") {
		t.Fatalf("unmapped project province was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" {
		t.Fatalf("unmapped project province reference was not blocked: %+v", decision)
	}
}

func TestPlanRebaseTargetMapAuthorityBlocksCustomizedTitleProvinceReference(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeText(t, fixture.profile, `schema_version = 2
name = "target-map-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "target"
unknown_policy = "block"
validation_sources = []
`)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "1;1;2;3;project;x\n", "2;1;2;3;target;x\n")
	rel := "history/titles/00_custom_titles.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "k_test = { province = 1 }\n")
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), "k_test = { province = 1 capital = 1 }\n")
	writeText(t, filepath.Join(fixture.target, filepath.FromSlash(rel)), "k_test = { province = 2 }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "title_history_province_reference_unproven") {
		t.Fatalf("target-authority title reference was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" {
		t.Fatalf("target-authority title reference was not blocked: %+v", decision)
	}
	for _, conflict := range transaction.Conflicts {
		if conflict.Code == "title_history_province_reference_unproven" {
			for _, action := range conflict.AllowedActions {
				if action == "keep_project" {
					t.Fatalf("target-authority title conflict allowed unsafe keep_project: %+v", conflict)
				}
			}
		}
	}
}

func TestPlanRebaseTargetMapAuthorityBlocksTargetDeleteOfModifiedReference(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeText(t, fixture.profile, `schema_version = 2
name = "target-map-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "target"
unknown_policy = "block"
validation_sources = []
`)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;base;x\n", "1;1;2;3;project;x\n", "2;1;2;3;target;x\n")
	rel := "history/provinces/00_deleted_upstream.txt"
	writeText(t, filepath.Join(fixture.base, filepath.FromSlash(rel)), "1 = { culture = base }\n")
	writeText(t, filepath.Join(fixture.project, filepath.FromSlash(rel)), "1 = { culture = project }\n")

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "map_reference_delete_modify_conflict") {
		t.Fatalf("target deletion of modified map reference was not blocked: status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" {
		t.Fatalf("target deletion of modified map reference was materialized: %+v", decision)
	}
}

func TestPlanRebaseDisabledMapAuthorityBlocksDivergentCoreMap(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	project := filepath.Join(root, "project")
	target := filepath.Join(root, "target")
	for _, directory := range []string{base, project, target} {
		if err := os.MkdirAll(filepath.Join(directory, "map_data"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "map_data", "definition.csv"), "1;1;2;3;base;x\n")
	writeText(t, filepath.Join(target, "map_data", "definition.csv"), "2;1;2;3;target;x\n")
	profile := filepath.Join(root, "profile.toml")
	writeText(t, profile, `schema_version = 2
name = "fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "disabled"
unknown_policy = "block"
validation_sources = []
`)
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), Sources: []indexer.Source{
		{Name: "project", Path: project, Rank: 1, Role: indexer.SourceRoleProject},
		{Name: "base", Path: base, Rank: 2, Role: indexer.SourceRoleDependency},
		{Name: "target", Path: target, Rank: 3, Role: indexer.SourceRoleDependency},
	}}
	transaction, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: filepath.Join(root, "out")}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusNeedsReview || !hasRebaseAdditionalConflictCode(transaction, "map_migration_disabled") {
		t.Fatalf("disabled map migration did not block divergent core map: %+v", transaction)
	}
}

func TestPlanRebaseProjectMapAuthorityUsesCoordinateDeltaRaster(t *testing.T) {
	fixture := newRebaseProjectMapFixture(t)
	writeRebaseProjectMapDefinitions(t, fixture, "1;1;2;3;land;x\n", "1;1;2;3;land;x\n", "1;1;2;3;land;x\n")
	base := testRebaseRaster(2, 1,
		0x00, 0x00, 0x00, 0xff,
		0x00, 0x00, 0x00, 0xff,
	)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 1, 0, 0x00, 0x00, 0xff, 0xff)
	writeRebaseSemanticAdditionalBytes(t, filepath.Join(fixture.base, "map_data", "provinces.png"), mustEncodeTestRebasePNG(t, base))
	writeRebaseSemanticAdditionalBytes(t, filepath.Join(fixture.project, "map_data", "provinces.png"), mustEncodeTestRebasePNG(t, project))
	writeRebaseSemanticAdditionalBytes(t, filepath.Join(fixture.target, "map_data", "provinces.png"), mustEncodeTestRebasePNG(t, target))

	transaction := fixture.plan(t)
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("coordinate map plan status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	decision := decisionForPath(t, transaction, "map_data/provinces.png")
	if decision.Adapter != "map_coordinate_delta" || decision.Action != "write_candidate" || decision.MapDelta == nil || decision.MapDelta.ChangedPixels != 1 {
		t.Fatalf("coordinate map decision = %+v", decision)
	}
	if transaction.Counts["map_coordinate_delta_files"] != 1 || transaction.Counts["map_coordinate_delta_pixels"] != 1 {
		t.Fatalf("coordinate map counters = %+v", transaction.Counts)
	}
	delta, err := os.ReadFile(filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(decision.MapDelta.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if hashBytes(delta) != decision.MapDelta.SHA256 {
		t.Fatal("stored coordinate delta hash differs from transaction metadata")
	}
}

type rebaseProjectMapFixture struct {
	base, project, target, artifacts, output, profile string
	cfg                                               indexer.Config
}

func newRebaseProjectMapFixture(t *testing.T) rebaseProjectMapFixture {
	t.Helper()
	root := t.TempDir()
	fixture := rebaseProjectMapFixture{
		base: filepath.Join(root, "base"), project: filepath.Join(root, "project"), target: filepath.Join(root, "target"),
		artifacts: filepath.Join(root, "artifacts"), output: filepath.Join(root, "migration-copy"), profile: filepath.Join(root, "profile.toml"),
	}
	for _, directory := range []string{fixture.base, fixture.project, fixture.target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, fixture.profile, `schema_version = 2
name = "project-map-fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = "project"
unknown_policy = "block"
validation_sources = []
`)
	fixture.cfg = indexer.Config{ArtifactRoot: fixture.artifacts, Sources: []indexer.Source{
		{Name: "project", Path: fixture.project, Rank: 1, Role: indexer.SourceRoleProject},
		{Name: "base", Path: fixture.base, Rank: 2, Role: indexer.SourceRoleDependency},
		{Name: "target", Path: fixture.target, Rank: 3, Role: indexer.SourceRoleDependency},
	}}
	return fixture
}

func writeRebaseProjectMapDefinitions(t *testing.T, fixture rebaseProjectMapFixture, base, project, target string) {
	t.Helper()
	writeText(t, filepath.Join(fixture.base, "map_data", "definition.csv"), base)
	writeText(t, filepath.Join(fixture.project, "map_data", "definition.csv"), project)
	writeText(t, filepath.Join(fixture.target, "map_data", "definition.csv"), target)
}

func (fixture rebaseProjectMapFixture) plan(t *testing.T) RebaseTransaction {
	t.Helper()
	transaction, err := PlanRebase(context.Background(), fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}
