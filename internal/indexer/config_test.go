package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigResolvesSourcePaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "ck3-index.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	cfgText := `database = "cache/test.sqlite"
artifact_root = "tmp/packages"
migration_snapshot_root = "tmp/migration-snapshots"
artifact_retention_hours = 24
[[source]]
name = "relative"
path = "../project"
rank = 1
[[source]]
name = "linux_abs"
path = "/data/godherja-beta"
rank = 2
[[source]]
name = "windows_abs"
path = "D:/mod-project/game"
rank = 3
resource_only = true
`
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 3 {
		t.Fatalf("sources = %d, want 3", len(cfg.Sources))
	}
	wantArtifacts := filepath.Clean(filepath.Join(filepath.Dir(cfgPath), "tmp/packages"))
	if cfg.ArtifactRoot != wantArtifacts || cfg.ArtifactRetentionHours != 24 {
		t.Fatalf("artifact config = %q/%d, want %q/24", cfg.ArtifactRoot, cfg.ArtifactRetentionHours, wantArtifacts)
	}
	wantSnapshots := filepath.Clean(filepath.Join(filepath.Dir(cfgPath), "tmp/migration-snapshots"))
	if cfg.MigrationSnapshotRoot != wantSnapshots {
		t.Fatalf("migration snapshot root = %q, want %q", cfg.MigrationSnapshotRoot, wantSnapshots)
	}
	if !cfg.GISEnabled || cfg.GISAnalysis != "terrain" || cfg.GISCacheMaxGiB != 8 || cfg.GISTimeoutSeconds != 900 {
		t.Fatalf("unexpected GIS defaults: enabled=%v analysis=%q max=%d timeout=%d", cfg.GISEnabled, cfg.GISAnalysis, cfg.GISCacheMaxGiB, cfg.GISTimeoutSeconds)
	}
	wantGISCache := filepath.Clean(filepath.Join(filepath.Dir(cfgPath), "cache/gis"))
	if cfg.GISCacheRoot != wantGISCache {
		t.Fatalf("GIS cache root = %q, want %q", cfg.GISCacheRoot, wantGISCache)
	}
	if cfg.SQLiteReadConnections != DefaultSQLiteReadConnections || cfg.SQLiteCacheMBPerConnection != DefaultSQLiteCacheMBPerConnection || cfg.SQLiteMMapLimitMB != DefaultSQLiteMMapLimitMB ||
		cfg.MCPMaxTasks != DefaultMCPMaxTasks || cfg.MCPMaxHeavyTasks != DefaultMCPMaxHeavyTasks || cfg.MCPMaxRasterTasks != DefaultMCPMaxRasterTasks ||
		cfg.MCPMaxQueuedTasks != DefaultMCPMaxQueuedTasks || cfg.MCPQueueTimeoutSeconds != DefaultMCPQueueTimeoutSeconds || cfg.MCPExecutionTimeoutSeconds != DefaultMCPExecutionTimeoutSeconds {
		t.Fatalf("resource defaults changed: %+v", cfg)
	}
	if cfg.MCPDatabaseName != "default" || cfg.MCPDatabaseDescription != "" || len(cfg.MCPDatabases) != 0 {
		t.Fatalf("MCP database catalog defaults changed: name=%q description=%q targets=%+v", cfg.MCPDatabaseName, cfg.MCPDatabaseDescription, cfg.MCPDatabases)
	}

	wantRelative := filepath.Clean(filepath.Join(filepath.Dir(cfgPath), "../project"))
	if cfg.Sources[0].Path != wantRelative {
		t.Fatalf("relative path = %q, want %q", cfg.Sources[0].Path, wantRelative)
	}

	wantLinuxAbs := filepath.Clean(filepath.FromSlash("/data/godherja-beta"))
	if cfg.Sources[1].Path != wantLinuxAbs {
		t.Fatalf("linux absolute path = %q, want %q", cfg.Sources[1].Path, wantLinuxAbs)
	}

	wantWindowsAbs := filepath.Clean(filepath.FromSlash("D:/mod-project/game"))
	if cfg.Sources[2].Path != wantWindowsAbs {
		t.Fatalf("windows absolute path = %q, want %q", cfg.Sources[2].Path, wantWindowsAbs)
	}
	if !cfg.Sources[2].ResourceOnly {
		t.Fatal("resource_only source flag was not preserved")
	}
}

func TestLoadConfigResolvesNamedMCPDatabaseCatalog(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config", "ck3-index.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	text := `database = "cache/project.sqlite"
mcp_database_name = "Project"
mcp_database_description = "Current project and overlays"
[[mcp_database]]
name = "Vanilla"
description = "Vanilla-only workspace"
config = "base-vanilla.toml"
[[mcp_database]]
name = "previous_snapshot"
database = "cache/previous.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
`
	if err := os.WriteFile(cfgPath, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPDatabaseName != "project" || cfg.MCPDatabaseDescription != "Current project and overlays" || len(cfg.MCPDatabases) != 2 {
		t.Fatalf("catalog = name=%q description=%q targets=%+v", cfg.MCPDatabaseName, cfg.MCPDatabaseDescription, cfg.MCPDatabases)
	}
	if target := cfg.MCPDatabases[0]; target.Name != "vanilla" || target.ConfigPath != filepath.Join(filepath.Dir(cfgPath), "base-vanilla.toml") || target.Database != "" {
		t.Fatalf("config target = %+v", target)
	}
	if target := cfg.MCPDatabases[1]; target.Name != "previous_snapshot" || target.Database != filepath.Join(filepath.Dir(cfgPath), "cache", "previous.sqlite") || target.ConfigPath != "" {
		t.Fatalf("snapshot target = %+v", target)
	}
}

func TestLoadConfigRejectsInvalidMCPDatabaseCatalog(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"duplicate primary name", `mcp_database_name = "project"
[[mcp_database]]
name = "PROJECT"
database = "other.sqlite"
`, `duplicate MCP database name "project"`},
		{"path and config together", `[[mcp_database]]
