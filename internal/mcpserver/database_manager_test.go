package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

func TestCanonicalMCPDatabasePathUsesHostCaseSemantics(t *testing.T) {
	upper := canonicalMCPDatabasePath(filepath.Join(t.TempDir(), "Index.sqlite"))
	lower := canonicalMCPDatabasePath(filepath.Join(filepath.Dir(upper), "index.sqlite"))
	if runtime.GOOS == "windows" && upper != lower {
		t.Fatalf("Windows database paths must compare case-insensitively: %q != %q", upper, lower)
	}
	if runtime.GOOS != "windows" && upper == lower {
		t.Fatalf("case-sensitive host collapsed distinct database paths: %q", upper)
	}
}

func TestMCPDatabaseManagerSwitchKeepsInflightLeasesAndReusesRetiredDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 11)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 22)
	cfg := databaseManagerTestConfig(firstPath, secondPath)

	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	firstLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	firstState := mustMCPIndexState(t, firstLease.DB)
	if firstState.Generation != 11 || firstLease.Identity.Name != "first" || firstLease.Identity.Epoch != 1 {
		t.Fatalf("first lease = %+v state=%+v", firstLease.Identity, firstState)
	}

	switched, err := manager.Switch(ctx, "second")
	if err != nil {
		toolErr := toolErrorFrom(err)
		t.Fatalf("switch failed: %s details=%+v", toolErr.Message, toolErr.Details)
	}
	if !switched.Changed || switched.Previous.Name != "first" || switched.Active.Name != "second" || switched.Active.Epoch != 2 {
		t.Fatalf("switch result = %+v", switched)
	}
	// The old lease must remain fully usable after the active pointer moves.
	if state := mustMCPIndexState(t, firstLease.DB); state.Generation != 11 {
		t.Fatalf("in-flight first lease changed database: %+v", state)
	}

	secondLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if state := mustMCPIndexState(t, secondLease.DB); state.Generation != 22 || secondLease.Identity.Name != "second" {
		t.Fatalf("second lease = %+v state=%+v", secondLease.Identity, state)
	}

	back, err := manager.Switch(ctx, "FIRST")
	if err != nil {
		t.Fatal(err)
	}
	if !back.Changed || back.Active.Name != "first" || back.Active.Epoch != 3 {
		t.Fatalf("switch back result = %+v", back)
	}
	reusedLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if reusedLease.DB != firstLease.DB {
		t.Fatal("switching back while the old lease was alive opened a duplicate database handle")
	}
	if state := mustMCPIndexState(t, secondLease.DB); state.Generation != 22 {
		t.Fatalf("retired second lease stopped working: %+v", state)
	}

	firstLease.Release()
	reusedLease.Release()
	secondLease.Release()
	manager.mu.Lock()
	_, secondStillLoaded := manager.loaded["second"]
	manager.mu.Unlock()
	if secondStillLoaded {
		t.Fatal("retired second database remained loaded after its final lease was released")
	}
}

func TestMCPDatabaseLeaseReleaseIsSharedAndIdempotentAcrossCopies(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 11)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 22)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	lease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := manager.Acquire()
	if err != nil {
		lease.Release()
		t.Fatal(err)
	}
	leaseCopy := lease
	lease.Release()
	leaseCopy.Release()

	manager.mu.Lock()
	refsAfterDuplicateRelease := manager.loaded["first"].refs
	manager.mu.Unlock()
	if refsAfterDuplicateRelease != 1 {
		peer.Release()
		t.Fatalf("duplicate release through a copied lease left refs=%d, want peer's one live ref", refsAfterDuplicateRelease)
	}
	if _, err := manager.Switch(context.Background(), "second"); err != nil {
		peer.Release()
		t.Fatal(err)
	}
	if state := mustMCPIndexState(t, peer.DB); state.Generation != 11 {
		peer.Release()
		t.Fatalf("copied lease release closed the peer's database: %+v", state)
	}
	manager.mu.Lock()
	_, retained := manager.loaded["first"]
	manager.mu.Unlock()
	if !retained {
		peer.Release()
		t.Fatal("retired database was closed while the peer lease was still live")
	}

	peer.Release()
	manager.mu.Lock()
	_, retained = manager.loaded["first"]
	manager.mu.Unlock()
	if retained {
		t.Fatal("retired database remained loaded after the real final lease release")
	}
}

