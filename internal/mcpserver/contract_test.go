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
	if len(definitions) != 29 {
		t.Fatalf("standard registry count = %d, want 29", len(definitions))
	}
	if len(legacyAliases) != 28 {
		t.Fatalf("legacy alias count = %d, want 28", len(legacyAliases))
	}
	if got := len(mcpToolsForProfile(ProfileStandard)); got != 29 {
		t.Fatalf("standard tools/list count = %d, want 29", got)
	}
	if got := len(mcpToolsForProfile(ProfileExpert)); got != 57 {
		t.Fatalf("expert tools/list count = %d, want 57", got)
	}

	seen := make(map[string]struct{}, len(definitions)+len(legacyAliases))
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
		if definition.Name == "ck3_package" || definition.Name == "map_migration_snapshot" || definition.Name == "map_province_migration" {
			if definition.Annotations.ReadOnlyHint || definition.Annotations.DestructiveHint || definition.Annotations.OpenWorldHint {
				t.Fatalf("tool %q annotations must be non-read-only, non-destructive, and closed-world", definition.Name)
			}
		} else if !definition.Annotations.ReadOnlyHint || definition.Annotations.DestructiveHint || definition.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations are not read-only/closed-world", definition.Name)
		}
	}
	for _, alias := range legacyAliases {
		if _, exists := seen[alias.Name]; exists {
			t.Fatalf("legacy alias collides with canonical tool %q", alias.Name)
		}
		seen[alias.Name] = struct{}{}
		if _, ok := findCanonicalTool(alias.Canonical); !ok {
			t.Fatalf("legacy alias %q targets missing canonical tool %q", alias.Name, alias.Canonical)
		}
	}
}

func TestToolsListGoldens(t *testing.T) {
	assertCatalogGolden(t, "legacy_tools_list.golden.sha256", legacyToolCatalog())
	assertCatalogGolden(t, "standard_tools_list.golden.sha256", mcpToolsForProfile(ProfileStandard))
	assertCatalogGolden(t, "expert_tools_list.golden.sha256", mcpToolsForProfile(ProfileExpert))
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
		"query_object":            {"id": "c_c114", "limit": 2},
		"query_object_types":      {"limit": 2},
		"find_refs":               {"id": "c_c114", "limit": 2},
		"query_loc":               {"id": "c_c114", "limit": 2},
		"query_resource":          {"id": "gfx/interface/contract.dds", "limit": 2},
		"query_examples":          {"id": "landed_title", "limit": 2},
		"query_rules":             {"id": "landed_title", "limit": 2},
		"query_patterns":          {"id": "landed_title", "limit": 2},
		"architecture_overview":   {"limit": 2},
		"dependency_graph":        {"id": "c_c114", "depth": 1, "limit": 2},
		"validate_project":        {"limit": 2},
		"health_check":            {"limit": 2},
		"explain_diagnostic":      {"id": "missing_object_reference", "limit": 2},
		"inspect_object":          {"id": "c_c114", "limit": 2},
		"prepare_edit":            {"id": "c_c114", "limit": 2},
		"preflight_code":          {"id": "c_c114", "limit": 2},
		"preflight_patch":         {"files": patchFiles, "limit": 2},
		"impact_patch":            {"files": patchFiles, "limit": 2},
		"preflight_dirty":         {"limit": 2},
		"diagnose_key":            {"id": "c_c114", "limit": 2},
		"lookup_scope":            {"id": "has_trait", "limit": 2},
		"lookup_datatype":         {"id": "character", "limit": 2},
		"lookup_shape":            {"id": "has_trait"},
		"lookup_define":           {"id": "contract_define"},
		"lookup_on_action":        {"id": "on_birth_child"},
		"lookup_iterator":         {"id": "any_courtier"},
		"lookup_example":          {"id": "add_gold"},
		"lookup_modifier":         {"id": "monthly_prestige"},
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
	if len(successArguments) != 57 {
		t.Fatalf("success case count = %d, want 57 callable canonical/legacy names", len(successArguments))
	}

	legacyNames := map[string]bool{}
	for _, alias := range legacyAliases {
		legacyNames[alias.Name] = true
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
			meta, hasMeta := result["_meta"].(map[string]any)
			if legacyNames[name] {
				if !hasMeta || meta["deprecated_tool"] != name {
					t.Fatalf("legacy result has no deprecation metadata: %+v", result)
				}
			} else if hasMeta {
				t.Fatalf("canonical result unexpectedly marked deprecated: %+v", meta)
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
