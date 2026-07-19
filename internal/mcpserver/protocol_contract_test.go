package mcpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
	"ck3-index/internal/packager"
)

func TestServeMCPProtocolContract(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ck3_inspect","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"not_a_tool","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(requests), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 5 {
		t.Fatalf("response count = %d, want 5: %s", len(responses), out.String())
	}

	initialize := responses[0]["result"].(map[string]any)
	if initialize["protocolVersion"] != latestMCPProtocolVersion {
		t.Fatalf("negotiated protocol version = %v, want %s", initialize["protocolVersion"], latestMCPProtocolVersion)
	}
	serverInfo := initialize["serverInfo"].(map[string]any)
	if serverInfo["version"] != buildinfo.Version {
		t.Fatalf("server version = %v, want buildinfo.Version %q", serverInfo["version"], buildinfo.Version)
	}
	capabilities := initialize["capabilities"].(map[string]any)
	toolCapabilities := capabilities["tools"].(map[string]any)
	if toolCapabilities["listChanged"] != false {
		t.Fatalf("static registry must advertise listChanged=false: %+v", toolCapabilities)
	}
	if _, ok := responses[1]["result"].(map[string]any); !ok {
		t.Fatalf("ping did not return an empty object: %+v", responses[1])
	}
	listed := responses[2]["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 29 {
		t.Fatalf("standard tools/list count = %d, want 29", len(listed))
	}
	first := listed[0].(map[string]any)
	for _, field := range []string{"title", "description", "inputSchema", "outputSchema", "annotations"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("advertised tool lacks %s: %+v", field, first)
		}
	}

	argumentError := responses[3]["result"].(map[string]any)
	if argumentError["isError"] != true || responses[3]["error"] != nil {
		t.Fatalf("known-tool argument failure must be CallToolResult isError=true: %+v", responses[3])
	}
	if _, ok := argumentError["structuredContent"].(map[string]any); !ok {
		t.Fatalf("tool error lacks structuredContent: %+v", argumentError)
	}
	unknownError := responses[4]["error"].(map[string]any)
	if int(unknownError["code"].(float64)) != rpcInvalidParams {
		t.Fatalf("unknown tool error code = %v, want %d", unknownError["code"], rpcInvalidParams)
	}
}

func TestHealthReportIncludesBinaryVersion(t *testing.T) {
	report := mcpHealthReport(indexer.HealthReport{})
	if report["binary_version"] != buildinfo.Version {
		t.Fatalf("health binary_version = %v, want %q", report["binary_version"], buildinfo.Version)
	}
}

func TestServeMCPNegotiatesSupportedAndUnknownProtocolVersions(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(requests), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if got := responses[0]["result"].(map[string]any)["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("supported client version negotiation = %v", got)
	}
	if got := responses[1]["result"].(map[string]any)["protocolVersion"]; got != latestMCPProtocolVersion {
		t.Fatalf("unknown client version negotiation = %v", got)
	}
	if code := int(responses[2]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidParams {
		t.Fatalf("missing initialize protocolVersion error = %d, want %d", code, rpcInvalidParams)
	}
}

func TestServeMCPValidJSONWrongShapeIsInvalidRequest(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := "[]\n" + `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want 2", len(responses))
	}
	if code := int(responses[0]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
		t.Fatalf("valid JSON wrong-shape error = %d, want %d", code, rpcInvalidRequest)
	}
}

func TestServeMCPRejectsInvalidRequestIDTypes(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":{"bad":1},"method":"ping"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want 2", len(responses))
	}
	for _, response := range responses {
		if response["id"] != nil || int(response["error"].(map[string]any)["code"].(float64)) != rpcInvalidRequest {
			t.Fatalf("invalid id response = %+v", response)
		}
	}
}

func TestServeMCPParseErrorThenContinues(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := "{broken json\n" + `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want parse error plus ping", len(responses))
	}
	parseError := responses[0]["error"].(map[string]any)
	if int(parseError["code"].(float64)) != rpcParseError {
		t.Fatalf("parse error code = %v, want %d", parseError["code"], rpcParseError)
	}
	if _, ok := responses[1]["result"].(map[string]any); !ok {
		t.Fatalf("server did not continue after complete malformed line: %+v", responses[1])
	}
}

