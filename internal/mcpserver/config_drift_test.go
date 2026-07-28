package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

// The server parses ck3-index.toml once at startup and scans from that
// in-memory copy. A config it can no longer read is therefore not "the scan's
// problem to report": the scan would happily index the previously configured
// roots and publish a fresh ready generation, which is indistinguishable from
// success. The check has to fail closed.
func TestCheckConfigDriftFailsClosedWhenTheConfigCannotBeReloaded(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)
	runtime := &Runtime{DB: db, Config: cfg}
	if err := checkConfigDrift(runtime); err != nil {
		t.Fatalf("unchanged configuration was reported as drifted: %+v", err)
	}

	if err := os.WriteFile(cfg.ConfigPath, []byte("database = \"cache/test.sqlite\"\n[[source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	toolErr := checkConfigDrift(runtime)
	if toolErr == nil {
		t.Fatal("an unparsable ck3-index.toml was allowed to scan from the previously loaded configuration")
	}
	if toolErr.Code != ErrorConfigChanged {
		t.Fatalf("unparsable configuration error code=%q want %q", toolErr.Code, ErrorConfigChanged)
	}

	if err := os.Remove(cfg.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if toolErr := checkConfigDrift(runtime); toolErr == nil || toolErr.Code != ErrorConfigChanged {
		t.Fatalf("a deleted ck3-index.toml was allowed to scan: %+v", toolErr)
	}
}

// The drift check must compare the internal scan fingerprint, not the redacted
// display identity: the latter keeps only the trailing path segments, so two
// unrelated roots can look identical.
func TestCheckConfigDriftDetectsARetargetedSourceRoot(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)
	runtime := &Runtime{DB: db, Config: cfg}

	dir := filepath.Dir(cfg.ConfigPath)
	other := filepath.Join(dir, "elsewhere", "project")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ConfigPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"elsewhere/project\"\nrank = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	toolErr := checkConfigDrift(runtime)
	if toolErr == nil || toolErr.Code != ErrorConfigChanged {
		t.Fatalf("a retargeted source root was not detected: %+v", toolErr)
	}
	onDisk, err := indexer.LoadConfig(cfg.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if indexer.ScanConfigFingerprint(cfg) == indexer.ScanConfigFingerprint(onDisk) {
		t.Fatal("retargeting a source root did not change the scan config fingerprint")
	}
}
