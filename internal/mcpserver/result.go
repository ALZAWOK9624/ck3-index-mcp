package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
)

func encodeToolResult(value any, visibility string) (map[string]any, error) {
	return encodeToolResultWithBudget(value, visibility, defaultToolResponseBytes)
}

func encodeToolResultWithBudget(value any, visibility string, responseBudget int) (map[string]any, error) {
	value = redactToolValue(value, visibility)
	if rendered, ok := value.(indexer.GUIQueryResult); ok && rendered.Preview != nil && len(rendered.Preview.PNG) > 0 {
		pngData := rendered.Preview.PNG
		rendered.Preview.PNG = nil
		data, structured, err := encodeStructuredValue(rendered)
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(data)},
				{
					"type": "image", "data": base64.StdEncoding.EncodeToString(pngData), "mimeType": "image/png",
					"annotations": map[string]any{"audience": []string{"user"}},
				},
			},
			"structuredContent": structured,
		}
		return enforceResponseBudget(result, responseBudget)
	}
	if rendered, ok := value.(indexer.MapRenderResult); ok {
		pngData := rendered.PNG
		rendered.PNG = nil
		data, structured, err := encodeStructuredValue(rendered)
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(data)},
				{
					"type":     "image",
					"data":     base64.StdEncoding.EncodeToString(pngData),
					"mimeType": "image/png",
					"annotations": map[string]any{
						"audience": []string{"user"},
					},
				},
			},
			"structuredContent": structured,
		}
		return enforceResponseBudget(result, responseBudget)
	}
	data, structured, err := encodeStructuredValue(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(data)}},
		"structuredContent": structured,
	}
	return enforceResponseBudget(result, responseBudget)
}

func enforceResponseBudget(result map[string]any, responseBudget int) (map[string]any, error) {
	if responseBudget <= 0 {
		responseBudget = defaultToolResponseBytes
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", err)
	}
	if len(data) > responseBudget {
		return nil, &responseTooLargeError{Actual: len(data), Limit: responseBudget}
	}
	return result, nil
}

func encodeStructuredValue(value any) ([]byte, map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode tool result: %w", err)
	}
	structured := structuredObject(data)
	canonicalizeNextActions(structured)
	data, err = json.Marshal(structured)
	if err != nil {
		return nil, nil, fmt.Errorf("encode canonical tool result: %w", err)
	}
	return data, structured, nil
}

// canonicalizeNextActions translates historical next_queries hints into structured,
// bounded action suggestions. They are advisory only: MCP never executes them
// automatically, and duplicates are removed to avoid client-side loops.
func canonicalizeNextActions(structured map[string]any) {
	items, ok := structured["next_queries"].([]any)
	if !ok {
		return
	}
	actions := make([]map[string]any, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		query, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tool, _ := query["tool"].(string)
		arguments := map[string]any{}
		if existing, ok := query["arguments"].(map[string]any); ok {
			for name, value := range existing {
				arguments[name] = value
			}
		}
		_, knownTool := findCanonicalTool(tool)
		definition, definitionFound := findCanonicalTool(tool)
		id, _ := query["id"].(string)
		mappedID := id == ""
		if id != "" {
			if tool == "ck3_diagnostics" && arguments["operation"] == "explain" {
				arguments["code"] = id
				mappedID = true
			} else if definitionFound {
				properties, _ := definition.InputSchema["properties"].(map[string]any)
				if _, acceptsID := properties["id"]; acceptsID {
					arguments["id"] = id
					mappedID = true
				} else if _, acceptsTarget := properties["target"]; acceptsTarget {
					arguments["target"] = id
					mappedID = true
				} else if _, acceptsQuery := properties["query"]; acceptsQuery {
					arguments["query"] = id
					mappedID = true
				}
			}
		}
		if historyYear, exists := arguments["history_year"]; exists {
			if _, hasYear := arguments["year"]; !hasYear {
				arguments["year"] = historyYear
			}
			delete(arguments, "history_year")
		}
		if !knownTool || !definitionFound || !mappedID {
			continue
		}
		data, err := json.Marshal(arguments)
		if err != nil {
			continue
		}
		// Actions are an API contract, not a loose hint. A historical
		// next_queries producer may only emit a next_actions item when its final
		// canonical argument object passes the actual registered tool schema.
		if err := validateArguments(data, definition.InputSchema, definition.CompatibilityProperties); err != nil {
			continue
		}
		key := tool + "\x00" + string(data)
		if seen[key] {
			continue
		}
		seen[key] = true
		action := map[string]any{
			"tool":       tool,
			"arguments":  arguments,
			"priority":   "normal",
			"confidence": "medium",
		}
		if reason, ok := query["reason"].(string); ok && strings.TrimSpace(reason) != "" {
			action["reason"] = reason
		}
		for _, name := range []string{"condition", "expected_result", "stop_if"} {
			if value, exists := query[name]; exists {
				action[name] = value
			}
		}
		actions = append(actions, action)
	}
	delete(structured, "next_queries")
	if len(actions) > 0 {
		structured["next_actions"] = actions
	}
}

