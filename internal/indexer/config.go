package indexer

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed default_config.toml
var defaultConfigText string

type Config struct {
	ConfigPath string
	Database   string
	// BaseDatabase is an optional prebuilt index holding only the immutable
	// upstream layers. A full refresh seeds from it instead of reparsing those
	// trees, so two projects that share the same game/mod sources pay that cost
	// once. A compatible published generation is preferred when available.
	BaseDatabase           string
	EngineLogs             string
	ArtifactRoot           string
	MigrationSnapshotRoot  string
	ArtifactRetentionHours int
	GISEnabled             bool
	GISAnalysis            string
	GISCacheRoot           string
	GISCacheMaxGiB         int
	GISTimeoutSeconds      int
	GISSidecarPath         string
	GISSidecarSHA256       string
	// SaveRoots are the only directories a save-reading tool may read from.
	// Saves arrive from untrusted uploads and never live under a source
	// root, so they get their own explicitly configured boundary.
	SaveRoots []string
	// SaveTokenMapRoot holds the version-matched *.tokens.txt maps that name
	// a binary save's fields.
	SaveTokenMapRoot string
	// SaveMaxBytes caps one save file.
	SaveMaxBytes int64
	// SQLite read-pool and MCP task limits are optional. Zero values preserve
	// the historical defaults so old TOML files and programmatic callers keep
	// the same behavior.
	SQLiteReadConnections      int
	SQLiteCacheMBPerConnection int
	SQLiteMMapLimitMB          int
	// MaxOpenDatabasePools and MaxSQLiteCacheBudgetMB bound the aggregate
	// SQLite resources held by the hot-switchable MCP database catalog. A
	// candidate pool is charged before it is opened, and retired pools remain
	// charged until their final in-flight lease is released.
	MaxOpenDatabasePools       int
	MaxSQLiteCacheBudgetMB     int
	MCPMaxTasks                int
	MCPMaxHeavyTasks           int
	MCPMaxRasterTasks          int
	MCPMaxQueuedTasks          int
	MCPQueueTimeoutSeconds     int
	MCPExecutionTimeoutSeconds int
	// MCPDatabaseName identifies the primary database in MCP responses.
	// MCPDatabases is a closed, administrator-configured catalog of additional
	// databases the running MCP process may select by name. Tool callers never
	// submit filesystem paths.
	MCPDatabaseName        string
	MCPDatabaseDescription string
	MCPDatabases           []MCPDatabaseTarget
	Sources                []Source
	ForceClean             bool
	// Full staged refreshes verify all file bytes before reusing derived rows.
	// This is internal execution state, never a TOML option.
	verifyContent bool
}

type MCPDatabaseTarget struct {
	Name        string
	Description string
	Database    string
	ConfigPath  string
}

// SourceRole identifies why a configured source exists. Rank remains solely
// responsible for CK3 override precedence; callers must not infer a source's
// identity from its name or rank.
type SourceRole string

const (
	SourceRoleProject    SourceRole = "project"
	SourceRoleDependency SourceRole = "dependency"
	SourceRoleGame       SourceRole = "game"
	SourceRoleReference  SourceRole = "reference"
)

type Source struct {
	Name         string
	Path         string
	Rank         int
	Role         SourceRole
	Private      bool
	ResourceOnly bool

	// privateSet lets TOML retain an explicit private=false for a project
	// source while old configurations continue to receive safe defaults.
	privateSet bool
}

// NormalizeConfig fills compatibility defaults for old source blocks and
// validates the resulting source model. New callers should use its returned
// configuration rather than deriving project/game identity from rank or name.
func NormalizeConfig(cfg Config) (Config, error) {
	if err := normalizeResourceLimits(&cfg); err != nil {
		return Config{}, err
	}
	if err := normalizeMCPDatabaseCatalog(&cfg); err != nil {
		return Config{}, err
	}
	sources, err := normalizeSources(cfg.Sources)
	if err != nil {
		return Config{}, err
	}
	cfg.Sources = sources
	return cfg, nil
}

const maxMCPDatabaseTargets = 16