func TestMCPDatabaseManagerCancellationBeforeCommitDoesNotSwitch(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 11)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 22)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	manager.beforeSwitchCommit = cancel
	before := manager.Current()
	if _, err := manager.Switch(ctx, "second"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-commit cancellation error=%v, want context.Canceled", err)
	}
	if after := manager.Current(); after != before {
		t.Fatalf("pre-commit cancellation changed active database: before=%+v after=%+v", before, after)
	}
	if usage := manager.ResourceUsage(); usage.LoadedDatabaseCount != 1 || usage.RetiredDatabaseCount != 0 || usage.AggregateSQLiteCacheBudgetMB != 32 {
		t.Fatalf("pre-commit cancellation leaked candidate reservation or pool: %+v", usage)
	}

	// The canceled attempt must release both the switch gate and its opening
	// reservation so a later independent request can make progress.
	if result, err := manager.Switch(context.Background(), "second"); err != nil || !result.Changed {
		t.Fatalf("switch after canceled attempt = %+v, %v", result, err)
	}
}

func TestMCPDatabaseManagerReportsRetiredLeaseResourceUsageInHealth(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 11)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 22)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	cfg.MaxOpenDatabasePools = 2
	cfg.MaxSQLiteCacheBudgetMB = 64
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	firstLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Switch(context.Background(), "second"); err != nil {
		firstLease.Release()
		t.Fatal(err)
	}
	usage := manager.ResourceUsage()
	if usage.LoadedDatabaseCount != 2 || usage.RetiredDatabaseCount != 1 || usage.AggregateSQLiteCacheBudgetMB != 64 || usage.MaxOpenDatabasePools != 2 || usage.MaxSQLiteCacheBudgetMB != 64 {
		firstLease.Release()
		t.Fatalf("resource usage with retired lease = %+v", usage)
	}

	secondLease, err := manager.Acquire()
	if err != nil {
		firstLease.Release()
		t.Fatal(err)
	}
	healthCtx := withMCPDatabaseContext(context.Background(), manager, secondLease.Identity)
	result, err := callMCPTool(healthCtx, secondLease.DB, secondLease.Config, json.RawMessage(`{"name":"ck3_health","arguments":{"mode":"quick"}}`))
	secondLease.Release()
	if err != nil {
		firstLease.Release()
		t.Fatal(err)
	}
	health := result.(map[string]any)["structuredContent"].(map[string]any)
	for key, want := range map[string]int{
		"loaded_database_count": 2, "retired_database_count": 1,
		"aggregate_sqlite_cache_budget_mb": 64, "max_open_database_pools": 2,
		"max_sqlite_cache_budget_mb": 64,
	} {
		if got := int(health[key].(float64)); got != want {
			firstLease.Release()
			t.Fatalf("health %s=%d, want %d: %+v", key, got, want, health)
		}
	}

	firstLease.Release()
	usage = manager.ResourceUsage()
	if usage.LoadedDatabaseCount != 1 || usage.RetiredDatabaseCount != 0 || usage.AggregateSQLiteCacheBudgetMB != 32 {
		t.Fatalf("resource usage after retired lease release = %+v", usage)
	}
}

