package mcpserver

import (
	"encoding/json"
	"fmt"
)

// Tool responses are normally much smaller because every evidence-producing
// operation already has a bounded limit. This is a second, transport-facing
// guard: it prevents an accidental new result field, HTML preview, or binary
// attachment from turning one MCP call into an unbounded context payload.
const (
	defaultToolResponseBytes = 2 << 20
	minToolResponseBytes     = 16 << 10
	maxToolResponseBytes     = 8 << 20
)

type responseControl struct {
	MaxResponseBytes int `json:"max_response_bytes,omitempty"`
}

func responseBudgetProperty() map[string]any {
	return integerProperty("Maximum encoded MCP tool-result size in bytes. Use limit/page to reduce semantic evidence first; this is a hard response safety budget.", minToolResponseBytes, maxToolResponseBytes, defaultToolResponseBytes)
}

// largeResultTools are the only tools whose result can realistically approach
// the response budget: they attach a PNG or a standalone HTML document. Every
// other tool is already bounded by limit/page to a few kilobytes of evidence.
var largeResultTools = map[string]bool{
	"map_render":         true,
	"map_terrain_edit":   true,
	"ck3_gui":            true,
	"ck3_dependencies":   true,
	"map_split_province": true,
}

// addResponseBudgetProperty advertises the budget knob only where it is
// plausibly useful, but keeps every tool accepting it. Publishing it on all 34
// tools cost roughly 7 KB of catalog for a transport control a caller sets on
// almost none of them; validateArguments runs before splitResponseControl, so
// the compatibility entry is what keeps the argument legal everywhere.
func addResponseBudgetProperty(definitions []ToolDefinition) []ToolDefinition {
	for i := range definitions {
		properties, ok := definitions[i].InputSchema["properties"].(map[string]any)
		if !ok || properties == nil {
			continue
		}
		if largeResultTools[definitions[i].Name] {
			properties["max_response_bytes"] = responseBudgetProperty()
			continue
		}
		definitions[i].CompatibilityProperties = append(
			append([]string(nil), definitions[i].CompatibilityProperties...),
			"max_response_bytes",
		)
	}
	return definitions
}

// splitResponseControl validates the common control at the catalog boundary,
// then removes it before a concrete handler decodes its documented arguments.
// This avoids duplicating MaxResponseBytes across every tool-specific struct.
func splitResponseControl(raw json.RawMessage) (json.RawMessage, responseControl, error) {
	control := responseControl{MaxResponseBytes: defaultToolResponseBytes}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, control, invalidArgument("", "arguments must be a JSON object")
	}
	if encoded, ok := fields["max_response_bytes"]; ok {
		if err := json.Unmarshal(encoded, &control.MaxResponseBytes); err != nil {
			return nil, control, invalidArgument("max_response_bytes", "max_response_bytes must be an integer")
		}
	}
	if control.MaxResponseBytes == 0 {
		control.MaxResponseBytes = defaultToolResponseBytes
	}
	if control.MaxResponseBytes < minToolResponseBytes || control.MaxResponseBytes > maxToolResponseBytes {
		return nil, control, invalidArgument("max_response_bytes", fmt.Sprintf("max_response_bytes must be between %d and %d", minToolResponseBytes, maxToolResponseBytes))
	}
	delete(fields, "max_response_bytes")
	clean, err := json.Marshal(fields)
	if err != nil {
		return nil, control, newToolError(ErrorInternal, "internal", "ck3-index could not prepare the tool request", false, nil, nil)
	}
	return clean, control, nil
}

type responseTooLargeError struct {
	Actual int
	Limit  int
}

func (e *responseTooLargeError) Error() string {
	return fmt.Sprintf("encoded tool result is %d bytes, above the %d byte response budget", e.Actual, e.Limit)
}
