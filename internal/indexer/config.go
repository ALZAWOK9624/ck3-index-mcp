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

type Source struct {
	Name string
	Path string
	Rank int
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
		name := strings.ToLower(entry.Name())
		if name == "descriptor.mod" || strings.HasSuffix(name, ".mod") {
			descriptors = append(descriptors, filepath.Join(src.Path, entry.Name()))
		}
	}
	sort.Strings(descriptors)
	seen := map[string]bool{}
	var out []string
	for _, path := range descriptors {
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
	if err := validateSources(cfg.Sources); err != nil {
		return Config{}, err
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
	names := map[string]bool{}
	ranks := map[int]bool{}
	for index, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" {
			return fmt.Errorf("source %d has no name", index+1)
		}
		if strings.TrimSpace(source.Path) == "" {
			return fmt.Errorf("source %q has no path", name)
		}
		if source.Rank <= 0 {
			return fmt.Errorf("source %q rank must be a positive integer", name)
		}
		nameKey := strings.ToLower(name)
		if names[nameKey] {
			return fmt.Errorf("duplicate source name %q", name)
		}
		if ranks[source.Rank] {
			return fmt.Errorf("duplicate source rank %d; source precedence must be unambiguous", source.Rank)
		}
		names[nameKey] = true
		ranks[source.Rank] = true
	}
	return nil
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
