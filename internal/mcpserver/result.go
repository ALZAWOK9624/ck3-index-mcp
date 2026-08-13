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

func encodeToolResultWithBudget(value any, visibility string, responseBudget int, trimmableFields ...string) (map[string]any, error) {
	value = redactToolValue(value, visibility)
	if rendered, ok := value.(indexer.MapTerrainEditResult); ok && len(rendered.PreviewPNG) > 0 {
		pngData := rendered.PreviewPNG
		rendered.PreviewPNG = nil
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
		return enforceResponseBudget(result, responseBudget, trimmableFields...)
	}
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
		return enforceResponseBudget(result, responseBudget, trimmableFields...)
	}
	if rendered, ok := value.(indexer.CoatOfArmsResult); ok && len(rendered.PNG) > 0 {
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
					"type": "image", "data": base64.StdEncoding.EncodeToString(pngData), "mimeType": "image/png",
					"annotations": map[string]any{"audience": []string{"user"}},
				},
			},
			"structuredContent": structured,
		}
		return enforceResponseBudget(result, responseBudget, trimmableFields...)
	}
	if rendered, ok := value.(indexer.MapRenderResult); ok {
		pngData := rendered.PNG
		hitData := rendered.HitPNG
		rendered.PNG = nil
		rendered.HitPNG = nil
		data, structured, err := encodeStructuredValue(rendered)
		if err != nil {
			return nil, err
		}
		content := []map[string]any{
			{"type": "text", "text": string(data)},
			{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(pngData),
				"mimeType": "image/png",
				"annotations": map[string]any{
					"audience": []string{"user"},
				},
			},
		}
		if len(hitData) > 0 {
			// The companion lookup plate is addressed to the assistant, not the
			// reader: it is an index of flat entity colours, not something to look
			// at. Without it a caller receives overlay.hit_map and every entity's
			// hit_color but has no plate to sample, so hover cannot be built.
			content = append(content, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(hitData),
				"mimeType": "image/png",
				"annotations": map[string]any{
					"audience": []string{"assistant"},
				},
			})
		}
		result := map[string]any{
			"content":           content,
			"structuredContent": structured,
		}
		return enforceResponseBudget(result, responseBudget, trimmableFields...)
	}
	data, structured, err := encodeStructuredValue(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(data)}},
		"structuredContent": structured,
	}
	return enforceResponseBudget(result, responseBudget, trimmableFields...)
}

func enforceResponseBudget(result map[string]any, responseBudget int, trimmableFields ...string) (map[string]any, error) {
	if responseBudget <= 0 {
		responseBudget = defaultToolResponseBytes
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", err)
	}
	if len(data) <= responseBudget {
		return result, nil
	}
	// Failing outright discarded a result the server had already paid to
	// compute and left the caller to guess a smaller limit. Evidence lists are
	// ordered by relevance, so dropping their tail keeps the answer that was
	// actually asked for and marks it truncated. A result carrying an image is
	// still refused: a PNG has no meaningful tail to drop.
	if trimmed, ok := trimResultToBudget(result, responseBudget, trimmableFields); ok {
		return trimmed, nil
	}
	return nil, &responseTooLargeError{Actual: len(data), Limit: responseBudget}
}

// trimResultToBudget repeatedly halves only relevance-ordered arrays the tool
// definition explicitly allowlisted. It reports false when no declared field
// can be shortened, preserving complete file, artifact, database, diagnostic
// contract, and action lists instead of silently returning a partial success.
func trimResultToBudget(result map[string]any, responseBudget int, trimmableFields []string) (map[string]any, bool) {
	if resultCarriesBinaryContent(result) {
		return nil, false
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return nil, false
	}
	trimmed := cloneStructured(structured)
	if trimmed == nil {
		return nil, false
	}
	for {
		name, longest := longestTrimmableArray(trimmed, trimmableFields)
		if name == "" {
			return nil, false
		}
		kept := len(longest) / 2
		trimmed[name] = longest[:kept]
		synchronizeTruncationMetadata(trimmed, name, len(longest), kept, trimmableFields)
		data, err := json.Marshal(trimmed)
		if err != nil {
			return nil, false
		}
		candidate := make(map[string]any, len(result))
		for key, value := range result {
			candidate[key] = value
		}
		candidate["content"] = []map[string]any{{"type": "text", "text": string(data)}}
		candidate["structuredContent"] = trimmed
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return nil, false
		}
		if len(encoded) <= responseBudget {
			return candidate, true
		}
	}
}