func normalizeMCPDatabaseCatalog(cfg *Config) error {
	name, err := normalizeMCPDatabaseName(cfg.MCPDatabaseName)
	if err != nil {
		return fmt.Errorf("mcp_database_name: %w", err)
	}
	if name == "" {
		name = "default"
	}
	cfg.MCPDatabaseName = name
	cfg.MCPDatabaseDescription = strings.TrimSpace(cfg.MCPDatabaseDescription)
	if len(cfg.MCPDatabaseDescription) > 512 {
		return fmt.Errorf("mcp_database_description must not exceed 512 bytes")
	}
	if len(cfg.MCPDatabases) > maxMCPDatabaseTargets {
		return fmt.Errorf("mcp_database may contain at most %d targets", maxMCPDatabaseTargets)
	}
	if len(cfg.MCPDatabases) > 0 && cfg.MaxOpenDatabasePools < 2 {
		return fmt.Errorf("max_open_database_pools must be at least 2 when mcp_database targets are configured so a candidate can be verified before replacing the active pool")
	}
	names := map[string]struct{}{name: {}}
	for index := range cfg.MCPDatabases {
		target := &cfg.MCPDatabases[index]
		target.Name, err = normalizeMCPDatabaseName(target.Name)
		if err != nil {
			return fmt.Errorf("mcp_database %d name: %w", index+1, err)
		}
		if target.Name == "" {
			return fmt.Errorf("mcp_database %d has no name", index+1)
		}
		if _, duplicate := names[target.Name]; duplicate {
			return fmt.Errorf("duplicate MCP database name %q", target.Name)
		}
		names[target.Name] = struct{}{}
		target.Description = strings.TrimSpace(target.Description)
		if len(target.Description) > 512 {
			return fmt.Errorf("mcp_database %q description must not exceed 512 bytes", target.Name)
		}
		target.Database = strings.TrimSpace(target.Database)
		target.ConfigPath = strings.TrimSpace(target.ConfigPath)
		if (target.Database == "") == (target.ConfigPath == "") {
			return fmt.Errorf("mcp_database %q must configure exactly one of database or config", target.Name)
		}
	}
	return nil
}

func normalizeMCPDatabaseName(value string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(value))
	if name == "" {
		return "", nil
	}
	if len(name) > 64 {
		return "", fmt.Errorf("must not exceed 64 bytes")
	}
	for index, r := range name {
		allowed := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !allowed || index == 0 && (r == '_' || r == '-') {
			return "", fmt.Errorf("must use lowercase letters, digits, underscores, or hyphens and start with a letter or digit")
		}
	}
	return name, nil
}

