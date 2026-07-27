package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestPlanRebasePrunesShadowAndMergesDisjointJominiFields(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	project := filepath.Join(root, "project")
	target := filepath.Join(root, "target")
	artifacts := filepath.Join(root, "artifacts")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{base, project, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(project, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(target, "common", "shadow.txt"), "value = target\n")
	writeText(t, filepath.Join(base, "common", "cultures.txt"), `culture_a = {
	ethos = old
	traditions = { tradition_old }
}
`)
	writeText(t, filepath.Join(project, "common", "cultures.txt"), `culture_a = {
	ethos = old
	traditions = { tradition_old tradition_project }
}
`)
	writeText(t, filepath.Join(target, "common", "cultures.txt"), `culture_a = {
	ethos = target
	traditions = { tradition_old }
}
`)
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")

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
`)
	cfg := indexer.Config{ArtifactRoot: artifacts, Sources: []indexer.Source{
		{Name: "project", Path: project, Rank: 1, Role: indexer.SourceRoleProject},
		{Name: "base", Path: base, Rank: 2, Role: indexer.SourceRoleDependency},
		{Name: "target", Path: target, Rank: 3, Role: indexer.SourceRoleDependency},
	}}
	transaction, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("plan status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	shadow := decisionForPath(t, transaction, "common/shadow.txt")
	if shadow.Classification != "shadow_copy" || shadow.Action != "delete_project" {
		t.Fatalf("shadow decision=%+v", shadow)
	}
	custom := decisionForPath(t, transaction, "common/k10_custom.txt")
	if custom.Classification != "project_added" || custom.Action != "keep_project" {
		t.Fatalf("custom decision=%+v", custom)
	}
	culture := decisionForPath(t, transaction, "common/cultures.txt")
	if culture.Action != "write_candidate" || culture.CandidatePath == "" || !culture.Safe {
		t.Fatalf("culture decision=%+v", culture)
	}
	candidate, err := os.ReadFile(filepath.Join(artifacts, "rebase-transactions", transaction.ID, filepath.FromSlash(culture.CandidatePath)))
	if err != nil {
		t.Fatal(err)
	}
	text := string(candidate)
	if !strings.Contains(text, "ethos = target") || !strings.Contains(text, "tradition_project") {
		t.Fatalf("semantic candidate did not retain disjoint changes:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(artifacts, "rebase-transactions", transaction.ID, rebaseReportName)); err != nil {
		t.Fatalf("plan did not publish review report: %v", err)
	}
}

func TestPlanRebaseRequiresExplicitMapAuthority(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	project := filepath.Join(root, "project")
	target := filepath.Join(root, "target")
	for _, directory := range []string{base, project, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "map_data", "definition.csv"), "1;1;2;3;land;x\n")
	writeText(t, filepath.Join(target, "map_data", "definition.csv"), "1;3;2;1;land;x\n")
	profile := filepath.Join(root, "profile.toml")
	writeText(t, profile, `schema_version = 2
name = "fixture"
project = "project"
base = "base"
target = "target"
migration_mode = "same_game_version"
base_game_version = "1.19.*"
target_game_version = "1.19.*"
map_authority = ""
unknown_policy = "block"
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
	if transaction.Status != RebaseStatusNeedsReview {
		t.Fatalf("status=%s want needs review", transaction.Status)
	}
	found := false
	for _, conflict := range transaction.Conflicts {
		found = found || conflict.Code == "map_authority_required"
	}
	if !found {
		t.Fatalf("missing map authority conflict: %+v", transaction.Conflicts)
	}
}

func decisionForPath(t *testing.T, transaction RebaseTransaction, path string) RebaseFileDecision {
	t.Helper()
	for _, decision := range transaction.Files {
		if decision.Path == path {
			return decision
		}
	}
	t.Fatalf("decision missing for %s", path)
	return RebaseFileDecision{}
}
