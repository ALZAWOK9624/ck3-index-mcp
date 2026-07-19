package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseMetadataIsSynchronized(t *testing.T) {
	root := repositoryRoot(t)
	version := strings.TrimSpace(readFile(t, filepath.Join(root, "VERSION")))
	if version != "0.4.0" {
		t.Fatalf("release version = %q, want 0.4.0", version)
	}

	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "plugin", "ck3-index", ".codex-plugin", "plugin.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != version {
		t.Fatalf("plugin manifest version = %q, VERSION = %q", manifest.Version, version)
	}
	changelog := readFile(t, filepath.Join(root, "CHANGELOG.md"))
	if !strings.Contains(changelog, "## "+version+" -") {
		t.Fatalf("CHANGELOG lacks a release heading for %s", version)
	}
}

func TestReleaseSourcePluginIsPortable(t *testing.T) {
	root := repositoryRoot(t)
	var settings struct {
		Version    int    `json:"version"`
		ConfigPath string `json:"config_path"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "plugin", "ck3-index", "config", "settings.json"))), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Version != 1 || settings.ConfigPath != "" {
		t.Fatalf("source plugin settings must be portable, got %#v", settings)
	}

	var mcp struct {
		Servers map[string]struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "plugin", "ck3-index", ".mcp.json"))), &mcp); err != nil {
		t.Fatal(err)
	}
	server, ok := mcp.Servers["ck3_index"]
	if !ok || server.Cwd != "." || !strings.Contains(strings.ToLower(server.Command), "powershell") {
		t.Fatalf("source Windows MCP launcher is invalid: %#v", server)
	}

	for _, relative := range []string{
		filepath.Join("plugin", "ck3-index", "scripts", "start-ck3-index.ps1"),
		filepath.Join("plugin", "ck3-index", "scripts", "start-ck3-index.sh"),
	} {
		script := readFile(t, filepath.Join(root, relative))
		if !strings.Contains(script, "incomplete") || !strings.Contains(script, "SHA-256") {
			t.Fatalf("%s does not fail closed for an incomplete or altered GIS sidecar", relative)
		}
	}
}

func TestReleaseScriptsEnforceRCAndReproducibilityGates(t *testing.T) {
	root := repositoryRoot(t)
	windows := readFile(t, filepath.Join(root, "tools", "release_personal_plugin.ps1"))
	for _, marker := range []string{
		"AllowUnlicensedRC",
		"PROJECT_LICENSE_MISSING",
		"-trimpath",
		"REPRODUCIBLE_BUILD_MISMATCH",
		"build_release_bundle.py",
		"verify_release_mcp.py",
		"config_path = ''",
	} {
		if !strings.Contains(windows, marker) {
			t.Fatalf("Windows release script lacks %q", marker)
		}
	}

	linux := readFile(t, filepath.Join(root, "tools", "release_plugin_linux.sh"))
	for _, marker := range []string{
		"CK3_INDEX_ALLOW_UNLICENSED_RC",
		"PROJECT_LICENSE_MISSING",
		"-trimpath",
		"REPRODUCIBLE_BUILD_MISMATCH",
		"build_release_bundle.py",
		"verify_release_mcp.py",
		`printf '{"config_path":"","version":1}`,
	} {
		if !strings.Contains(linux, marker) {
			t.Fatalf("Linux release script lacks %q", marker)
		}
	}
	if strings.Contains(linux, "https://www.whiteboxgeo.com/") {
		t.Fatal("Linux release script must read WhiteboxTools URLs and hashes from the pinned manifest")
	}
}

func TestPinnedThirdPartyReleaseMetadata(t *testing.T) {
	root := repositoryRoot(t)
	var manifest struct {
		Version   string `json:"version"`
		Platforms map[string]struct {
			ArchiveSHA256 string `json:"archive_sha256"`
			BinarySHA256  string `json:"binary_sha256"`
		} `json:"platforms"`
	}
	path := filepath.Join(root, "third_party", "whitebox-tools-v2.4.0.json")
	if err := json.Unmarshal([]byte(readFile(t, path)), &manifest); err != nil {
		t.Fatal(err)
	}
	hash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, platform := range []string{"windows-x64", "linux-x64"} {
		entry, ok := manifest.Platforms[platform]
		if !ok || !hash.MatchString(entry.ArchiveSHA256) || !hash.MatchString(entry.BinarySHA256) {
			t.Fatalf("invalid pinned WhiteboxTools metadata for %s", platform)
		}
	}
	if manifest.Version != "2.4.0" {
		t.Fatalf("WhiteboxTools version = %q, want 2.4.0", manifest.Version)
	}
	license, err := os.ReadFile(filepath.Join(root, "third_party", "WHITEBOXTOOLS_LICENSE.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(license), "The MIT License") {
		t.Fatal("bundled WhiteboxTools license text is missing or unexpected")
	}
}

func TestSkillDocumentsEveryCanonicalMapCLI(t *testing.T) {
	root := repositoryRoot(t)
	canonical := readFile(t, filepath.Join(root, "skill", "ck3-coding", "SKILL.md"))
	plugin := readFile(t, filepath.Join(root, "plugin", "ck3-index", "skills", "ck3-coding", "SKILL.md"))
	if canonical != plugin {
		t.Fatal("plugin skill copy differs from the canonical skill")
	}
	for _, command := range []string{
		"ck3-index map audit [operation]",
		"ck3-index map province-mapping <spec.json>",
		"ck3-index map physical-context <spec.json>",
		"ck3-index map migration-snapshot <spec.json>",
		"ck3-index map migrate <spec.json>",
		"ck3-index map recipes",
		"ck3-index map metric <spec.json>",
		"ck3-index map route <spec.json>",
		"ck3-index map render <spec.json>",
	} {
		if !strings.Contains(canonical, command) {
			t.Fatalf("model-facing skill lacks CLI command %q", command)
		}
	}
}
