package indexer

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed default_config.toml
var defaultConfigText string

type Config struct {
	ConfigPath string
	Database   string
	Sources    []Source
	ForceClean bool
}

type Source struct {
	Name string
	Path string
	Rank int
}

func WriteDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(defaultConfigText), 0644)
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	cfg := Config{ConfigPath: path, Database: "cache/ck3_index.sqlite"}
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
				cfg.Database = filepath.FromSlash(val)
			}
			continue
		}
		switch key {
		case "name":
			cur.Name = val
		case "path":
			cur.Path = resolveConfigPath(filepath.Dir(path), val)
		case "rank":
			n, _ := strconv.Atoi(val)
			cur.Rank = n
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, err
	}
	if len(cfg.Sources) == 0 {
		return Config{}, fmt.Errorf("no sources configured in %s", path)
	}
	return cfg, nil
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
