package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
	"ck3-index/internal/migrator"
)

func TestToolRegistryContract(t *testing.T) {
	definitions := registry()
	if len(definitions) != 30 {
		t.Fatalf("standard registry count = %d, want 30", len(definitions))
	}
	if got := len(mcpTools()); got != 30 {
		t.Fatalf("tools/list count = %d, want 30", got)
	}

	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Title) == "" {
			t.Fatal("registry contains a tool without a name or title")
		}
		if _, exists := seen[definition.Name]; exists {
			t.Fatalf("registry contains duplicate tool %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		if strings.TrimSpace(definition.Description) == "" || definition.Handler == nil {
			t.Fatalf("tool %q has no description or handler", definition.Name)
		}
		if got := definition.InputSchema["type"]; got != "object" {
			t.Fatalf("tool %q input schema type = %v, want object", definition.Name, got)
		}
		if additional, ok := definition.InputSchema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("tool %q must reject undocumented top-level properties", definition.Name)
		}
		if got := definition.OutputSchema["type"]; got != "object" {
			t.Fatalf("tool %q output schema type = %v, want object", definition.Name, got)
		}
		if definition.Name == "ck3_refresh" || definition.Name == "ck3_package" || definition.Name == "map_migration_snapshot" || definition.Name == "map_province_migration" {
			if definition.Annotations.ReadOnlyHint || definition.Annotations.DestructiveHint || definition.Annotations.OpenWorldHint {
				t.Fatalf("tool %q annotations must be non-read-only, non-destructive, and closed-world", definition.Name)
			}
		} else if !definition.Annotations.ReadOnlyHint || definition.Annotations.DestructiveHint || definition.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations are not read-only/closed-world", definition.Name)
		}
	}
}

func TestPriorityToolsPublishPreciseOutputSchemas(t *testing.T) {
	for _, name := range []string{"ck3_search", "ck3_workspace", "ck3_preflight", "ck3_refresh"} {
		definition, ok := findCanonicalTool(name)
		if !ok {
			t.Fatalf("%s is missing", name)
		}
		alternatives, ok := definition.OutputSchema["oneOf"].([]any)
		if !ok || len(alternatives) < 2 {
			t.Fatalf("%s output schema is not a precise success/error union: %+v", name, definition.OutputSchema)
		}
		errorSchema := alternatives[len(alternatives)-1].(map[string]any)
		assertClosedSchemaMatchesType(t, name+" error", errorSchema, reflect.TypeOf(struct {
			Code      string         `json:"code"`
			Category  string         `json:"category"`
			Message   string         `json:"message"`
			Retryable bool           `json:"retryable"`
			Field     string         `json:"field"`
			Details   map[string]any `json:"details"`
			Recovery  map[string]any `json:"recovery"`
		}{}), nil)
	}

	refresh, _ := findCanonicalTool("ck3_refresh")
	refreshSuccess := refresh.OutputSchema["oneOf"].([]any)[0].(map[string]any)
	refreshStats := refreshSuccess["properties"].(map[string]any)["refresh"].(map[string]any)
	assertClosedSchemaMatchesType(t, "refresh scan stats", refreshStats, reflect.TypeOf(indexer.ScanStats{}), nil)

	search, _ := findCanonicalTool("ck3_search")
	searchSuccess := search.OutputSchema["oneOf"].([]any)[0].(map[string]any)
	topologyNullable := searchSuccess["properties"].(map[string]any)["topology"].(map[string]any)
	topology := topologyNullable["oneOf"].([]any)[0].(map[string]any)
	topologyProperties := topology["properties"].(map[string]any)
	assertClosedSchemaMatchesType(t, "search topology", topology, reflect.TypeOf(indexer.LLMTopology{}), nil)
	assertClosedSchemaMatchesType(t, "search topology node", topologyProperties["nodes"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.LLMTopologyNode{}), nil)
	assertClosedSchemaMatchesType(t, "search topology edge", topologyProperties["edges"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.LLMTopologyEdge{}), nil)
	assertClosedSchemaMatchesType(t, "search topology path", topologyProperties["paths_from_center"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.LLMTopologyPath{}), nil)

	workspace, _ := findCanonicalTool("ck3_workspace")
	capabilities := workspace.OutputSchema["oneOf"].([]any)[1].(map[string]any)
	capability := capabilities["properties"].(map[string]any)["capabilities"].(map[string]any)["items"].(map[string]any)
	assertClosedSchemaMatchesType(t, "workspace capability", capability, reflect.TypeOf(workspaceCapability{}), nil)
	mode := capability["properties"].(map[string]any)["mode_details"].(map[string]any)["items"].(map[string]any)
	assertClosedSchemaMatchesType(t, "workspace capability mode", mode, reflect.TypeOf(workspaceCapabilityMode{}), nil)
}

