package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func mapEditFixture(t *testing.T) (*indexer.DB, indexer.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, cfg, dir
}

func terrainMCPArgs(confirm bool) map[string]any {
	return map[string]any{
		"operation": "compose",
		"confirm":   confirm,
		"layers": []any{map[string]any{
			"id": "fixture-range", "kind": "range", "domain": "land",
			"geometry": map[string]any{
				"type":        "point",
				"coordinates": []any{map[string]any{"x": 0, "y": 0}},
			},
			"width_px": 3, "feather_px": 1, "strength": 0.1,
			"roughness": 0.3, "detail": 1, "seed": 7,
		}},
	}
}

// fingerprintTree hashes every file under root so a test can prove nothing
// under it moved, without knowing which files the fixture happens to contain.
func fingerprintTree(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash.Write([]byte(filepath.ToSlash(rel)))
		hash.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(hash.Sum(nil))
}

func TestMCPTerrainEditWithoutConfirmationReturnsPreviewAndWritesNothing(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	result := callToolForTest(t, db, cfg, "map_terrain_edit", terrainMCPArgs(false))
	if result["isError"] == true {
		t.Fatalf("terrain preview failed: %+v", result)
	}
	content := result["content"].([]map[string]any)
	if len(content) != 2 || content[1]["type"] != "image" || content[1]["mimeType"] != "image/png" {
		t.Fatalf("preview did not return text plus PNG: %+v", content)
	}
	data, err := base64.StdEncoding.DecodeString(content[1]["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("invalid terrain preview PNG: %v", err)
	}
	structured := result["structuredContent"].(map[string]any)
	if id, _ := structured["artifact_id"].(string); id != "" {
		t.Fatalf("preview unexpectedly published artifact %q", id)
	}
	if _, err := os.Stat(filepath.Join(cfg.ArtifactRoot, "map-edits")); !os.IsNotExist(err) {
		t.Fatal("preview created the map-edits store")
	}
}

func TestMCPMapApplySplitStillRequiresExplicitConfirmation(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	result := callToolForTest(t, db, cfg, "map_apply_split", map[string]any{
		"province_id": 1,
		"seeds":       []any{map[string]any{"x": 0, "y": 0}, map[string]any{"x": 0, "y": 1}},
	})
	if result["isError"] != true {
		t.Fatalf("map_apply_split wrote without confirmation: %+v", result)
	}
	text := result["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "confirm") {
		t.Fatalf("refusal does not name confirm: %s", text)
	}
}

func TestMCPMapEditToolsLeaveEveryConfiguredSourceUntouched(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	before := map[string]string{}
	for _, source := range cfg.Sources {
		before[source.Name] = fingerprintTree(t, source.Path)
	}

	terrain := callToolForTest(t, db, cfg, "map_terrain_edit", terrainMCPArgs(true))
	if terrain["isError"] == true {
		t.Fatalf("terrain edit failed: %+v", terrain)
	}
	split := callToolForTest(t, db, cfg, "map_apply_split", map[string]any{
		"province_id": 1,
		"seeds":       []any{map[string]any{"x": 0, "y": 0}, map[string]any{"x": 0, "y": 1}},
		"confirm":     true,
	})
	if split["isError"] == true {
		t.Fatalf("apply split failed: %+v", split)
	}

	for _, source := range cfg.Sources {
		if fingerprintTree(t, source.Path) != before[source.Name] {
			t.Fatalf("configured source %q was modified by a map edit tool", source.Name)
		}
	}

	for index, result := range []map[string]any{terrain, split} {
		structured := result["structuredContent"].(map[string]any)
		if structured["applied"] != false {
			t.Fatalf("result does not report applied=false: %+v", structured)
		}
		dir, _ := structured["artifact_dir"].(string)
		if index == 0 {
			id, _ := structured["artifact_id"].(string)
			if id == "" {
				t.Fatalf("terrain result carries no artifact id: %+v", structured)
			}
			dir = filepath.Join(cfg.ArtifactRoot, "map-edits", id)
			if structured["requires_ck3_repack"] != true {
				t.Fatalf("terrain result did not require CK3 repacking: %+v", structured)
			}
		} else if dir == "" {
			t.Fatalf("split result carries no artifact directory: %+v", structured)
		}
		if entries, err := os.ReadDir(filepath.Join(dir, "map_data")); err != nil || len(entries) == 0 {
			t.Fatalf("artifact directory %s holds no map_data output: %v", dir, err)
		}
	}
}

func TestMCPMapEditToolsRejectPublicVisibility(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	args := terrainMCPArgs(true)
	args["visibility"] = "public"
	result := callToolForTest(t, db, cfg, "map_terrain_edit", args)
	if result["isError"] != true {
		t.Fatalf("public visibility was accepted for a source-derived raster: %+v", result)
	}
}

func TestMCPTerrainEditReportsACorrectableRequestAsInvalidArguments(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	args := terrainMCPArgs(true)
	args["region"] = map[string]any{"x": 900, "y": 900, "width": 8, "height": 8}
	result := callToolForTest(t, db, cfg, "map_terrain_edit", args)
	if result["isError"] != true {
		t.Fatalf("a region off the map was accepted: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != ErrorInvalidArguments {
		t.Fatalf("a correctable request was not reported as invalid arguments: %+v", structured)
	}
}

func TestMCPTerrainEditRejectsRetiredSynthesizeInterface(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	result := callToolForTest(t, db, cfg, "map_terrain_edit", map[string]any{
		"operation": "synthesize",
		"features": []any{map[string]any{
			"kind": "range", "path": []any{map[string]any{"x": 0, "y": 0}},
			"width": 1, "amplitude": 5,
		}},
	})
	if result["isError"] != true {
		t.Fatalf("retired synthesize/features/amplitude interface was accepted: %+v", result)
	}
}
