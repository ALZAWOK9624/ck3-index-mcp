package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigResolvesSourcePaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nested", "ck3-index.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	cfgText := `database = "cache/test.sqlite"
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