func normalizeResourceLimits(cfg *Config) error {
	// A process-wide cache budget did not exist in older configurations. Keep
	// those configurations loadable when they intentionally use a larger
	// single SQLite pool: an omitted new limit defaults to at least the pool
	// that already had to be opened. An explicitly configured budget remains a
	// hard bound and is still rejected below.
	cacheBudgetUnspecified := cfg.MaxSQLiteCacheBudgetMB == 0
	limits := []struct {
		name         string
		value        *int
		defaultValue int
	}{
		{"sqlite_read_connections", &cfg.SQLiteReadConnections, DefaultSQLiteReadConnections},
		{"sqlite_cache_mb_per_connection", &cfg.SQLiteCacheMBPerConnection, DefaultSQLiteCacheMBPerConnection},
		{"sqlite_mmap_limit_mb", &cfg.SQLiteMMapLimitMB, DefaultSQLiteMMapLimitMB},
		{"max_open_database_pools", &cfg.MaxOpenDatabasePools, DefaultMaxOpenDatabasePools},
		{"max_sqlite_cache_budget_mb", &cfg.MaxSQLiteCacheBudgetMB, DefaultMaxSQLiteCacheBudgetMB},
		{"mcp_max_tasks", &cfg.MCPMaxTasks, DefaultMCPMaxTasks},
		{"mcp_max_heavy_tasks", &cfg.MCPMaxHeavyTasks, DefaultMCPMaxHeavyTasks},
		{"mcp_max_raster_tasks", &cfg.MCPMaxRasterTasks, DefaultMCPMaxRasterTasks},
		{"mcp_max_queued_tasks", &cfg.MCPMaxQueuedTasks, DefaultMCPMaxQueuedTasks},
		{"mcp_queue_timeout_seconds", &cfg.MCPQueueTimeoutSeconds, DefaultMCPQueueTimeoutSeconds},
		{"mcp_execution_timeout_seconds", &cfg.MCPExecutionTimeoutSeconds, DefaultMCPExecutionTimeoutSeconds},
	}
	for _, limit := range limits {
		if *limit.value < 0 {
			return fmt.Errorf("%s must be a positive integer", limit.name)
		}
		if *limit.value == 0 {
			*limit.value = limit.defaultValue
		}
	}
	if cacheBudgetUnspecified && cfg.SQLiteCacheMBPerConnection > 0 &&
		cfg.SQLiteReadConnections <= int(^uint(0)>>1)/cfg.SQLiteCacheMBPerConnection {
		singlePoolBudgetMB := cfg.SQLiteReadConnections * cfg.SQLiteCacheMBPerConnection
		if cfg.MaxSQLiteCacheBudgetMB < singlePoolBudgetMB {
			cfg.MaxSQLiteCacheBudgetMB = singlePoolBudgetMB
		}
	}
	if cfg.MCPMaxHeavyTasks >= cfg.MCPMaxTasks {
		return fmt.Errorf("mcp_max_heavy_tasks must be lower than mcp_max_tasks so ordinary requests retain one execution slot")
	}
	if cfg.MCPMaxRasterTasks > cfg.MCPMaxHeavyTasks {
		return fmt.Errorf("mcp_max_raster_tasks must not exceed the shared mcp_max_heavy_tasks budget")
	}
	if cfg.MCPMaxHeavyTasks >= cfg.SQLiteReadConnections {
		return fmt.Errorf("sqlite_read_connections must exceed mcp_max_heavy_tasks so ordinary requests retain one database connection")
	}
	if cfg.SQLiteReadConnections > cfg.MaxSQLiteCacheBudgetMB/cfg.SQLiteCacheMBPerConnection {
		return fmt.Errorf("max_sqlite_cache_budget_mb must cover sqlite_read_connections * sqlite_cache_mb_per_connection for at least one database pool")
	}
	return nil
}

func normalizeSources(sources []Source) ([]Source, error) {
	return normalizeSourcesWithProject(sources, true)
}

