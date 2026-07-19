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
