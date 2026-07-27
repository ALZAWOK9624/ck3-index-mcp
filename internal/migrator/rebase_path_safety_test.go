package migrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestResolveRebasePathRejectsSymbolicLinkComponents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	requireRebaseTestSymlink(t, target, link)

	if _, err := resolveRebasePath(filepath.Join(link, "new-artifacts")); err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("resolveRebasePath accepted symlink component: %v", err)
	}
}

func TestEnsureStorageOutsideSourcesRejectsOverlap(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "project")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	sources := []indexer.Source{{Name: "project", Path: source}}
	if err := ensureStorageOutsideSources(filepath.Join(source, "rebase-artifacts"), sources); err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("storage under source was accepted: %v", err)
	}
}

func TestEnsureStorageOutsideSourcesRejectsSourceSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "project")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "project-link")
	requireRebaseTestSymlink(t, source, link)
	if err := ensureStorageOutsideSources(filepath.Join(root, "outside"), []indexer.Source{{Name: "linked-project", Path: link}}); err == nil || !strings.Contains(err.Error(), "unsafe configured source") {
		t.Fatalf("symbolic-link source was accepted: %v", err)
	}
}

func TestRebaseArtifactRootRejectsSymlinkedArtifactPath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "artifact-link")
	requireRebaseTestSymlink(t, external, link)

	_, err := rebaseArtifactRoot(indexer.Config{
		ArtifactRoot: link,
		Sources:      []indexer.Source{{Name: "source", Path: source}},
	}, RebaseOptions{})
	if err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("rebase artifact root followed symbolic link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(external, "rebase-transactions")); !os.IsNotExist(err) {
		t.Fatalf("rebase artifact directory escaped through symbolic link: %v", err)
	}
}

func TestRebaseTransactionStorageRejectsSymlinkedCandidateManualAndReportPaths(t *testing.T) {
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	id := rebaseTransactionPrefix + "0123456789abcdef"
	transactionDir, err := rebaseTransactionDir(artifactRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(transactionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}

	requireRebaseTestSymlink(t, external, filepath.Join(transactionDir, "candidates"))
	decision := &RebaseFileDecision{}
	if err := storeRebaseCandidate(artifactRoot, id, "common/example.txt", []byte("value = yes\n"), decision); err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("candidate write followed a symlinked directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(external, "common", "example.txt")); !os.IsNotExist(err) {
		t.Fatalf("candidate escaped transaction storage: %v", err)
	}

	if err := os.Remove(filepath.Join(transactionDir, "candidates")); err != nil {
		t.Fatal(err)
	}
	requireRebaseTestSymlink(t, external, filepath.Join(transactionDir, "manual"))
	_, _, err = rebaseManualResolutionFile(artifactRoot, id, RebaseResolution{
		ConflictID: "conflict", ManualPath: "manual/example.txt", SHA256: "00",
	}, "common/example.txt")
	if err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("manual resolution followed a symlinked directory: %v", err)
	}

	reportLink := filepath.Join(root, "report-link")
	requireRebaseTestSymlink(t, external, reportLink)
	if err := writeRebaseReviewFileAtomic(filepath.Join(reportLink, rebaseReportName), []byte("report")); err == nil || !strings.Contains(err.Error(), "symbolic-link") {
		t.Fatalf("report write followed a symlinked directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(external, rebaseReportName)); !os.IsNotExist(err) {
		t.Fatalf("report escaped transaction storage: %v", err)
	}
}

func requireRebaseTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable in this test environment: %v", err)
	}
}