func TestMCPDatabaseManagerRejectsCandidateBeforeExceedingProcessPoolBudgets(t *testing.T) {
	tests := []struct {
		name       string
		maxPools   int
		maxCacheMB int
		detailKey  string
		detailWant int
	}{
		{name: "pool count", maxPools: 2, maxCacheMB: 96, detailKey: "max_open_database_pools", detailWant: 2},
		{name: "aggregate cache", maxPools: 3, maxCacheMB: 64, detailKey: "max_sqlite_cache_budget_mb", detailWant: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
			secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 2)
			thirdPath := createReadyMCPDatabase(t, dir, "third.sqlite", 3)
			cfg := databaseManagerTestConfig(firstPath, secondPath)
			cfg.MCPDatabases = append(cfg.MCPDatabases, indexer.MCPDatabaseTarget{Name: "third", Description: "third fixture", Database: thirdPath})
			cfg.MaxOpenDatabasePools = test.maxPools
			cfg.MaxSQLiteCacheBudgetMB = test.maxCacheMB
			firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
			if err != nil {
				t.Fatal(err)
			}
			manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
			if err != nil {
				_ = firstDB.Close()
				t.Fatal(err)
			}
			defer manager.Close()

			firstLease, err := manager.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Switch(context.Background(), "second"); err != nil {
				firstLease.Release()
				t.Fatal(err)
			}
			before := manager.Current()
			_, err = manager.Switch(context.Background(), "third")
			if err == nil {
				firstLease.Release()
				t.Fatal("candidate exceeded the configured process budget but switch succeeded")
			}
			toolErr := toolErrorFrom(err)
			detailValue, detailOK := toolErr.Details[test.detailKey].(int)
			if toolErr.Code != ErrorServerBusy || toolErr.Details["reason"] != "database_pool_budget_exceeded" || !detailOK || detailValue != test.detailWant {
				firstLease.Release()
				t.Fatalf("budget rejection = %+v", toolErr)
			}
			if after := manager.Current(); after != before {
				firstLease.Release()
				t.Fatalf("rejected switch changed active database: before=%+v after=%+v", before, after)
			}
			usage := manager.ResourceUsage()
			if usage.LoadedDatabaseCount != 2 || usage.RetiredDatabaseCount != 1 || usage.AggregateSQLiteCacheBudgetMB != 64 {
				firstLease.Release()
				t.Fatalf("rejected candidate leaked resources: usage=%+v", usage)
			}

			firstLease.Release()
			if _, err := manager.Switch(context.Background(), "third"); err != nil {
				t.Fatalf("switch remained blocked after retired lease release: %v", err)
			}
			if active := manager.Current(); active.Name != "third" {
				t.Fatalf("active database after budget became available = %+v", active)
			}
		})
	}
}