func resultCarriesBinaryContent(result map[string]any) bool {
	items, ok := result["content"].([]map[string]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if kind, _ := item["type"].(string); kind != "text" {
			return true
		}
	}
	return false
}

// longestTrimmableArray returns the largest explicitly allowed top-level array.
// Single-element arrays are left alone: halving them yields an empty list that
// tells the caller nothing it could not learn from truncated alone.
func longestTrimmableArray(structured map[string]any, allowed []string) (string, []any) {
	var name string
	var longest []any
	for _, key := range allowed {
		value := structured[key]
		items, ok := value.([]any)
		if !ok || len(items) < 2 {
			continue
		}
		if len(items) > len(longest) || (len(items) == len(longest) && key < name) {
			name, longest = key, items
		}
	}
	return name, longest
}

func synchronizeTruncationMetadata(structured map[string]any, field string, previous, kept int, trimmableFields []string) {
	structured["truncated"] = true
	truncation, _ := structured["truncation"].(map[string]any)
	if truncation == nil {
		truncation = map[string]any{}
		structured["truncation"] = truncation
	}
	entry, _ := truncation[field].(map[string]any)
	if entry == nil {
		entry = map[string]any{"original": previous}
		truncation[field] = entry
	}
	entry["returned"] = kept

	// Generic count/served/returned fields are safe to synchronize only when
	// this response has one relevance-ordered collection. With both evidence
	// and suggestions present, those names do not say which collection they
	// count; field-specific counts remain unambiguous in either case.
	presentCollections := 0
	for _, candidate := range trimmableFields {
		if items, ok := structured[candidate].([]any); ok && len(items) > 0 {
			presentCollections++
		}
	}
	if presentCollections == 1 {
		for _, name := range []string{"count", "served", "returned"} {
			if _, exists := structured[name]; exists {
				structured[name] = kept
			}
		}
	}
	fieldCount := field + "_count"
	if _, exists := structured[fieldCount]; exists {
		structured[fieldCount] = kept
	}

	// Response-budget trimming removes the tail of the page already returned;
	// it does not create a semantic next page. Advancing page+1 would skip the
	// omitted tail because query pagination starts after the original page
	// limit. Preserve has_more/next_page and only synchronize the returned count
	// on the pagination object that actually belongs to this field.
	paginationField := ""
	switch field {
	case "evidence":
		paginationField = "pagination"
	case "suggestions":
		paginationField = "suggestion_pagination"
	}
	if pagination, ok := structured[paginationField].(map[string]any); ok {
		pagination["returned"] = kept
	}
}

