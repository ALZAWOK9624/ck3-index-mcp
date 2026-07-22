package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func framedJSON(s string) string {
	return "Content-Length: " + strconv.Itoa(len(s)) + "\r\n\r\n" + s
}

func TestLongLivedMCPReadsNewScanGenerationWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "decisions", "generation_test.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("generation_one = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	reader, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	call := json.RawMessage(`{"name":"ck3_diagnostics","arguments":{"operation":"summary","limit":2}}`)
	firstRaw, err := callMCPTool(context.Background(), reader, cfg, call)
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(map[string]any)["indexState"].(map[string]any)["scan_generation"].(int64)
	if err := os.WriteFile(path, []byte("generation_one = {}\ngeneration_two = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.ScanFiles(context.Background(), cfg, []string{"common/decisions/generation_test.txt"}); err != nil {
		t.Fatal(err)
	}
	secondRaw, err := callMCPTool(context.Background(), reader, cfg, call)
	if err != nil {
		t.Fatal(err)
	}
	second := secondRaw.(map[string]any)["indexState"].(map[string]any)["scan_generation"].(int64)
	if second <= first {
		t.Fatalf("long-lived MCP stayed on stale generation: first=%d second=%d", first, second)
	}
}

func writeMCPMapFixture(t *testing.T, dir string) indexer.Config {
	t.Helper()
	project := filepath.Join(dir, "project")
	for _, rel := range []string{"map_data", "common/landed_titles", "common/province_terrain", "history/provinces", "history/titles", "gui"} {
		if err := os.MkdirAll(filepath.Join(project, filepath.FromSlash(rel)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(project, filepath.FromSlash(rel)), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("map_data/definition.csv", "province;red;green;blue\n1;255;0;0\n2;0;255;0\n")
	write("map_data/default.map", "")
	write("map_data/adjacencies.csv", "From;To;Type;Through;start_x;start_y;stop_x;stop_y;Comment\n1;2;sea;-1;-1;-1;-1;-1;fixture\n-1;-1;;-1;-1;-1;-1;-1;\n")
	write("map_data/geographical_regions.txt", "test_region = { provinces = { 1 } }\n")
	write("common/province_terrain/00_province_terrain.txt", "default_land=plains\n1=hills\n2=forest\n")
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	img.SetRGBA(0, 1, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 1, color.RGBA{G: 255, A: 255})
	f, err := os.Create(filepath.Join(project, "map_data", "provinces.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	heightmap := image.NewGray16(image.Rect(0, 0, 2, 2))
	heightmap.SetGray16(0, 0, color.Gray16{Y: 50000})
	heightmap.SetGray16(0, 1, color.Gray16{Y: 48000})
	heightmap.SetGray16(1, 0, color.Gray16{Y: 12000})
	heightmap.SetGray16(1, 1, color.Gray16{Y: 10000})
	heightFile, err := os.Create(filepath.Join(project, "map_data", "heightmap.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(heightFile, heightmap); err != nil {
		heightFile.Close()
		t.Fatal(err)
	}
	if err := heightFile.Close(); err != nil {
		t.Fatal(err)
	}
	write("common/landed_titles/00_landed_titles.txt", `e_test = { k_k11 = { d_d33 = { c_c114 = { b_b1 = { province = 1 } b_b2 = { province = 2 } } } } }`)
	write("history/provinces/test.txt", `1 = { culture = culture_a religion = faith_a } 2 = { culture = culture_a religion = faith_a }`)
	write("history/titles/test.txt", `k_k11 = { 6248.1.1 = { holder = char72 } }`)
	write("gui/project.gui", `types Demo {
type private_panel = container { button = { visible = [ShowPrivatePanel] down = [PrivateScript.IsShown] raw_text = [PrivatePanelLabel] onclick = [PrivateScript.Execute] } }
type private_grid = dynamicgridbox {
	datamodel = [PrivateRows]
	datamodel_wrap = 2
	addcolumn = 100
	addrow = 30
	item { button = { raw_text = [PrivateRow.GetName] enabled = [PrivateRow.CanSelect] onclick = [PrivateRow.Select] } }
}
}`)
	for _, name := range []string{"base", "target"} {
		destination := filepath.Join(dir, name)
		if err := filepath.WalkDir(project, func(source string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(project, source)
			if err != nil {
				return err
			}
			targetPath := filepath.Join(destination, rel)
			if entry.IsDir() {
				return os.MkdirAll(targetPath, 0o755)
			}
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			return os.WriteFile(targetPath, data, 0o644)
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
[[source]]
name = "base"
path = "base"
rank = 2
[[source]]
name = "target"
path = "target"
rank = 3
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMCPGUIUsesIndexPrivacyBoundary(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	private := callToolForTest(t, db, cfg, "ck3_gui", map[string]any{"operation": "type", "symbol": "private_panel", "visibility": "private"})
	privateContent := private["structuredContent"].(map[string]any)
	if privateContent["found"] != true || int(privateContent["files"].(float64)) != 1 {
		t.Fatalf("private GUI query did not use indexed project file: %+v", privateContent)
	}
	preview := callToolForTest(t, db, cfg, "ck3_gui", map[string]any{"operation": "preview", "symbol": "private_panel", "visibility": "private", "width": 320, "height": 180})
	previewContent := preview["content"].([]map[string]any)
	if len(previewContent) != 2 || previewContent[1]["type"] != "image" || previewContent[1]["mimeType"] != "image/png" {
		t.Fatalf("private GUI preview did not return text plus PNG ImageContent: %+v", previewContent)
	}
	pngBytes, err := base64.StdEncoding.DecodeString(previewContent[1]["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("invalid GUI preview PNG: %v", err)
	}
	htmlPreview := callToolForTest(t, db, cfg, "ck3_gui", map[string]any{
		"operation": "preview", "symbol": "private_panel", "visibility": "private", "width": 320, "height": 180,
		"format": "html", "html_mode": "inspector", "language": "raw",
		"sample_values": []map[string]any{
			{"property": "visible", "expression": "[ShowPrivatePanel]", "value": "true"},
			{"property": "text", "expression": "[PrivatePanelLabel]", "value": "Provided label"},
		},
		"runtime_facts": []map[string]any{
			{"expression": "ShowPrivatePanel", "value": true},
			{"expression": "PrivateScript.IsShown", "value": false},
		},
		"action_effects": []map[string]any{
			{
				"expression": "[PrivateScript.Execute]",
				"updates": []map[string]any{
					{"expression": "PrivateScript.IsShown", "operation": "set", "value": true},
				},
			},
		},
	})
	htmlContent := htmlPreview["content"].([]map[string]any)
	if len(htmlContent) != 1 || htmlContent[0]["type"] != "text" {
		t.Fatalf("HTML-only GUI preview unexpectedly returned binary content: %+v", htmlContent)
	}
	htmlStructured := htmlPreview["structuredContent"].(map[string]any)
	previewValue := htmlStructured["preview"].(map[string]any)
	if previewValue["format"] != "html" {
		t.Fatalf("GUI HTML format missing: %+v", previewValue)
	}
	scenarioValue := previewValue["scenario"].(map[string]any)
	if scenarioValue["source"] != "provided" || int(scenarioValue["applied"].(float64)) != 2 || int(scenarioValue["unused"].(float64)) != 0 {
		t.Fatalf("GUI sample scenario contract invalid: %+v", scenarioValue)
	}
	runtimeValue := previewValue["runtime"].(map[string]any)
	if runtimeValue["source"] != "provided" || len(runtimeValue["plans"].([]any)) == 0 || len(runtimeValue["facts"].([]any)) == 0 {
		t.Fatalf("GUI bounded runtime contract invalid: %+v", runtimeValue)
	}
	actions := runtimeValue["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["operation"] != "provided_effect" || actions[0].(map[string]any)["source"] != "provided" {
		t.Fatalf("GUI provided action effect contract invalid: %+v", actions)
	}
	htmlValue := previewValue["html"].(map[string]any)
	document, _ := htmlValue["document"].(string)
	if htmlValue["mode"] != "inspector" || htmlValue["scripts"] != true || htmlValue["script_policy"] != "fixed-generator-script" || htmlValue["external_requests"] != false || !strings.Contains(document, `id="ck3-gui-inspector"`) || !strings.Contains(document, `data-ck3-scenario-source="provided"`) || !strings.Contains(document, `data-ck3-runtime-operation="provided_effect"`) || !strings.Contains(document, `Provided label`) {
		t.Fatalf("GUI HTML preview contract invalid: %+v", htmlValue)
	}
	gridPreview := callToolForTest(t, db, cfg, "ck3_gui", map[string]any{
		"operation": "preview", "symbol": "private_grid", "visibility": "private",
		"format": "html", "html_mode": "inspector", "width": 320, "height": 180,
		"model_samples": []map[string]any{{
			"target": "private_grid", "datamodel": "[PrivateRows]",
			"rows": []map[string]any{
				{"id": "alpha", "samples": []map[string]any{
					{"property": "text", "expression": "[PrivateRow.GetName]", "value": "Alpha"},
					{"property": "enabled", "expression": "[PrivateRow.CanSelect]", "value": "true"},
				}},
				{"id": "beta", "samples": []map[string]any{
					{"property": "text", "expression": "[PrivateRow.GetName]", "value": "Beta"},
					{"property": "enabled", "expression": "[PrivateRow.CanSelect]", "value": "false"},
				}},
			},
		}},
	})
	gridStructured := gridPreview["structuredContent"].(map[string]any)
	gridValue := gridStructured["preview"].(map[string]any)
	modelSamples := gridValue["model_samples"].(map[string]any)
	if modelSamples["source"] != "provided" || int(modelSamples["applied_rows"].(float64)) != 2 || int(modelSamples["applied_samples"].(float64)) != 4 {
		t.Fatalf("GUI model_samples contract invalid: %+v", modelSamples)
	}
	gridHTML := gridValue["html"].(map[string]any)["document"].(string)
	if !strings.Contains(gridHTML, `data-ck3-model-row-id="alpha"`) ||
		!strings.Contains(gridHTML, `data-ck3-model-row-id="beta"`) ||
		!strings.Contains(gridHTML, `function sameModelRow(left,right)`) {
		t.Fatalf("GUI model row inspector metadata missing")
	}
	public := callToolForTest(t, db, cfg, "ck3_gui", map[string]any{"operation": "type", "symbol": "private_panel", "visibility": "public"})
	publicContent := public["structuredContent"].(map[string]any)
	if publicContent["found"] == true || int(publicContent["files"].(float64)) != 0 {
		t.Fatalf("public GUI query leaked indexed project file: %+v", publicContent)
	}
}

func emptyMCPConfig(dbPath string) indexer.Config {
	return indexer.Config{
		ConfigPath: filepath.Join(filepath.Dir(dbPath), "ck3-index.toml"), Database: filepath.Base(dbPath),
		ArtifactRoot: filepath.Join(filepath.Dir(dbPath), "artifacts"), ArtifactRetentionHours: 168,
	}
}

func TestMCPMapRouteAndHealthRejectIncompleteDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bare.sqlite")
	db, err := indexer.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := emptyMCPConfig(dbPath)

	route := callToolForTest(t, db, cfg, "map_route", map[string]any{"from": "1", "to": "2", "mode": "land"})
	routeJSON, err := json.Marshal(route["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routeJSON), `"status":"blocked"`) || !strings.Contains(string(routeJSON), indexer.MapDatabaseIncompleteCode) || strings.Contains(string(routeJSON), indexer.MapRouteNoPathCode) {
		t.Fatalf("incomplete map route response = %s", routeJSON)
	}

	health := callToolForTest(t, db, cfg, "ck3_health", map[string]any{})
	healthJSON, err := json.Marshal(health["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(healthJSON), indexer.MapDatabaseIncompleteCode) || !strings.Contains(string(healthJSON), `"complete":false`) {
		t.Fatalf("incomplete map health response = %s", healthJSON)
	}
	if !strings.Contains(string(healthJSON), `"gis":`) || !strings.Contains(string(healthJSON), `"available":false`) {
		t.Fatalf("health response omitted the path-redacted GIS sidecar status: %s", healthJSON)
	}
	if strings.Contains(string(healthJSON), filepath.ToSlash(filepath.Dir(dbPath))) || strings.Contains(string(healthJSON), filepath.Dir(dbPath)) {
		t.Fatalf("health response leaked an absolute path: %s", healthJSON)
	}
}

func TestServeMCPIgnoresNotifications(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	msg := framedJSON(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(msg), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification produced a response: %q", out.String())
	}
}

func TestServeMCPInitializePrioritizesSemanticIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	msg := framedJSON(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(msg), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"instructions":"CK3 semantic index`) || !strings.Contains(out.String(), "ck3_refresh status/files") || !strings.Contains(out.String(), "before raw text search") {
		t.Fatalf("initialize did not include semantic-index guidance: %q", out.String())
	}
	if strings.HasPrefix(out.String(), "Content-Length:") || !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("initialize response is not newline-delimited JSON: %q", out.String())
	}
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &response); err != nil {
		t.Fatalf("initialize response is not a JSON line: %v", err)
	}
}

func TestCallMCPToolRejectsBadArguments(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"name":"query_object","arguments":"bad"}`),
		json.RawMessage(`{"name":"query_object","arguments":{}}`),
		json.RawMessage(`{"name":"preflight_patch","arguments":{}}`),
	} {
		result, err := callMCPTool(context.Background(), db, indexer.Config{}, raw)
		if err != nil {
			t.Fatalf("known-tool argument error escaped as protocol error: %v", err)
		}
		if result.(map[string]any)["isError"] != true {
			t.Fatalf("expected isError=true tool result, got %+v", result)
		}
	}
}

func TestCallMCPToolRejectsNullArgumentsWithoutPanicking(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ck3_health", "query_object", "preflight_patch"} {
		raw := json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":null}`, name))
		got, callErr := callMCPTool(context.Background(), db, indexer.Config{}, raw)
		if callErr != nil {
			t.Fatalf("%s null arguments escaped as protocol error: %v", name, callErr)
		}
		result := got.(map[string]any)
		if result["isError"] != true {
			t.Fatalf("%s null arguments result = %+v, want isError=true", name, result)
		}
	}
}

func TestLookupIteratorFallsBackToOfficialExample(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := json.RawMessage(`{"name":"lookup_iterator","arguments":{"id":"any_courtier"}}`)
	got, err := callMCPTool(context.Background(), db, indexer.Config{}, raw)
	if err != nil {
		t.Fatal(err)
	}
	outer := got.(map[string]any)
	content := outer["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, `"found":true`) || !strings.Contains(text, "any_courtier") || !strings.Contains(text, "guidance") {
		t.Fatalf("expected iterator fallback with guidance, got %s", text)
	}
}

func TestMCPMapToolsRegisteredAndCallable(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tools := mcpTools()
	registered := map[string]bool{}
	for _, tool := range tools {
		if name, ok := tool["name"].(string); ok {
			registered[name] = true
		}
	}
	expectedTools := []string{
		"ck3_search", "ck3_inspect", "ck3_review", "ck3_workspace", "ck3_dependencies", "ck3_prepare_edit", "ck3_preflight", "ck3_impact",
		"ck3_diagnostics", "ck3_refresh", "ck3_script_reference", "ck3_health", "ck3_package", "ck3_gui", "map_migration_snapshot", "map_province_migration", "map_asset_audit", "map_province_mapping", "map_province_info", "map_physical_context", "map_neighbors", "map_spatial_relation", "map_strategic_passages",
		"map_title_context", "map_assignment_plan", "map_building_candidates", "map_recipe_catalog", "map_build_metric", "map_route", "map_render",
	}
	if len(tools) != len(expectedTools) {
		t.Fatalf("expected all %d MCP tools, got %d", len(expectedTools), len(tools))
	}
	for _, name := range expectedTools {
		if !registered[name] {
			t.Fatalf("expected MCP tool %s to be registered", name)
		}
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toolJSON), `"maximum":`+strconv.Itoa(indexer.MapRenderMaxWidth)) {
		t.Fatalf("MCP map_render width limit drifted from renderer maximum %d", indexer.MapRenderMaxWidth)
	}
	if !strings.Contains(string(toolJSON), `"maximum":`+strconv.Itoa(indexer.MapRenderMaxHeight)) {
		t.Fatalf("MCP map_render height limit drifted from renderer maximum %d", indexer.MapRenderMaxHeight)
	}

	raw := json.RawMessage(`{"name":"map_title_context","arguments":{"id":"k_k11","year":6253,"limit":4}}`)
	got, err := callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer := got.(map[string]any)
	content := outer["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"map_title_context"`) || !strings.Contains(text, `"holder":"char72"`) {
		t.Fatalf("expected MCP map title context with char72 holder, got %s", text)
	}

	raw = json.RawMessage(`{"name":"map_spatial_relation","arguments":{"from":"1","to":"2","year":6253}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"map_spatial_relation"`) || !strings.Contains(text, `"adjacency_kind":"land_border"`) || !strings.Contains(text, `"distance_pixels":`) {
		t.Fatalf("expected MCP precision spatial relation, got %s", text)
	}

	raw = json.RawMessage(`{"name":"map_recipe_catalog","arguments":{}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"development_network"`) || !strings.Contains(text, `"map_recipe_catalog"`) || !strings.Contains(text, `"marker_sources":["capitals","holy_sites","special_buildings","vegetation","holdings","lakes","strategic_portals"]`) {
		t.Fatalf("expected map recipe catalog, got %s", text)
	}

	raw = json.RawMessage(`{"name":"map_strategic_passages","arguments":{"target":"e_test","limit":4}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"map_strategic_passages"`) || !strings.Contains(text, `"passage_kind":"strait"`) {
		t.Fatalf("expected indexed strategic passage result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"map_build_metric","arguments":{"recipe":"development_network","target":"k_k11","year":6253}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"map_build_metric"`) || !strings.Contains(text, `"provenance":"derived"`) {
		t.Fatalf("expected map metric result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"map_render","arguments":{"target":"k_k11","year":6253,"width":400,"layers":[{"type":"fill","metric":{"recipe":"development_network"}},{"type":"borders","level":"county"}]}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	if len(content) != 2 || content[1]["type"] != "image" || content[1]["mimeType"] != "image/png" {
		t.Fatalf("expected text plus PNG ImageContent, got %+v", content)
	}
	pngBytes, err := base64.StdEncoding.DecodeString(content[1]["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("invalid MCP PNG: %v", err)
	}
	raw = json.RawMessage(`{"name":"map_render","arguments":{"recipe":"duchy_political_atlas","target":"e_test","width":400}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	if len(content) != 2 || content[1]["type"] != "image" || !strings.Contains(content[0]["text"].(string), `"year":6254`) {
		t.Fatalf("expected atlas recipe metadata plus PNG ImageContent, got %+v", content)
	}
	raw = json.RawMessage(`{"name":"map_render","arguments":{"recipe":"thematic_atlas","theme":"culture","level":"barony","target":"e_test","width":400}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if len(content) != 2 || content[1]["mimeType"] != "image/png" || !strings.Contains(text, `"history_year":6254`) || !strings.Contains(text, `"level":"barony"`) {
		t.Fatalf("expected adaptive barony thematic atlas plus PNG, got %+v", content)
	}
	raw = json.RawMessage(`{"name":"map_render","arguments":{"font_path":"C:/private/font.ttf","layers":[{"type":"borders","level":"county"}]}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	if outer["isError"] != true || !strings.Contains(outer["content"].([]map[string]any)[0]["text"].(string), "does not accept argument field") {
		t.Fatalf("expected MCP map_render to reject request-supplied font paths, got %+v", got)
	}

	raw = json.RawMessage(`{"name":"ck3_search","arguments":{"query":"c_c114","limit":4}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"ck3_search"`) || !strings.Contains(text, `"name":"c_c114"`) {
		t.Fatalf("expected high-level semantic search result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"ck3_inspect","arguments":{"id":"c_c114","limit":4}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"ck3_inspect"`) || !strings.Contains(text, `"definitions":1`) {
		t.Fatalf("expected high-level inspect result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"ck3_review","arguments":{"files":[{"path":"common/decisions/review.txt","content":"review_decision = { is_shown = { has_cultural_parameter = unlock_bad } }"}],"limit":6}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"ck3_review"`) || !strings.Contains(text, "scope_mismatch") {
		t.Fatalf("expected high-level review scope result, got %s", text)
	}
}

func TestMCPQueryObjectTypesReturnsLLMResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "project", "common", "traits"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "common", "traits", "test_traits.txt"), []byte(`test_trait = { desc = test_trait_desc }`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "project", "common", "culture", "traditions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "common", "culture", "traditions", "test_traditions.txt"), []byte(`tradition_test_unlock = {
	parameters = {
		unlock_test_maa = yes
	}
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "project", "common", "men_at_arms_types"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "common", "men_at_arms_types", "test_maa.txt"), []byte(`test_maa = {
	can_recruit = {
		valid_for_maa_trigger = { PARAMETER = unlock_test_maa }
	}
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "project", "events"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "events", "chain.txt"), []byte(`mcp.chain.a = { type = character_event immediate = { trigger_event = mcp.chain.b } }
mcp.chain.b = { type = character_event immediate = { trigger_event = mcp.chain.a } }
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := json.RawMessage(`{"name":"query_object_types","arguments":{"limit":3}}`)
	got, err := callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer := got.(map[string]any)
	content := outer["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"query_object_types"`) || !strings.Contains(text, `"guidance"`) {
		t.Fatalf("expected LLM object type result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"query_patterns","arguments":{"id":"trait","limit":3}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"query_patterns"`) || !strings.Contains(text, `"field_pattern"`) {
		t.Fatalf("expected LLM pattern result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"preflight_patch","arguments":{"limit":5,"files":[{"path":"common/traits/patch_traits.txt","content":"patch_trait = { desc = test_trait_desc }"}]}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"preflight_patch"`) || !strings.Contains(text, `"needs_scan":true`) {
		t.Fatalf("expected LLM preflight patch result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"health_check","arguments":{"limit":3}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"status"`) || !strings.Contains(text, `"tables"`) {
		t.Fatalf("expected health result, got %s", text)
	}
	if strings.Contains(text, cfgPath) || strings.Contains(text, filepath.Join(dir, "cache", "test.sqlite")) {
		t.Fatalf("health_check should not expose local absolute paths, got %s", text)
	}

	raw = json.RawMessage(`{"name":"architecture_overview","arguments":{"limit":3}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"architecture_overview"`) || !strings.Contains(text, `"hotspots"`) || !strings.Contains(text, `"object_type"`) {
		t.Fatalf("expected architecture overview result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"dependency_graph","arguments":{"id":"test_trait","limit":3,"depth":2}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"dependency_graph"`) || !strings.Contains(text, `"center_definition"`) {
		t.Fatalf("expected dependency graph result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"dependency_graph","arguments":{"id":"test_maa","limit":8,"depth":2}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"edge_type":"unlocks_men_at_arms"`) || !strings.Contains(text, `"edge_type":"defines_parameter"`) {
		t.Fatalf("expected semantic MAA unlock graph result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"ck3_dependencies","arguments":{"id":"event:mcp.chain.a","operation":"event_chain","direction":"callees","depth":2,"include_on_actions":true,"limit":4}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"event_chain"`) || !strings.Contains(text, `"cycles":[["event:mcp.chain.a","event:mcp.chain.b"]]`) || !strings.Contains(text, `"relation":"trigger_event"`) {
		t.Fatalf("expected event-chain topology result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"dependency_graph","arguments":{"id":"test_maa","limit":8,"mode":"public","allow_project":false}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if strings.Contains(text, `"counts"`) || strings.Contains(text, `"redacted"`) || !strings.Contains(text, `"summary":"Public visibility returns only evidence with a configured non-private source."`) {
		t.Fatalf("public graph retained aggregate visibility side channels: %s", text)
	}
}