func TestMCPDatabaseManagerBindsDistinctEngineRulesToEachDatabase(t *testing.T) {
	manager := newDistinctRuleDatabaseManager(t)
	defer manager.Close()

	first, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	assertMCPDatabaseRule(t, first.DB, "first_database_trigger", true)
	assertMCPDatabaseRule(t, first.DB, "second_database_trigger", false)
	first.Release()

	if _, err := manager.Switch(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	assertMCPDatabaseRule(t, second.DB, "second_database_trigger", true)
	assertMCPDatabaseRule(t, second.DB, "first_database_trigger", false)
}

func TestMCPDatabaseManagerInflightLeaseKeepsItsEngineRulesAfterSwitch(t *testing.T) {
	manager := newDistinctRuleDatabaseManager(t)
	defer manager.Close()
	first, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := manager.Switch(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	assertMCPDatabaseRule(t, first.DB, "first_database_trigger", true)
	assertMCPDatabaseRule(t, first.DB, "second_database_trigger", false)

	second, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	assertMCPDatabaseRule(t, second.DB, "second_database_trigger", true)
}

func TestMCPDatabaseManagerSwitchBackReusesOriginalEngineRules(t *testing.T) {
	manager := newDistinctRuleDatabaseManager(t)
	defer manager.Close()
	first, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := manager.Switch(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Switch(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	reused, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer reused.Release()
	if reused.DB != first.DB {
		t.Fatal("switching A -> B -> A did not reuse the still-leased A database")
	}
	assertMCPDatabaseRule(t, reused.DB, "first_database_trigger", true)
	assertMCPDatabaseRule(t, reused.DB, "second_database_trigger", false)
}

func TestMCPDatabaseManagerRejectsHealthReportThatIsNotQueryReady(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 2)
	setMCPDatabaseMeta(t, secondPath, "scan_status", indexer.IndexStatusInitializing)
	cfg := databaseManagerTestConfig(firstPath, secondPath)

	secondDB, err := indexer.OpenReadOnlyWithOptions(secondPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	health, healthErr := secondDB.HealthConfiguredDepth(context.Background(), indexer.Config{
		ConfigPath: cfg.ConfigPath, Database: secondPath,
		SQLiteReadConnections: cfg.SQLiteReadConnections, MCPMaxTasks: cfg.MCPMaxTasks,
		MCPMaxHeavyTasks: cfg.MCPMaxHeavyTasks, MCPMaxRasterTasks: cfg.MCPMaxRasterTasks,
	}, indexer.HealthQuick)
	_ = secondDB.Close()
	if healthErr != nil || health.CanServeIndexQueries() || health.ScanStatus != indexer.IndexStatusInitializing {
		t.Fatalf("non-ready health = %+v err=%v", health, healthErr)
	}

	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()
	_, err = manager.Switch(context.Background(), "second")
	if err == nil {
		t.Fatal("non-ready database was activated")
	}
	toolErr := toolErrorFrom(err)
	if toolErr.Code != ErrorDatabaseTargetUnavailable || toolErr.Details["reason"] != "health_not_ready" {
		t.Fatalf("non-ready switch error = code=%s details=%+v", toolErr.Code, toolErr.Details)
	}
	if current := manager.Current(); current.Name != "first" || current.Epoch != 1 {
		t.Fatalf("failed switch changed active database: %+v", current)
	}
	if usage := manager.ResourceUsage(); usage.LoadedDatabaseCount != 1 || usage.RetiredDatabaseCount != 0 || usage.AggregateSQLiteCacheBudgetMB != 32 {
		t.Fatalf("non-ready candidate leaked its pool reservation: %+v", usage)
	}
}

func TestMCPDatabaseManagerReportsStableTargetErrors(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
	cfg := databaseManagerTestConfig(firstPath, filepath.Join(dir, "missing.sqlite"))
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	if _, err := manager.Switch(context.Background(), "unknown"); toolErrorFrom(err).Code != ErrorDatabaseTargetNotFound {
		t.Fatalf("unknown target error = %+v", err)
	}
	if _, err := manager.Switch(context.Background(), "second"); toolErrorFrom(err).Code != ErrorDatabaseTargetUnavailable {
		t.Fatalf("missing target error = %+v", err)
	}
	if usage := manager.ResourceUsage(); usage.LoadedDatabaseCount != 1 || usage.RetiredDatabaseCount != 0 || usage.AggregateSQLiteCacheBudgetMB != 32 {
		t.Fatalf("missing target leaked its pool reservation: %+v", usage)
	}
	items := manager.List()
	if len(items) != 2 || !items[0].Active || !items[0].Available || items[1].Available {
		t.Fatalf("database catalog = %+v", items)
	}
}

func TestCK3DatabaseAcceptsConfiguredNamesButRejectsCallerPaths(t *testing.T) {
	dir := t.TempDir()
	dbPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
	db, err := indexer.OpenReadOnlyWithOptions(dbPath, indexer.DefaultSQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := emptyMCPConfig(dbPath)
	cfg.MCPDatabaseName = "first"
	raw := json.RawMessage(`{"name":"ck3_database","arguments":{"operation":"switch","path":"C:/not-allowed.sqlite"}}`)
	result, err := callMCPTool(context.Background(), db, cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	structured := result.(map[string]any)["structuredContent"].(map[string]any)
	if structured["code"] != ErrorInvalidArguments {
		t.Fatalf("caller path was not rejected by the closed schema: %+v", structured)
	}
}

func TestMCPDatabaseManagerConcurrentAcquireAndSwitch(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 101)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 202)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 80)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				lease, err := manager.Acquire()
				if err != nil {
					errs <- err
					return
				}
				state, stateErr := lease.DB.IndexState(ctx)
				if stateErr != nil || (state.Generation != 101 && state.Generation != 202) {
					errs <- fmt.Errorf("lease %s returned state %+v: %w", lease.Identity.Name, state, stateErr)
				}
				lease.Release()
			}
		}()
	}
	for attempt := 0; attempt < 12; attempt++ {
		name := "first"
		if attempt%2 == 0 {
			name = "second"
		}
		if _, err := manager.Switch(ctx, name); err != nil {
			errs <- err
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPDatabaseManagerSerializesConcurrentSwitchesWithinPoolBudget(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 101)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 202)
	thirdPath := createReadyMCPDatabase(t, dir, "third.sqlite", 303)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	cfg.MCPDatabases = append(cfg.MCPDatabases, indexer.MCPDatabaseTarget{Name: "third", Database: thirdPath})
	cfg.MaxOpenDatabasePools = 2
	cfg.MaxSQLiteCacheBudgetMB = 64
	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	names := []string{"first", "second", "third"}
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for attempt := 0; attempt < 6; attempt++ {
				name := names[(worker+attempt)%len(names)]
				if _, err := manager.Switch(context.Background(), name); err != nil {
					errs <- fmt.Errorf("switch to %s: %w", name, err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	usage := manager.ResourceUsage()
	if usage.LoadedDatabaseCount != 1 || usage.RetiredDatabaseCount != 0 || usage.AggregateSQLiteCacheBudgetMB != 32 {
		t.Fatalf("concurrent switches leaked a live or opening pool: %+v", usage)
	}
}

func TestMCPDatabaseManagerSwitchesWorkspaceConfigTarget(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 7)
	_ = createReadyMCPDatabase(t, dir, "second.sqlite", 8)
	if err := os.MkdirAll(filepath.Join(dir, "empty-project"), 0755); err != nil {
		t.Fatal(err)
	}
	targetConfigPath := filepath.Join(dir, "second.toml")
	targetConfig := `database = "second.sqlite"
[[source]]
name = "project"
path = "empty-project"
rank = 1
role = "project"
`
	if err := os.WriteFile(targetConfigPath, []byte(targetConfig), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := databaseManagerTestConfig(firstPath, "")
	cfg.MCPDatabases[0].Database = ""
	cfg.MCPDatabases[0].ConfigPath = targetConfigPath

	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	defer manager.Close()
	result, err := manager.Switch(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	wantConfigIdentity := filepath.ToSlash(filepath.Join(filepath.Base(dir), "second.toml"))
	if result.Active.Name != "second" || result.ScanGeneration != 8 || result.Active.ConfigIdentity != wantConfigIdentity {
		t.Fatalf("workspace-config switch = %+v", result)
	}
	lease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if len(lease.Config.Sources) != 1 || filepath.Base(lease.Config.Sources[0].Path) != "empty-project" || lease.Config.MCPMaxTasks != cfg.MCPMaxTasks {
		t.Fatalf("target runtime config = %+v", lease.Config)
	}
}

func TestServeMCPHotSwitchesNamedDatabaseForSubsequentCalls(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 31)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 42)
	cfg := databaseManagerTestConfig(firstPath, secondPath)
	// The test writes each dependent request only after the previous response,
	// matching how an agent must use this stateful control tool.
	cfg.MCPMaxTasks = 2

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"database-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ck3_database","arguments":{"operation":"list"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ck3_database","arguments":{"operation":"switch","name":"second"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ck3_health","arguments":{}}}`,
	}
	var output synchronizedBuffer
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), cfg, firstPath, reader, &output) }()
	if _, err := io.WriteString(writer, strings.Join(requests[:3], "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(output.String(), "\n") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := io.WriteString(writer, requests[3]+"\n"); err != nil {
		t.Fatal(err)
	}
	for strings.Count(output.String(), "\n") < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := io.WriteString(writer, requests[4]+"\n"); err != nil {
		t.Fatal(err)
	}
	for strings.Count(output.String(), "\n") < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, output.String())
	if len(responses) != 4 {
		t.Fatalf("response count = %d, want 4: %s", len(responses), output.String())
	}

	listed := toolStructuredContent(t, responseByID(t, responses, "2"))
	databases := listed["databases"].([]any)
	if len(databases) != 2 || databases[0].(map[string]any)["name"] != "first" || databases[1].(map[string]any)["name"] != "second" {
		t.Fatalf("listed databases = %+v", databases)
	}
	switchResponse := responseByID(t, responses, "3")["result"].(map[string]any)
	switchResult := switchResponse["structuredContent"].(map[string]any)
	if switchResult["changed"] != true || switchResult["active"].(map[string]any)["name"] != "second" {
		t.Fatalf("switch response = %+v", switchResponse)
	}
	healthResponse := responseByID(t, responses, "4")["result"].(map[string]any)
	if healthResponse["database"].(map[string]any)["name"] != "second" {
		t.Fatalf("health database metadata = %+v", healthResponse["database"])
	}
	health := healthResponse["structuredContent"].(map[string]any)
	if health["scan_generation"].(float64) != 42 || health["active_database"].(map[string]any)["name"] != "second" || health["configured_database_count"].(float64) != 2 ||
		health["loaded_database_count"].(float64) != 1 || health["retired_database_count"].(float64) != 0 || health["aggregate_sqlite_cache_budget_mb"].(float64) != 32 ||
		health["max_open_database_pools"].(float64) != indexer.DefaultMaxOpenDatabasePools || health["max_sqlite_cache_budget_mb"].(float64) != indexer.DefaultMaxSQLiteCacheBudgetMB {
		t.Fatalf("health after switch = %+v", health)
	}
}

func databaseManagerTestConfig(firstPath, secondPath string) indexer.Config {
	return indexer.Config{
		ConfigPath:             filepath.Join(filepath.Dir(firstPath), "ck3-index.toml"),
		Database:               firstPath,
		MCPDatabaseName:        "first",
		MCPDatabaseDescription: "first fixture",
		MCPDatabases: []indexer.MCPDatabaseTarget{{
			Name: "second", Description: "second fixture", Database: secondPath,
		}},
		SQLiteReadConnections: 4, SQLiteCacheMBPerConnection: 8, SQLiteMMapLimitMB: 16,
		MCPMaxTasks: 4, MCPMaxHeavyTasks: 1, MCPMaxRasterTasks: 1, MCPMaxQueuedTasks: 4,
		MCPQueueTimeoutSeconds: 5, MCPExecutionTimeoutSeconds: 30,
	}
}

func TestDatabaseManagerReloadsImmutablePublishedGeneration(t *testing.T) {
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
	generationName := ".first.sqlite.generation-2-0123456789abcdef.sqlite"
	generationPath := createReadyMCPDatabase(t, dir, generationName, 2)
	cfg := databaseManagerTestConfig(firstPath, "")
	cfg.MCPDatabases = nil
	first, err := indexer.OpenReadOnly(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, first)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	defer manager.Close()

	oldLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer oldLease.Release()
	if err := os.WriteFile(firstPath+".current", []byte(filepath.Base(generationPath)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := manager.ReloadCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Release()
	newState, err := reloaded.DB.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := oldLease.DB.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if newState.Generation != 2 || oldState.Generation != 1 {
		t.Fatalf("immutable reload states old=%+v new=%+v", oldState, newState)
	}
	if reloaded.Identity.Epoch != 2 || reloaded.Identity.databasePath != generationPath {
		t.Fatalf("reload identity = %+v", reloaded.Identity)
	}
	newLease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Release()
	if newLease.Identity.databasePath != generationPath {
		t.Fatalf("new lease remained on %q, want %q", newLease.Identity.databasePath, generationPath)
	}
	oldLease.Release()
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("retired database generation was not reclaimed after its last lease: %v", err)
	}
}

func createReadyMCPDatabase(t *testing.T, dir, name string, generation int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	values := map[string]string{
		"scan_generation":    fmt.Sprint(generation),
		"scan_revision":      fmt.Sprintf("fixture-%d", generation),
		"scan_status":        "ready",
		"index_rule_version": indexer.CurrentIndexRuleVersion(),
	}
	for key, value := range values {
		if _, err := sqlDB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO map_provinces(province_id, perimeter) VALUES(1,1)`,
		`INSERT INTO map_province_geometry(province_id,fill_rle,boundary_rle) VALUES(1,X'00',X'00')`,
		`INSERT INTO map_adjacencies(province_id,neighbor_id,border_len) VALUES(1,1,0)`,
		`INSERT INTO map_titles(title_id,title_type,province_id) VALUES('b_fixture','barony',1)`,
	} {
		if _, err := sqlDB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func newDistinctRuleDatabaseManager(t *testing.T) *mcpDatabaseManager {
	t.Helper()
	dir := t.TempDir()
	firstPath := createReadyMCPDatabase(t, dir, "first.sqlite", 1)
	secondPath := createReadyMCPDatabase(t, dir, "second.sqlite", 2)
	firstLogs := createMCPTestEngineLogs(t, dir, "first-logs", "first_database_trigger", "character")
	secondLogs := createMCPTestEngineLogs(t, dir, "second-logs", "second_database_trigger", "province")
	bindMCPDatabaseEngineFingerprint(t, firstPath, firstLogs)
	bindMCPDatabaseEngineFingerprint(t, secondPath, secondLogs)

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	secondConfigPath := filepath.Join(dir, "second.toml")
	secondConfig := fmt.Sprintf("database = %q\nengine_logs = %q\n[[source]]\nname = \"project\"\npath = %q\nrank = 1\nrole = \"project\"\n",
		filepath.ToSlash(secondPath), filepath.ToSlash(secondLogs), filepath.ToSlash(project))
	if err := os.WriteFile(secondConfigPath, []byte(secondConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := databaseManagerTestConfig(firstPath, "")
	cfg.EngineLogs = firstLogs
	cfg.MCPDatabases[0].Database = ""
	cfg.MCPDatabases[0].ConfigPath = secondConfigPath

	firstDB, err := indexer.OpenReadOnlyWithOptions(firstPath, cfg.SQLiteReadOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := firstDB.RestoreEngineRules(context.Background(), firstLogs); err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	manager, err := newMCPDatabaseManager(cfg, firstPath, firstDB)
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	return manager
}

func createMCPTestEngineLogs(t *testing.T, dir, name, trigger, scope string) string {
	t.Helper()
	logs := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(logs, "data_types"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"effects.log", "event_targets.log", "event_scopes.log"} {
		if err := os.WriteFile(filepath.Join(logs, file), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	text := fmt.Sprintf("%s - fixture\nSupported Scopes: %s\n", trigger, scope)
	if err := os.WriteFile(filepath.Join(logs, "triggers.log"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return logs
}

func bindMCPDatabaseEngineFingerprint(t *testing.T, path, logs string) {
	t.Helper()
	bundle, err := indexer.LoadEngineBundle(context.Background(), logs)
	if err != nil {
		t.Fatal(err)
	}
	setMCPDatabaseMeta(t, path, "engine_data_fingerprint", bundle.Fingerprint)
}

func setMCPDatabaseMeta(t *testing.T, path, key, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		t.Fatal(err)
	}
}

func assertMCPDatabaseRule(t *testing.T, db *indexer.DB, key string, want bool) {
	t.Helper()
	found := db.LookupScope(key) != nil
	if found != want {
		t.Fatalf("database rule %q found=%v want=%v", key, found, want)
	}
}

func mustMCPIndexState(t *testing.T, db *indexer.DB) indexer.IndexState {
	t.Helper()
	state, err := db.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func toolStructuredContent(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		encoded, _ := json.Marshal(response)
		t.Fatalf("response has no CallToolResult: %s", encoded)
	}
	if result["isError"] == true {
		t.Fatalf("tool returned error: %+v", result)
	}
	return result["structuredContent"].(map[string]any)
}
