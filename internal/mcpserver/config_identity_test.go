package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

// TestHealthReportsLiveConfigIdentity pins the disclosure that makes a
// mis-pointed index detectable on the first call: a workspace may hold several
// ck3-index.toml files, and without the resolved roots every response looks
// identical no matter which tree is indexed.
func TestHealthReportsLiveConfigIdentity(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)

	result := callToolForTest(t, db, cfg, "ck3_health", map[string]any{})
	if result["isError"] == true {
		t.Fatalf("health failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)

	configPath, _ := body["config_path"].(string)
	if configPath == "" {
		t.Fatal("health did not report the live config path")
	}
	if filepath.Base(configPath) != "ck3-index.toml" {
		t.Fatalf("unexpected config path %q", configPath)
	}

	source := singleSourceIdentity(t, body["sources"])
	if source["name"] != "project" {
		t.Fatalf("unexpected source identity: %+v", source)
	}
	root, _ := source["root"].(string)
	if filepath.Base(root) != "project" {
		t.Fatalf("source root does not identify the configured tree: %q", root)
	}
	// The tree must be identifiable without disclosing the host layout: the
	// report carries the tail of the path, never the absolute path.
	if filepath.IsAbs(filepath.FromSlash(root)) {
		t.Fatalf("source root disclosed an absolute path: %q", root)
	}
	if filepath.IsAbs(filepath.FromSlash(configPath)) {
		t.Fatalf("config path disclosed an absolute path: %q", configPath)
	}
}

// singleSourceIdentity unwraps one source entry from a tool response. Values
// arrive as generic JSON, so assert on the decoded shape rather than the Go type.
func singleSourceIdentity(t *testing.T, value any) map[string]any {
	t.Helper()
	entries, ok := value.([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("expected exactly one resolved source, got %T %+v", value, value)
	}
	source, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("source entry has unexpected shape: %T", entries[0])
	}
	return source
}

// TestRefreshStatusReportsSourceRoots keeps "which roots would this scan read"
// answerable before a scan is started, not only afterwards.
func TestRefreshStatusReportsSourceRoots(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)

	result := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "status"})
	if result["isError"] == true {
		t.Fatalf("refresh status failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	root := singleSourceIdentity(t, body["source_roots"])
	if root["name"] != "project" {
		t.Fatalf("unexpected source root: %+v", root)
	}
	if path, _ := root["root"].(string); filepath.Base(path) != "project" {
		t.Fatalf("source root does not point at the configured tree: %q", path)
	}
}

// rewriteConfigSource points the on-disk config at a different, existing tree
// without touching the config this server already loaded.
func rewriteConfigSource(t *testing.T, cfg indexer.Config) {
	t.Helper()
	root := filepath.Dir(cfg.ConfigPath)
	moved := filepath.Join(root, "other-project")
	if err := os.MkdirAll(filepath.Join(moved, "common", "traits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, "common", "traits", "other.txt"), []byte("other_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"other-project\"\nrank = 1\n"
	if err := os.WriteFile(cfg.ConfigPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRefreshRefusesAfterConfigChangedOnDisk is the regression that matters
// most: before this check a full scan run after a config edit completed
// normally, incremented the generation and returned status=ready while having
// indexed the previously configured roots. Success and failure were
// indistinguishable, so the scan had to be refused outright.
func TestRefreshRefusesAfterConfigChangedOnDisk(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)

	before := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "status"})
	beforeGeneration := before["structuredContent"].(map[string]any)["scan_generation"]

	rewriteConfigSource(t, cfg)

	for _, call := range []struct {
		name      string
		arguments map[string]any
	}{
		{"full", map[string]any{"operation": "full"}},
		{"files", map[string]any{"operation": "files", "paths": []string{"common/traits/refresh_trait.txt"}}},
	} {
		result := callToolForTest(t, db, cfg, "ck3_refresh", call.arguments)
		if result["isError"] != true {
			t.Fatalf("operation=%s scanned with a stale configuration instead of refusing: %+v", call.name, result)
		}
		body := result["structuredContent"].(map[string]any)
		if body["code"] != ErrorConfigChanged {
			t.Fatalf("operation=%s returned %v, want %s", call.name, body["code"], ErrorConfigChanged)
		}
		if body["category"] == "" || body["message"] == "" {
			t.Fatalf("operation=%s error lacks category/message: %+v", call.name, body)
		}
	}

	after := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "status"})
	if got := after["structuredContent"].(map[string]any)["scan_generation"]; got != beforeGeneration {
		t.Fatalf("a refused refresh still published a generation: before=%v after=%v", beforeGeneration, got)
	}
}

// TestRefreshAllowsCosmeticConfigEdits guards the other direction: comparing
// the resolved source model rather than raw bytes means comments and formatting
// must not be mistaken for a real change.
func TestRefreshAllowsCosmeticConfigEdits(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)

	body := "# a comment added after startup\n\ndatabase = \"cache/test.sqlite\"\n\n[[source]]\nname   = \"project\"\npath   = \"project\"\nrank   = 1\n"
	if err := os.WriteFile(cfg.ConfigPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	result := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "full"})
	if result["isError"] == true {
		structured := result["structuredContent"].(map[string]any)
		if structured["code"] == ErrorConfigChanged {
			t.Fatalf("a comment-only config edit was treated as a source change: %+v", structured)
		}
		t.Fatalf("full refresh failed: %+v", structured)
	}
}