func TestServeMCPRejectsOversizedFramedRequest(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMCPMessageBytes+1)
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 1 {
		t.Fatalf("response count = %d, want 1", len(responses))
	}
	rpcErr := responses[0]["error"].(map[string]any)
	if int(rpcErr["code"].(float64)) != rpcMessageTooLarge {
		t.Fatalf("oversized request error code = %v, want %d", rpcErr["code"], rpcMessageTooLarge)
	}
}

func TestMCPEnvelopeCanCarryMaximumDecodedPackage(t *testing.T) {
	base64Bytes := ((packager.MCPLimits.MaxTotalBytes + 2) / 3) * 4
	if int64(maxMCPMessageBytes) <= base64Bytes {
		t.Fatalf("MCP envelope limit %d cannot carry %d bytes of Base64 package data", maxMCPMessageBytes, base64Bytes)
	}
}

func TestCanonicalSchemaFixesAndPublicMapRedaction(t *testing.T) {
	for _, name := range []string{"map_build_metric", "map_render"} {
		definition, ok := findCanonicalTool(name)
		if !ok {
			t.Fatalf("missing canonical tool %s", name)
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		levels := schemaStrings(properties["level"].(map[string]any)["enum"])
		if !containsString(levels, "region") {
			t.Fatalf("%s level schema omits region: %v", name, levels)
		}
	}
	assignment, _ := findCanonicalTool("map_assignment_plan")
	properties := assignment.InputSchema["properties"].(map[string]any)
	if _, exists := properties["mode"]; exists {
		t.Fatal("canonical map_assignment_plan still exposes ambiguous mode")
	}
	if required := schemaStrings(assignment.InputSchema["required"]); len(required) != 1 || required[0] != "target" {
		t.Fatalf("map_assignment_plan required fields = %v, want [target]", required)
	}

	private := indexer.MapAssignmentPlanResult{PatchFiles: []indexer.PatchFileInput{{Path: "history/provinces/private.txt", Content: "secret"}}}
	redacted := redactToolValue(private, "public").(indexer.MapAssignmentPlanResult)
	if len(redacted.PatchFiles) != 0 {
		t.Fatalf("public map assignment retained patch files: %+v", redacted.PatchFiles)
	}
}

func TestToolErrorsDoNotEchoPatchContent(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := "DO_NOT_ECHO_THIS_PATCH_BODY"
	result := callToolForTest(t, db, indexer.Config{}, "ck3_review", map[string]any{
		"files":   []map[string]any{{"path": "common/traits/test.txt", "content": secret}},
		"unknown": secret,
	})
	if result["isError"] != true {
		t.Fatalf("unknown field did not return tool error: %+v", result)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), secret) {
		t.Fatalf("tool error echoed patch content: %s", data)
	}
}

