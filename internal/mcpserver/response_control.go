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

// addResponseBudgetProperty keeps the response-size contract consistent for
// every canonical tool without forcing each handler argument struct to carry
// an unrelated transport field.
func addResponseBudgetProperty(definitions []ToolDefinition) []ToolDefinition {
	for i := range definitions {
		properties, ok := definitions[i].InputSchema["properties"].(map[string]any)
		if !ok || properties == nil {
			continue
		}
		properties["max_response_bytes"] = responseBudgetProperty()
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