// normalizeSourcesWithProject is used by documentation-only game-source
// readers as well as full workspace configuration. A game-only fixture can
// legitimately lack a project source; scanning and refresh still use the
// strict public normalizeSources entry point above.
func normalizeSourcesWithProject(sources []Source, requireProject bool) ([]Source, error) {
	out := append([]Source(nil), sources...)
	// Old configs encoded identity only indirectly through conventional source
	// names and ranks. Preserve them once at the config boundary: reserve the
	// old game slot, then choose the lowest-precedence non-game legacy source as
	// the project. Runtime code below this boundary only sees explicit roles.
	hasExplicitProject := false
	for _, source := range out {
		if SourceRole(strings.ToLower(strings.TrimSpace(string(source.Role)))) == SourceRoleProject {
			hasExplicitProject = true
			break
		}
	}
	legacyProject := -1
	if !hasExplicitProject {
		for index, source := range out {
			if strings.TrimSpace(string(source.Role)) != "" || legacyGameSource(source) {
				continue
			}
			if legacyProject < 0 || source.Rank < out[legacyProject].Rank {
				legacyProject = index
			}
		}
	}
	names := map[string]bool{}
	ranks := map[int]bool{}
	projects := 0
	for index := range out {
		source := &out[index]
		name := strings.TrimSpace(source.Name)
		if name == "" {
			return nil, fmt.Errorf("source %d has no name", index+1)
		}
		if strings.TrimSpace(source.Path) == "" {
			return nil, fmt.Errorf("source %q has no path", name)
		}
		if source.Rank <= 0 {
			return nil, fmt.Errorf("source rank must be a positive integer (source %q)", name)
		}
		nameKey := strings.ToLower(name)
		if names[nameKey] {
			return nil, fmt.Errorf("duplicate source name %q", name)
		}
		if ranks[source.Rank] {
			return nil, fmt.Errorf("duplicate source rank %d; source precedence must be unambiguous", source.Rank)
		}
		names[nameKey] = true
		ranks[source.Rank] = true

		inputRole := SourceRole(strings.ToLower(strings.TrimSpace(string(source.Role))))
		role := inputRole
		if role == "" {
			// Compatibility migration only: historical configs did not carry a
			// role. The normalized Source always does, and all operational code
			// below this boundary uses Role rather than repeating this heuristic.
			switch {
			case legacyGameSource(*source):
				role = SourceRoleGame
			case index == legacyProject:
				role = SourceRoleProject
			default:
				role = SourceRoleDependency
			}
		}
		switch role {
		case SourceRoleProject, SourceRoleDependency, SourceRoleGame, SourceRoleReference:
		default:
			return nil, fmt.Errorf("source %q has unsupported role %q", name, source.Role)
		}
		if source.ResourceOnly && role == SourceRoleProject {
			return nil, fmt.Errorf("source %q cannot be both project and resource_only", name)
		}
		source.Role = role
		// A role-less source is a legacy configuration, so retain the former
		// safe project default. An explicit Source.Role supplied through the Go
		// API may intentionally set Private=false and must be preserved.
		if !source.privateSet && inputRole == "" && role == SourceRoleProject {
			source.Private = true
		}
		if role == SourceRoleProject {
			projects++
		}
	}
	if requireProject && projects != 1 {
		return nil, fmt.Errorf("configuration requires exactly one project source, found %d", projects)
	}
	return out, nil
}

func legacyGameSource(source Source) bool {
	switch strings.ToLower(strings.TrimSpace(source.Name)) {
	case "game", "vanilla", "ck3":
		return true
	default:
		return false
	}
}

// ProjectSource returns the one configured writable-project identity. It does
// not make any filesystem writeability claim: refresh only reads source files.
func ProjectSource(cfg Config) (Source, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Source{}, err
	}
	for _, source := range normalized.Sources {
		if source.Role == SourceRoleProject {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("configuration has no project source")
}

// GameSource returns the configured CK3 game installation, if one exists.
func GameSource(cfg Config) (Source, bool) {
	sources, err := normalizeSourcesWithProject(cfg.Sources, false)
	if err != nil {
		return Source{}, false
	}
	for _, source := range sources {
		if source.Role == SourceRoleGame && strings.TrimSpace(source.Path) != "" {
			return source, true
		}
	}
	return Source{}, false
}

// PrivateSourceNames supplies the source-name policy used by public MCP
// filtering. Patch evidence is handled separately because it is never a
// configured source.
func PrivateSourceNames(cfg Config) map[string]bool {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return nil
	}
	result := make(map[string]bool, len(normalized.Sources))
	for _, source := range normalized.Sources {
		// Keep both true and false entries. Public filtering is fail-closed for
		// unknown provenance, so omitting an explicitly public source would
		// accidentally redact it as if it were unknown.
		result[strings.ToLower(source.Name)] = source.Private
	}
	return result
}

func sourceReplacePaths(src Source) ([]string, error) {
	if src.ResourceOnly {
		return nil, nil
	}
	entries, err := os.ReadDir(src.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var descriptors []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source %q descriptor entry must not be a symbolic link", src.Name)
		}
		name := strings.ToLower(entry.Name())
		if name == "descriptor.mod" || strings.HasSuffix(name, ".mod") {
			descriptors = append(descriptors, filepath.Join(src.Path, entry.Name()))
		}
	}
	sort.Strings(descriptors)
	seen := map[string]bool{}
	var out []string
	for _, path := range descriptors {
		if _, err := sourceRegularFileInfo(path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			key, value, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(key) != "replace_path" {
				continue
			}
			rel := normalizeReplacePath(value)
			if rel != "" && !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
		scanErr := sc.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	sort.Strings(out)
	return out, nil
}

func normalizeReplacePath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	value = strings.Trim(filepath.ToSlash(value), "/")
	if value == "" || value == "." || strings.HasPrefix(value, "../") {
		return ""
	}
	return strings.ToLower(value)
}

