package migrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRebaseMaterializesReviewedConflictResolution(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	base := filepath.Join(root, "base")
	target := filepath.Join(root, "target")
	for _, directory := range []string{project, base, target} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeText(t, filepath.Join(base, "common", "conflict.txt"), "value = base\n")
	writeText(t, filepath.Join(project, "common", "conflict.txt"), "value = project\n")
	writeText(t, filepath.Join(target, "common", "conflict.txt"), "value = target\n")
	profile := writeRebaseLifecycleProfile(t, root)
	output := filepath.Join(root, "migration-copy")
	cfg := rebaseLifecycleConfig(root, project, base, target)
	transaction, err := PlanRebase(context.Background(), cfg, RebasePlanSpec{ProfilePath: profile, OutputDir: output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusNeedsReview || len(transaction.Conflicts) != 1 {
		t.Fatalf("expected one review conflict, got status=%s conflicts=%+v", transaction.Status, transaction.Conflicts)
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseResolutions(rebaseRoot, transaction.ID, []RebaseResolution{{
		ConflictID: transaction.Conflicts[0].ID,
		Action:     "keep_project",
	}}); err != nil {
		t.Fatal(err)
	}
	transaction, err = BuildRebase(context.Background(), cfg, transaction.ID, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Status != RebaseStatusBuilt || len(transaction.Resolutions) != 1 {
		t.Fatalf("reviewed build did not retain resolution: %+v", transaction)
	}
	data, err := os.ReadFile(filepath.Join(output, "common", "conflict.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "value = project\n" {
		t.Fatalf("reviewed output = %q, want project resolution", got)
	}
}

func TestManualResolutionRequiresTransactionScopedHashCheckedFile(t *testing.T) {
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Conflicts: []RebaseConflict{{
			ID:             "conflict-1",
			AllowedActions: []string{"manual"},
		}},
	}
	if _, err := validateRebaseReviewResolutions(transaction, []RebaseResolution{{ConflictID: "conflict-1", Action: "manual", ManualPath: "outside.txt"}}); err == nil {
		t.Fatal("manual resolution without manual/ path and SHA-256 was accepted")
	}
}
