package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"ck3-index/internal/indexer"
)

type mcpDatabaseIdentity struct {
	Name             string `json:"name"`
	Epoch            uint64 `json:"epoch"`
	DatabaseIdentity string `json:"database_identity"`
	ConfigIdentity   string `json:"config_identity,omitempty"`
	databasePath     string
}

type mcpDatabaseSummary struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	Mode             string `json:"mode"`
	Active           bool   `json:"active"`
	Available        bool   `json:"available"`
	DatabaseIdentity string `json:"database_identity"`
	ConfigIdentity   string `json:"config_identity,omitempty"`
}

type mcpDatabaseSwitchResult struct {
	Previous            mcpDatabaseIdentity `json:"previous"`
	Active              mcpDatabaseIdentity `json:"active"`
	Changed             bool                `json:"changed"`
	Status              string              `json:"status"`
	ScanGeneration      int64               `json:"scan_generation,omitempty"`
	ScanRevision        string              `json:"scan_revision,omitempty"`
	ScanStatus          string              `json:"scan_status,omitempty"`
	DatabaseFingerprint string              `json:"database_fingerprint,omitempty"`
	Guidance            []string            `json:"guidance"`
}

type mcpDatabaseController interface {
	Catalog() (mcpDatabaseIdentity, []mcpDatabaseSummary)
	List() []mcpDatabaseSummary
	Current() mcpDatabaseIdentity
	Switch(context.Context, string) (mcpDatabaseSwitchResult, error)
}

type mcpDatabaseReload struct {
	DB       *indexer.DB
	Config   indexer.Config
	Identity mcpDatabaseIdentity
	release  func()
}

func (reload mcpDatabaseReload) Release() {
	if reload.release != nil {
		reload.release()
	}
}

// mcpDatabaseReloader is optional so small test controllers and compatibility
// callers do not need to implement generation rebinding. The real manager uses
// it after an immutable full publication.
type mcpDatabaseReloader interface {
	ReloadCurrent(context.Context) (mcpDatabaseReload, error)
}

type mcpDatabaseResourceReporter interface {
	ResourceUsage() mcpDatabaseResourceUsage
}

type mcpDatabaseResourceUsage struct {
	Active                       mcpDatabaseIdentity
	ConfiguredDatabaseCount      int
	LoadedDatabaseCount          int
	RetiredDatabaseCount         int
	AggregateSQLiteCacheBudgetMB int
	MaxOpenDatabasePools         int
	MaxSQLiteCacheBudgetMB       int
}

type mcpDatabaseContext struct {
	Controller mcpDatabaseController
	Identity   mcpDatabaseIdentity
}

type mcpDatabaseContextKey struct{}

func withMCPDatabaseContext(ctx context.Context, controller mcpDatabaseController, identity mcpDatabaseIdentity) context.Context {
	return context.WithValue(ctx, mcpDatabaseContextKey{}, mcpDatabaseContext{Controller: controller, Identity: identity})
}

func mcpDatabaseContextFrom(ctx context.Context) (mcpDatabaseContext, bool) {
	value, ok := ctx.Value(mcpDatabaseContextKey{}).(mcpDatabaseContext)
	return value, ok
}

type mcpDatabaseSpec struct {
	name        string
	description string
	mode        string
	config      indexer.Config
	dbPath      string
}

type managedMCPDatabase struct {
	spec           mcpDatabaseSpec
	db             *indexer.DB
	refs           int
	retired        bool
	deleteOnRetire bool
}

type mcpDatabaseLease struct {
	DB       *indexer.DB
	Config   indexer.Config
	Identity mcpDatabaseIdentity
	release  func()
}

func (lease mcpDatabaseLease) Release() {
	if lease.release != nil {
		lease.release()
	}
}

type mcpDatabaseManager struct {
	mu                         sync.Mutex
	switchGate                 chan struct{}
	specs                      map[string]mcpDatabaseSpec
	order                      []string
	loaded                     map[string]*managedMCPDatabase
	active                     *managedMCPDatabase
	epoch                      uint64
	closed                     bool
	openingDatabaseCount       int
	openingSQLiteCacheBudgetMB int
	maxOpenDatabasePools       int
	maxSQLiteCacheBudgetMB     int
	// beforeSwitchCommit is a deterministic test seam for the narrow boundary
	// between successful candidate validation and the atomic activation lock.
	// Production managers leave it nil.
	beforeSwitchCommit func()
}

