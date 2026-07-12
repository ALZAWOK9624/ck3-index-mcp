package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func writeMCPMapFixture(t *testing.T, dir string) indexer.Config {
	t.Helper()
	project := filepath.Join(dir, "project")
	for _, rel := range []string{"map_data", "common/landed_titles", "common/province_terrain", "history/provinces", "history/titles"} {
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
	write("common/province_terrain/00_province_terrain.txt", "default_land=plains\n1=hills\n2=forest\n")
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
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
	write("common/landed_titles/00_landed_titles.txt", `e_test = { k_k11 = { d_d33 = { c_c114 = { b_b1 = { province = 1 } b_b2 = { province = 2 } } } } }`)
	write("history/provinces/test.txt", `1 = { culture = culture_a religion = faith_a } 2 = { culture = culture_a religion = faith_a }`)
	write("history/titles/test.txt", `k_k11 = { 6248.1.1 = { holder = char72 } }`)
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
	return cfg
}

func emptyMCPConfig(dbPath string) indexer.Config {
	return indexer.Config{ConfigPath: filepath.Join(filepath.Dir(dbPath), "ck3-index.toml"), Database: filepath.Base(dbPath)}
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
	if err := serveMCP(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(msg), &out); err != nil {
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
	msg := framedJSON(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if err := serveMCP(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(msg), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"instructions":"Primary CK3/Godherja semantic index`) || !strings.Contains(out.String(), "before raw text search") {
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

	raw := json.RawMessage(`{"name":"query_object","arguments":"bad"}`)
	if _, err := callMCPTool(context.Background(), db, indexer.Config{}, raw); err == nil {
		t.Fatal("expected invalid arguments error")
	}
	raw = json.RawMessage(`{"name":"query_object","arguments":{}}`)
	if _, err := callMCPTool(context.Background(), db, indexer.Config{}, raw); err == nil {
		t.Fatal("expected missing id error")
	}
	raw = json.RawMessage(`{"name":"preflight_patch","arguments":{}}`)
	if _, err := callMCPTool(context.Background(), db, indexer.Config{}, raw); err == nil {
		t.Fatal("expected missing files error")
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
		"ck3_search", "ck3_inspect", "ck3_review",
		"query_object", "query_object_types", "find_refs", "query_loc", "query_resource", "query_examples", "query_rules", "query_patterns",
		"architecture_overview", "dependency_graph", "validate_project", "health_check", "explain_diagnostic", "inspect_object", "prepare_edit",
		"preflight_code", "preflight_patch", "impact_patch", "preflight_dirty", "diagnose_key", "lookup_scope", "lookup_datatype", "lookup_shape", "lookup_define",
		"lookup_on_action", "lookup_iterator", "lookup_example", "lookup_modifier", "map_province_info", "map_neighbors", "map_title_context",
		"map_assignment_plan", "map_building_candidates",
	}
	if len(tools) != len(expectedTools) {
		t.Fatalf("expected all %d MCP tools, got %d", len(expectedTools), len(tools))
	}
	for _, name := range expectedTools {
		if !registered[name] {
			t.Fatalf("expected MCP tool %s to be registered", name)
		}
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

	raw = json.RawMessage(`{"name":"dependency_graph","arguments":{"id":"test_maa","limit":8,"mode":"public","allow_project":false}}`)
	got, err = callMCPTool(context.Background(), db, emptyMCPConfig(filepath.Join(dir, "cache", "test.sqlite")), raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"redacted"`) || !strings.Contains(text, `"counts"`) {
		t.Fatalf("expected public graph redaction with counts, got %s", text)
	}
}
