package indexer

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func mapManifestFixture(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	write := func(rel, content string) {
		path := filepath.Join(game, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("map_data/definition.csv", "0;0;0;0;x;x;\n1;255;0;0;first;x;\n")
	write("map_data/default.map", "max_provinces = 2\n")
	return Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
}

// A configured GIS sidecar used to disable map-cache reuse outright, and that
// is the production configuration -- so the map cache was rebuilt on every full
// scan and never once paid for itself. Describing the sidecar in the
// fingerprint is what makes reuse possible at all.
func TestConfiguredGISSidecarNoLongerDisablesMapCacheReuse(t *testing.T) {
	ctx := context.Background()
	cfg := mapManifestFixture(t)

	plain, err := collectMapInputManifest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !plain.Reusable {
		t.Fatal("a configuration without GIS reported an unreusable map cache")
	}

	sidecar := filepath.Join(t.TempDir(), "whitebox_tools")
	if err := os.WriteFile(sidecar, []byte("sidecar contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	gis := cfg
	gis.GISEnabled = true
	gis.GISSidecarPath = sidecar
	gis.GISSidecarSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	gis.GISAnalysis = "terrain"

	withGIS, err := collectMapInputManifest(ctx, gis)
	if err != nil {
		t.Fatal(err)
	}
	if !withGIS.Reusable {
		t.Fatal("a configured GIS sidecar still refuses map-cache reuse")
	}
	if withGIS.Fingerprint == plain.Fingerprint {
		t.Fatal("enabling GIS did not change the map input fingerprint")
	}
}

// Refusing reuse was a blunt way of never retaining a stale sidecar. The
// replacement only holds if a sidecar that changes, disappears, or stops
// running changes the fingerprint and forces the rebuild.
func TestMapInputFingerprintTracksSidecarState(t *testing.T) {
	ctx := context.Background()
	cfg := mapManifestFixture(t)
	sidecarDir := t.TempDir()
	sidecar := filepath.Join(sidecarDir, "whitebox_tools")
	if err := os.WriteFile(sidecar, []byte("sidecar contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.GISEnabled = true
	cfg.GISSidecarPath = sidecar
	cfg.GISSidecarSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	cfg.GISAnalysis = "terrain"

	present, err := collectMapInputManifest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := collectMapInputManifest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if present.Fingerprint != stable.Fingerprint {
		t.Fatal("an unchanged configuration produced two different fingerprints; the cache could never match")
	}

	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	removed, err := collectMapInputManifest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Fingerprint == present.Fingerprint {
		t.Fatal("removing the sidecar did not change the fingerprint; stale GIS data would be retained")
	}

	// A re-pinned release hash has to invalidate too.
	repinned := cfg
	repinned.GISSidecarSHA256 = "1111111111111111111111111111111111111111111111111111111111111111"
	other, err := collectMapInputManifest(ctx, repinned)
	if err != nil {
		t.Fatal(err)
	}
	if other.Fingerprint == removed.Fingerprint {
		t.Fatal("re-pinning the sidecar hash did not change the fingerprint")
	}
}

// The caller collects the active set and hashes every map input before deciding
// whether a rebuild is needed. rebuildMapCache must consume that work rather
// than repeat it: for a real project the input set includes history, terrain,
// object data and large rasters, so repeating it means a second full source
// walk and a second read of every one of those files.
func TestRebuildMapCacheDoesNotRecollectItsInputs(t *testing.T) {
	data, err := os.ReadFile("map_context.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "func rebuildMapCache(")
	if start < 0 {
		t.Fatal("rebuildMapCache not found; this guard is stale")
	}
	next := regexp.MustCompile(`(?m)^func `).FindStringIndex(source[start+1:])
	end := len(source)
	if next != nil {
		end = start + 1 + next[0]
	}
	body := source[start:end]
	for _, forbidden := range []string{"collectActiveMapFiles(", "mapInputFingerprintForActive("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("rebuildMapCache calls %s again; the caller already paid for that walk and hash", forbidden)
		}
	}
	if !strings.Contains(body, "manifest.Active") {
		t.Error("rebuildMapCache no longer consumes the manifest's active set")
	}
}