func newMCPDatabaseManager(cfg indexer.Config, dbPath string, db *indexer.DB) (*mcpDatabaseManager, error) {
	specs, order, err := resolveMCPDatabaseSpecs(cfg, dbPath)
	if err != nil {
		return nil, err
	}
	primary := specs[order[0]]
	active := &managedMCPDatabase{spec: primary, db: db}
	initialCacheBudget := mcpDatabaseCacheBudget(primary)
	if primary.config.MaxOpenDatabasePools < 1 || initialCacheBudget > primary.config.MaxSQLiteCacheBudgetMB {
		return nil, fmt.Errorf("active MCP database exceeds the configured process SQLite pool budget")
	}
	return &mcpDatabaseManager{
		switchGate:             make(chan struct{}, 1),
		specs:                  specs,
		order:                  order,
		loaded:                 map[string]*managedMCPDatabase{primary.name: active},
		active:                 active,
		epoch:                  1,
		maxOpenDatabasePools:   primary.config.MaxOpenDatabasePools,
		maxSQLiteCacheBudgetMB: primary.config.MaxSQLiteCacheBudgetMB,
	}, nil
}

func resolveMCPDatabaseSpecs(root indexer.Config, dbPath string) (map[string]mcpDatabaseSpec, []string, error) {
	root = effectiveMCPDatabaseConfig(root)
	if len(root.MCPDatabases) > 0 && root.MaxOpenDatabasePools < 2 {
		return nil, nil, fmt.Errorf("max_open_database_pools must be at least 2 when MCP database targets are configured")
	}
	if !mcpDatabaseFitsCacheBudget(root, root.MaxSQLiteCacheBudgetMB) {
		return nil, nil, fmt.Errorf("the primary MCP database pool exceeds max_sqlite_cache_budget_mb")
	}
	primaryName := strings.ToLower(strings.TrimSpace(root.MCPDatabaseName))
	if primaryName == "" {
		primaryName = "default"
	}
	root.MCPDatabaseName = primaryName
	specs := map[string]mcpDatabaseSpec{
		primaryName: {
			name: primaryName, description: strings.TrimSpace(root.MCPDatabaseDescription), mode: "primary",
			config: root, dbPath: filepath.Clean(dbPath),
		},
	}
	order := []string{primaryName}
	paths := map[string]string{canonicalMCPDatabasePath(dbPath): primaryName}
	for _, target := range root.MCPDatabases {
		var targetConfig indexer.Config
		mode := "snapshot"
		if target.ConfigPath != "" {
			loaded, err := indexer.LoadConfig(target.ConfigPath)
			if err != nil {
				return nil, nil, fmt.Errorf("load MCP database target %q config: %w", target.Name, err)
			}
			targetConfig = loaded
			mode = "workspace_config"
		} else {
			targetConfig = root
			targetConfig.Database = target.Database
			targetConfig.BaseDatabase = ""
		}
		description := strings.TrimSpace(target.Description)
		if description == "" {
			description = strings.TrimSpace(targetConfig.MCPDatabaseDescription)
		}
		targetConfig = effectiveMCPDatabaseConfig(targetConfig)
		targetConfig.MCPDatabaseName = target.Name
		targetConfig.MCPDatabaseDescription = target.Description
		targetConfig.MCPDatabases = nil
		applyMCPServiceLimits(&targetConfig, root)
		if !mcpDatabaseFitsCacheBudget(targetConfig, root.MaxSQLiteCacheBudgetMB) {
			return nil, nil, fmt.Errorf("MCP database target %q requires more SQLite page cache than max_sqlite_cache_budget_mb", target.Name)
		}
		if targetConfig.SQLiteReadConnections <= root.MCPMaxHeavyTasks {
			return nil, nil, fmt.Errorf("MCP database target %q has %d SQLite connections but the service allows %d expensive tasks; at least one ordinary-query connection must remain", target.Name, targetConfig.SQLiteReadConnections, root.MCPMaxHeavyTasks)
		}
		targetPath, err := indexer.ConfiguredDatabasePath(targetConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve MCP database target %q: %w", target.Name, err)
		}
		canonical := canonicalMCPDatabasePath(targetPath)
		if existing, duplicate := paths[canonical]; duplicate {
			return nil, nil, fmt.Errorf("MCP database targets %q and %q resolve to the same SQLite database", existing, target.Name)
		}
		paths[canonical] = target.Name
		specs[target.Name] = mcpDatabaseSpec{
			name: target.Name, description: description, mode: mode,
			config: targetConfig, dbPath: targetPath,
		}
		order = append(order, target.Name)
	}
	return specs, order, nil
}

