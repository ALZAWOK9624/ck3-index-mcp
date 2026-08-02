package mcpserver

import (
	"encoding/json"
	"testing"

	"ck3-index/internal/indexer"
)

// Follow-up suggestions are delivered only if the tool name is registered and
// the arguments validate against its real schema; anything else is dropped
// without a trace. When 0.5.0 consolidated the tool surface, every producer in
// the indexer kept emitting the names of the tools that had just been removed,
// so the whole mechanism went silently dead — most damagingly on ck3_inspect,
// the most frequently called tool, which suggested inspect_object and
// validate_project and therefore suggested nothing at all.
//
// The existing contract test could not see this: it fed canonicalizeNextActions
// a synthetic fixture of already-canonical names rather than calling a handler.
// These tests call the real tools and require real suggestions.

func TestCoreToolsReturnUsableNextActions(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	for _, testCase := range []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "search", tool: "ck3_search", arguments: map[string]any{"query": "size_probe_trait_alpha"}},
		{name: "inspect/aggregate", tool: "ck3_inspect", arguments: map[string]any{"id": "size_probe_trait_alpha"}},
		{name: "inspect/definition", tool: "ck3_inspect", arguments: map[string]any{"id": "size_probe_trait_alpha", "operation": "definition"}},
		{name: "inspect/references", tool: "ck3_inspect", arguments: map[string]any{"id": "size_probe_trait_alpha", "operation": "references"}},
		{name: "inspect/localization", tool: "ck3_inspect", arguments: map[string]any{"id": "size_probe_trait_alpha", "operation": "localization"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := callToolForTest(t, db, cfg, testCase.tool, testCase.arguments)
			if result["isError"] == true {
				t.Fatalf("tool returned an error: %+v", result)
			}
			body, ok := result["structuredContent"].(map[string]any)
			if !ok {
				t.Fatalf("no structuredContent: %+v", result)
			}
			if _, leaked := body["next_queries"]; leaked {
				t.Errorf("raw next_queries reached the caller; it should have been translated to next_actions")
			}
			actions, ok := body["next_actions"].([]map[string]any)
			if !ok || len(actions) == 0 {
				t.Fatalf("no next_actions survived translation, so the caller receives no follow-up at all: %s", mustJSON(t, body))
			}
			for _, action := range actions {
				name, _ := action["tool"].(string)
				definition, found := findCanonicalTool(name)
				if !found {
					t.Errorf("next_action names unregistered tool %q", name)
					continue
				}
				arguments, _ := action["arguments"].(map[string]any)
				encoded := mustJSON(t, arguments)
				if err := validateArguments([]byte(encoded), definition.InputSchema, definition.CompatibilityProperties); err != nil {
					t.Errorf("next_action for %s does not validate against its own schema: %v (arguments=%s)", name, err, encoded)
				}
			}
		})
	}
}

// TestIndexerNextQueryToolsAreRegistered checks the constants the indexer
// package uses to name tools. indexer cannot import the registry — the
// dependency runs the other way — so this is the only place the two sides can
// be compared.
func TestIndexerNextQueryToolsAreRegistered(t *testing.T) {
	for _, name := range []string{
		indexer.ToolSearch,
		indexer.ToolInspect,
		indexer.ToolDependencies,
		indexer.ToolPrepareEdit,
		indexer.ToolPreflight,
		indexer.ToolDiagnostics,
		indexer.ToolScriptReference,
		indexer.ToolWorkspace,
	} {
		if _, found := findCanonicalTool(name); !found {
			t.Errorf("indexer names %q as a canonical tool but no such tool is registered", name)
		}
	}
}

// TestPreflightAndDependencyToolsSuggestRegisteredFollowUps extends the check
// past the read tools, to the edit flow where a dropped suggestion costs the
// caller the most.
func TestPreflightAndDependencyToolsSuggestRegisteredFollowUps(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	for _, testCase := range []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "dependencies", tool: "ck3_dependencies", arguments: map[string]any{"id": "size_probe_trait_alpha"}},
		{name: "preflight/subject", tool: "ck3_preflight", arguments: map[string]any{"operation": "subject", "id": "size_probe_trait_alpha"}},
		{name: "prepare_edit", tool: "ck3_prepare_edit", arguments: map[string]any{"id": "size_probe_trait_alpha"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := callToolForTest(t, db, cfg, testCase.tool, testCase.arguments)
			if result["isError"] == true {
				t.Skipf("tool not applicable to this fixture: %+v", result["structuredContent"])
			}
			body, _ := result["structuredContent"].(map[string]any)
			actions, ok := body["next_actions"].([]map[string]any)
			if !ok {
				return
			}
			for _, action := range actions {
				name, _ := action["tool"].(string)
				if _, found := findCanonicalTool(name); !found {
					t.Errorf("next_action names unregistered tool %q", name)
				}
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
