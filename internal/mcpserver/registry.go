package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

type ToolProfile string

const (
	ProfileStandard ToolProfile = "standard"
	ProfileExpert   ToolProfile = "expert"
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

type LegacyAlias struct {
	Name        string
	Canonical   string
	Operation   string
	Kind        string
	Description string
	InputSchema map[string]any
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
	canonicalTools          = buildCanonicalTools()
	legacyAliases           = buildLegacyAliases()
	standardAdvertisedTools = buildAdvertisedTools(ProfileStandard)
	expertAdvertisedTools   = buildAdvertisedTools(ProfileExpert)
)

func readOnlyAnnotations() ToolAnnotations {
	return ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, OpenWorldHint: false}
}

func artifactAnnotations() ToolAnnotations {
	return ToolAnnotations{ReadOnlyHint: false, DestructiveHint: false, OpenWorldHint: false}
}

func configuredProfile() ToolProfile {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CK3_INDEX_MCP_PROFILE")), string(ProfileExpert)) {
		return ProfileExpert
	}
	return ProfileStandard
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

func findLegacyAlias(name string) (*LegacyAlias, bool) {
	for i := range legacyAliases {
		if legacyAliases[i].Name == name {
			return &legacyAliases[i], true
		}
	}
	return nil, false
}

func mcpTools() []map[string]any {
	return mcpToolsForProfile(configuredProfile())
}

func mcpToolsForProfile(profile ToolProfile) []map[string]any {
	if profile == ProfileExpert {
		return expertAdvertisedTools
	}
	return standardAdvertisedTools
}

func buildAdvertisedTools(profile ToolProfile) []map[string]any {
	tools := make([]map[string]any, 0, len(canonicalTools)+len(legacyAliases))
	for _, definition := range canonicalTools {
		tools = append(tools, advertisedCanonicalTool(definition))
	}
	if profile == ProfileExpert {
		for _, alias := range legacyAliases {
			canonical, ok := findCanonicalTool(alias.Canonical)
			if !ok {
				continue
			}
			tools = append(tools, advertisedAliasTool(alias, *canonical))
		}
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

func advertisedAliasTool(alias LegacyAlias, canonical ToolDefinition) map[string]any {
	description := alias.Description
	if description != "" {
		description += " "
	}
	description += "Deprecated compatibility entry; use " + canonical.Name + "."
	return map[string]any{
		"name":         alias.Name,
		"title":        "Deprecated: " + alias.Name,
		"description":  description,
		"inputSchema":  alias.InputSchema,
		"outputSchema": canonical.OutputSchema,
		"annotations":  canonical.Annotations,
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

func LegacyToolDocumentation() []ToolDocumentation {
	docs := make([]ToolDocumentation, 0, len(legacyAliases))
	for _, alias := range legacyAliases {
		docs = append(docs, ToolDocumentation{
			Name: alias.Name, Title: "Deprecated: " + alias.Name, Description: alias.Description,
			InputSchema: cloneSchema(alias.InputSchema), Canonical: alias.Canonical, Deprecated: true,
		})
	}
	return docs
}
