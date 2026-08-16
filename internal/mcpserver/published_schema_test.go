package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// walkSchemaNodes visits every schema object reachable from a tool input
// schema, naming each node by the path a validator would report it under.
func walkSchemaNodes(path string, node map[string]any, visit func(string, map[string]any)) {
	if node == nil {
		return
	}
	visit(path, node)
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, child := range properties {
			if nested, ok := child.(map[string]any); ok {
				walkSchemaNodes(fmt.Sprintf("%s.properties[%s]", path, name), nested, visit)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		walkSchemaNodes(path+".items", items, visit)
	}
	if additional, ok := node["additionalProperties"].(map[string]any); ok {
		walkSchemaNodes(path+".additionalProperties", additional, visit)
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		alternatives, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for index, item := range alternatives {
			if nested, ok := item.(map[string]any); ok {
				walkSchemaNodes(fmt.Sprintf("%s.%s[%d]", path, keyword, index), nested, visit)
			}
		}
	}
}

// TestAdvertisedInputSchemasSatisfyStrictFunctionDeclarationRules encodes the
// two rules a strict function-declaration validator applies to required: it is
// only allowed on an object, and every name it lists must be declared beside
// it. Gemini rejects the whole tools payload when either is broken, so one
// stray branch makes every tool in this server unreachable from that client.
func TestAdvertisedInputSchemasSatisfyStrictFunctionDeclarationRules(t *testing.T) {
	for _, tool := range mcpTools() {
		name, _ := tool["name"].(string)
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s advertises no input schema", name)
		}
		if kind, _ := schema["type"].(string); kind != "object" {
			t.Errorf("%s: parameters must be an object, got %q", name, kind)
		}
		walkSchemaNodes("parameters", schema, func(path string, node map[string]any) {
			for _, keyword := range []string{"oneOf", "anyOf"} {
				if alternatives, ok := node[keyword].([]any); ok && pureRequiredAlternatives(alternatives) {
					t.Errorf("%s: %s.%s carries branches that name required fields and nothing else", name, path, keyword)
				}
			}
			required := schemaStrings(node["required"])
			if len(required) == 0 {
				return
			}
			if kind, _ := node["type"].(string); kind != "object" {
				t.Errorf("%s: %s.required is declared on a %q node", name, path, kind)
			}
			properties, _ := node["properties"].(map[string]any)
			for _, field := range required {
				if _, declared := properties[field]; !declared {
					t.Errorf("%s: %s.required names %q, which is not declared beside it", name, path, field)
				}
			}
		})
	}
}

// TestFoldedAlternativesStayEnforcedAndDescribed pins the trade the published
// form makes: the machine-readable branch is gone, the prose that replaces it
// names the same choice, and the server still refuses a call that satisfies no
// alternative.
func TestFoldedAlternativesStayEnforcedAndDescribed(t *testing.T) {
	for _, expectation := range []struct {
		tool      string
		path      []string
		choices   string
		rejected  string
		acceptedA string
		acceptedB string
	}{
		{
			tool: "ck3_search", choices: "Provide one of: query, queries.",
			rejected: `{"limit":2}`, acceptedA: `{"query":"c_c114"}`, acceptedB: `{"queries":["c_c114"]}`,
		},
		{
			tool: "map_render", choices: "Provide one of: layers, recipe.",
			rejected: `{"theme":"political"}`, acceptedA: `{"recipe":"political_atlas"}`,
			acceptedB: `{"layers":[{"type":"fill"}]}`,
		},
	} {
		definition, ok := findCanonicalTool(expectation.tool)
		if !ok {
			t.Fatalf("%s is not a canonical tool", expectation.tool)
		}
		published := publishedInputSchema(definition.InputSchema)
		description, _ := published["description"].(string)
		if !strings.HasSuffix(description, expectation.choices) {
			t.Errorf("%s: published description %q does not end with %q", expectation.tool, description, expectation.choices)
		}
		if err := validateArguments(json.RawMessage(expectation.rejected), definition.InputSchema, definition.CompatibilityProperties); err == nil {
			t.Errorf("%s: %s was accepted; the folded constraint stopped being enforced", expectation.tool, expectation.rejected)
		}
		for _, accepted := range []string{expectation.acceptedA, expectation.acceptedB} {
			if err := validateArguments(json.RawMessage(accepted), definition.InputSchema, definition.CompatibilityProperties); err != nil {
				t.Errorf("%s: %s was rejected: %v", expectation.tool, accepted, err)
			}
		}
	}
}

// TestFoldedItemAlternativesStayEnforced covers the nested branches, which sit
// inside array items rather than at the top of the schema.
func TestFoldedItemAlternativesStayEnforced(t *testing.T) {
	for _, expectation := range []struct {
		tool     string
		property string
		rejected string
		accepted string
	}{
		{
			tool: "ck3_package", property: "files",
			rejected: `{"metadata":{"name":"n","slug":"slug","version":"1","supported_version":"1.19.*","tags":["t"]},"files":[{"path":"descriptor.mod"}]}`,
			accepted: `{"metadata":{"name":"n","slug":"slug","version":"1","supported_version":"1.19.*","tags":["t"]},"files":[{"path":"descriptor.mod","content":"x"}]}`,
		},
		{
			tool: "map_province_migration", property: "resolutions",
			rejected: `{"snapshot_id":"s","target":"t","resolutions":[{"action":"drop"}]}`,
			accepted: `{"snapshot_id":"s","target":"t","resolutions":[{"action":"drop","conflict_id":"c"}]}`,
		},
	} {
		definition, ok := findCanonicalTool(expectation.tool)
		if !ok {
			t.Fatalf("%s is not a canonical tool", expectation.tool)
		}
		published := publishedInputSchema(definition.InputSchema)
		properties, _ := published["properties"].(map[string]any)
		property, _ := properties[expectation.property].(map[string]any)
		items, _ := property["items"].(map[string]any)
		description, _ := items["description"].(string)
		if !strings.Contains(description, "Provide one of: ") {
			t.Errorf("%s: %s items lost the folded choice, description is %q", expectation.tool, expectation.property, description)
		}
		if err := validateArguments(json.RawMessage(expectation.rejected), definition.InputSchema, definition.CompatibilityProperties); err == nil {
			t.Errorf("%s: an item satisfying no alternative was accepted", expectation.tool)
		}
		if err := validateArguments(json.RawMessage(expectation.accepted), definition.InputSchema, definition.CompatibilityProperties); err != nil {
			t.Errorf("%s: a valid item was rejected: %v", expectation.tool, err)
		}
	}
}
