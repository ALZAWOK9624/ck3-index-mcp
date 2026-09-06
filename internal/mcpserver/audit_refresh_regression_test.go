package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

// A separate MCP runtime and cache in a real OS process. The test intentionally
// provides no in-process invalidation notification to the querying process.
func TestAuditBaselineWriterProcess(t *testing.T) {
	path := os.Getenv("CK3_AUDIT_BASELINE_CONFIG")
	if path == "" {
		t.Skip("subprocess helper")
	}
	cfg, err := indexer.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	dbPath, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, _ := json.Marshal(map[string]any{"name": "ck3_diagnostic_baseline", "arguments": map[string]any{"operation": os.Getenv("CK3_AUDIT_BASELINE_OPERATION"), "baseline": "audit"}})
	result, err := callMCPTool(context.Background(), db, cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] == true {
		t.Fatalf("child baseline operation failed: %+v", result)
	}
}

func auditBaselineInOtherProcess(t *testing.T, cfg indexer.Config, operation string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAuditBaselineWriterProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CK3_AUDIT_BASELINE_CONFIG="+cfg.ConfigPath, "CK3_AUDIT_BASELINE_OPERATION="+operation)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("baseline %s subprocess: %v\n%s", operation, err, output)
	}
}

func TestAuditBaselineRevisionAcrossMCPProcesses(t *testing.T) {
	cfg, db, path := writeRefreshFixture(t)
	// Keep the baseline empty, proving that its existence/version does not
	// depend on diagnostic entries being present.
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.ScanFiles(context.Background(), cfg, []string{"common/traits/refresh_trait.txt"}); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"operation":"summary","baseline":"audit"}`)
	raw := json.RawMessage(`{"name":"ck3_diagnostics","arguments":{"operation":"summary","baseline":"audit"}}`)
	call := func(wantError bool) {
		t.Helper()
		result, err := callMCPTool(context.Background(), db, cfg, raw)
		if err != nil {
			t.Fatal(err)
		}
		if (result.(map[string]any)["isError"] == true) != wantError {
			t.Fatalf("want error=%v: %+v", wantError, result)
		}
	}
	var lastRevision string
	for _, operation := range []string{"save", "clear", "save", "save", "clear"} {
		before, err := db.IndexState(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		dbPath, _ := indexer.ConfiguredDatabasePath(cfg)
		oldKey := toolCacheKey("ck3_diagnostics", dbPath, 0, before.Generation, before.Revision, args, before.BaselineRevision)
		stale, hasStale := mcpReadToolCache.get(oldKey)
		auditBaselineInOtherProcess(t, cfg, operation)
		after, err := db.IndexState(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if after.BaselineRevision == "" || after.BaselineRevision == lastRevision {
			t.Fatalf("persistent baseline version did not advance: %+v", after)
		}
		if after.Generation != before.Generation || after.Revision != before.Revision {
			t.Fatal("baseline write changed semantic generation")
		}
		lastRevision = after.BaselineRevision
		if hasStale {
			// Exact invalidation -> stale in-flight backfill ordering. Future
			// reads must be safe even when this old entry survives indefinitely.
			mcpReadToolCache.invalidateTool("ck3_diagnostics")
			mcpReadToolCache.put(oldKey, stale)
		}
		call(operation == "clear")
		if operation == "save" {
			baselines, err := db.ListDiagnosticBaselines(context.Background())
			if err != nil || len(baselines) != 1 || baselines[0].Count != 0 {
				t.Fatalf("empty baseline lost: %+v %v", baselines, err)
			}
		}
	}
}

func TestAuditBaselineChangeDuringQueryRetriesBeforeCaching(t *testing.T) {
	cfg, db, _ := writeRefreshFixture(t)
	auditBaselineInOtherProcess(t, cfg, "save")
	definition, _ := findCanonicalTool("ck3_diagnostics")
	original := definition.Handler
	defer func() { definition.Handler = original }()
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	definition.Handler = func(ctx context.Context, runtime *Runtime, def *ToolDefinition, args json.RawMessage) (toolOutput, error) {
		out, err := original(ctx, runtime, def, args)
		once.Do(func() { close(ready); <-release })
		return out, err
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	type response struct {
		value any
		err   error
	}
	done := make(chan response, 1)
	raw := json.RawMessage(`{"name":"ck3_diagnostics","arguments":{"operation":"summary","baseline":"audit"}}`)
	go func() { value, err := callMCPTool(context.Background(), db, cfg, raw); done <- response{value, err} }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("query did not reach gate")
	}
	auditBaselineInOtherProcess(t, cfg, "clear")
	unblock()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.value.(map[string]any)["isError"] != true {
			t.Fatalf("old baseline result escaped after retry: %+v", result.value)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("query did not finish")
	}
	definition.Handler = original
	result, err := callMCPTool(context.Background(), db, cfg, raw)
	if err != nil || result.(map[string]any)["isError"] != true {
		t.Fatalf("stale result cached: %+v %v", result, err)
	}
}

func TestAuditCommittedRefreshStatusFailureIsWarning(t *testing.T) {
	for _, operation := range []string{"full", "files"} {
		t.Run(operation, func(t *testing.T) {
			cfg, db, path := writeRefreshFixture(t)
			if err := os.WriteFile(path, []byte("audit_committed = {}\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var stats indexer.ScanStats
			var err error
			if operation == "full" {
				stats, err = indexer.ScanFullStaged(context.Background(), cfg)
			} else {
				stats, err = indexer.ScanFiles(context.Background(), cfg, []string{"common/traits/refresh_trait.txt"})
			}
			if err != nil || !stats.Committed {
				t.Fatalf("refresh failed: %+v %v", stats, err)
			}
			runtime := &Runtime{DB: db, Config: cfg}
			if operation == "full" {
				// A directory cannot be opened as a SQLite anchor.
				runtime.Config.Database = t.TempDir()
			} else {
				db.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			out := completedRefreshOutput(ctx, runtime, operation, stats)
			if !out.Committed || !out.StateUnverified {
				t.Fatalf("commit status lost: %+v", out)
			}
			value := out.Value.(map[string]any)
			if value["committed"] != true || value["index"] != nil || value["warnings"] == nil {
				t.Fatalf("dishonest fallback: %+v", value)
			}
			definition, _ := findCanonicalTool("ck3_refresh")
			assertToolValueMatchesOutputSchema(t, definition, value)
		})
	}
}
