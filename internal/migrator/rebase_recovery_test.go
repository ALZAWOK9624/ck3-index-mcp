package migrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

func TestPromoteRebaseResumesInterruptedRenameLayouts(t *testing.T) {
	for _, layout := range []string{"backup_moved", "promoted"} {
		t.Run(layout, func(t *testing.T) {
			fixture := newRebaseRecoveryFixture(t)
			rebaseRoot, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			projectPath, err := resolveRebasePath(fixture.project)
			if err != nil {
				t.Fatal(err)
			}
			backupPath, err := rebaseBackupPath(projectPath, fixture.transaction.ID)
			if err != nil {
				t.Fatal(err)
			}
			fixture.transaction.Status = RebaseStatusPromoting
			fixture.transaction.Promotion = &RebasePromotionReceipt{
				Stage: rebasePromotionIntentStage, FormalPath: projectPath, BackupPath: backupPath,
				PreviousSHA256: fixture.transaction.ProjectTreeFingerprint, PromotedSHA256: fixture.transaction.Validation.CopySHA256,
			}
			if err := writeRebaseTransaction(rebaseRoot, &fixture.transaction); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(projectPath, backupPath); err != nil {
				t.Fatal(err)
			}
			if layout == "promoted" {
				if err := os.Rename(fixture.output, projectPath); err != nil {
					t.Fatal(err)
				}
			}

			resumed, err := PromoteRebase(context.Background(), fixture.cfg, fixture.transaction.ID, RebaseOptions{})
			if err != nil {
				t.Fatalf("resume promotion from %s: %v", layout, err)
			}
			if resumed.Status != RebaseStatusPromoted || resumed.Promotion == nil || resumed.Promotion.Stage != rebasePromotionCompleteStage {
				t.Fatalf("promotion was not finalized: %+v", resumed)
			}
			if _, err := os.Stat(filepath.Join(projectPath, "common", "k10_custom.txt")); err != nil {
				t.Fatalf("resumed promotion left formal project missing: %v", err)
			}
			if _, err := os.Stat(backupPath); err != nil {
				t.Fatalf("resumed promotion lost recoverable backup: %v", err)
			}
		})
	}
}

func TestRollbackRebaseResumesInterruptedRenameLayouts(t *testing.T) {
	for _, layout := range []string{"promoted_moved", "rolled_back"} {
		t.Run(layout, func(t *testing.T) {
			fixture := newRebaseRecoveryFixture(t)
			promoted, err := PromoteRebase(context.Background(), fixture.cfg, fixture.transaction.ID, RebaseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			projectPath, err := resolveRebasePath(fixture.project)
			if err != nil {
				t.Fatal(err)
			}
			backupPath, err := rebaseBackupPath(projectPath, promoted.ID)
			if err != nil {
				t.Fatal(err)
			}
			rebaseRoot, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			promoted.Status = RebaseStatusRollingBack
			promoted.Promotion.Stage = rebaseRollbackIntentStage
			if err := writeRebaseTransaction(rebaseRoot, &promoted); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(projectPath, fixture.output); err != nil {
				t.Fatal(err)
			}
			if layout == "rolled_back" {
				if err := os.Rename(backupPath, projectPath); err != nil {
					t.Fatal(err)
				}
			}

			resumed, err := RollbackRebase(context.Background(), fixture.cfg, promoted.ID, RebaseOptions{})
			if err != nil {
				t.Fatalf("resume rollback from %s: %v", layout, err)
			}
			if resumed.Status != RebaseStatusRolledBack || resumed.Promotion == nil || resumed.Promotion.Stage != rebaseRollbackCompleteStage {
				t.Fatalf("rollback was not finalized: %+v", resumed)
			}
			data, err := os.ReadFile(filepath.Join(projectPath, "common", "shadow.txt"))
			if err != nil || string(data) != "value = old\n" {
				t.Fatalf("resumed rollback did not restore formal project: %q err=%v", data, err)
			}
			if _, err := os.Stat(filepath.Join(fixture.output, "common", "k10_custom.txt")); err != nil {
				t.Fatalf("resumed rollback lost promoted migration copy: %v", err)
			}
		})
	}
}

func TestResumeRebaseContinuesRetryableBuildAndValidationStages(t *testing.T) {
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
	transaction, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction.Status = RebaseStatusFailed
	transaction.Progress = RebaseProgress{Phase: "build_failed", Message: "simulated cancellation"}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	transaction, err = ResumeRebase(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil || transaction.Status != RebaseStatusBuilt {
		t.Fatalf("build resume result=%+v err=%v", transaction, err)
	}
	transaction.Status = RebaseStatusFailed
	transaction.Progress = RebaseProgress{Phase: "validate_failed", Message: "simulated cancellation"}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	transaction, err = ResumeRebase(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil || transaction.Status != RebaseStatusValidated {
		t.Fatalf("validation resume result=%+v err=%v", transaction, err)
	}
}

func TestResumeRebaseRestartsInterruptedPlanningWithFreshTransaction(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "migration-copy")
	for _, directory := range []string{project, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	profile := writeRebaseLifecycleProfile(t, root)
	cfg := rebaseLifecycleConfig(root, project, base, target)
	failed, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err == nil || failed.Status != RebaseStatusFailed || failed.Progress.Phase != "inventory" {
		t.Fatalf("fixture did not create an interrupted planning transaction: transaction=%+v err=%v", failed, err)
	}
	if failed.PlanProfilePath == "" {
		t.Fatal("failed planning transaction did not preserve restart inputs")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRebase(context.Background(), cfg, failed.ID, RebaseOptions{})
	if err != nil {
		t.Fatalf("planning restart failed: %v", err)
	}
	if resumed.ID == failed.ID || resumed.Status != RebaseStatusReadyToBuild {
		t.Fatalf("planning restart did not create a clean new transaction: failed=%+v resumed=%+v", failed, resumed)
	}
}

type rebaseRecoveryFixture struct {
	cfg         indexer.Config
	transaction RebaseTransaction
	project     string
	output      string
}

func newRebaseRecoveryFixture(t *testing.T) rebaseRecoveryFixture {
	t.Helper()
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
	transaction, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = BuildRebase(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ValidateRebase(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = ApproveRebaseSmoke(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rebaseRecoveryFixture{cfg: cfg, transaction: transaction, project: project, output: output}
}
