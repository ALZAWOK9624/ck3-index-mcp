package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

// Promotion replaces the whole project directory. If the main index keeps
// reporting a ready generation afterwards, queries answer from the previous
// project's object graph while re-reading the migrated files from disk, so the
// cache must be invalidated before the first rename rather than after the last.
func TestPromoteRebaseInvalidatesTheMainIndex(t *testing.T) {
	ctx := context.Background()
	fixture := newRebaseConcurrencyFixture(t)
	databasePath := filepath.Join(t.TempDir(), "index.sqlite")
	db, err := indexer.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	transaction := runRebaseToSmokeApproval(t, ctx, fixture)
	transaction, err = PromoteRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Promotion == nil || !transaction.Promotion.IndexInvalidated {
		t.Fatalf("promotion receipt did not record the index invalidation: %+v", transaction.Promotion)
	}
	if strings.TrimSpace(transaction.Promotion.IndexRecovery) == "" {
		t.Fatal("promotion receipt does not tell the operator how to recover the index")
	}
	// A published index is marked stale in place; see the indexer package for
	// the state transition itself. Here the contract under test is that
	// promotion refuses to complete unless the invalidation succeeded.
	state, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Ready() {
		t.Fatalf("index reports ready after the project tree was replaced: %+v", state)
	}
}

// preserve_paths names external state that must not be copied through the
// transaction: it churns during a long migration, invalidates fingerprints for
// no reason, and can be enormous. It must instead stay with whichever
// directory is the formal project.
func TestPromoteRebaseCarriesPreservedProjectStateInsteadOfCopyingIt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(project, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(target, "common", "shadow.txt"), "value = target\n")
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	writeText(t, filepath.Join(project, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeText(t, filepath.Join(project, "cache", "index.bin"), "rebuildable\n")

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
owned_prefixes = ["k10_"]
validation_sources = []
preserve_paths = [".git", "cache"]
`)
	fixture := rebaseConcurrencyFixture{
		cfg:     rebaseLifecycleConfig(root, project, base, target),
		profile: profile,
		output:  filepath.Join(root, "migration-copy"),
	}
	transaction := runRebaseToSmokeApproval(t, ctx, fixture)

	// The migration copy carries Mod content only.
	if _, err := os.Stat(filepath.Join(fixture.output, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf("preserved VCS metadata was copied into the migration copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.output, "common", "k10_custom.txt")); err != nil {
		t.Fatalf("migration copy is missing project content: %v", err)
	}

	if _, err := PromoteRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	// After promotion the preserved entries live with the promoted project and
	// are gone from the backup: exactly one copy, never discarded.
	for _, rel := range []string{".git/HEAD", "cache/index.bin", "common/k10_custom.txt"} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("promoted project is missing %s: %v", rel, err)
		}
	}
	backup, err := rebaseBackupPath(project, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backup, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf("preserved state was duplicated into the backup: %v", err)
	}
}

// Automatic Jomini merge is only defensible where the top-level key names one
// CK3 object. Anything else must stop for review instead of being merged with
// a generic identity.
func TestPlanRebaseBlocksSemanticMergeOutsideTheAllowlist(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rel := "gfx/models/units.asset"
	writeText(t, filepath.Join(base, filepath.FromSlash(rel)), "entity = {\n\tname = \"a\"\n}\n")
	writeText(t, filepath.Join(project, filepath.FromSlash(rel)), "entity = {\n\tname = \"a\"\n\tscale = 2\n}\n")
	writeText(t, filepath.Join(target, filepath.FromSlash(rel)), "entity = {\n\tname = \"a\"\n\tclone = yes\n}\n")

	fixture := rebaseConcurrencyFixture{
		cfg:     rebaseLifecycleConfig(root, project, base, target),
		profile: writeRebaseLifecycleProfile(t, root),
		output:  filepath.Join(root, "migration-copy"),
	}
	transaction, err := PlanRebase(ctx, fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionForPath(t, transaction, rel)
	if decision.Action != "conflict" {
		t.Fatalf("non-allowlisted both-changed file was auto-merged: %+v", decision)
	}
	found := false
	for _, conflict := range transaction.Conflicts {
		if conflict.Code == "semantic_domain_not_allowlisted" && conflict.Path == rel {
			found = true
		}
	}
	if !found {
		t.Fatalf("no allowlist conflict was recorded: %+v", transaction.Conflicts)
	}
}

func TestGUISemanticIdentityDistinguishesRootWidgetsByName(t *testing.T) {
	parsed := parseRebaseJomini("gui/window.gui", `window = {
	name = "first_window"
}
window = {
	name = "second_window"
}
template my_template {
	size = { 10 10 }
}
`)
	if len(parsed.Errors) > 0 {
		t.Fatalf("GUI fixture did not parse: %v", parsed.Errors)
	}
	var ids []string
	for _, node := range parsed.Nodes {
		id, stable := semanticNodeID("gui/window.gui", node)
		if !stable {
			t.Fatalf("named GUI declaration reported an unstable identity: %s", id)
		}
		ids = append(ids, id)
	}
	want := []string{"gui:root:window:first_window", "gui:root:window:second_window", "gui:template:my_template"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("GUI semantic ids = %v, want %v", ids, want)
	}
}

func TestGUISemanticIdentityMarksUnnamedRootWidgetsUnstable(t *testing.T) {
	parsed := parseRebaseJomini("gui/window.gui", "window = {\n\tsize = { 10 10 }\n}\n")
	if len(parsed.Errors) > 0 {
		t.Fatalf("GUI fixture did not parse: %v", parsed.Errors)
	}
	if len(parsed.Nodes) != 1 {
		t.Fatalf("expected one root widget, got %d", len(parsed.Nodes))
	}
	if _, stable := semanticNodeID("gui/window.gui", parsed.Nodes[0]); stable {
		t.Fatal("an unnamed root widget was treated as a stable object identity")
	}
}

func runRebaseToSmokeApproval(t *testing.T, ctx context.Context, fixture rebaseConcurrencyFixture) RebaseTransaction {
	t.Helper()
	transaction, err := PlanRebase(ctx, fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("plan status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	if transaction, err = BuildRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	if transaction, err = ValidateRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	if transaction, err = ApproveRebaseSmoke(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatal(err)
	}
	return transaction
}
