package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfiguredDatabasePathFollowsAtomicGenerationPointer(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{ConfigPath: filepath.Join(dir, "ck3-index.toml"), Database: "cache/index.sqlite"}
	anchor, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ConfiguredDatabasePath(cfg); err != nil || got != anchor {
		t.Fatalf("path without pointer = %q, %v; want %q", got, err, anchor)
	}
	if err := os.MkdirAll(filepath.Dir(anchor), 0o755); err != nil {
		t.Fatal(err)
	}
	first := publishedDatabaseGenerationPath(anchor, 2, "first-revision")
	second := publishedDatabaseGenerationPath(anchor, 3, "second-revision")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishDatabasePointer(anchor, first); err != nil {
		t.Fatal(err)
	}
	if got, err := ConfiguredDatabasePath(cfg); err != nil || got != first {
		t.Fatalf("first published path = %q, %v; want %q", got, err, first)
	}
	// Replacing an existing pointer is the normal second-publication path and
	// specifically exercises Windows' replace-existing rename semantics.
	if err := publishDatabasePointer(anchor, second); err != nil {
		t.Fatal(err)
	}
	if got, err := ConfiguredDatabasePath(cfg); err != nil || got != second {
		t.Fatalf("second published path = %q, %v; want %q", got, err, second)
	}
}

func TestOrphanedPublishedGenerationsAreReclaimedButCurrentIsKept(t *testing.T) {
	dir := t.TempDir()
	anchor := filepath.Join(dir, "index.sqlite")
	current := publishedDatabaseGenerationPath(anchor, 3, "current")
	retired := publishedDatabaseGenerationPath(anchor, 2, "retired")
	for _, path := range []string{current, retired, retired + "-wal"} {
		if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := publishDatabasePointer(anchor, current); err != nil {
		t.Fatal(err)
	}
	removeOrphanedPublishedDatabaseGenerations(anchor)
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current published generation was removed: %v", err)
	}
	for _, path := range []string{retired, retired + "-wal"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired published generation artifact remained: %s (%v)", path, err)
		}
	}
}

func TestConfiguredDatabasePathRejectsEscapingPointer(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{ConfigPath: filepath.Join(dir, "ck3-index.toml"), Database: "index.sqlite"}
	anchor, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publishedDatabasePointerPath(anchor), []byte("../outside.sqlite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfiguredDatabasePath(cfg); err == nil {
		t.Fatal("escaping database generation pointer was accepted")
	}
}