func TestPriorityToolRepresentativeOutputsMatchPublishedSchemas(t *testing.T) {
	search, _ := findCanonicalTool("ck3_search")
	searchValue := indexer.LLMResult{
		Query:   "event:test.1",
		Intent:  "event_chain",
		Summary: "bounded topology",
		Evidence: []indexer.LLMEvidence{{
			Kind: "object", Type: "event", Name: "test.1", Line: 3,
		}},
		Topology: &indexer.LLMTopology{
			Center: "event:test.1", Direction: "callees", IncludeOnActions: true, MaxDepth: 2,
			Nodes: []indexer.LLMTopologyNode{{
				ID: "event:test.1", Type: "event", Name: "test.1", Defined: true, Distance: 0,
			}},
			Edges: []indexer.LLMTopologyEdge{{
				From: "event:test.1", To: "event:test.2", Resolution: "unresolved",
			}},
			PathsFromCenter: []indexer.LLMTopologyPath{{To: "event:test.2", Nodes: []string{"event:test.1", "event:test.2"}}},
		},
		NextQueries: []indexer.LLMNextQuery{{Tool: "object", ID: "event:test.1"}},
	}
	searchSuccess := search.OutputSchema["oneOf"].([]any)[0].(map[string]any)
	topologySchema := searchSuccess["properties"].(map[string]any)["topology"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)
	assertDecodedValueMatchesSchema(t, "representative search topology", searchValue.Topology, topologySchema)
	assertToolValueMatchesOutputSchema(t, search, searchValue)

	refresh, _ := findCanonicalTool("ck3_refresh")
	refreshValue := map[string]any{
		"operation": "files",
		"refresh_status": indexer.RefreshStatus{
			Status: "ready",
			Index: indexer.IndexState{
				Generation: 2,
				Revision:   "revision",
				Status:     "ready",
			},
			Project:           indexer.RefreshProjectStatus{Configured: true, Accessible: true, Writable: true, Refreshable: true, Private: true},
			EngineRules:       indexer.RefreshEngineStatus{Available: true, Current: true},
			NeedsFullScan:     false,
			FullScanAvailable: true,
		},
		"is_scanning":     false,
		"status":          "ready",
		"index":           indexer.IndexState{Generation: 2, Revision: "revision", Status: "ready"},
		"scan_generation": 2,
		"needs_full_scan": false,
		"refresh": indexer.ScanStats{
			Database: "", Files: 1, BySource: map[string]int{"project": 1},
			ChangedFiles: 1, PathOutcomes: []indexer.RefreshPathOutcome{{Path: "events/test.txt", Status: "updated"}},
		},
		"changed_files":             1,
		"removed_files":             0,
		"missing_files":             []string{},
		"path_outcomes":             []indexer.RefreshPathOutcome{{Path: "events/test.txt", Status: "updated"}},
		"changed_symbols":           []string{"event:test.1"},
		"changed_symbols_truncated": false,
		"diagnostic_delta":          &indexer.DiagnosticDelta{},
	}
	assertToolValueMatchesOutputSchema(t, refresh, refreshValue)

	errorValue := map[string]any{
		"code": ErrorIndexStale, "category": "index_state", "message": "stale",
		"retryable": true, "field": "", "details": nil, "recovery": map[string]any{"guidance": "refresh"},
	}
	assertDecodedValueMatchesSchema(t, "structured tool error", errorValue, toolErrorOutputSchema())
}

func TestToolsListGoldens(t *testing.T) {
	assertCatalogGolden(t, "standard_tools_list.golden.sha256", mcpTools())
}