func collectSourceReplacePaths(sources []Source) (map[int][]string, error) {
	out := map[int][]string{}
	for _, src := range sources {
		paths, err := sourceReplacePaths(src)
		if err != nil {
			return nil, fmt.Errorf("read replace_path for source %s: %w", src.Name, err)
		}
		out[src.Rank] = append(out[src.Rank], paths...)
	}
	return out, nil
}

func relReplacedByHigherSource(rel string, sourceRank int, replacePaths map[int][]string) bool {
	_, _, ok := replacePathEvidence(rel, sourceRank, replacePaths)
	return ok
}

// replacePathEvidence returns the highest-priority descriptor rule that hides
// rel. Keeping the rule and owning rank lets agent-facing queries explain why
// a file is absent instead of exposing only an opaque overridden boolean.
func replacePathEvidence(rel string, sourceRank int, replacePaths map[int][]string) (int, string, bool) {
	rel = strings.ToLower(strings.Trim(filepath.ToSlash(rel), "/"))
	bestRank := 0
	bestRule := ""
	for rank, paths := range replacePaths {
		if rank >= sourceRank {
			continue
		}
		for _, prefix := range paths {
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				if bestRank == 0 || rank < bestRank || (rank == bestRank && len(prefix) > len(bestRule)) {
					bestRank = rank
					bestRule = prefix
				}
			}
		}
	}
	return bestRank, bestRule, bestRank != 0
}

func WriteDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(defaultConfigText), 0644)
}

type configTOML struct {
	Database                   string            `toml:"database"`
	BaseDatabase               string            `toml:"base_database"`
	EngineLogs                 string            `toml:"engine_logs"`
	ArtifactRoot               string            `toml:"artifact_root"`
	MigrationSnapshotRoot      string            `toml:"migration_snapshot_root"`
	ArtifactRetentionHours     *int              `toml:"artifact_retention_hours"`
	GISEnabled                 *bool             `toml:"gis_enabled"`
	GISAnalysis                string            `toml:"gis_analysis"`
	GISCacheRoot               string            `toml:"gis_cache_root"`
	GISCacheMaxGiB             *int              `toml:"gis_cache_max_gib"`
	GISTimeoutSeconds          *int              `toml:"gis_timeout_seconds"`
	GISSidecarPath             string            `toml:"gis_sidecar_path"`
	GISSidecarSHA256           string            `toml:"gis_sidecar_sha256"`
	SaveRoots                  []string          `toml:"save_roots"`
	SaveTokenMapRoot           string            `toml:"save_token_map_root"`
	SaveMaxBytes               *int64            `toml:"save_max_bytes"`
	SQLiteReadConnections      *int              `toml:"sqlite_read_connections"`
	SQLiteCacheMBPerConnection *int              `toml:"sqlite_cache_mb_per_connection"`
	SQLiteMMapLimitMB          *int              `toml:"sqlite_mmap_limit_mb"`
	MaxOpenDatabasePools       *int              `toml:"max_open_database_pools"`
	MaxSQLiteCacheBudgetMB     *int              `toml:"max_sqlite_cache_budget_mb"`
	MCPMaxTasks                *int              `toml:"mcp_max_tasks"`
	MCPMaxHeavyTasks           *int              `toml:"mcp_max_heavy_tasks"`
	MCPMaxRasterTasks          *int              `toml:"mcp_max_raster_tasks"`
	MCPMaxQueuedTasks          *int              `toml:"mcp_max_queued_tasks"`
	MCPQueueTimeoutSeconds     *int              `toml:"mcp_queue_timeout_seconds"`
	MCPExecutionTimeoutSeconds *int              `toml:"mcp_execution_timeout_seconds"`
	MCPDatabaseName            string            `toml:"mcp_database_name"`
	MCPDatabaseDescription     string            `toml:"mcp_database_description"`
	MCPDatabases               []mcpDatabaseTOML `toml:"mcp_database"`
	Sources                    []sourceTOML      `toml:"source"`
}

