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
)

//go:embed default_config.toml
var defaultConfigText string

type Config struct {
	ConfigPath             string
	Database               string
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
	Sources                []Source
	ForceClean             bool
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
	Name    string
	Path    string
	Rank    int
	Role    SourceRole
	Private bool

	// privateSet lets TOML retain an explicit private=false for a project
	// source while old configurations continue to receive safe defaults.
	privateSet bool
}

// NormalizeConfig fills compatibility defaults for old source blocks and
// validates the resulting source model. New callers should use its returned
// configuration rather than deriving project/game identity from rank or name.
func NormalizeConfig(cfg Config) (Config, error) {
	sources, err := normalizeSources(cfg.Sources)
	if err != nil {
		return Config{}, err
	}
	cfg.Sources = sources
	return cfg, nil
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
			return nil, fmt.Errorf("source %q rank must be a positive integer", name)
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

func LoadConfig(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	f, err := os.Open(absPath)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	cfg := Config{
		ConfigPath: absPath, ArtifactRetentionHours: 168,
		GISEnabled: true, GISAnalysis: "terrain", GISCacheMaxGiB: 8, GISTimeoutSeconds: 900,
	}
	databaseConfigured := false
	var cur *Source
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[[source]]" {
			cfg.Sources = append(cfg.Sources, Source{})
			cur = &cfg.Sources[len(cfg.Sources)-1]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.Trim(strings.TrimSpace(v), `"`)
		if cur == nil {
			if key == "database" {
				databaseConfigured = true
				cfg.Database = filepath.FromSlash(val)
			} else if key == "engine_logs" {
				cfg.EngineLogs = resolveConfigPath(filepath.Dir(absPath), val)
			} else if key == "artifact_root" {
				cfg.ArtifactRoot = resolveConfigPath(filepath.Dir(absPath), val)
			} else if key == "migration_snapshot_root" {
				cfg.MigrationSnapshotRoot = resolveConfigPath(filepath.Dir(absPath), val)
			} else if key == "artifact_retention_hours" {
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					return Config{}, fmt.Errorf("artifact_retention_hours must be a positive integer")
				}
				cfg.ArtifactRetentionHours = n
			} else if key == "gis_enabled" {
				value, err := strconv.ParseBool(val)
				if err != nil {
					return Config{}, fmt.Errorf("gis_enabled must be true or false")
				}
				cfg.GISEnabled = value
			} else if key == "gis_analysis" {
				cfg.GISAnalysis = strings.ToLower(val)
			} else if key == "gis_cache_root" {
				cfg.GISCacheRoot = resolveConfigPath(filepath.Dir(absPath), val)
			} else if key == "gis_cache_max_gib" {
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					return Config{}, fmt.Errorf("gis_cache_max_gib must be a positive integer")
				}
				cfg.GISCacheMaxGiB = n
			} else if key == "gis_timeout_seconds" {
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					return Config{}, fmt.Errorf("gis_timeout_seconds must be a positive integer")
				}
				cfg.GISTimeoutSeconds = n
			} else if key == "gis_sidecar_path" {
				cfg.GISSidecarPath = resolveConfigPath(filepath.Dir(absPath), val)
			} else if key == "gis_sidecar_sha256" {
				cfg.GISSidecarSHA256 = strings.ToLower(strings.TrimSpace(val))
			}
			continue
		}
		switch key {
		case "name":
			cur.Name = val
		case "path":
			if strings.TrimSpace(val) == "" {
				return Config{}, fmt.Errorf("source path must not be empty")
			}
			cur.Path = resolveConfigPath(filepath.Dir(absPath), val)
		case "rank":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return Config{}, fmt.Errorf("source rank must be a positive integer, got %q", val)
			}
			cur.Rank = n
		case "role":
			cur.Role = SourceRole(strings.ToLower(val))
		case "private":
			private, err := strconv.ParseBool(val)
			if err != nil {
				return Config{}, fmt.Errorf("source private must be true or false")
			}
			cur.Private = private
			cur.privateSet = true
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, err
	}
	if !databaseConfigured || strings.TrimSpace(cfg.Database) == "" {
		return Config{}, fmt.Errorf("database must be explicitly configured in %s", path)
	}
	if len(cfg.Sources) == 0 {
		return Config{}, fmt.Errorf("no sources configured in %s", path)
	}
	// TOML cannot distinguish an omitted bool from false after decoding. Keep
	// the file-format default conservative for explicitly declared project
	// sources while allowing programmatic Config callers to use Private=false.
	for i := range cfg.Sources {
		if cfg.Sources[i].Role == SourceRoleProject && !cfg.Sources[i].privateSet {
			cfg.Sources[i].Private = true
		}
	}
	var normalizeErr error
	if cfg, normalizeErr = NormalizeConfig(cfg); normalizeErr != nil {
		return Config{}, normalizeErr
	}
	if cfg.ArtifactRoot == "" {
		cfg.ArtifactRoot = resolveConfigPath(filepath.Dir(absPath), "cache/artifacts")
	}
	if cfg.MigrationSnapshotRoot == "" {
		cfg.MigrationSnapshotRoot = resolveConfigPath(filepath.Dir(absPath), "cache/migration-snapshots")
	}
	if cfg.GISAnalysis != "terrain" && cfg.GISAnalysis != "full" {
		return Config{}, fmt.Errorf("gis_analysis must be terrain or full")
	}
	if cfg.GISCacheRoot == "" {
		cfg.GISCacheRoot = resolveConfigPath(filepath.Dir(absPath), "cache/gis")
	}
	if cfg.GISSidecarPath == "" {
		name := "whitebox_tools"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		cfg.GISSidecarPath = resolveConfigPath(filepath.Dir(absPath), filepath.ToSlash(filepath.Join("sidecar", name)))
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

func validateSources(sources []Source) error {
	_, err := normalizeSources(sources)
	return err
}

// ConfiguredDatabasePath is the single authority for resolving the index
// database used by scans, CLI commands, and MCP. A relative path is always
// anchored to the configuration file; it is never interpreted relative to the
// caller's current working directory.
func ConfiguredDatabasePath(cfg Config) (string, error) {
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
