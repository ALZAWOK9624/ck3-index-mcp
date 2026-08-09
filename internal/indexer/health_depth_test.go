package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newHealthDB(t *testing.T) (*DB, Config) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "traits", "traits.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("health_trait = { value = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, cfg
}

// The whole point of the quick form is that it reaches the same verdict without
// re-deriving what the scanner already recorded. If the status could differ,
// the split would be a correctness problem rather than a cost one.
func TestQuickAndDeepHealthAgreeOnStatus(t *testing.T) {
	db, cfg := newHealthDB(t)
	ctx := context.Background()
	quick, err := db.HealthConfiguredDepth(ctx, cfg, HealthQuick)
	if err != nil {
		t.Fatal(err)
	}
	deep, err := db.HealthConfiguredDepth(ctx, cfg, HealthDeep)
	if err != nil {
		t.Fatal(err)
	}
	if quick.Status != deep.Status {
		t.Fatalf("quick status %q != deep status %q", quick.Status, deep.Status)
	}
	if quick.Depth != "quick" || deep.Depth != "deep" {
		t.Fatalf("reports do not name their depth: quick=%q deep=%q", quick.Depth, deep.Depth)
	}
	if quick.MCPMaxTasks != DefaultMCPMaxTasks || quick.MCPMaxHeavyTasks != DefaultMCPMaxHeavyTasks ||
		quick.MCPMaxRasterTasks != DefaultMCPMaxRasterTasks || quick.MCPMaxQueuedTasks != DefaultMCPMaxQueuedTasks ||
		quick.MCPQueueTimeoutSecs != DefaultMCPQueueTimeoutSeconds || quick.MCPExecutionTimeoutSecs != DefaultMCPExecutionTimeoutSeconds ||
		quick.MaxOpenDatabasePools != DefaultMaxOpenDatabasePools || quick.MaxSQLiteCacheBudgetMB != DefaultMaxSQLiteCacheBudgetMB ||
		quick.LoadedDatabaseCount != 1 || quick.RetiredDatabaseCount != 0 || quick.AggregateSQLiteCacheBudgetMB != DefaultSQLiteReadConnections*DefaultSQLiteCacheMBPerConnection ||
		quick.SQLiteOrdinaryReserve != DefaultSQLiteReadConnections-DefaultMCPMaxHeavyTasks {
		t.Fatalf("configured health omitted normalized MCP resource limits: %+v", quick)
	}
	for _, field := range []struct {
		name        string
		quick, deep any
	}{
		{"scan_generation", quick.ScanGeneration, deep.ScanGeneration},
		{"index_rule_version", quick.IndexRuleVersion, deep.IndexRuleVersion},
		{"authoritative_database", quick.AuthoritativeDatabase, deep.AuthoritativeDatabase},
		{"map_database.complete", quick.MapDatabase.Complete, deep.MapDatabase.Complete},
		{"fts5_available", quick.FTS5Available, deep.FTS5Available},
	} {
		if field.quick != field.deep {
			t.Errorf("%s differs between depths: quick=%v deep=%v", field.name, field.quick, field.deep)
		}
	}
}

// Counts the scanner recorded must survive the quick path, and must agree with
// counting the table for real. A stale meta value would be worse than no value.
func TestQuickHealthCountsMatchTheRealCounts(t *testing.T) {
	db, cfg := newHealthDB(t)
	ctx := context.Background()
	quick, err := db.HealthConfiguredDepth(ctx, cfg, HealthQuick)
	if err != nil {
		t.Fatal(err)
	}
	deep, err := db.HealthConfiguredDepth(ctx, cfg, HealthDeep)
	if err != nil {
		t.Fatal(err)
	}
	if len(quick.Tables) == 0 {
		t.Fatal("quick health reported no table totals at all")
	}
	for table, quickCount := range quick.Tables {
		deepCount, counted := deep.Tables[table]
		if !counted {
			t.Errorf("quick reported %s but deep did not count it", table)
			continue
		}
		if quickCount != deepCount {
			t.Errorf("%s: quick reported %d, real count is %d", table, quickCount, deepCount)
		}
	}
	// The two FTS virtual tables are the ones worth not walking; deep still has
	// to report them or the deep form has lost its reason to exist.
	for _, table := range deepOnlyHealthTables {
		if _, counted := deep.Tables[table]; !counted {
			t.Errorf("deep health stopped reporting %s", table)
		}
		if _, served := quick.Tables[table]; served {
			t.Errorf("quick health walked the %s virtual table", table)
		}
	}
}

// Verifying the sidecar may hash 512 MiB and launch a subprocess. The memo is
// keyed by the trusted content address, not mutable source metadata: once
// published, replacing the installation source must not redirect execution.
// Memo hits still hash the published copy, but do not repeat the subprocess.
func TestGISSidecarVerificationMemoUsesTrustedContentAddress(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "whitebox_tools")
	if err := os.WriteFile(sidecar, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		GISEnabled:       true,
		GISSidecarPath:   sidecar,
		GISSidecarSHA256: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		GISAnalysis:      "terrain",
		GISCacheRoot:     filepath.Join(dir, "cache"),
	}

	first, ok := gisSidecarMemoKey(cfg)
	if !ok {
		t.Fatal("a pinned sidecar has no memo key")
	}
	if same, _ := gisSidecarMemoKey(cfg); same != first {
		t.Fatal("memo key is not stable for an unchanged trust configuration")
	}
	// Rewriting the source must not change the trusted published identity.
	if err := os.WriteFile(sidecar, []byte("a different binary entirely"), 0o755); err != nil {
		t.Fatal(err)
	}
	if changed, _ := gisSidecarMemoKey(cfg); changed != first {
		t.Fatal("mutable source metadata leaked into the published content identity")
	}
	// A re-pinned release hash must also miss.
	repinned := cfg
	repinned.GISSidecarSHA256 = "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe"
	if other, _ := gisSidecarMemoKey(repinned); other == first {
		t.Fatal("re-pinning the release hash did not change the identity")
	}
	// A failed verification must never be memoized: this sidecar cannot match
	// its configured hash, so every call has to reach the real check.
	status := InspectGISSidecar(context.Background(), cfg)
	if status.Available {
		t.Fatal("a hash mismatch reported an available sidecar")
	}
	key, _ := gisSidecarMemoKey(cfg)
	gisSidecarMemo.Lock()
	_, memoized := gisSidecarMemo.entries[key]
	gisSidecarMemo.Unlock()
	if memoized {
		t.Fatal("a failed verification was pinned in the memo")
	}
}