func TestCanonicalLimitSchemasAreUniform(t *testing.T) {
	assertLimit := func(label string, property map[string]any) {
		t.Helper()
		if property["type"] != "integer" || property["minimum"] != 1 || property["maximum"] != 20 || property["default"] != 8 {
			t.Fatalf("%s limit schema is not the canonical 1..20/default 8 contract: %+v", label, property)
		}
	}
	for _, definition := range canonicalTools {
		properties, _ := definition.InputSchema["properties"].(map[string]any)
		if raw, ok := properties["limit"]; ok {
			assertLimit(definition.Name, raw.(map[string]any))
		}
	}
	render, _ := findCanonicalTool("map_render")
	layerLimit := render.InputSchema["properties"].(map[string]any)["layers"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["limit"].(map[string]any)
	assertLimit("map_render.layers[].limit", layerLimit)
}

func TestPhysicalContextSchemaExposesSurfaceMaterials(t *testing.T) {
	tool, ok := findCanonicalTool("map_physical_context")
	if !ok {
		t.Fatal("map_physical_context is missing")
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	operation := properties["operation"].(map[string]any)
	values := operation["enum"].([]string)
	if !slices.Contains(values, "surface") {
		t.Fatalf("map_physical_context operation enum does not expose surface: %+v", values)
	}
}

func TestMapRouteSchemaAdvertisesRuntimeDefaults(t *testing.T) {
	tool, ok := findCanonicalTool("map_route")
	if !ok {
		t.Fatal("map_route is missing")
	}
	properties := tool.InputSchema["properties"].(map[string]any)
	want := map[string]any{
		"mode": "mixed", "objective": "shortest", "context_level": "duchy",
		"label_language": "bilingual", "verbose": false,
	}
	for name, expected := range want {
		if got := properties[name].(map[string]any)["default"]; got != expected {
			t.Fatalf("map_route %s default = %v, want %v", name, got, expected)
		}
	}
}

func TestNestedSchemaAlternativesAreEnforced(t *testing.T) {
	packaging, _ := findCanonicalTool("ck3_package")
	prefix := `{"metadata":{"name":"Example","slug":"example_mod","version":"1","supported_version":"1.*","tags":["Gameplay"]},"files":[`
	for _, raw := range []json.RawMessage{
		json.RawMessage(prefix + `{"path":"common/test.txt"}]}`),
		json.RawMessage(prefix + `{"path":"common/test.txt","content":"x","content_base64":"eA=="}]}`),
	} {
		if err := validateArguments(raw, packaging.InputSchema, packaging.CompatibilityProperties); err == nil {
			t.Fatalf("ck3_package accepted a file without exactly one payload: %s", raw)
		}
	}
	if err := validateArguments(json.RawMessage(prefix+`{"path":"common/test.txt","content":"x"}]}`), packaging.InputSchema, packaging.CompatibilityProperties); err != nil {
		t.Fatalf("ck3_package rejected one text payload: %v", err)
	}

	migration, _ := findCanonicalTool("map_province_migration")
	if err := validateArguments(json.RawMessage(`{"snapshot_id":"s","target":"t","resolutions":[{"action":"drop"}]}`), migration.InputSchema, migration.CompatibilityProperties); err == nil {
		t.Fatal("map_province_migration accepted a resolution without conflict_id or source_province")
	}
	if err := validateArguments(json.RawMessage(`{"snapshot_id":"s","target":"t","resolutions":[{"conflict_id":"c","action":"drop"}]}`), migration.InputSchema, migration.CompatibilityProperties); err != nil {
		t.Fatalf("map_province_migration rejected a keyed resolution: %v", err)
	}

	gui, _ := findCanonicalTool("ck3_gui")
	raw := json.RawMessage(`{"operation":"preview","model_samples":[{"rows":[{"id":"r","samples":[{"property":"text","expression":"GetName","value":"Example"}]}]}]}`)
	if err := validateArguments(raw, gui.InputSchema, gui.CompatibilityProperties); err == nil {
		t.Fatal("ck3_gui accepted model_samples without target or datamodel")
	}
}

func TestGUIActionEffectsSchemaIsBoundedAndTyped(t *testing.T) {
	gui, ok := findCanonicalTool("ck3_gui")
	if !ok {
		t.Fatal("ck3_gui is missing")
	}
	properties := gui.InputSchema["properties"].(map[string]any)
	effects := properties["action_effects"].(map[string]any)
	if effects["maxItems"] != indexer.GUIRuntimeMaxActionEffects {
		t.Fatalf("action_effects maxItems = %v", effects["maxItems"])
	}
	effect := effects["items"].(map[string]any)
	updates := effect["properties"].(map[string]any)["updates"].(map[string]any)
	if updates["minItems"] != 1 || updates["maxItems"] != indexer.GUIRuntimeMaxActionUpdates {
		t.Fatalf("action_effect updates bounds = %+v", updates)
	}
	update := updates["items"].(map[string]any)
	updateProperties := update["properties"].(map[string]any)
	operation := updateProperties["operation"].(map[string]any)
	if !reflect.DeepEqual(operation["enum"], []string{"set", "toggle"}) {
		t.Fatalf("action effect operation enum = %+v", operation["enum"])
	}
	if additional, ok := update["additionalProperties"].(bool); !ok || additional {
		t.Fatal("action effect updates must reject undocumented fields")
	}
}

func TestGUIModelSamplesSchemaIsBoundedAndTyped(t *testing.T) {
	gui, ok := findCanonicalTool("ck3_gui")
	if !ok {
		t.Fatal("ck3_gui is missing")
	}
	properties := gui.InputSchema["properties"].(map[string]any)
	collections := properties["model_samples"].(map[string]any)
	if collections["maxItems"] != indexer.GUIModelSamplesMaxCollections {
		t.Fatalf("model_samples maxItems = %v", collections["maxItems"])
	}
	collection := collections["items"].(map[string]any)
	rows := collection["properties"].(map[string]any)["rows"].(map[string]any)
	if rows["minItems"] != 1 || rows["maxItems"] != indexer.GUIModelSamplesMaxRows {
		t.Fatalf("model sample row bounds = %+v", rows)
	}
	row := rows["items"].(map[string]any)
	rowProperties := row["properties"].(map[string]any)
	id := rowProperties["id"].(map[string]any)
	if id["maxLength"] != indexer.GUIModelSamplesMaxIDLength {
		t.Fatalf("model sample row id bound = %+v", id)
	}
	samples := rowProperties["samples"].(map[string]any)
	if samples["minItems"] != 1 || samples["maxItems"] != indexer.GUIModelSamplesMaxSamples {
		t.Fatalf("model sample expression bounds = %+v", samples)
	}
	sample := samples["items"].(map[string]any)
	property := sample["properties"].(map[string]any)["property"].(map[string]any)
	if !reflect.DeepEqual(property["enum"], []string{"text", "texture", "visible", "enabled"}) {
		t.Fatalf("model sample property enum = %+v", property["enum"])
	}
	if additional, ok := collection["additionalProperties"].(bool); !ok || additional {
		t.Fatal("model sample collections must reject undocumented fields")
	}
}

func TestCanonicalSchemasMatchTypedArguments(t *testing.T) {
	types := map[string]reflect.Type{
		"ck3_search":              reflect.TypeOf(ck3SearchArgs{}),
		"ck3_inspect":             reflect.TypeOf(ck3InspectArgs{}),
		"ck3_review":              reflect.TypeOf(ck3ReviewArgs{}),
		"ck3_workspace":           reflect.TypeOf(ck3WorkspaceArgs{}),
		"ck3_refresh":             reflect.TypeOf(ck3RefreshArgs{}),
		"ck3_dependencies":        reflect.TypeOf(ck3DependenciesArgs{}),
		"ck3_prepare_edit":        reflect.TypeOf(ck3PrepareEditArgs{}),
		"ck3_preflight":           reflect.TypeOf(ck3PreflightArgs{}),
		"ck3_impact":              reflect.TypeOf(ck3ImpactArgs{}),
		"ck3_diagnostics":         reflect.TypeOf(ck3DiagnosticsArgs{}),
		"ck3_script_reference":    reflect.TypeOf(ck3ScriptReferenceArgs{}),
		"ck3_health":              reflect.TypeOf(ck3HealthArgs{}),
		"ck3_package":             reflect.TypeOf(ck3PackageArgs{}),
		"ck3_gui":                 reflect.TypeOf(ck3GUIArgs{}),
		"map_asset_audit":         reflect.TypeOf(mapAssetAuditArgs{}),
		"map_province_mapping":    reflect.TypeOf(mapProvinceMappingArgs{}),
		"map_migration_snapshot":  reflect.TypeOf(mapMigrationSnapshotArgs{}),
		"map_province_migration":  reflect.TypeOf(mapProvinceMigrationArgs{}),
		"map_province_info":       reflect.TypeOf(mapProvinceInfoArgs{}),
		"map_physical_context":    reflect.TypeOf(mapPhysicalContextArgs{}),
		"map_neighbors":           reflect.TypeOf(mapNeighborsArgs{}),
		"map_spatial_relation":    reflect.TypeOf(mapSpatialRelationArgs{}),
		"map_strategic_passages":  reflect.TypeOf(mapStrategicPassagesArgs{}),
		"map_title_context":       reflect.TypeOf(mapTitleContextArgs{}),
		"map_assignment_plan":     reflect.TypeOf(mapAssignmentPlanArgs{}),
		"map_building_candidates": reflect.TypeOf(mapBuildingCandidatesArgs{}),
		"map_recipe_catalog":      reflect.TypeOf(mapRecipeCatalogArgs{}),
		"map_build_metric":        reflect.TypeOf(mapBuildMetricArgs{}),
		"map_route":               reflect.TypeOf(mapRouteArgs{}),
		"map_render":              reflect.TypeOf(mapRenderArgs{}),
	}
	if len(types) != len(canonicalTools) {
		t.Fatalf("typed argument registry count = %d, canonical tools = %d", len(types), len(canonicalTools))
	}
	internalFields := map[string][]string{
		// Provenance is assigned by recipes or derived from model-supplied values;
		// accepting it from MCP callers would allow provenance spoofing.
		"map_build_metric": {"provenance"},
	}
	// max_response_bytes is decoded once by the MCP runtime and deliberately
	// removed before tool-specific structs are decoded. It is a transport
	// control shared by every canonical tool, not a semantic handler field.
	sharedRuntimeFields := map[string]bool{"max_response_bytes": true}
	for _, definition := range canonicalTools {
		typ, ok := types[definition.Name]
		if !ok {
			t.Fatalf("canonical tool %s has no typed argument contract", definition.Name)
		}
		fields := map[string]bool{}
		collectJSONFields(typ, fields)
		for _, compatibility := range definition.CompatibilityProperties {
			delete(fields, compatibility)
		}
		for _, internal := range internalFields[definition.Name] {
			delete(fields, internal)
		}
		properties, _ := definition.InputSchema["properties"].(map[string]any)
		var schemaOnly, structOnly []string
		for name := range properties {
			if sharedRuntimeFields[name] {
				continue
			}
			if !fields[name] {
				schemaOnly = append(schemaOnly, name)
			}
		}
		for name := range fields {
			if _, exists := properties[name]; !exists {
				structOnly = append(structOnly, name)
			}
		}
		sort.Strings(schemaOnly)
		sort.Strings(structOnly)
		if len(schemaOnly) > 0 || len(structOnly) > 0 {
			t.Fatalf("%s schema/typed-argument drift: schema_only=%v struct_only=%v", definition.Name, schemaOnly, structOnly)
		}
	}
}

func collectJSONFields(typ reflect.Type, fields map[string]bool) {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" {
			collectJSONFields(field.Type, fields)
			continue
		}
		if field.PkgPath != "" {
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = true
	}
}

func TestNestedMapSchemasMatchTypedArguments(t *testing.T) {
	metric, _ := findCanonicalTool("map_build_metric")
	metricProperties := metric.InputSchema["properties"].(map[string]any)
	componentSchema := metricProperties["components"].(map[string]any)["items"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map metric component", componentSchema, reflect.TypeOf(indexer.MapMetricComponent{}), nil)
	weights := componentSchema["properties"].(map[string]any)["weights"].(map[string]any)
	if weights["type"] != "object" || weights["additionalProperties"].(map[string]any)["type"] != "number" {
		t.Fatalf("map metric weights schema is not string-to-number: %+v", weights)
	}
	assertClosedSchemaMatchesType(t, "map graph transform", metricProperties["transform"].(map[string]any), reflect.TypeOf(indexer.MapGraphTransform{}), nil)
	metricValueSchema := metricProperties["values"].(map[string]any)["items"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map metric value", metricValueSchema, reflect.TypeOf(indexer.MapMetricValue{}), nil)

	render, _ := findCanonicalTool("map_render")
	renderProperties := render.InputSchema["properties"].(map[string]any)
	layerSchema := renderProperties["layers"].(map[string]any)["items"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map render layer", layerSchema, reflect.TypeOf(indexer.MapRenderLayer{}), nil)
	layerProperties := layerSchema["properties"].(map[string]any)
	nestedMetric := layerProperties["metric"].(map[string]any)
	assertClosedSchemaMatchesType(t, "nested map metric", nestedMetric, reflect.TypeOf(indexer.MapMetricSpec{}), []string{"provenance"})
	assertClosedSchemaMatchesType(t, "nested map metric value", layerProperties["values"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.MapMetricValue{}), nil)
	assertClosedSchemaMatchesType(t, "map render edge", layerProperties["edges"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.MapRenderEdge{}), nil)
	routeSchema := renderProperties["route"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map render route", routeSchema, reflect.TypeOf(indexer.MapRouteResult{}), nil)
	routeProperties := routeSchema["properties"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map render route subject", routeProperties["resolved_from"].(map[string]any), reflect.TypeOf(indexer.MapResolvedSubject{}), nil)
	assertClosedSchemaMatchesType(t, "map render route point", routeProperties["path"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.MapRoutePoint{}), nil)
	assertClosedSchemaMatchesType(t, "map render route leg", routeProperties["legs"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.MapRouteLeg{}), nil)
	assertClosedSchemaMatchesType(t, "map render route corridor", routeProperties["corridor_targets"].(map[string]any), reflect.TypeOf(indexer.MapRouteCorridorTargets{}), nil)
	failureSchema := routeProperties["error"].(map[string]any)
	assertClosedSchemaMatchesType(t, "map render route failure", failureSchema, reflect.TypeOf(indexer.MapRouteFailure{}), nil)
	assertClosedSchemaMatchesType(t, "map render route candidate", failureSchema["properties"].(map[string]any)["resolution_candidates"].(map[string]any)["items"].(map[string]any), reflect.TypeOf(indexer.MapSubjectCandidate{}), nil)
	assertClosedSchemaMatchesType(t, "map render route timings", routeProperties["timings_ms"].(map[string]any), reflect.TypeOf(indexer.MapRouteTimings{}), nil)
}

func assertClosedSchemaMatchesType(t *testing.T, label string, schema map[string]any, typ reflect.Type, ignored []string) {
	t.Helper()
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("%s schema must set additionalProperties=false", label)
	}
	fields := map[string]bool{}
	collectJSONFields(typ, fields)
	for _, name := range ignored {
		delete(fields, name)
	}
	properties, _ := schema["properties"].(map[string]any)
	var schemaOnly, structOnly []string
	for name := range properties {
		if !fields[name] {
			schemaOnly = append(schemaOnly, name)
		}
	}
	for name := range fields {
		if _, exists := properties[name]; !exists {
			structOnly = append(structOnly, name)
		}
	}
	sort.Strings(schemaOnly)
	sort.Strings(structOnly)
	if len(schemaOnly) > 0 || len(structOnly) > 0 {
		t.Fatalf("%s schema/type drift: schema_only=%v struct_only=%v", label, schemaOnly, structOnly)
	}
}

func assertToolValueMatchesOutputSchema(t *testing.T, definition *ToolDefinition, value any) {
	t.Helper()
	_, structured, err := encodeStructuredValue(value)
	if err != nil {
		t.Fatalf("encode %s representative output: %v", definition.Name, err)
	}
	assertDecodedValueMatchesSchema(t, definition.Name+" representative output", structured, definition.OutputSchema)
}

func assertDecodedValueMatchesSchema(t *testing.T, label string, value any, schema map[string]any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", label, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}
	if err := validateDecodedProperty(label, decoded, schema); err != nil {
		var alternativeErrors []string
		if alternatives, ok := schema["oneOf"].([]any); ok {
			for index, raw := range alternatives {
				alternative, _ := raw.(map[string]any)
				alternativeErrors = append(alternativeErrors, fmt.Sprintf("%d=%v", index, validateDecodedProperty(label, decoded, alternative)))
			}
		}
		t.Fatalf("%s does not match output schema: %v alternatives=[%s]\nvalue=%s", label, err, strings.Join(alternativeErrors, "; "), data)
	}
}

func assertCatalogGolden(t *testing.T, filename string, catalog any) {
	t.Helper()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	got := fmt.Sprintf("%x", sum)
	wantBytes, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(wantBytes))
	if got != want {
		t.Fatalf("%s changed: got sha256 %s, want %s", filename, got, want)
	}
}

func TestEveryCallableToolHasSuccessAndMalformedArgumentCases(t *testing.T) {
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
	snapshot, err := migrator.CreateSnapshot(context.Background(), cfg, migrator.SnapshotSpec{Project: "project", Base: "base"})
	if err != nil {
		t.Fatal(err)
	}

	patchFiles := []map[string]any{{
		"path":    "common/traits/contract_traits.txt",
		"content": "contract_trait = { desc = contract_trait_desc }",
	}}
	successArguments := map[string]map[string]any{
		"ck3_search":           {"query": "c_c114", "limit": 2},
		"ck3_inspect":          {"id": "c_c114", "limit": 2},
		"ck3_review":           {"files": patchFiles, "limit": 2},
		"ck3_workspace":        {"operation": "overview", "limit": 2},
		"ck3_dependencies":     {"id": "c_c114", "depth": 1, "limit": 2},
		"ck3_prepare_edit":     {"id": "c_c114", "operation": "context", "limit": 2},
		"ck3_preflight":        {"operation": "patch", "files": patchFiles, "limit": 2},
		"ck3_impact":           {"files": patchFiles, "limit": 2},
		"ck3_diagnostics":      {"operation": "summary", "limit": 2},
		"ck3_refresh":          {"operation": "status"},
		"ck3_script_reference": {"kind": "shape", "id": "has_trait", "limit": 2},
		"ck3_health":           {},
		"ck3_package": {
			"metadata": map[string]any{"name": "Contract Mod", "slug": "contract_mod", "version": "1.0", "supported_version": "1.19.*", "tags": []string{"Gameplay"}},
			"files":    []map[string]any{{"path": "common/scripted_triggers/contract_package.txt", "content": "contract_package_trigger = { always = yes }"}},
		},
		"ck3_gui":                 {"operation": "summary", "limit": 2},
		"map_asset_audit":         {"operation": "summary", "limit": 2},
		"map_province_mapping":    {"source": "project", "target": "active", "limit": 2},
		"map_migration_snapshot":  {"project": "project", "base": "base"},
		"map_province_migration":  {"snapshot_id": snapshot.SnapshotID, "target": "target", "output_name": "contract_fork"},
		"map_province_info":       {"id": "1", "year": 6253, "limit": 2},
		"map_physical_context":    {"target_type": "region", "target": "region:test_region", "operation": "oceanography", "include_adjacent_water": true, "limit": 2},
		"map_neighbors":           {"id": "1", "radius": 1, "year": 6253, "limit": 2},
		"map_spatial_relation":    {"from": "1", "to": "2", "year": 6253, "limit": 2},
		"map_strategic_passages":  {"target": "e_test", "limit": 2},
		"map_title_context":       {"id": "k_k11", "year": 6253, "limit": 2},
		"map_assignment_plan":     {"target": "k_k11", "assignment_mode": "religion", "year": 6253, "limit": 2},
		"map_building_candidates": {"target": "k_k11", "year": 6253, "limit": 2},
		"map_recipe_catalog":      {},
		"map_build_metric":        {"recipe": "development_network", "target": "k_k11", "year": 6253, "limit": 2},
		"map_route":               {"from": "1", "to": "2", "mode": "land", "year": 6253, "limit": 2},
		"map_render":              {"target": "k_k11", "year": 6253, "width": 400, "layers": []map[string]any{{"type": "borders", "level": "county"}}},
	}
	if len(successArguments) != 30 {
		t.Fatalf("success case count = %d, want 30 canonical names", len(successArguments))
	}
	for name, args := range successArguments {
		name, args := name, args
		t.Run(name+"/success", func(t *testing.T) {
			result := callToolForTest(t, db, cfg, name, args)
			if result["isError"] == true {
				t.Fatalf("success contract returned tool error: %+v", result)
			}
			if _, ok := result["structuredContent"].(map[string]any); !ok {
				t.Fatalf("success contract has no structuredContent: %+v", result)
			}
			if _, hasMeta := result["_meta"]; hasMeta {
				t.Fatalf("canonical result unexpectedly has compatibility metadata: %+v", result)
			}
		})
		t.Run(name+"/malformed_arguments", func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"name": name, "arguments": "bad"})
			if err != nil {
				t.Fatal(err)
			}
			resultAny, err := callMCPTool(context.Background(), db, cfg, raw)
			if err != nil {
				t.Fatalf("known-tool argument error escaped as protocol error: %v", err)
			}
			result := resultAny.(map[string]any)
			if result["isError"] != true {
				t.Fatalf("malformed arguments did not return isError=true: %+v", result)
			}
		})
	}
}

func callToolForTest(t *testing.T, db *indexer.DB, cfg indexer.Config, name string, arguments any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	result, err := callMCPTool(context.Background(), db, cfg, raw)
	if err != nil {
		t.Fatalf("tool %s returned protocol error: %v", name, err)
	}
	return result.(map[string]any)
}
