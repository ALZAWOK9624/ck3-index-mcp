package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestRebaseLifecycleBuildValidatePromoteAndRollback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(project, "common", "shadow.txt"), "value = old\n")
	writeText(t, filepath.Join(target, "common", "shadow.txt"), "value = target\n")
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")

	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusReadyToBuild {
		t.Fatalf("plan status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}

	transaction, err = BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusBuilt {
		t.Fatalf("build status=%s", transaction.Status)
	}
	if _, err := os.Stat(filepath.Join(output, "common", "shadow.txt")); !os.IsNotExist(err) {
		t.Fatalf("byte-identical upstream shadow survived in migration overlay: %v", err)
	}
	copyData, err := os.ReadFile(filepath.Join(output, "common", "k10_custom.txt"))
	if err != nil || string(copyData) != "k10_value = yes\n" {
		t.Fatalf("project-owned overlay was not copied: %q err=%v", copyData, err)
	}
	original, err := os.ReadFile(filepath.Join(project, "common", "shadow.txt"))
	if err != nil || string(original) != "value = old\n" {
		t.Fatalf("build changed formal project: %q err=%v", original, err)
	}

	transaction, err = ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusValidated || transaction.Validation.Blocked || transaction.Validation.CopySHA256 == "" {
		t.Fatalf("validation did not produce a promotable copy: %+v", transaction.Validation)
	}
	if transaction.Validation.SmokeApproved {
		t.Fatal("validation incorrectly implied an in-game smoke approval")
	}
	if _, err := PromoteRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil {
		t.Fatal("promotion succeeded without smoke approval")
	}

	transaction, err = ApproveRebaseSmoke(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = PromoteRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusPromoted || transaction.Promotion == nil {
		t.Fatalf("promotion did not persist receipt: %+v", transaction)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("promoted migration copy unexpectedly remains at output path: %v", err)
	}
	promotedCustom, err := os.ReadFile(filepath.Join(project, "common", "k10_custom.txt"))
	if err != nil || string(promotedCustom) != "k10_value = yes\n" {
		t.Fatalf("promoted project does not contain reviewed overlay: %q err=%v", promotedCustom, err)
	}
	backupShadow, err := os.ReadFile(filepath.Join(transaction.Promotion.BackupPath, "common", "shadow.txt"))
	if err != nil || string(backupShadow) != "value = old\n" {
		t.Fatalf("recoverable formal-project backup is missing: %q err=%v", backupShadow, err)
	}

	transaction, err = RollbackRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusRolledBack || transaction.Promotion.RolledBackAt == "" {
		t.Fatalf("rollback receipt is incomplete: %+v", transaction.Promotion)
	}
	restoredShadow, err := os.ReadFile(filepath.Join(project, "common", "shadow.txt"))
	if err != nil || string(restoredShadow) != "value = old\n" {
		t.Fatalf("formal project was not restored: %q err=%v", restoredShadow, err)
	}
	if _, err := os.Stat(filepath.Join(output, "common", "k10_custom.txt")); err != nil {
		t.Fatalf("promoted copy was not retained for recovery after rollback: %v", err)
	}
}

func TestBuildRebaseRejectsInputDriftWithoutOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = before\n")
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = after\n")
	if _, err := BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "project input changed") {
		t.Fatalf("project drift did not block build: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("drift blocker created an output overlay: %v", err)
	}
}

func TestBuildRebaseRejectsByteIdenticalProjectSourceRetarget(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	retarget := filepath.Join(root, "project-retarget")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, retarget, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{project, retarget} {
		writeText(t, filepath.Join(directory, "common", "k10_custom.txt"), "k10_value = same\n")
	}
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sources[0].Path = retarget
	if _, err := BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "configured project source root changed") {
		t.Fatalf("byte-identical source retarget did not block build: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("source identity blocker created an output overlay: %v", err)
	}
}

func TestRollbackRebaseRefusesChangedPromotedProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = yes\n")
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(ctx, cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = BuildRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ValidateRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ApproveRebaseSmoke(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = PromoteRebase(ctx, cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(project, "common", "k10_custom.txt"), "k10_value = changed_after_promote\n")
	if _, err := RollbackRebase(ctx, cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "formal project changed") {
		t.Fatalf("changed promoted project did not block rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(transaction.Promotion.BackupPath, "common", "k10_custom.txt")); err != nil {
		t.Fatalf("rollback safety check damaged recoverable backup: %v", err)
	}
}

func writeRebaseLifecycleProfile(t *testing.T, root string) string {
	t.Helper()
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
	return profile
}

func rebaseLifecycleConfig(root, project, base, target string) indexer.Config {
	return indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts"), Sources: []indexer.Source{
		{Name: "project", Path: project, Rank: 1, Role: indexer.SourceRoleProject},
		{Name: "base", Path: base, Rank: 2, Role: indexer.SourceRoleDependency},
		{Name: "target", Path: target, Rank: 3, Role: indexer.SourceRoleDependency},
	}}
}