func TestMapRenderAnyOfRejectsNullAlternatives(t *testing.T) {
	definition, _ := findCanonicalTool("map_render")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"recipe":null}`),
		json.RawMessage(`{"layers":null}`),
	} {
		if err := validateArguments(raw, definition.InputSchema, definition.CompatibilityProperties); err == nil {
			t.Fatalf("map_render accepted missing/null recipe and layers: %s", raw)
		}
	}
}

func TestCanonicalSchemasRejectExplicitNullForTypedFields(t *testing.T) {
	definition, _ := findCanonicalTool("ck3_search")
	if err := validateArguments(json.RawMessage(`{"query":"trait","limit":null}`), definition.InputSchema, definition.CompatibilityProperties); err == nil {
		t.Fatal("typed optional field accepted explicit null")
	}
}

func TestNestedMapSchemaBoundsAreEnforced(t *testing.T) {
	metric, _ := findCanonicalTool("map_build_metric")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"transform":{"rounds":17}}`),
		json.RawMessage(`{"components":[{"field":"terrain","unknown":1}]}`),
	} {
		if err := validateArguments(raw, metric.InputSchema, metric.CompatibilityProperties); err == nil {
			t.Fatalf("map_build_metric accepted unsafe nested input: %s", raw)
		}
	}
	render, _ := findCanonicalTool("map_render")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"layers":[{"type":"borders","line_width":65}]}`),
		json.RawMessage(`{"layers":[{"type":"fill","metric":{"limit":20}}]}`),
	} {
		if err := validateArguments(raw, render.InputSchema, render.CompatibilityProperties); err == nil {
			t.Fatalf("map_render accepted unsafe nested input: %s", raw)
		}
	}
}

func TestToolErrorsRedactConfiguredPathsCaseInsensitively(t *testing.T) {
	t.Setenv("CK3_INDEX_MAP_FONT", `C:\Private\Fonts\Map.ttf`)
	runtime := &Runtime{
		DBPath: `C:\Private\Cache\Index.sqlite`,
		Config: indexer.Config{
			ConfigPath: `C:\Private\Config\ck3-index.toml`,
			Sources:    []indexer.Source{{Path: `C:\Private\Project`}},
		},
	}
	message := sanitizeToolError(errors.New(`failed at c:/private/cache/index.sqlite, C:\PRIVATE\PROJECT and c:\private\fonts\map.ttf`), runtime)
	if strings.Contains(strings.ToLower(message), "private") || strings.Count(message, "<redacted-path>") != 3 {
		t.Fatalf("configured paths were not redacted: %s", message)
	}
}

func TestNextQueriesUseCanonicalToolsAndExplicitArguments(t *testing.T) {
	result, err := encodeToolResult(indexer.LLMResult{
		Intent:  "fixture",
		Summary: "fixture",
		NextQueries: []indexer.LLMNextQuery{
			{Tool: "query_rules", ID: "trait", Reason: "fixture"},
			{Tool: "explain_diagnostic", ID: "scope_mismatch", Reason: "fixture"},
			{Tool: "map_building_candidates", ID: "k_example", Reason: "fixture"},
		},
	}, "private")
	if err != nil {
		t.Fatal(err)
	}
	structured := result["structuredContent"].(map[string]any)
	queries := structured["next_queries"].([]any)
	first := queries[0].(map[string]any)
	if first["tool"] != "ck3_prepare_edit" {
		t.Fatalf("legacy next query was not canonicalized: %+v", first)
	}
	firstArgs := first["arguments"].(map[string]any)
	if firstArgs["operation"] != "rules" || firstArgs["id"] != "trait" {
		t.Fatalf("canonical prepare arguments are incomplete: %+v", firstArgs)
	}
	if _, legacyID := first["id"]; legacyID {
		t.Fatalf("canonical next query retained legacy top-level id: %+v", first)
	}
	second := queries[1].(map[string]any)
	secondArgs := second["arguments"].(map[string]any)
	if second["tool"] != "ck3_diagnostics" || secondArgs["operation"] != "explain" || secondArgs["code"] != "scope_mismatch" {
		t.Fatalf("canonical diagnostic arguments are incomplete: %+v", second)
	}
	third := queries[2].(map[string]any)
	thirdArgs := third["arguments"].(map[string]any)
	if third["tool"] != "map_building_candidates" || thirdArgs["target"] != "k_example" {
		t.Fatalf("canonical target arguments are incomplete: %+v", third)
	}
	if _, legacyID := third["id"]; legacyID {
		t.Fatalf("canonical target query retained legacy top-level id: %+v", third)
	}
}

func TestNextQueriesRewriteDeprecatedHistoryYear(t *testing.T) {
	result, err := encodeToolResult(map[string]any{
		"next_queries": []map[string]any{{
			"tool":      "map_render",
			"arguments": map[string]any{"history_year": 6254, "recipe": "political_atlas"},
		}},
	}, "private")
	if err != nil {
		t.Fatal(err)
	}
	query := result["structuredContent"].(map[string]any)["next_queries"].([]any)[0].(map[string]any)
	arguments := query["arguments"].(map[string]any)
	if arguments["year"] != float64(6254) {
		t.Fatalf("next query year = %v, want 6254", arguments["year"])
	}
	if _, exists := arguments["history_year"]; exists {
		t.Fatalf("next query retained deprecated history_year: %+v", arguments)
	}
}

func TestToolResultEncodingRejectsNonFiniteJSON(t *testing.T) {
	if _, err := encodeToolResult(map[string]any{"value": math.NaN()}, "private"); err == nil {
		t.Fatal("non-finite tool result was silently encoded as a successful response")
	}
}

func TestCallToolReportsUnavailableIndexState(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := callToolForTest(t, db, indexer.Config{}, "ck3_script_reference", map[string]any{"kind": "shape", "id": "has_trait"})
	if result["isError"] == true {
		t.Fatalf("static script reference should remain usable without index state: %+v", result)
	}
	state := result["indexState"].(map[string]any)
	if state["status"] != "unavailable" || state["error_code"] != "INDEX_STATE_UNAVAILABLE" {
		t.Fatalf("unavailable index state was not propagated: %+v", state)
	}
}

func TestScriptReferencePrefersPublishedLiveOnActionEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	game := t.TempDir()
	onActionDir := filepath.Join(game, "common", "on_action")
	if err := os.MkdirAll(onActionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onActionDir, "fixture.txt"), []byte(`# Root = character
