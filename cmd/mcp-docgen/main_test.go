package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratedDocumentationIsCurrent(t *testing.T) {
	root := repositoryRoot(t)
	if err := run(root, true); err != nil {
		t.Fatal(err)
	}
}

func TestUserDocumentationIsChineseAndSkillCatalogStaysEnglish(t *testing.T) {
	root := repositoryRoot(t)
	readme := readFile(t, filepath.Join(root, "README.md"))
	reference := readFile(t, filepath.Join(root, "docs", "MCP_TOOL_REFERENCE.md"))
	skill := readFile(t, filepath.Join(root, "skill", "ck3-coding", "SKILL.md"))

	checks := map[string]struct {
		content  string
		expected []string
	}{
		"README":         {content: readme, expected: []string{"标准模式", "核心工具", "地图工具"}},
		"tool reference": {content: reference, expected: []string{"MCP 工具参考", "| 参数 | 必填 | 类型 | 约束 | 说明 |", "已弃用的专家模式别名"}},
	}
	for name, check := range checks {
		for _, expected := range check.expected {
			if !strings.Contains(check.content, expected) {
				t.Fatalf("%s is missing Chinese marker %q", name, expected)
			}
		}
	}
	if strings.Contains(reference, "| Field | Required | Type | Constraints | Description |") {
		t.Fatal("tool reference still contains the English field table header")
	}
	if !strings.Contains(skill, "## MCP Tools") || !strings.Contains(skill, "### Core Tools") {
		t.Fatal("model-facing skill catalog must remain English")
	}
}

func TestReleaseMetadataUsesRepositoryVersion(t *testing.T) {
	root := repositoryRoot(t)
	version := strings.TrimSpace(readFile(t, filepath.Join(root, "VERSION")))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`).MatchString(version) {
		t.Fatalf("VERSION is not a release semver: %q", version)
	}

	var manifest struct {
		Version string `json:"version"`
	}
	manifestPath := filepath.Join(root, "plugin", "ck3-index", ".codex-plugin", "plugin.json")
	if err := json.Unmarshal([]byte(readFile(t, manifestPath)), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != version {
		t.Fatalf("plugin version = %q, VERSION = %q", manifest.Version, version)
	}

	startScript := readFile(t, filepath.Join(root, "plugin", "ck3-index", "scripts", "start-ck3-index.ps1"))
	if !strings.Contains(startScript, ".codex-plugin\\plugin.json") || strings.Contains(startScript, "ck3-index-v0.") {
		t.Fatal("plugin launcher must derive its binary name from plugin.json")
	}

	var settings struct {
		ConfigPath string `json:"config_path"`
	}
	settingsPath := filepath.Join(root, "plugin", "ck3-index", "config", "settings.json")
	if err := json.Unmarshal([]byte(readFile(t, settingsPath)), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ConfigPath != "" {
		t.Fatal("source plugin settings must not contain a machine-specific config path")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
