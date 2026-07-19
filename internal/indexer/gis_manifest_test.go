package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

func TestWhiteboxReleaseManifestMatchesRuntimeContract(t *testing.T) {
	type platform struct {
		ArchiveURL    string `json:"archive_url"`
		ArchiveSHA256 string `json:"archive_sha256"`
		Binary        string `json:"binary"`
		BinarySHA256  string `json:"binary_sha256"`
	}
	var manifest struct {
		Version      string              `json:"version"`
		License      string              `json:"license"`
		LicenseFile  string              `json:"license_file"`
		Platforms    map[string]platform `json:"platforms"`
		AllowedTools []string            `json:"allowed_tools"`
	}
	root := filepath.Join("..", "..", "third_party")
	raw, err := os.ReadFile(filepath.Join(root, "whitebox-tools-v2.4.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != whiteboxRequiredVersion || manifest.License != "MIT" {
		t.Fatalf("unexpected pinned WhiteboxTools release: version=%q license=%q", manifest.Version, manifest.License)
	}
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, name := range []string{"windows-x64", "linux-x64"} {
		entry, ok := manifest.Platforms[name]
		if !ok || entry.ArchiveURL == "" || entry.Binary == "" || !sha.MatchString(entry.ArchiveSHA256) || !sha.MatchString(entry.BinarySHA256) {
			t.Fatalf("platform %q is missing a pinned download or SHA-256: %+v", name, entry)
		}
	}
	if _, err := os.Stat(filepath.Join(root, manifest.LicenseFile)); err != nil {
		t.Fatalf("bundled WhiteboxTools license is unavailable: %v", err)
	}
	runtimeTools := make([]string, 0, len(whiteboxAllowedTools))
	for tool := range whiteboxAllowedTools {
		runtimeTools = append(runtimeTools, tool)
	}
	sort.Strings(runtimeTools)
	sort.Strings(manifest.AllowedTools)
	if len(runtimeTools) != len(manifest.AllowedTools) {
		t.Fatalf("release allowlist length=%d runtime=%d", len(manifest.AllowedTools), len(runtimeTools))
	}
	for i := range runtimeTools {
		if runtimeTools[i] != manifest.AllowedTools[i] {
			t.Fatalf("release allowlist drift: manifest=%v runtime=%v", manifest.AllowedTools, runtimeTools)
		}
	}
}