# scope:reason = flag:MCP_DOC_SENTINEL
live_on_action_fixture = { }
`), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('live_on_action_fixture','on_action','none','','fixture','C:\\private\\logs\\on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col)
		VALUES('on_action','live_on_action_fixture','',0,0,'vanilla',3,'common/on_action/fixture.txt',3,1)`); err != nil {
		t.Fatal(err)
	}
	cfg := indexer.Config{Sources: []indexer.Source{{Name: "vanilla", Path: game, Rank: 3}}}
	result := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "live_on_action_fixture"})
	if result["isError"] == true {
		t.Fatalf("live on_action reference failed: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["found"] != true || structured["rule_source"] != "engine_logs" || structured["confidence"] != "high" {
		t.Fatalf("live on_action did not take precedence: %+v", structured)
	}
	rules := structured["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["input_scopes"].([]any)[0] != "none" || rules[0].(map[string]any)["rule_source"] != "engine_logs/on_actions.log" {
		t.Fatalf("none scope was not preserved in MCP response: %+v", structured)
	}
	documentation := structured["documentation_contract"].(map[string]any)
	if documentation["status"] != "documented" || documentation["evidence_kind"] != "vanilla_adjacent_top_level_comments" || documentation["review_only"] != true || documentation["engine_evidence_available"] != true {
		t.Fatalf("documentation contract did not remain separate from engine evidence: %+v", documentation)
	}
	candidates := documentation["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("unexpected documentation candidates: %+v", documentation)
	}
	candidate := candidates[0].(map[string]any)
	root := candidate["root"].(map[string]any)
	if root["status"] != "explicit" || root["documented_type"] != "character" || candidate["review_status"] != "engine_none_with_documented_root" || candidate["confidence"] != "medium" {
		t.Fatalf("comment root was merged or inferred incorrectly: %+v", candidate)
	}
	bindings := candidate["bindings"].([]any)
	if len(bindings) != 1 || bindings[0].(map[string]any)["name"] != "reason" || bindings[0].(map[string]any)["value_kind"] != "flag" {
		t.Fatalf("documentation binding projection was not conservative: %+v", candidate)
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "MCP_DOC_SENTINEL") || strings.Contains(string(encoded), filepath.Base(game)) || strings.Contains(string(encoded), `"source":"vanilla"`) {
		t.Fatalf("MCP documentation projection leaked prose, physical root, or source identity: %s", encoded)
	}
	public := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "live_on_action_fixture", "visibility": "public"})
	if public["isError"] == true || public["structuredContent"].(map[string]any)["documentation_contract"].(map[string]any)["status"] != "documented" {
		t.Fatalf("public on_action reference lost safe vanilla documentation evidence: %+v", public)
	}
	publicEncoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicEncoded), "MCP_DOC_SENTINEL") || strings.Contains(string(publicEncoded), filepath.Base(game)) || strings.Contains(string(publicEncoded), `"source":"vanilla"`) {
		t.Fatalf("public on_action response leaked documentation internals: %s", publicEncoded)
	}
}