func effectiveMCPDatabaseConfig(cfg indexer.Config) indexer.Config {
	options := cfg.SQLiteReadOptions()
	cfg.SQLiteReadConnections = options.Connections
	cfg.SQLiteCacheMBPerConnection = options.CacheMBPerConnection
	cfg.SQLiteMMapLimitMB = options.MMapLimitMB
	if cfg.MaxOpenDatabasePools <= 0 {
		cfg.MaxOpenDatabasePools = indexer.DefaultMaxOpenDatabasePools
	}
	if cfg.MaxSQLiteCacheBudgetMB <= 0 {
		cfg.MaxSQLiteCacheBudgetMB = indexer.DefaultMaxSQLiteCacheBudgetMB
	}
	limiter := newMCPTaskLimiter(cfg)
	cfg.MCPMaxTasks, cfg.MCPMaxHeavyTasks, cfg.MCPMaxRasterTasks = limiter.limits()
	cfg.MCPMaxQueuedTasks = limiter.queueLimit()
	cfg.MCPQueueTimeoutSeconds = int(mcpQueueTimeout(cfg).Seconds())
	cfg.MCPExecutionTimeoutSeconds = int(mcpExecutionTimeout(cfg).Seconds())
	return cfg
}

func applyMCPServiceLimits(target *indexer.Config, service indexer.Config) {
	target.MaxOpenDatabasePools = service.MaxOpenDatabasePools
	target.MaxSQLiteCacheBudgetMB = service.MaxSQLiteCacheBudgetMB
	target.MCPMaxTasks = service.MCPMaxTasks
	target.MCPMaxHeavyTasks = service.MCPMaxHeavyTasks
	target.MCPMaxRasterTasks = service.MCPMaxRasterTasks
	target.MCPMaxQueuedTasks = service.MCPMaxQueuedTasks
	target.MCPQueueTimeoutSeconds = service.MCPQueueTimeoutSeconds
	target.MCPExecutionTimeoutSeconds = service.MCPExecutionTimeoutSeconds
}

func mcpDatabaseFitsCacheBudget(cfg indexer.Config, budgetMB int) bool {
	options := cfg.SQLiteReadOptions()
	return budgetMB > 0 && options.CacheMBPerConnection > 0 && options.Connections <= budgetMB/options.CacheMBPerConnection
}

func mcpDatabaseCacheBudget(spec mcpDatabaseSpec) int {
	options := spec.config.SQLiteReadOptions()
	return options.Connections * options.CacheMBPerConnection
}

