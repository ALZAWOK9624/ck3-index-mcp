package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ck3-index/internal/migrator"
)

// CLI output is intentionally part of the contract for plan and status.  The
// command package writes directly to os.Stdout, so keep those narrow capture
// windows serialized even if a future caller makes these tests parallel.
var rebaseCLIStdoutMu sync.Mutex

func TestMigrateCLIInitRefusesOverwrite(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "migration.toml")
	if err := run(context.Background(), []string{"migrate", "init", profile}); err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "schema_version = 2") ||
		!strings.Contains(string(data), "migration_mode = \"same_game_version\"") ||
		!strings.Contains(string(data), "base_game_version = \"1.19.*\"") ||
		!strings.Contains(string(data), "unknown_policy = \"block\"") {
		t.Fatalf("migrate init wrote an incomplete profile template:\n%s", data)
	}
	if err := run(context.Background(), []string{"migrate", "init", profile}); err == nil || !strings.Contains(err.Error(), "profile already exists") {
		t.Fatalf("second migrate init error = %v, want overwrite refusal", err)
	}
}

func TestMigrateCLIPlanThenStatusEmitsTransaction(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"project", "base", "target"} {
		if err := os.MkdirAll(filepath.Join(root, name, "common"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRebaseCLITestFile(t, filepath.Join(root, "base", "common", "shadow.txt"), "value = old\n")
	writeRebaseCLITestFile(t, filepath.Join(root, "project", "common", "shadow.txt"), "value = old\n")
	writeRebaseCLITestFile(t, filepath.Join(root, "target", "common", "shadow.txt"), "value = target\n")

	configPath := filepath.Join(root, "ck3-index.toml")
	writeRebaseCLITestFile(t, configPath, `database = "cache/test.sqlite"
artifact_root = "artifacts"

[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
private = true

[[source]]
name = "base"
path = "base"
rank = 2
role = "dependency"
private = false

[[source]]
name = "target"
path = "target"
rank = 3
role = "dependency"
private = false
`)
	profilePath := filepath.Join(root, "migration.toml")
	writeRebaseCLITestFile(t, profilePath, `schema_version = 2
name = "cli-fixture"
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
	outputDir := filepath.Join(root, "migration-copy")
	specPath := filepath.Join(root, "plan.json")
	specData, err := json.Marshal(migrator.RebasePlanSpec{ProfilePath: profilePath, OutputDir: outputDir})
	if err != nil {
		t.Fatal(err)
	}
	writeRebaseCLITestFile(t, specPath, string(specData))

	planOutput, err := captureRebaseCLIStdout(func() error {
		return run(context.Background(), []string{"--config", configPath, "migrate", "plan", specPath})
	})
	if err != nil {
		t.Fatalf("migrate plan: %v", err)
	}
	var planned migrator.RebaseTransaction
	if err := json.Unmarshal([]byte(planOutput), &planned); err != nil {
		t.Fatalf("migrate plan output is not a transaction JSON document: %v\n%s", err, planOutput)
	}
	if planned.ID == "" || planned.Status != migrator.RebaseStatusReadyToBuild {
		t.Fatalf("migrate plan transaction = %+v", planned)
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatalf("migrate plan unexpectedly materialized an output overlay: %v", err)
	}

	statusOutput, err := captureRebaseCLIStdout(func() error {
		return run(context.Background(), []string{"--config", configPath, "migrate", "status", planned.ID})
	})
	if err != nil {
		t.Fatalf("migrate status: %v", err)
	}
	var loaded migrator.RebaseTransaction
	if err := json.Unmarshal([]byte(statusOutput), &loaded); err != nil {
		t.Fatalf("migrate status output is not a transaction JSON document: %v\n%s", err, statusOutput)
	}
	if loaded.ID != planned.ID || loaded.Status != planned.Status || loaded.ProjectFingerprint != planned.ProjectFingerprint {
		t.Fatalf("migrate status returned a different transaction: planned=%+v loaded=%+v", planned, loaded)
	}
	if err := run(context.Background(), []string{"--config", configPath, "migrate", "status", planned.ID, "--resume"}); err == nil || !strings.Contains(err.Error(), "no resumable interrupted stage") {
		t.Fatalf("migrate status --resume on a ready transaction = %v, want resumable-stage refusal rather than CLI usage error", err)
	}
}

func TestMigrateCLIHelpListsMigrationCommands(t *testing.T) {
	help, err := captureRebaseCLIStdout(func() error {
		return run(context.Background(), []string{"help"})
	})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, wanted := range []string{
		"migrate init <profile>",
		"migrate plan <spec>",
		"migrate status <id> [--resume]",
		"migrate review <id>",
		"migrate build <id>",
		"migrate validate <id>",
		"migrate approve-smoke <id>",
		"migrate promote <id>",
		"migrate rollback <id>",
	} {
		if !strings.Contains(help, wanted) {
			t.Fatalf("help does not advertise %q:\n%s", wanted, help)
		}
	}
	if err := run(context.Background(), []string{"migrate"}); err == nil || !strings.Contains(err.Error(), "usage: ck3-index migrate") {
		t.Fatalf("bare migrate usage = %v, want migrate subcommand usage", err)
	}
}

func writeRebaseCLITestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureRebaseCLIStdout(call func() error) (output string, callErr error) {
	rebaseCLIStdoutMu.Lock()
	defer rebaseCLIStdoutMu.Unlock()

	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	var captured bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&captured, reader)
		close(done)
	}()
	previous := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
		_ = writer.Close()
		<-done
		_ = reader.Close()
		output = captured.String()
	}()
	return "", call()
}