func TestOnActionReferenceKeepsTigerStaticContractDuringFinalizing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	game := t.TempDir()
	onActionDir := filepath.Join(game, "common", "on_action")
	if err := os.MkdirAll(onActionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onActionDir, "fixture.txt"), []byte(`# Root = character
on_army_monthly = { }
`), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','finalizing'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('on_army_monthly','on_action','none','','fixture','C:\\private\\logs\\on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	cfg := indexer.Config{Sources: []indexer.Source{{Name: "vanilla", Path: game, Rank: 3}}}
	result := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "on_army_monthly"})
	if result["isError"] == true {
		t.Fatalf("static on_action reference was blocked while finalizing: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["found"] != true || structured["rule_source"] != "tiger_fallback" || structured["confidence"] != "medium" {
		t.Fatalf("finalizing lookup did not use Tiger fallback: %+v", structured)
	}
	if _, leaked := structured["rules"]; leaked {
		t.Fatalf("unpublished engine rule leaked into static fallback: %+v", structured)
	}
	tiger := structured["tiger_contract"].(map[string]any)
	if tiger["definition"] != "on_army_monthly" || tiger["rule_source"] != "tiger_static" || tiger["diagnostic_effect"] != "none" || tiger["root"].(map[string]any)["static_type"] != "character" {
		t.Fatalf("Tiger static evidence was not kept separate: %+v", tiger)
	}
	documentation := structured["documentation_contract"].(map[string]any)
	if documentation["engine_evidence_available"] != false || documentation["status"] != "documented" {
		t.Fatalf("documentation layer read unpublished engine state: %+v", documentation)
	}
	candidate := documentation["candidates"].([]any)[0].(map[string]any)
	if candidate["engine_rule_found"] != false || candidate["review_status"] != "engine_evidence_unavailable" {
		t.Fatalf("finalizing documentation candidate mixed engine evidence: %+v", candidate)
	}
}

func TestCallToolBlocksFinalizingIndexButKeepsStaticReference(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','finalizing')`); err != nil {
		t.Fatal(err)
	}
	blocked := callToolForTest(t, db, indexer.Config{}, "ck3_search", map[string]any{"query": "fixture"})
	if blocked["isError"] != true || blocked["structuredContent"].(map[string]any)["code"] != "INDEX_REFRESH_IN_PROGRESS" {
		t.Fatalf("finalizing cache was not blocked: %+v", blocked)
	}
	static := callToolForTest(t, db, indexer.Config{}, "ck3_script_reference", map[string]any{"kind": "shape", "id": "has_trait"})
	if static["isError"] == true {
		t.Fatalf("static reference should remain usable while cache finalizes: %+v", static)
	}
}

func TestCallToolRejectsSecondGenerationChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO meta(key,value) VALUES('scan_generation','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatal(err)
	}

	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_generation_change", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: readOnlyAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			_, err := writer.ExecContext(ctx, `UPDATE meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='scan_generation'`)
			return toolOutput{Value: map[string]any{"ok": true}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_generation_change", map[string]any{})
	if result["isError"] != true {
		t.Fatalf("second generation change returned success: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != "INDEX_CHANGED_DURING_QUERY" {
		t.Fatalf("second generation change code = %v", structured["code"])
	}
}

func TestArtifactToolIsNotRetriedAfterGenerationChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO meta(key,value) VALUES('scan_generation','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatal(err)
	}

	calls := 0
	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_artifact_generation_change", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: artifactAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			calls++
			_, err := writer.ExecContext(ctx, `UPDATE meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='scan_generation'`)
			return toolOutput{Value: map[string]any{"artifact_id": "test"}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_artifact_generation_change", map[string]any{})
	if result["isError"] != true {
		t.Fatalf("unstable artifact tool returned success: %+v", result)
	}
	if calls != 1 {
		t.Fatalf("non-idempotent artifact handler ran %d times, want 1", calls)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != "INDEX_CHANGED_DURING_QUERY" {
		t.Fatalf("artifact generation-change code = %v", structured["code"])
	}
}

func TestCallToolReportsIndexStateFailureAfterHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_index_state_failure", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: readOnlyAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			_, err := writer.ExecContext(ctx, `DROP TABLE meta`)
			return toolOutput{Value: map[string]any{"ok": true}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_index_state_failure", map[string]any{})
	if result["isError"] == true {
		t.Fatalf("successful handler should preserve its result with an explicit unverified state: %+v", result)
	}
	state := result["indexState"].(map[string]any)
	if state["status"] != "unavailable" || state["error_code"] != "INDEX_STATE_UNAVAILABLE" {
		t.Fatalf("post-handler index-state failure was not propagated: %+v", state)
	}
}

func TestRetainedTargetAliasConflictsAreRejected(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"map_assignment_plan", "map_building_candidates"} {
		result := callToolForTest(t, db, indexer.Config{}, name, map[string]any{"target": "c_one", "id": "c_two"})
		if result["isError"] != true {
			t.Fatalf("%s accepted conflicting target/id aliases: %+v", name, result)
		}
		if !strings.Contains(result["content"].([]map[string]any)[0]["text"].(string), "conflict") {
			t.Fatalf("%s conflict error is not actionable: %+v", name, result)
		}
	}
}

func TestLegacyVisibilityTyposAreRejected(t *testing.T) {
	for _, args := range []visibilityArgs{
		{Mode: "publci"},
		{PrivacyMode: "publci"},
	} {
		if _, _, err := args.options(0); err == nil {
			t.Fatalf("invalid legacy visibility silently fell back to private: %+v", args)
		}
	}
	opts, visibility, err := (visibilityArgs{Mode: "religion"}).optionsWithDomainMode(0, true)
	if err != nil || visibility != "private" || !opts.AllowProject {
		t.Fatalf("assignment domain mode no longer preserves private compatibility: opts=%+v visibility=%q err=%v", opts, visibility, err)
	}
}

func TestCompactMapRenderResultOmitsBulkMetricTables(t *testing.T) {
	source := indexer.MapRenderResult{Metrics: []indexer.MapMetricResult{{
		Values:     []indexer.MapMetricValue{{ID: "c_one", Value: 1}},
		Categories: []indexer.MapCount{{ID: "one", Count: 1}},
		Outliers:   []indexer.MapMetricValue{{ID: "c_one", Value: 1}},
		RecipeSpec: &indexer.MapMetricSpec{Values: []indexer.MapMetricValue{{ID: "c_one", Value: 1}}},
	}}}
	compact := compactMapRenderResult(source)
	metric := compact.Metrics[0]
	if len(metric.Values) != 0 || len(metric.Categories) != 0 || len(metric.Outliers) != 0 || len(metric.RecipeSpec.Values) != 0 {
		t.Fatalf("compact map render retained bulk metric data: %+v", metric)
	}
	if len(source.Metrics[0].Values) != 1 {
		t.Fatal("map render compaction mutated the source value")
	}
}

func TestLegacyAliasMatchesCanonicalBusinessResult(t *testing.T) {
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
	canonical := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{"id": "c_c114", "operation": "definition", "limit": 3})
	legacy := callToolForTest(t, db, cfg, "query_object", map[string]any{"id": "c_c114", "limit": 3})
	canonicalJSON, _ := json.Marshal(canonical["structuredContent"])
	legacyJSON, _ := json.Marshal(legacy["structuredContent"])
	if !bytes.Equal(canonicalJSON, legacyJSON) {
		t.Fatalf("legacy/canonical business results differ:\ncanonical=%s\nlegacy=%s", canonicalJSON, legacyJSON)
	}
	meta := legacy["_meta"].(map[string]any)
	if meta["replacement"] != "ck3_inspect" {
		t.Fatalf("legacy replacement metadata = %+v", meta)
	}
}

func TestRetainedMapNamesAcceptLegacyAliases(t *testing.T) {
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
	assignment := callToolForTest(t, db, cfg, "map_assignment_plan", map[string]any{
		"id": "k_k11", "mode": "religion", "privacy_mode": "public", "limit": 2,
	})
	if assignment["isError"] == true {
		t.Fatalf("legacy map_assignment_plan aliases failed: %+v", assignment)
	}
	structured := assignment["structuredContent"].(map[string]any)
	if _, exposed := structured["patch_files"]; exposed {
		t.Fatalf("public legacy assignment exposed patch files: %+v", structured["patch_files"])
	}
	building := callToolForTest(t, db, cfg, "map_building_candidates", map[string]any{"id": "k_k11", "year": 6253, "limit": 2})
	if building["isError"] == true {
		t.Fatalf("legacy map_building_candidates id alias failed: %+v", building)
	}
}

func createProtocolTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func decodeResponseLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid JSON response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
