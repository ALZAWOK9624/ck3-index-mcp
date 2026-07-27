package migrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestBuildRebaseResolvesCrossFileSemanticMoveWithOneOwner(t *testing.T) {
	for _, action := range []string{"keep_project", "use_target", "manual"} {
		t.Run(action, func(t *testing.T) {
			fixture := newRebaseSemanticAdditionalFixture(t)
			oldPath := "common/culture/cultures/original.txt"
			newPath := "common/culture/cultures/moved.txt"
			writeText(t, filepath.Join(fixture.base, filepath.FromSlash(oldPath)), "culture_a = { ethos = base }\n")
			writeText(t, filepath.Join(fixture.project, filepath.FromSlash(newPath)), "culture_a = { ethos = project }\n")
			writeText(t, filepath.Join(fixture.target, filepath.FromSlash(oldPath)), "culture_a = { ethos = target traditions = { target_tradition } }\n")

			transaction := fixture.plan(t)
			conflict := rebaseCrossFileTestConflict(t, transaction)
			if conflict.CounterpartPath != oldPath || conflict.TargetRemovalCandidatePath == "" {
				t.Fatalf("cross-file plan lacks paired ownership evidence: %+v", conflict)
			}
			resolution := RebaseResolution{ConflictID: conflict.ID, Action: action}
			if action == "manual" {
				manualPath := filepath.Join(fixture.artifacts, "rebase-transactions", transaction.ID, "manual", "moved.txt")
				manual := []byte("culture_a = { ethos = manual traditions = { manual_tradition } }\n")
				writeRebaseSemanticAdditionalBytes(t, manualPath, manual)
				resolution.ManualPath = "manual/moved.txt"
				resolution.SHA256 = hashBytes(manual)
			}
			rebaseRoot, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRebaseResolutions(rebaseRoot, transaction.ID, []RebaseResolution{resolution}); err != nil {
				t.Fatal(err)
			}
			transaction, err = BuildRebase(context.Background(), fixture.cfg, transaction.ID, RebaseOptions{})
			if err != nil {
				t.Fatalf("build %s resolution: %v", action, err)
			}
			if transaction.Status != RebaseStatusBuilt {
				t.Fatalf("build status=%s", transaction.Status)
			}
			if err := verifyRebaseCrossFileMoveUniqueness(context.Background(), fixture.output, sourceForCrossFileTest(t, fixture, "target"), transaction); err != nil {
				t.Fatalf("effective stack retains duplicate owner: %v", err)
			}

			movedData, movedErr := os.ReadFile(filepath.Join(fixture.output, filepath.FromSlash(newPath)))
			oldData, oldErr := os.ReadFile(filepath.Join(fixture.output, filepath.FromSlash(oldPath)))
			switch action {
			case "keep_project":
				if movedErr != nil || !strings.Contains(string(movedData), "ethos = project") {
					t.Fatalf("keep_project did not retain moved owner: %q err=%v", movedData, movedErr)
				}
				if oldErr != nil || strings.Contains(string(oldData), "culture_a") {
					t.Fatalf("keep_project target shadow still owns moved object: %q err=%v", oldData, oldErr)
				}
			case "use_target":
				if !os.IsNotExist(movedErr) || !os.IsNotExist(oldErr) {
					t.Fatalf("use_target unexpectedly materialized project files: moved=%v old=%v", movedErr, oldErr)
				}
			case "manual":
				if movedErr != nil || !strings.Contains(string(movedData), "ethos = manual") {
					t.Fatalf("manual did not retain manual owner: %q err=%v", movedData, movedErr)
				}
				if oldErr != nil || strings.Contains(string(oldData), "culture_a") {
					t.Fatalf("manual target shadow still owns moved object: %q err=%v", oldData, oldErr)
				}
			}
		})
	}
}

func rebaseCrossFileTestConflict(t *testing.T, transaction RebaseTransaction) RebaseConflict {
	t.Helper()
	for _, conflict := range transaction.Conflicts {
		if conflict.Code == "semantic_cross_file_move_conflict" {
			return conflict
		}
	}
	t.Fatalf("cross-file move did not produce a conflict: %+v", transaction)
	return RebaseConflict{}
}

func sourceForCrossFileTest(t *testing.T, fixture rebaseSemanticAdditionalFixture, name string) indexer.Source {
	t.Helper()
	for _, source := range fixture.cfg.Sources {
		if source.Name == name {
			return source
		}
	}
	t.Fatalf("missing source %s", name)
	return indexer.Source{}
}
