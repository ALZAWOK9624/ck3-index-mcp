package migrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

// The status check that admits a lifecycle command and the publication that
// ends it sit at opposite ends of a long copy or rename. Without a
// cross-process lock two `migrate build` invocations can both pass the check
// and then write contradictory end states over one another.
func TestRebaseLifecycleRefusesToRunWhileAnotherOperationHoldsTheLock(t *testing.T) {
	ctx := context.Background()
	fixture := newRebaseConcurrencyFixture(t)
	transaction, err := PlanRebase(ctx, fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}

	held, err := acquireRebaseTransactionLock(root, transaction.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("build ran while the transaction was locked: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRebase(ctx, fixture.cfg, transaction.ID, RebaseOptions{}); err != nil {
		t.Fatalf("build after the lock was released: %v", err)
	}
}

func TestRebaseLockReleaseRequiresMatchingNonce(t *testing.T) {
	root := t.TempDir()
	id, err := newRebaseTransactionID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureRebaseDirectory(dir); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireRebaseTransactionLock(root, id, "plan")
	if err != nil {
		t.Fatal(err)
	}
	record, err := readRebaseLockRecord(lock.path)
	if err != nil {
		t.Fatal(err)
	}
	record.Nonce = "replacement-owner"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock.path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "no longer owned") {
		t.Fatalf("release accepted a replacement lock: %v", err)
	}
	if current, err := readRebaseLockRecord(lock.path); err != nil || current.Nonce != "replacement-owner" {
		t.Fatalf("replacement lock was removed: record=%+v err=%v", current, err)
	}
}

func TestRebaseLockReclaimsTheObservedStaleNonce(t *testing.T) {
	root := t.TempDir()
	id, err := newRebaseTransactionID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := rebaseTransactionDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureRebaseDirectory(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, rebaseLockName)
	old := time.Now().Add(-rebaseLockStaleAfter - time.Minute).UTC().Format(time.RFC3339Nano)
	record := rebaseLockRecord{PID: -1, Host: rebaseLockHost(), Nonce: "abandoned-owner", Operation: "build", AcquiredAt: old, HeartbeatAt: old}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireRebaseTransactionLock(root, id, "build")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	current, err := readRebaseLockRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Nonce == "" || current.Nonce == record.Nonce || current.Nonce != lock.nonce {
		t.Fatalf("stale nonce was not replaced safely: %+v", current)
	}
}

// The lock orders normal operation; the manifest revision is the last defence
// for a writer that somehow still holds a stale in-memory copy.
func TestWriteRebaseTransactionRefusesStaleRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newRebaseConcurrencyFixture(t)
	transaction, err := PlanRebase(ctx, fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := loadRebaseTransactionFromRoot(root, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := stale
	fresh.Progress.Message = "written by the process that still owns the transaction"
	if err := writeRebaseTransaction(root, &fresh); err != nil {
		t.Fatal(err)
	}
	stale.Status = RebaseStatusFailed
	stale.Progress.Message = "written from a superseded copy"
	if err := writeRebaseTransaction(root, &stale); err == nil || !strings.Contains(err.Error(), "modified concurrently") {
		t.Fatalf("stale manifest write was accepted: %v", err)
	}
	current, err := loadRebaseTransactionFromRoot(root, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == RebaseStatusFailed {
		t.Fatalf("stale writer overwrote the newer transaction state: %+v", current.Progress)
	}
}

// Planning must not rewrite the whole transaction once per classified file.
// The decision journal is what makes that possible, so it has to round-trip.
func TestPlanRebaseKeepsDecisionsOutOfTheManifest(t *testing.T) {
	ctx := context.Background()
	fixture := newRebaseConcurrencyFixture(t)
	transaction, err := PlanRebase(ctx, fixture.cfg, RebasePlanSpec{ProfilePath: fixture.profile, OutputDir: fixture.output}, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction.Files) == 0 {
		t.Fatal("plan produced no file decisions")
	}
	root, err := rebaseArtifactRoot(fixture.cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := rebaseTransactionDir(root, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, rebaseManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), `"files"`) {
		t.Fatalf("the per-file decision list is still embedded in the manifest:\n%s", manifest)
	}
	if _, err := os.Stat(filepath.Join(dir, rebaseDecisionsName)); err != nil {
		t.Fatalf("decision journal was not published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rebaseProgressName)); err != nil {
		t.Fatalf("progress projection was not published: %v", err)
	}
	reloaded, err := loadRebaseTransactionFromRoot(root, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Files) != len(transaction.Files) {
		t.Fatalf("reloaded decision count=%d want %d", len(reloaded.Files), len(transaction.Files))
	}
	for index := range reloaded.Files {
		got, want := reloaded.Files[index], transaction.Files[index]
		if got.Path != want.Path || got.Action != want.Action || got.Classification != want.Classification ||
			got.Safe != want.Safe || got.Result.SHA256 != want.Result.SHA256 {
			t.Fatalf("decision %d did not round-trip through the journal:\n got %+v\nwant %+v", index, got, want)
		}
	}
}

type rebaseConcurrencyFixture struct {
	cfg     indexer.Config
	profile string
	output  string
}

func newRebaseConcurrencyFixture(t *testing.T) rebaseConcurrencyFixture {
	t.Helper()
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
	return rebaseConcurrencyFixture{
		cfg:     rebaseLifecycleConfig(root, project, base, target),
		profile: writeRebaseLifecycleProfile(t, root),
		output:  filepath.Join(root, "migration-copy"),
	}
}