func cloneStructured(structured map[string]any) map[string]any {
	data, err := json.Marshal(structured)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil
	}
	return cloned
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
		if canonical, allowed := argumentAliases[tool]["history_year"]; allowed {
			if historyYear, exists := arguments["history_year"]; exists {
				if _, hasCanonical := arguments[canonical]; !hasCanonical {
					arguments[canonical] = historyYear
				}
				delete(arguments, "history_year")
			}
		}
		if !definitionFound || !mappedID {
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
		// Only what varies. priority and confidence were emitted here as the
		// fixed strings "normal" and "medium" on every action ever produced,
		// costing the caller bytes to be told nothing.
		action := map[string]any{
			"tool":      tool,
			"arguments": arguments,
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
	result := map[string]any{
		"content":           []map[string]any{{"type": "text", "text": message}},
		"structuredContent": structuredObject(data),
		"isError":           true,
	}
	if runtime != nil {
		result["database"] = runtime.databaseIdentity()
	}
	return result
}

func encodeInternalToolError(runtime *Runtime, code, message string) map[string]any {
	category := "index_state"
	retryable := true
	if code == ErrorInternal {
		category = "internal"
		retryable = false
	}
	return encodeToolError(newToolError(code, category, message, retryable, nil, nil), runtime)
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
		"status":                           h.Status,
		"depth":                            h.Depth,
		"binary_version":                   buildinfo.Version,
		"binary_revision":                  buildinfo.Revision,
		"database_mb":                      h.DatabaseMB,
		"database_version":                 h.DatabaseVersion,
		"database_fingerprint":             h.DatabaseFingerprint,
		"authoritative_database":           h.AuthoritativeDatabase,
		"schema_version":                   h.SchemaVersion,
		"map_database":                     h.MapDatabase,
		"tables":                           h.Tables,
		"index_rule_version":               h.IndexRuleVersion,
		"scan_generation":                  h.ScanGeneration,
		"scan_revision":                    h.ScanRevision,
		"scan_committed_at":                h.ScanCommittedAt,
		"scan_status":                      h.ScanStatus,
		"missing_indexes":                  h.MissingIndexes,
		"wal_files":                        wal,
		"mcp_configured":                   h.MCPConfigured,
		"mcp_serving":                      true,
		"sqlite_read_connections":          h.SQLiteReadConnections,
		"sqlite_cache_per_connection_mb":   h.SQLiteCachePerConnMB,
		"sqlite_cache_budget_mb":           h.SQLiteCacheBudgetMB,
		"sqlite_mmap_limit_mb":             h.SQLiteMMapLimitMB,
		"loaded_database_count":            h.LoadedDatabaseCount,
		"retired_database_count":           h.RetiredDatabaseCount,
		"aggregate_sqlite_cache_budget_mb": h.AggregateSQLiteCacheBudgetMB,
		"max_open_database_pools":          h.MaxOpenDatabasePools,
		"max_sqlite_cache_budget_mb":       h.MaxSQLiteCacheBudgetMB,
		"active_tasks":                     h.ActiveTasks,
		"active_expensive_tasks":           h.ActiveExpensiveTasks,
		"active_heavy_tasks":               h.ActiveHeavyTasks,
		"active_raster_tasks":              h.ActiveRasterTasks,
		"queued_tasks":                     h.QueuedTasks,
		"queued_expensive_tasks":           h.QueuedExpensiveTasks,
		"queued_heavy_tasks":               h.QueuedHeavyTasks,
		"queued_raster_tasks":              h.QueuedRasterTasks,
		"mcp_max_tasks":                    h.MCPMaxTasks,
		"mcp_max_heavy_tasks":              h.MCPMaxHeavyTasks,
		"mcp_max_raster_tasks":             h.MCPMaxRasterTasks,
		"mcp_max_queued_tasks":             h.MCPMaxQueuedTasks,
		"mcp_queue_timeout_seconds":        h.MCPQueueTimeoutSecs,
		"mcp_execution_timeout_seconds":    h.MCPExecutionTimeoutSecs,
		"sqlite_connections_reserved_for_ordinary_tasks": h.SQLiteOrdinaryReserve,
		"estimated_task_memory_mb":                       h.EstimatedTaskMemoryMB,
		"guidance":                                       h.Guidance,
	}
	// Health is served only under private visibility (see handleHealth), so the
	// resolved config path and source roots stay out of any public response.
	if h.ConfigPath != "" {
		result["config_path"] = h.ConfigPath
	}
	if len(h.Sources) > 0 {
		result["sources"] = h.Sources
	}
	if h.GIS != nil {
		result["gis"] = *h.GIS
	}
	return result
}