name = "other"
database = "other.sqlite"
config = "other.toml"
`, `must configure exactly one of database or config`},
		{"missing target location", `[[mcp_database]]
name = "other"
`, `must configure exactly one of database or config`},
		{"invalid name", `[[mcp_database]]
name = "../other"
database = "other.sqlite"
`, `must use lowercase letters, digits, underscores, or hyphens`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "ck3-index.toml")
			text := "database = \"cache/test.sqlite\"\n" + tt.body + `[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
`
			if err := os.WriteFile(cfgPath, []byte(text), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigAcceptsLowMemoryResourceLimits(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	text := `database = "cache/test.sqlite"
sqlite_read_connections = 2
sqlite_cache_mb_per_connection = 16
sqlite_mmap_limit_mb = 256
mcp_max_tasks = 4
mcp_max_heavy_tasks = 1
mcp_max_raster_tasks = 1
mcp_max_queued_tasks = 8
mcp_queue_timeout_seconds = 3
mcp_execution_timeout_seconds = 60
[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
`
	if err := os.WriteFile(cfgPath, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SQLiteReadConnections != 2 || cfg.SQLiteCacheMBPerConnection != 16 || cfg.SQLiteMMapLimitMB != 256 || cfg.MCPMaxTasks != 4 || cfg.MCPMaxHeavyTasks != 1 || cfg.MCPMaxRasterTasks != 1 || cfg.MCPMaxQueuedTasks != 8 || cfg.MCPQueueTimeoutSeconds != 3 || cfg.MCPExecutionTimeoutSeconds != 60 {
		t.Fatalf("resource limits=%+v", cfg)
	}
	options := cfg.SQLiteReadOptions()
	if options.Connections != 2 || options.CacheMBPerConnection != 16 || options.MMapLimitMB != 256 {
		t.Fatalf("SQLite options=%+v", options)
	}
}

func TestLoadConfigRejectsInconsistentResourceLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits string
		want   string
	}{
		{
			name:   "ordinary task slot is reserved",
			limits: "mcp_max_tasks = 2\nmcp_max_heavy_tasks = 2\n",
			want:   "ordinary requests retain one execution slot",
		},
		{
			name:   "raster shares expensive budget",
			limits: "mcp_max_tasks = 4\nmcp_max_heavy_tasks = 1\nmcp_max_raster_tasks = 2\n",
			want:   "shared mcp_max_heavy_tasks budget",
		},
		{
			name:   "ordinary database connection is reserved",
			limits: "sqlite_read_connections = 2\nmcp_max_tasks = 4\nmcp_max_heavy_tasks = 2\n",
			want:   "ordinary requests retain one database connection",
		},
		{
			name:   "queue timeout is positive",
			limits: "mcp_queue_timeout_seconds = -1\n",
			want:   "mcp_queue_timeout_seconds must be a positive integer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "ck3-index.toml")
			text := "database = \"cache/test.sqlite\"\n" + tt.limits + `[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
`
			if err := os.WriteFile(cfgPath, []byte(text), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(cfgPath); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigRequiresExplicitDatabase(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("[[source]]\nname = \"game\"\npath = \"game\"\nrank = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("LoadConfig accepted a configuration without an explicit database")
	}
}

func TestConfiguredDatabasePathIsAnchoredToConfig(t *testing.T) {
	dir := t.TempDir()
	path, err := ConfiguredDatabasePath(Config{ConfigPath: filepath.Join(dir, "config", "ck3-index.toml"), Database: "cache/index.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "config", "cache", "index.sqlite")
	if path != want {
		t.Fatalf("ConfiguredDatabasePath() = %q, want %q", path, want)
	}
	if _, err := ConfiguredDatabasePath(Config{Database: "relative.sqlite"}); err == nil {
		t.Fatal("relative database without config path was accepted")
	}
}

func TestLoadConfigRejectsAmbiguousOrIncompleteSources(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"invalid rank", `[[source]]
name = "game"
path = "game"
rank = "oops"
`, "source rank must be a positive integer"},
		{"zero rank", `[[source]]
name = "game"
path = "game"
rank = 0
`, "source rank must be a positive integer"},
		{"missing name", `[[source]]
path = "game"
rank = 1
`, "has no name"},
		{"missing path", `[[source]]
name = "game"
rank = 1
`, "has no path"},
		{"duplicate name", `[[source]]
name = "game"
path = "game"
rank = 1
[[source]]
name = "GAME"
path = "other"
rank = 2
`, "duplicate source name"},
		{"duplicate rank", `[[source]]
name = "game"
path = "game"
rank = 1
[[source]]
name = "project"
path = "project"
rank = 1
`, "duplicate source rank"},
		{"resource only project", `[[source]]
name = "project"
path = "project"
rank = 1
role = "project"
resource_only = true
`, "cannot be both project and resource_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ck3-index.toml")
			text := "database = \"cache/test.sqlite\"\n" + tt.body
			if err := os.WriteFile(path, []byte(text), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadConfig error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigRejectsUnknownAndMalformedTOML(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown top level field",
			body: `database = "cache/test.sqlite"
surprise = true
[[source]]
name = "project"
path = "project"
rank = 1
`,
			want: "unknown configuration field(s): surprise",
		},
		{
			name: "unknown source field",
			body: `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
surprise = true
`,
			want: "unknown configuration field(s): source.surprise",
		},
		{
			name: "malformed toml",
			body: `database = "cache/test.sqlite
`,
			want: "decode TOML config",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ck3-index.toml")
			if err := os.WriteFile(path, []byte(test.body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadConfig error=%v, want substring %q", err, test.want)
			}
		})
	}
}