type mcpDatabaseTOML struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Database    string `toml:"database"`
	Config      string `toml:"config"`
}

type sourceTOML struct {
	Name         string `toml:"name"`
	Path         string `toml:"path"`
	Rank         any    `toml:"rank"`
	Role         string `toml:"role"`
	Private      *bool  `toml:"private"`
	ResourceOnly bool   `toml:"resource_only"`
}

// defaultSaveMaxBytes is generous for a real CK3 save (a 1066 start is under
// 1 MiB, a late megacampaign tens of MiB) while still refusing an upload that
// is obviously not a save.
const defaultSaveMaxBytes int64 = 512 << 20

func LoadConfig(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	var decoded configTOML
	metadata, err := toml.DecodeFile(absPath, &decoded)
	if err != nil {
		return Config{}, fmt.Errorf("decode TOML config %s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("unknown configuration field(s): %s", strings.Join(keys, ", "))
	}
	if strings.TrimSpace(decoded.Database) == "" {
		return Config{}, fmt.Errorf("database must be explicitly configured in %s", path)
	}
	if len(decoded.Sources) == 0 {
		return Config{}, fmt.Errorf("no sources configured in %s", path)
	}
	baseDir := filepath.Dir(absPath)
	cfg := Config{
		ConfigPath:             absPath,
		Database:               filepath.FromSlash(decoded.Database),
		BaseDatabase:           resolveOptionalConfigPath(baseDir, decoded.BaseDatabase),
		EngineLogs:             resolveOptionalConfigPath(baseDir, decoded.EngineLogs),
		ArtifactRoot:           resolveOptionalConfigPath(baseDir, decoded.ArtifactRoot),
		MigrationSnapshotRoot:  resolveOptionalConfigPath(baseDir, decoded.MigrationSnapshotRoot),
		ArtifactRetentionHours: 168,
		GISEnabled:             true,
		GISAnalysis:            strings.ToLower(strings.TrimSpace(decoded.GISAnalysis)),
		GISCacheRoot:           resolveOptionalConfigPath(baseDir, decoded.GISCacheRoot),
		GISCacheMaxGiB:         8,
		GISTimeoutSeconds:      900,
		GISSidecarPath:         resolveOptionalConfigPath(baseDir, decoded.GISSidecarPath),
		GISSidecarSHA256:       strings.ToLower(strings.TrimSpace(decoded.GISSidecarSHA256)),
		MCPDatabaseName:        decoded.MCPDatabaseName,
		MCPDatabaseDescription: decoded.MCPDatabaseDescription,
	}
	for _, input := range decoded.MCPDatabases {
		cfg.MCPDatabases = append(cfg.MCPDatabases, MCPDatabaseTarget{
			Name:        input.Name,
			Description: input.Description,
			Database:    resolveOptionalConfigPath(baseDir, input.Database),
			ConfigPath:  resolveOptionalConfigPath(baseDir, input.Config),
		})
	}
	if cfg.GISAnalysis == "" {
		cfg.GISAnalysis = "terrain"
	}
	if decoded.ArtifactRetentionHours != nil {
		if *decoded.ArtifactRetentionHours <= 0 {
			return Config{}, fmt.Errorf("artifact_retention_hours must be a positive integer")
		}
		cfg.ArtifactRetentionHours = *decoded.ArtifactRetentionHours
	}
	if decoded.GISEnabled != nil {
		cfg.GISEnabled = *decoded.GISEnabled
	}
	if decoded.GISCacheMaxGiB != nil {
		if *decoded.GISCacheMaxGiB <= 0 {
			return Config{}, fmt.Errorf("gis_cache_max_gib must be a positive integer")
		}
		cfg.GISCacheMaxGiB = *decoded.GISCacheMaxGiB
	}
	if decoded.GISTimeoutSeconds != nil {
		if *decoded.GISTimeoutSeconds <= 0 {
			return Config{}, fmt.Errorf("gis_timeout_seconds must be a positive integer")
		}
		cfg.GISTimeoutSeconds = *decoded.GISTimeoutSeconds
	}
	cfg.SaveTokenMapRoot = resolveOptionalConfigPath(baseDir, decoded.SaveTokenMapRoot)
	cfg.SaveMaxBytes = defaultSaveMaxBytes
	if decoded.SaveMaxBytes != nil {
		if *decoded.SaveMaxBytes <= 0 {
			return Config{}, fmt.Errorf("save_max_bytes must be a positive integer")
		}
		cfg.SaveMaxBytes = *decoded.SaveMaxBytes
	}
	resourceLimits := []struct {
		name  string
		input *int
		value *int
	}{
		{"sqlite_read_connections", decoded.SQLiteReadConnections, &cfg.SQLiteReadConnections},
		{"sqlite_cache_mb_per_connection", decoded.SQLiteCacheMBPerConnection, &cfg.SQLiteCacheMBPerConnection},
		{"sqlite_mmap_limit_mb", decoded.SQLiteMMapLimitMB, &cfg.SQLiteMMapLimitMB},
		{"max_open_database_pools", decoded.MaxOpenDatabasePools, &cfg.MaxOpenDatabasePools},
		{"max_sqlite_cache_budget_mb", decoded.MaxSQLiteCacheBudgetMB, &cfg.MaxSQLiteCacheBudgetMB},
		{"mcp_max_tasks", decoded.MCPMaxTasks, &cfg.MCPMaxTasks},
		{"mcp_max_heavy_tasks", decoded.MCPMaxHeavyTasks, &cfg.MCPMaxHeavyTasks},
		{"mcp_max_raster_tasks", decoded.MCPMaxRasterTasks, &cfg.MCPMaxRasterTasks},
		{"mcp_max_queued_tasks", decoded.MCPMaxQueuedTasks, &cfg.MCPMaxQueuedTasks},
		{"mcp_queue_timeout_seconds", decoded.MCPQueueTimeoutSeconds, &cfg.MCPQueueTimeoutSeconds},
		{"mcp_execution_timeout_seconds", decoded.MCPExecutionTimeoutSeconds, &cfg.MCPExecutionTimeoutSeconds},
	}
	for _, limit := range resourceLimits {
		if limit.input == nil {
			continue
		}
		if *limit.input <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive integer", limit.name)
		}
		*limit.value = *limit.input
	}
	seenSaveRoots := make(map[string]struct{}, len(decoded.SaveRoots))
	for _, root := range decoded.SaveRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			return Config{}, fmt.Errorf("save_roots must not contain an empty path")
		}
		resolved := resolveOptionalConfigPath(baseDir, trimmed)
		if _, duplicate := seenSaveRoots[resolved]; duplicate {
			return Config{}, fmt.Errorf("save_roots lists %q more than once", root)
		}
		seenSaveRoots[resolved] = struct{}{}
		cfg.SaveRoots = append(cfg.SaveRoots, resolved)
	}
	cfg.Sources = make([]Source, 0, len(decoded.Sources))
	for _, input := range decoded.Sources {
		rank, err := strictPositiveSourceRank(input.Rank)
		if err != nil {
			return Config{}, err
		}
		source := Source{
			Name:         strings.TrimSpace(input.Name),
			Path:         resolveOptionalConfigPath(baseDir, input.Path),
			Rank:         rank,
			Role:         SourceRole(strings.ToLower(strings.TrimSpace(input.Role))),
			ResourceOnly: input.ResourceOnly,
		}
		if input.Private != nil {
			source.Private = *input.Private
			source.privateSet = true
		}
		cfg.Sources = append(cfg.Sources, source)
	}
	for i := range cfg.Sources {
		if cfg.Sources[i].Role == SourceRoleProject && !cfg.Sources[i].privateSet {
			cfg.Sources[i].Private = true
		}
	}
	if cfg, err = NormalizeConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.BaseDatabase != "" {
		databasePath, databaseErr := ConfiguredDatabasePath(cfg)
		if databaseErr != nil {
			return Config{}, databaseErr
		}
		// Seeding copies the base over the staging cache. Letting the two names
		// resolve to one file would make a refresh overwrite the shared upstream
		// index with a single project's snapshot.
		if canonicalConfigPath(databasePath) == canonicalConfigPath(cfg.BaseDatabase) {
			return Config{}, fmt.Errorf("base_database must name a different file than database")
		}
	}
	if cfg.ArtifactRoot == "" {
		cfg.ArtifactRoot = resolveConfigPath(baseDir, "cache/artifacts")
	}
	if cfg.MigrationSnapshotRoot == "" {
		cfg.MigrationSnapshotRoot = resolveConfigPath(baseDir, "cache/migration-snapshots")
	}
	if cfg.GISAnalysis != "terrain" && cfg.GISAnalysis != "full" {
		return Config{}, fmt.Errorf("gis_analysis must be terrain or full")
	}
	if cfg.GISCacheRoot == "" {
		cfg.GISCacheRoot = resolveConfigPath(baseDir, "cache/gis")
	}
	if cfg.GISSidecarPath == "" {
		name := "whitebox_tools"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		cfg.GISSidecarPath = resolveConfigPath(baseDir, filepath.ToSlash(filepath.Join("sidecar", name)))
	}
	if value := strings.TrimSpace(os.Getenv("CK3_INDEX_GIS_SIDECAR_PATH")); value != "" {
		cfg.GISSidecarPath = filepath.Clean(value)
	}
	if value := strings.TrimSpace(os.Getenv("CK3_INDEX_GIS_SIDECAR_SHA256")); value != "" {
		cfg.GISSidecarSHA256 = strings.ToLower(value)
	}
	if cfg.GISSidecarSHA256 != "" {
		if len(cfg.GISSidecarSHA256) != 64 {
			return Config{}, fmt.Errorf("gis_sidecar_sha256 must be a 64-character SHA-256 value")
		}
		for _, r := range cfg.GISSidecarSHA256 {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return Config{}, fmt.Errorf("gis_sidecar_sha256 must contain lowercase hexadecimal characters only")
			}
		}
	}
	return cfg, nil
}

