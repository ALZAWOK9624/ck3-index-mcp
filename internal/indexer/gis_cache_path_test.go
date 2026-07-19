package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureContainedGISDirRejectsEscapeAndNonDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	candidate := filepath.Join(root, "0123456789abcdef")
	if err := ensureContainedGISDir(root, candidate); err != nil {
		t.Fatalf("safe GIS cache directory was rejected: %v", err)
	}
	if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
		t.Fatalf("safe GIS cache directory was not created: info=%v err=%v", info, err)
	}
	if err := ensureContainedGISDir(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("GIS cache path escaped its configured root")
	}
	fileComponent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(fileComponent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureContainedGISDir(root, filepath.Join(fileComponent, "child")); err == nil {
		t.Fatal("GIS cache path accepted a regular file as a directory component")
	}
}

func TestEnsureContainedGISDirRejectsSymbolicLinks(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(parent, "root-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Skipf("symbolic links are unavailable on this platform: %v", err)
	}
	if err := ensureContainedGISDir(rootLink, filepath.Join(rootLink, "0123456789abcdef")); err == nil {
		t.Fatal("GIS cache accepted a symbolic-link root")
	}

	root := filepath.Join(parent, "cache")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	candidateLink := filepath.Join(root, "0123456789abcdef")
	if err := os.Symlink(target, candidateLink); err != nil {
		t.Skipf("candidate symbolic link is unavailable on this platform: %v", err)
	}
	if err := ensureContainedGISDir(root, candidateLink); err == nil {
		t.Fatal("GIS cache accepted a symbolic-link candidate")
	}
}
