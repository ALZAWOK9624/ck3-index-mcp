package mcpserver

import (
	"context"
	"encoding/json"
)

type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

type ToolHandler func(context.Context, *Runtime, *ToolDefinition, json.RawMessage) (toolOutput, error)

type ToolDefinition struct {
	Name                    string
	Title                   string
	Description             string
	InputSchema             map[string]any
	OutputSchema            map[string]any
	Annotations             ToolAnnotations
	Handler                 ToolHandler
	CompatibilityProperties []string
	// TrimmableFields is an explicit allowlist of top-level structuredContent
	// arrays whose relevance-ordered tail may be dropped to meet a response
	// budget. Complete contracts such as files, patch_files, databases, and
	// artifacts must never appear here.
	TrimmableFields []string
}

var toolTrimmableFields = map[string][]string{
	"ck3_search":       {"evidence", "suggestions"},
	"ck3_inspect":      {"evidence"},
	"ck3_review":       {"evidence"},
	"ck3_dependencies": {"evidence"},
	"ck3_prepare_edit": {"evidence"},
	"ck3_preflight":    {"evidence"},
	"ck3_impact":       {"evidence"},
	"ck3_diagnostics":  {"evidence"},
}

func declareTrimmableResponseFields(definitions []ToolDefinition) []ToolDefinition {
	for i := range definitions {
		definitions[i].TrimmableFields = append([]string(nil), toolTrimmableFields[definitions[i].Name]...)
	}
	return definitions
}

type ToolDocumentation struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Canonical   string
	Deprecated  bool
	Annotations ToolAnnotations
}

var (
	canonicalTools  = buildCanonicalTools()
	advertisedTools = buildAdvertisedTools()
)

func readOnlyAnnotations() ToolAnnotations {
	return ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, OpenWorldHint: false}
}

func artifactAnnotations() ToolAnnotations {
	return ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, OpenWorldHint: false}
}

func registry() []ToolDefinition {
	return append([]ToolDefinition(nil), canonicalTools...)
}

func findCanonicalTool(name string) (*ToolDefinition, bool) {
	for i := range canonicalTools {
		if canonicalTools[i].Name == name {
			return &canonicalTools[i], true
		}
	}
	return nil, false
}

func mcpTools() []map[string]any {
	return append([]map[string]any(nil), advertisedTools...)
}

func buildAdvertisedTools() []map[string]any {
	tools := make([]map[string]any, 0, len(canonicalTools))
	for _, definition := range canonicalTools {
		tools = append(tools, advertisedCanonicalTool(definition))
	}
	return tools
}

// advertisedCanonicalTool deliberately omits outputSchema. MCP treats it as
// optional, and a caller gains nothing from it here: every tool already returns
// structuredContent, and the precise unions are supersets no client rejects. It
// was 24 KB of the 120 KB catalog — a fifth of the context every session paid
// before asking anything. ToolDefinition.OutputSchema is retained because
// contract_test asserts real handler output against it; that check is the
// schema's actual value, and it does not require publishing it.
func advertisedCanonicalTool(definition ToolDefinition) map[string]any {
	return map[string]any{
		"name":        definition.Name,
		"title":       definition.Title,
		"description": definition.Description,
		"inputSchema": publishedInputSchema(definition.InputSchema),
		"annotations": definition.Annotations,
	}
}

func CanonicalToolDocumentation() []ToolDocumentation {
	docs := make([]ToolDocumentation, 0, len(canonicalTools))
	for _, definition := range canonicalTools {
		docs = append(docs, ToolDocumentation{
			Name: definition.Name, Title: definition.Title, Description: definition.Description,
			InputSchema: cloneSchema(definition.InputSchema), Canonical: definition.Name,
			Annotations: definition.Annotations,
		})
	}
	return docs
}