func canonicalMCPDatabasePath(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func redactedMCPPath(path string) string {
	trimmed := strings.Trim(filepath.ToSlash(strings.TrimSpace(path)), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) <= 2 {
		return trimmed
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

func (manager *mcpDatabaseManager) identityLocked(database *managedMCPDatabase) mcpDatabaseIdentity {
	if database == nil {
		return mcpDatabaseIdentity{}
	}
	return mcpDatabaseIdentity{
		Name: database.spec.name, Epoch: manager.epoch,
		DatabaseIdentity: redactedMCPPath(database.spec.dbPath),
		ConfigIdentity:   indexer.DisplayConfigPath(database.spec.config),
		databasePath:     database.spec.dbPath,
	}
}

func (manager *mcpDatabaseManager) Current() mcpDatabaseIdentity {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.identityLocked(manager.active)
}

func (manager *mcpDatabaseManager) ResourceUsage() mcpDatabaseResourceUsage {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.resourceUsageLocked()
}

func (manager *mcpDatabaseManager) resourceUsageLocked() mcpDatabaseResourceUsage {
	usage := mcpDatabaseResourceUsage{
		Active:                       manager.identityLocked(manager.active),
		ConfiguredDatabaseCount:      len(manager.order),
		LoadedDatabaseCount:          len(manager.loaded) + manager.openingDatabaseCount,
		AggregateSQLiteCacheBudgetMB: manager.openingSQLiteCacheBudgetMB,
		MaxOpenDatabasePools:         manager.maxOpenDatabasePools,
		MaxSQLiteCacheBudgetMB:       manager.maxSQLiteCacheBudgetMB,
	}
	for _, database := range manager.loaded {
		usage.AggregateSQLiteCacheBudgetMB += mcpDatabaseCacheBudget(database.spec)
		if database.retired {
			usage.RetiredDatabaseCount++
		}
	}
	return usage
}

func (manager *mcpDatabaseManager) List() []mcpDatabaseSummary {
	_, result := manager.Catalog()
	return result
}

func (manager *mcpDatabaseManager) Catalog() (mcpDatabaseIdentity, []mcpDatabaseSummary) {
	manager.mu.Lock()
	activeName := ""
	activeIdentity := mcpDatabaseIdentity{}
	if manager.active != nil {
		activeName = manager.active.spec.name
		activeIdentity = manager.identityLocked(manager.active)
	}
	specs := make([]mcpDatabaseSpec, 0, len(manager.order))
	for _, name := range manager.order {
		specs = append(specs, manager.specs[name])
	}
	manager.mu.Unlock()

	result := make([]mcpDatabaseSummary, 0, len(specs))
	for _, spec := range specs {
		info, err := os.Stat(spec.dbPath)
		available := err == nil && !info.IsDir()
		result = append(result, mcpDatabaseSummary{
			Name: spec.name, Description: spec.description, Mode: spec.mode,
			Active: spec.name == activeName, Available: available,
			DatabaseIdentity: redactedMCPPath(spec.dbPath),
			ConfigIdentity:   indexer.DisplayConfigPath(spec.config),
		})
	}
	return activeIdentity, result
}

func (manager *mcpDatabaseManager) Acquire() (mcpDatabaseLease, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.active == nil {
		return mcpDatabaseLease{}, fmt.Errorf("MCP database manager is closed")
	}
	database := manager.active
	database.refs++
	var releaseOnce sync.Once
	return mcpDatabaseLease{
		DB: database.db, Config: database.spec.config, Identity: manager.identityLocked(database),
		// A lease is a value and can be copied by callers. Keep the once state
		// inside the shared closure so every copy releases the database ref at
		// most once in total, rather than decrementing another in-flight lease.
		release: func() { releaseOnce.Do(func() { manager.release(database) }) },
	}, nil
}

func (manager *mcpDatabaseManager) release(database *managedMCPDatabase) {
	var closeDB *indexer.DB
	var cleanupSpec *mcpDatabaseSpec
	manager.mu.Lock()
	if database.refs > 0 {
		database.refs--
	}
	if database.retired && database.refs == 0 {
		if manager.loaded[database.spec.name] == database {
			delete(manager.loaded, database.spec.name)
		}
		closeDB = database.db
		if database.deleteOnRetire {
			spec := database.spec
			cleanupSpec = &spec
		}
	}
	manager.mu.Unlock()
	if closeDB != nil {
		_ = closeDB.Close()
	}
	if cleanupSpec != nil {
		_ = indexer.RemoveRetiredDatabaseGeneration(cleanupSpec.config, cleanupSpec.dbPath)
	}
}

// ReloadCurrent opens the immutable generation selected by the just-published
// pointer and atomically makes it active for subsequent leases. Existing
// requests retain their old DB object until they release it, which is exactly
// the snapshot behavior the former in-place table copy was trying to provide
// at much greater I/O cost.
func (manager *mcpDatabaseManager) ReloadCurrent(ctx context.Context) (mcpDatabaseReload, error) {
	select {
	case manager.switchGate <- struct{}{}:
		defer func() { <-manager.switchGate }()
	case <-ctx.Done():
		return mcpDatabaseReload{}, ctx.Err()
	}

	manager.mu.Lock()
	if manager.closed || manager.active == nil {
		manager.mu.Unlock()
		return mcpDatabaseReload{}, newToolError(ErrorDatabaseSwitchUnavailable, "database", "the MCP database manager is not available", true, nil, nil)
	}
	old := manager.active
	spec := old.spec
	manager.mu.Unlock()

	publishedPath, err := indexer.ConfiguredDatabasePath(spec.config)
	if err != nil {
		return mcpDatabaseReload{}, err
	}
	if canonicalMCPDatabasePath(publishedPath) == canonicalMCPDatabasePath(spec.dbPath) {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.closed || manager.active != old {
			return mcpDatabaseReload{}, newToolError(ErrorDatabaseSwitchUnavailable, "database", "the active database changed during generation reload", true, nil, nil)
		}
		return mcpDatabaseReload{DB: old.db, Config: old.spec.config, Identity: manager.identityLocked(old)}, nil
	}

	spec.dbPath = publishedPath
	candidate, err := openManagedMCPDatabase(ctx, spec)
	if err != nil {
		return mcpDatabaseReload{}, err
	}
	health, err := candidate.db.HealthConfiguredDepth(ctx, candidate.spec.config, indexer.HealthQuick)
	if err != nil {
		_ = candidate.db.Close()
		return mcpDatabaseReload{}, databaseTargetUnavailable(spec.name, "health_check_failed")
	}
	if !health.CanServeIndexQueries() {
		_ = candidate.db.Close()
		return mcpDatabaseReload{}, databaseTargetHealthUnavailable(spec.name, health)
	}
	latestPath, err := indexer.ConfiguredDatabasePath(spec.config)
	if err != nil {
		_ = candidate.db.Close()
		return mcpDatabaseReload{}, err
	}
	if canonicalMCPDatabasePath(latestPath) != canonicalMCPDatabasePath(publishedPath) {
		_ = candidate.db.Close()
		return mcpDatabaseReload{}, newToolError(ErrorConflictingGeneration, "database", "a newer database generation was published during reload", true, nil,
			map[string]any{"guidance": "Retry ck3_refresh status so the MCP server can bind the latest published generation."})
	}

	var closeOld *indexer.DB
	manager.mu.Lock()
	if manager.closed || manager.active != old {
		manager.mu.Unlock()
		_ = candidate.db.Close()
		return mcpDatabaseReload{}, newToolError(ErrorDatabaseSwitchUnavailable, "database", "the active database changed during generation reload", true, nil, nil)
	}
	old.retired = true
	old.deleteOnRetire = true
	candidate.retired = false
	manager.specs[spec.name] = spec
	manager.loaded[spec.name] = candidate
	manager.active = candidate
	manager.epoch++
	candidate.refs++ // Pin the rebound runtime until its current tool call ends.
	var releaseOnce sync.Once
	releaseCandidate := func() { releaseOnce.Do(func() { manager.release(candidate) }) }
	identity := manager.identityLocked(candidate)
	if old.refs == 0 {
		closeOld = old.db
	}
	manager.mu.Unlock()
	if closeOld != nil {
		_ = closeOld.Close()
		_ = indexer.RemoveRetiredDatabaseGeneration(old.spec.config, old.spec.dbPath)
	}
	return mcpDatabaseReload{DB: candidate.db, Config: candidate.spec.config, Identity: identity, release: releaseCandidate}, nil
}

func (manager *mcpDatabaseManager) Switch(ctx context.Context, rawName string) (mcpDatabaseSwitchResult, error) {
	name := strings.ToLower(strings.TrimSpace(rawName))
	select {
	case manager.switchGate <- struct{}{}:
		defer func() { <-manager.switchGate }()
	case <-ctx.Done():
		return mcpDatabaseSwitchResult{}, ctx.Err()
	}

	manager.mu.Lock()
	if manager.closed || manager.active == nil {
		manager.mu.Unlock()
		return mcpDatabaseSwitchResult{}, newToolError(ErrorDatabaseSwitchUnavailable, "database", "the MCP database manager is not available", true, nil, nil)
	}
	spec, exists := manager.specs[name]
	previous := manager.identityLocked(manager.active)
	if !exists {
		available := append([]string(nil), manager.order...)
		manager.mu.Unlock()
		return mcpDatabaseSwitchResult{}, newToolError(ErrorDatabaseTargetNotFound, "database", "the requested MCP database name is not configured", false,
			map[string]any{"name": name, "available": available}, map[string]any{"tool": "ck3_database", "operation": "list"})
	}
	if manager.active.spec.name == name {
		active := manager.identityLocked(manager.active)
		manager.mu.Unlock()
		return mcpDatabaseSwitchResult{
			Previous: previous, Active: active, Changed: false, Status: "ready",
			Guidance: []string{"The requested database was already active; subsequent calls continue to use it."},
		}, nil
	}
	beforeCommit := manager.beforeSwitchCommit
	candidate := manager.loaded[name]
	if candidate != nil {
		candidate.refs++ // Pin a retired database while it is revalidated.
	}
	reservedCacheBudgetMB := 0
	if candidate == nil {
		reservedCacheBudgetMB = mcpDatabaseCacheBudget(spec)
		usage := manager.resourceUsageLocked()
		projectedPools := usage.LoadedDatabaseCount + 1
		projectedCacheBudgetMB := uint64(usage.AggregateSQLiteCacheBudgetMB) + uint64(reservedCacheBudgetMB)
		cacheBudgetExceeded := usage.AggregateSQLiteCacheBudgetMB > manager.maxSQLiteCacheBudgetMB ||
			reservedCacheBudgetMB > manager.maxSQLiteCacheBudgetMB-usage.AggregateSQLiteCacheBudgetMB
		if projectedPools > manager.maxOpenDatabasePools || cacheBudgetExceeded {
			manager.mu.Unlock()
			return mcpDatabaseSwitchResult{}, databaseResourceBudgetExceeded(name, usage, projectedPools, projectedCacheBudgetMB)
		}
		manager.openingDatabaseCount++
		manager.openingSQLiteCacheBudgetMB += reservedCacheBudgetMB
	}
	manager.mu.Unlock()
	reservationActive := reservedCacheBudgetMB > 0
	releaseReservation := func() {
		if !reservationActive {
			return
		}
		manager.mu.Lock()
		manager.releaseOpeningReservationLocked(reservedCacheBudgetMB)
		manager.mu.Unlock()
		reservationActive = false
	}
	defer releaseReservation()

	newlyOpened := false
	if candidate == nil {
		opened, err := openManagedMCPDatabase(ctx, spec)
		if err != nil {
			return mcpDatabaseSwitchResult{}, err
		}
		candidate = opened
		newlyOpened = true
	}
	health, err := candidate.db.HealthConfiguredDepth(ctx, candidate.spec.config, indexer.HealthQuick)
	if err != nil {
		if newlyOpened {
			_ = candidate.db.Close()
		} else {
			manager.release(candidate)
		}
		return mcpDatabaseSwitchResult{}, databaseTargetUnavailable(name, "health_check_failed")
	}
	if !health.CanServeIndexQueries() {
		if newlyOpened {
			_ = candidate.db.Close()
		} else {
			manager.release(candidate)
		}
		return mcpDatabaseSwitchResult{}, databaseTargetHealthUnavailable(name, health)
	}
	if err := ctx.Err(); err != nil {
		if newlyOpened {
			_ = candidate.db.Close()
		} else {
			manager.release(candidate)
		}
		return mcpDatabaseSwitchResult{}, err
	}
	if beforeCommit != nil {
		beforeCommit()
	}

	var closeOld *indexer.DB
	manager.mu.Lock()
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		if newlyOpened {
			_ = candidate.db.Close()
		} else {
			manager.release(candidate)
		}
		return mcpDatabaseSwitchResult{}, err
	}
	if manager.closed {
		manager.mu.Unlock()
		if newlyOpened {
			_ = candidate.db.Close()
		} else {
			manager.release(candidate)
		}
		return mcpDatabaseSwitchResult{}, newToolError(ErrorDatabaseSwitchUnavailable, "database", "the MCP database manager closed during the switch", true, nil, nil)
	}
	old := manager.active
	old.retired = true
	candidate.retired = false
	if reservationActive {
		manager.releaseOpeningReservationLocked(reservedCacheBudgetMB)
		reservationActive = false
	}
	if !newlyOpened && candidate.refs > 0 {
		candidate.refs-- // Release the switch pin after reactivation.
	}
	manager.loaded[name] = candidate
	manager.active = candidate
	manager.epoch++
	active := manager.identityLocked(candidate)
	if old.refs == 0 {
		if manager.loaded[old.spec.name] == old {
			delete(manager.loaded, old.spec.name)
		}
		closeOld = old.db
	}
	manager.mu.Unlock()
	if closeOld != nil {
		_ = closeOld.Close()
	}
	return mcpDatabaseSwitchResult{
		Previous: previous, Active: active, Changed: true, Status: health.Status,
		ScanGeneration: health.ScanGeneration, ScanRevision: health.ScanRevision, ScanStatus: health.ScanStatus,
		DatabaseFingerprint: health.DatabaseFingerprint,
		Guidance:            []string{"The switch is complete. Calls already running remain bound to their original database; subsequent calls use the active database named here."},
	}, nil
}

func (manager *mcpDatabaseManager) releaseOpeningReservationLocked(cacheBudgetMB int) {
	if manager.openingDatabaseCount > 0 {
		manager.openingDatabaseCount--
	}
	if cacheBudgetMB >= manager.openingSQLiteCacheBudgetMB {
		manager.openingSQLiteCacheBudgetMB = 0
	} else {
		manager.openingSQLiteCacheBudgetMB -= cacheBudgetMB
	}
}

func openManagedMCPDatabase(ctx context.Context, spec mcpDatabaseSpec) (*managedMCPDatabase, error) {
	info, err := os.Stat(spec.dbPath)
	if err != nil || info.IsDir() {
		return nil, databaseTargetUnavailable(spec.name, "database_missing")
	}
	db, err := indexer.OpenReadOnlyWithOptions(spec.dbPath, spec.config.SQLiteReadOptions())
	if err != nil {
		return nil, databaseTargetUnavailable(spec.name, "open_failed")
	}
	if err := db.RestoreEngineRules(ctx, spec.config.EngineLogs); err != nil {
		_ = db.Close()
		return nil, databaseTargetUnavailable(spec.name, "engine_rules_unavailable")
	}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &managedMCPDatabase{spec: spec, db: db}, nil
}

func databaseTargetUnavailable(name, reason string) error {
	return newToolError(ErrorDatabaseTargetUnavailable, "database", "the configured MCP database could not be opened safely", true,
		map[string]any{"name": name, "reason": reason},
		map[string]any{"guidance": "Check the administrator-configured database or config file, rebuild that index if needed, then retry by name."})
}

func databaseResourceBudgetExceeded(name string, usage mcpDatabaseResourceUsage, projectedPools int, projectedCacheBudgetMB uint64) error {
	return newToolError(ErrorServerBusy, "database",
		"opening the configured MCP database would exceed the process SQLite pool budget", true,
		map[string]any{
			"name": name, "reason": "database_pool_budget_exceeded",
			"loaded_database_count":            usage.LoadedDatabaseCount,
			"retired_database_count":           usage.RetiredDatabaseCount,
			"aggregate_sqlite_cache_budget_mb": usage.AggregateSQLiteCacheBudgetMB,
			"projected_loaded_database_count":  projectedPools,
			"projected_sqlite_cache_budget_mb": projectedCacheBudgetMB,
			"max_open_database_pools":          usage.MaxOpenDatabasePools,
			"max_sqlite_cache_budget_mb":       usage.MaxSQLiteCacheBudgetMB,
		},
		map[string]any{"guidance": "Wait for calls holding retired database leases to finish, or have the administrator raise both process limits after reviewing memory capacity."})
}

func databaseTargetHealthUnavailable(name string, health indexer.HealthReport) error {
	return newToolError(ErrorDatabaseTargetUnavailable, "database", "the configured MCP database is not ready for index queries", true,
		map[string]any{
			"name": name, "reason": "health_not_ready", "status": health.Status,
			"scan_status": health.ScanStatus, "authoritative_database": health.AuthoritativeDatabase,
			"map_database": health.MapDatabase, "fts5_available": health.FTS5Available,
			"missing_indexes": health.MissingIndexes, "index_rule_version": health.IndexRuleVersion,
		},
		map[string]any{"guidance": "Rebuild or repair the configured target until health --require-ready succeeds, then retry the switch."})
}

func (manager *mcpDatabaseManager) Close() {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.closed = true
	manager.active = nil
	var databases []*indexer.DB
	for name, loaded := range manager.loaded {
		loaded.retired = true
		if loaded.refs == 0 {
			databases = append(databases, loaded.db)
			delete(manager.loaded, name)
		}
	}
	manager.mu.Unlock()
	for _, db := range databases {
		_ = db.Close()
	}
}
