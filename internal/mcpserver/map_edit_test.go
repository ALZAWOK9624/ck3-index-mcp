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

func planSplitForMCP(t *testing.T, db *indexer.DB, cfg indexer.Config) (string, string) {
	t.Helper()
	result := callToolForTest(t, db, cfg, "map_split_province", map[string]any{
		"province_id":        1,
		"seeds":              []any{map[string]any{"x": 0, "y": 0}, map[string]any{"x": 0, "y": 1}},
		"emit_definition":    true,
		"emit_history":       true,
		"emit_landed_titles": true,
	})
	if result["isError"] == true {
		t.Fatalf("split planning failed: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	planID, _ := structured["plan_id"].(string)
	planHash, _ := structured["plan_hash"].(string)
	if planID == "" || planHash == "" {
		t.Fatalf("split plan identity missing: %+v", structured)
	}
	return planID, planHash
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
	planID, planHash := planSplitForMCP(t, db, cfg)
	result := callToolForTest(t, db, cfg, "map_apply_split", map[string]any{
		"plan_id": planID, "plan_hash": planHash,
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
	planID, planHash := planSplitForMCP(t, db, cfg)
	split := callToolForTest(t, db, cfg, "map_apply_split", map[string]any{"plan_id": planID, "plan_hash": planHash, "confirm": true})
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
		dir := ""
		if index == 0 {
			id, _ := structured["artifact_id"].(string)
			if id == "" {
				t.Fatalf("terrain result carries no artifact id: %+v", structured)
			}
			dir = filepath.Join(cfg.ArtifactRoot, "map-edits", id)
			if structured["requires_ck3_repack"] != true {
				t.Fatalf("terrain result did not require CK3 repacking: %+v", structured)
			}
		} else {
			id, _ := structured["artifact_id"].(string)
			if id == "" {
				t.Fatalf("split result carries no artifact id: %+v", structured)
			}
			dir = filepath.Join(cfg.ArtifactRoot, "map-edits", "split-results", id)
		}
		if entries, err := os.ReadDir(filepath.Join(dir, "map_data")); err != nil || len(entries) == 0 {
			t.Fatalf("artifact directory %s holds no map_data output: %v", dir, err)
		}
	}
}

func TestMCPMapArtifactRequestKeysRecoverCommittedPublications(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)

	terrainArgs := terrainMCPArgs(true)
	terrainArgs["request_key"] = "review-terrain-1"
	firstTerrain := callToolForTest(t, db, cfg, "map_terrain_edit", terrainArgs)
	secondTerrain := callToolForTest(t, db, cfg, "map_terrain_edit", terrainArgs)
	if firstTerrain["isError"] == true || secondTerrain["isError"] == true {
		t.Fatalf("idempotent terrain publication failed: first=%+v second=%+v", firstTerrain, secondTerrain)
	}
	firstTerrainValue := firstTerrain["structuredContent"].(map[string]any)
	secondTerrainValue := secondTerrain["structuredContent"].(map[string]any)
	if firstTerrainValue["artifact_id"] != secondTerrainValue["artifact_id"] || secondTerrainValue["replayed"] != true {
		t.Fatalf("terrain retry did not recover one artifact: first=%+v second=%+v", firstTerrainValue, secondTerrainValue)
	}

	planArgs := map[string]any{
		"province_id":        1,
		"seeds":              []any{map[string]any{"x": 0, "y": 0}, map[string]any{"x": 0, "y": 1}},
		"emit_definition":    true,
		"emit_history":       true,
		"emit_landed_titles": true,
		"request_key":        "review-plan-1",
	}
	firstPlan := callToolForTest(t, db, cfg, "map_split_province", planArgs)
	secondPlan := callToolForTest(t, db, cfg, "map_split_province", planArgs)
	if firstPlan["isError"] == true || secondPlan["isError"] == true {
		t.Fatalf("idempotent split planning failed: first=%+v second=%+v", firstPlan, secondPlan)
	}
	firstPlanValue := firstPlan["structuredContent"].(map[string]any)
	secondPlanValue := secondPlan["structuredContent"].(map[string]any)
	if firstPlanValue["plan_id"] != secondPlanValue["plan_id"] {
		t.Fatalf("split plan retry created a second id: first=%+v second=%+v", firstPlanValue, secondPlanValue)
	}
	secondPlanState := secondPlanValue["plan_state"].(map[string]any)
	if secondPlanState["replayed"] != true {
		t.Fatalf("split plan retry was not reported as replayed: %+v", secondPlanState)
	}

	applyArgs := map[string]any{
		"plan_id": firstPlanValue["plan_id"], "plan_hash": firstPlanValue["plan_hash"],
		"confirm": true, "request_key": "review-apply-1",
	}
	firstApply := callToolForTest(t, db, cfg, "map_apply_split", applyArgs)
	secondApply := callToolForTest(t, db, cfg, "map_apply_split", applyArgs)
	if firstApply["isError"] == true || secondApply["isError"] == true {
		t.Fatalf("idempotent split apply failed: first=%+v second=%+v", firstApply, secondApply)
	}
	firstApplyValue := firstApply["structuredContent"].(map[string]any)
	secondApplyValue := secondApply["structuredContent"].(map[string]any)
	if firstApplyValue["artifact_id"] != secondApplyValue["artifact_id"] {
		t.Fatalf("split apply retry created a second artifact: first=%+v second=%+v", firstApplyValue, secondApplyValue)
	}
	secondApplyState := secondApplyValue["artifact_state"].(map[string]any)
	if secondApplyState["replayed"] != true {
		t.Fatalf("split apply retry was not reported as replayed: %+v", secondApplyState)
	}

	listed := callToolForTest(t, db, cfg, "map_artifact", map[string]any{"operation": "list", "limit": 10})
	if listed["isError"] == true {
		t.Fatalf("artifact list failed: %+v", listed)
	}
	listedValue := listed["structuredContent"].(map[string]any)
	if count, _ := listedValue["count"].(float64); count < 3 {
		t.Fatalf("artifact list missed committed publications: %+v", listedValue)
	}
	inspected := callToolForTest(t, db, cfg, "map_artifact", map[string]any{
		"operation": "inspect", "artifact_id": firstApplyValue["artifact_id"],
	})
	if inspected["isError"] == true || inspected["structuredContent"].(map[string]any)["status"] != "committed" {
		t.Fatalf("artifact inspect did not verify the split result: %+v", inspected)
	}

	conflictArgs := terrainMCPArgs(true)
	conflictArgs["request_key"] = "review-terrain-1"
	conflictArgs["region"] = map[string]any{"x": 0, "y": 0, "width": 1, "height": 1}
	conflict := callToolForTest(t, db, cfg, "map_terrain_edit", conflictArgs)
	if conflict["isError"] != true {
		t.Fatalf("request_key reuse with different input was accepted: %+v", conflict)
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

func TestMCPMapApplySplitRejectsDefinitionDriftAfterPlanning(t *testing.T) {
	db, cfg, _ := mapEditFixture(t)
	planID, planHash := planSplitForMCP(t, db, cfg)
	definitionPath, _, err := indexer.ActiveMapAsset(cfg, "map_data/definition.csv")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(definitionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# drift after plan\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result := callToolForTest(t, db, cfg, "map_apply_split", map[string]any{
		"plan_id": planID, "plan_hash": planHash, "confirm": true,
	})
	if result["isError"] != true {
		t.Fatalf("stale split plan was applied: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != ErrorSourceChanged {
		t.Fatalf("definition drift error=%+v", structured)
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
