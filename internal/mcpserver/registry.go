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
}

type ToolDocumentation struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Canonical   string
	Deprecated  bool
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

func advertisedCanonicalTool(definition ToolDefinition) map[string]any {
	return map[string]any{
		"name":         definition.Name,
		"title":        definition.Title,
		"description":  definition.Description,
		"inputSchema":  definition.InputSchema,
		"outputSchema": definition.OutputSchema,
		"annotations":  definition.Annotations,
	}
}

func CanonicalToolDocumentation() []ToolDocumentation {
	docs := make([]ToolDocumentation, 0, len(canonicalTools))
	for _, definition := range canonicalTools {
		docs = append(docs, ToolDocumentation{
			Name: definition.Name, Title: definition.Title, Description: definition.Description,
			InputSchema: cloneSchema(definition.InputSchema), Canonical: definition.Name,
		})
	}
	return docs
}