func encodeToolError(err error, runtime *Runtime) map[string]any {
	typed := toolErrorFrom(err)
	message := sanitizeToolError(typed, runtime)
	field, _ := typed.Details["field"].(string)
	payload := map[string]any{
		"code":      typed.Code,
		"category":  typed.Category,
		"message":   message,
		"retryable": typed.Retryable,
		"field":     field,
		"details":   typed.Details,
		"recovery":  typed.Recovery,
	}
	data, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": message}},
		"structuredContent": structuredObject(data),
		"isError":           true,
	}
}

func encodeInternalToolError(code, message string) map[string]any {
	category := "index_state"
	retryable := true
	if code == ErrorInternal {
		category = "internal"
		retryable = false
	}
	return encodeToolError(newToolError(code, category, message, retryable, nil, nil), nil)
}

func structuredObject(data []byte) map[string]any {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err == nil && object != nil {
		return object
	}
	var value any
	_ = json.Unmarshal(data, &value)
	return map[string]any{"value": value}
}

func redactToolValue(value any, visibility string) any {
	if visibility != "public" {
		return value
	}
	switch result := value.(type) {
	case indexer.MapAssignmentPlanResult:
		result.PatchFiles = nil
		return result
	case *indexer.MapAssignmentPlanResult:
		copy := *result
		copy.PatchFiles = nil
		return &copy
	default:
		return value
	}
}

func mcpHealthReport(h indexer.HealthReport) map[string]any {
	wal := make([]map[string]any, 0, len(h.WALFiles))
	for _, f := range h.WALFiles {
		name := f.Name
		if name == "" {
			name = "db-sidecar"
			switch {
			case strings.HasSuffix(f.Path, "-wal"):
				name = "wal"
			case strings.HasSuffix(f.Path, "-shm"):
				name = "shm"
			}
		}
		item := map[string]any{"name": name, "exists": f.Exists}
		if f.SizeMB > 0 {
			item["size_mb"] = f.SizeMB
		}
		wal = append(wal, item)
	}
	result := map[string]any{
		"status":                 h.Status,
		"binary_version":         buildinfo.Version,
		"database_mb":            h.DatabaseMB,
		"database_version":       h.DatabaseVersion,
		"database_fingerprint":   h.DatabaseFingerprint,
		"authoritative_database": h.AuthoritativeDatabase,
		"schema_version":         h.SchemaVersion,
		"map_database":           h.MapDatabase,
		"tables":                 h.Tables,
		"index_rule_version":     h.IndexRuleVersion,
		"scan_generation":        h.ScanGeneration,
		"scan_revision":          h.ScanRevision,
		"scan_committed_at":      h.ScanCommittedAt,
		"scan_status":            h.ScanStatus,
		"missing_indexes":        h.MissingIndexes,
		"wal_files":              wal,
		"mcp_configured":         h.MCPConfigured,
		"mcp_serving":            true,
		"guidance":               h.Guidance,
	}
	if h.GIS != nil {
		result["gis"] = *h.GIS
	}
	return result
}
