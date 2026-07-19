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
	value = redactToolValue(value, visibility)
	if rendered, ok := value.(indexer.GUIQueryResult); ok && rendered.Preview != nil && len(rendered.Preview.PNG) > 0 {
		pngData := rendered.Preview.PNG
		rendered.Preview.PNG = nil
		data, structured, err := encodeStructuredValue(rendered)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(data)},
				{
					"type": "image", "data": base64.StdEncoding.EncodeToString(pngData), "mimeType": "image/png",
					"annotations": map[string]any{"audience": []string{"user"}},
				},
			},
			"structuredContent": structured,
		}, nil
	}
	if rendered, ok := value.(indexer.MapRenderResult); ok {
		pngData := rendered.PNG
		rendered.PNG = nil
		data, structured, err := encodeStructuredValue(rendered)
		if err != nil {
			return nil, err
		}
		return map[string]any{
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
		}, nil
	}
	data, structured, err := encodeStructuredValue(value)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(data)}},
		"structuredContent": structured,
	}, nil
}

func encodeStructuredValue(value any) ([]byte, map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode tool result: %w", err)
	}
	structured := structuredObject(data)
	canonicalizeNextQueries(structured)
	data, err = json.Marshal(structured)
	if err != nil {
		return nil, nil, fmt.Errorf("encode canonical tool result: %w", err)
	}
	return data, structured, nil
}

func canonicalizeNextQueries(structured map[string]any) {
	items, ok := structured["next_queries"].([]any)
	if !ok {
		return
	}
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
		knownTool := false
		if alias, ok := findLegacyAlias(tool); ok {
			query["tool"] = alias.Canonical
			tool = alias.Canonical
			knownTool = true
			if alias.Operation != "" {
				arguments["operation"] = alias.Operation
			}
			if alias.Kind != "" {
				arguments["kind"] = alias.Kind
			}
		} else if _, ok := findCanonicalTool(tool); ok {
			knownTool = true
		}
		id, _ := query["id"].(string)
		mappedID := id == ""
		if id != "" {
			if tool == "ck3_diagnostics" && arguments["operation"] == "explain" {
				arguments["code"] = id
				mappedID = true
			} else if definition, ok := findCanonicalTool(tool); ok {
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
		if len(arguments) > 0 {
			query["arguments"] = arguments
		}
		if historyYear, exists := arguments["history_year"]; exists {
			if _, hasYear := arguments["year"]; !hasYear {
				arguments["year"] = historyYear
			}
			delete(arguments, "history_year")
			query["arguments"] = arguments
		}
		if knownTool && mappedID {
			delete(query, "id")
		}
	}
}

func encodeToolError(message string) map[string]any {
	payload := map[string]any{"error": message}
	data, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": message}},
		"structuredContent": structuredObject(data),
		"isError":           true,
	}
}

func encodeInternalToolError(code, message string) map[string]any {
	payload := map[string]any{"error": message, "code": code}
	data, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": message}},
		"structuredContent": structuredObject(data),
		"isError":           true,
	}
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