func strictPositiveSourceRank(value any) (int, error) {
	rank, ok := value.(int64)
	if !ok || rank <= 0 || strconv.IntSize == 32 && rank > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("source rank must be a positive integer")
	}
	return int(rank), nil
}

func resolveOptionalConfigPath(baseDir, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return resolveConfigPath(baseDir, value)
}

func validateSources(sources []Source) error {
	_, err := normalizeSources(sources)
	return err
}

// ConfiguredDatabaseAnchorPath resolves the stable path named by the
// configuration. Full refreshes publish immutable generation files next to
// this anchor and atomically move a small pointer between them; publication
// locks and orphan sweeps must therefore use the anchor rather than whichever
// generation is current at one instant.
func ConfiguredDatabaseAnchorPath(cfg Config) (string, error) {
	value := strings.TrimSpace(cfg.Database)
	if value == "" {
		return "", fmt.Errorf("database is not configured")
	}
	native := filepath.FromSlash(value)
	if isConfigAbsPath(value) || filepath.IsAbs(native) {
		return filepath.Clean(native), nil
	}
	if strings.TrimSpace(cfg.ConfigPath) == "" {
		return "", fmt.Errorf("relative database path requires a configuration file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(cfg.ConfigPath), native)), nil
}

// ConfiguredDatabasePath is the single authority for resolving the currently
// published index database used by scans, CLI commands, and MCP. A relative
// configured path is always anchored to the configuration file; it is never
// interpreted relative to the caller's current working directory.
func ConfiguredDatabasePath(cfg Config) (string, error) {
	anchor, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		return "", err
	}
	return resolvePublishedDatabasePath(anchor)
}

func resolveConfigPath(baseDir, value string) string {
	native := filepath.FromSlash(value)
	if isConfigAbsPath(value) || filepath.IsAbs(native) {
		return filepath.Clean(native)
	}
	return filepath.Clean(filepath.Join(baseDir, native))
}

func isConfigAbsPath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '/' || value[2] == '\\') {
		return true
	}
	return false
}
