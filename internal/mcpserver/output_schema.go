package mcpserver

func nullableObjectSchema(schema map[string]any) map[string]any {
	return map[string]any{"oneOf": []any{schema, map[string]any{"type": "null"}}}
}

func preciseToolOutputSchema(successSchemas ...map[string]any) map[string]any {
	alternatives := make([]any, 0, len(successSchemas)+1)
	for _, schema := range successSchemas {
		alternatives = append(alternatives, schema)
	}
	alternatives = append(alternatives, toolErrorOutputSchema())
	return map[string]any{
		"type":  "object",
		"oneOf": alternatives,
	}
}

func toolErrorOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"code":      stringProperty("Stable machine-readable error code."),
		"category":  stringProperty("Error category."),
		"message":   stringProperty("Path-redacted error summary."),
		"retryable": booleanProperty("Whether retrying after recovery may succeed."),
		"field":     stringProperty("Invalid argument field, when applicable."),
		"details": nullableObjectSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}),
		"recovery": nullableObjectSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}),
	}, "code", "category", "message", "retryable", "field", "details", "recovery")
}

func llmResultOutputSchema() map[string]any {
	evidence := llmEvidenceOutputSchema()
	stringArray := arrayProperty("", map[string]any{"type": "string"})
	integerMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}}
	topologyNode := objectSchema(map[string]any{
		"id":         map[string]any{"type": "string"},
		"type":       map[string]any{"type": "string"},
		"name":       map[string]any{"type": "string"},
		"defined":    map[string]any{"type": "boolean"},
		"distance":   map[string]any{"type": "integer", "minimum": 0},
		"in_degree":  map[string]any{"type": "integer", "minimum": 0},
		"out_degree": map[string]any{"type": "integer", "minimum": 0},
		"event_type": map[string]any{"type": "string"},
		"title":      map[string]any{"type": "string"},
		"source":     map[string]any{"type": "string"},
		"path":       map[string]any{"type": "string"},
		"line":       map[string]any{"type": "integer", "minimum": 0},
	}, "id", "type", "name", "defined", "distance", "in_degree", "out_degree")
	topologyEdge := objectSchema(map[string]any{
		"from":       map[string]any{"type": "string"},
		"to":         map[string]any{"type": "string"},
		"relation":   map[string]any{"type": "string"},
		"phase":      map[string]any{"type": "string"},
		"confidence": map[string]any{"type": "string"},
		"resolution": map[string]any{"type": "string"},
		"reason":     map[string]any{"type": "string"},
		"source":     map[string]any{"type": "string"},
		"path":       map[string]any{"type": "string"},
		"line":       map[string]any{"type": "integer", "minimum": 0},
		"column":     map[string]any{"type": "integer", "minimum": 0},
	}, "from", "to", "resolution")
	topologyPath := objectSchema(map[string]any{
		"to":    map[string]any{"type": "string"},
		"nodes": stringArray,
	}, "to", "nodes")
	return objectSchema(map[string]any{
		"query":   stringProperty("Normalized query."),
		"intent":  stringProperty("Canonical result intent."),
		"summary": stringProperty("Bounded summary."),
		"counts":  integerMap,
		"hotspots": map[string]any{
			"type": "object",
			"additionalProperties": map[string]any{
				"type":  "array",
				"items": evidence,
			},
		},
		"guidance":          stringArray,
		"evidence":          arrayProperty("", evidence),
		"redacted":          map[string]any{"type": "integer", "minimum": 0},
		"needs_refresh":     map[string]any{"type": "boolean"},
		"needs_scan":        map[string]any{"type": "boolean"},
		"impact":            integerMap,
		"missing_loc_keys":  stringArray,
		"missing_resources": stringArray,
		"scope_fix_hints":   stringArray,
		"topology": nullableObjectSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"center":             map[string]any{"type": "string"},
				"direction":          map[string]any{"type": "string"},
				"include_on_actions": map[string]any{"type": "boolean"},
				"max_depth":          map[string]any{"type": "integer"},
				"nodes":              arrayProperty("", topologyNode),
				"edges":              arrayProperty("", topologyEdge),
				"roots":              stringArray,
				"leaves":             stringArray,
				"cycles":             arrayProperty("", stringArray),
				"paths_from_center":  arrayProperty("", topologyPath),
				"truncated":          map[string]any{"type": "boolean"},
			},
			"required": []string{"center", "direction", "include_on_actions", "max_depth", "nodes", "edges"},
		}),
		"truncated": map[string]any{"type": "boolean"},
		"pagination": nullableObjectSchema(objectSchema(map[string]any{
			"page":      map[string]any{"type": "integer", "minimum": 1},
			"limit":     map[string]any{"type": "integer", "minimum": 1},
			"returned":  map[string]any{"type": "integer", "minimum": 0},
			"has_more":  map[string]any{"type": "boolean"},
			"next_page": map[string]any{"type": "integer", "minimum": 1},
		}, "page", "limit", "returned", "has_more")),
		"next_actions": arrayProperty("", objectSchema(map[string]any{
			"tool":            map[string]any{"type": "string"},
			"arguments":       map[string]any{"type": "object", "additionalProperties": true},
			"reason":          map[string]any{"type": "string"},
			"priority":        map[string]any{"type": "string"},
			"confidence":      map[string]any{"type": "string"},
			"condition":       map[string]any{},
			"expected_result": map[string]any{},
			"stop_if":         map[string]any{},
		}, "tool", "arguments", "priority", "confidence")),
	}, "intent", "summary")
}

func llmEvidenceOutputSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"kind", "type", "name", "source", "path", "detail", "edge_type", "snippet", "suggestion", "rule_source"} {
		properties[field] = map[string]any{"type": "string"}
	}
	properties["line"] = map[string]any{"type": "integer", "minimum": 0}
	properties["column"] = map[string]any{"type": "integer", "minimum": 0}
	return objectSchema(properties, "kind")
}

func refreshOutputSchema() map[string]any {
	indexState := objectSchema(map[string]any{
		"scan_generation":   map[string]any{"type": "integer", "minimum": 0},
		"scan_revision":     map[string]any{"type": "string"},
		"scan_committed_at": map[string]any{"type": "string"},
		"scan_status":       map[string]any{"type": "string"},
	}, "scan_generation")
	project := objectSchema(map[string]any{
		"configured":  map[string]any{"type": "boolean"},
		"accessible":  map[string]any{"type": "boolean"},
		"writable":    map[string]any{"type": "boolean"},
		"refreshable": map[string]any{"type": "boolean"},
		"private":     map[string]any{"type": "boolean"},
	}, "configured", "accessible", "writable", "refreshable", "private")
	engine := objectSchema(map[string]any{
		"available": map[string]any{"type": "boolean"},
		"current":   map[string]any{"type": "boolean"},
	}, "available", "current")
	scanError := objectSchema(map[string]any{
		"code": map[string]any{"type": "string"},
		"at":   map[string]any{"type": "string"},
	}, "code", "at")
	refreshStatus := objectSchema(map[string]any{
		"status":              map[string]any{"type": "string"},
		"index":               indexState,
		"project":             project,
		"engine_rules":        engine,
		"last_scan_error":     scanError,
		"needs_full_scan":     map[string]any{"type": "boolean"},
		"full_scan_available": map[string]any{"type": "boolean"},
		"full_scan_guidance":  map[string]any{"type": "string"},
	}, "status", "index", "project", "engine_rules", "needs_full_scan", "full_scan_available")
	delta := objectSchema(map[string]any{
		"added":     map[string]any{"type": "integer", "minimum": 0},
		"resolved":  map[string]any{"type": "integer", "minimum": 0},
		"remaining": map[string]any{"type": "integer", "minimum": 0},
	}, "added", "resolved", "remaining")
	stats := scanStatsOutputSchema(delta)
	success := objectSchema(map[string]any{
		"operation":       stringProperty("Refresh operation.", "status", "files", "full"),
		"refresh_status":  refreshStatus,
		"is_scanning":     map[string]any{"type": "boolean"},
		"status":          map[string]any{"type": "string"},
		"index":           indexState,
		"scan_generation": map[string]any{"type": "integer", "minimum": 0},
		"needs_full_scan": map[string]any{"type": "boolean"},
		"last_error": objectSchema(map[string]any{
			"code":    map[string]any{"type": "string"},
			"message": map[string]any{"type": "string"},
		}, "code", "message"),
		"refresh":       stats,
		"changed_files": map[string]any{"type": "integer", "minimum": 0},
		"removed_files": map[string]any{"type": "integer", "minimum": 0},
		"missing_files": arrayProperty("", map[string]any{"type": "string"}),
		"path_outcomes": arrayProperty("", objectSchema(map[string]any{
			"path":   map[string]any{"type": "string"},
			"status": map[string]any{"type": "string"},
		}, "path", "status")),
		"changed_symbols":           arrayProperty("", map[string]any{"type": "string"}),
		"changed_symbols_truncated": map[string]any{"type": "boolean"},
		"diagnostic_delta":          nullableObjectSchema(delta),
	}, "operation", "refresh_status", "is_scanning", "status", "index", "scan_generation", "needs_full_scan")
	return preciseToolOutputSchema(success)
}

func scanStatsOutputSchema(delta map[string]any) map[string]any {
	integer := map[string]any{"type": "integer", "minimum": 0}
	properties := map[string]any{
		"database":                  map[string]any{"type": "string"},
		"no_op":                     map[string]any{"type": "boolean"},
		"timings_ms":                map[string]any{"type": "object", "additionalProperties": integer},
		"by_source":                 map[string]any{"type": "object", "additionalProperties": integer},
		"wal_checkpoint":            nullableObjectSchema(objectSchema(map[string]any{"mode": map[string]any{"type": "string"}, "busy": integer, "log_frames": integer, "checkpointed_frames": integer}, "mode", "busy", "log_frames", "checkpointed_frames")),
		"missing_files":             arrayProperty("", map[string]any{"type": "string"}),
		"path_outcomes":             arrayProperty("", objectSchema(map[string]any{"path": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}}, "path", "status")),
		"changed_symbols":           arrayProperty("", map[string]any{"type": "string"}),
		"changed_symbols_truncated": map[string]any{"type": "boolean"},
		"diagnostic_delta":          nullableObjectSchema(delta),
	}
	for _, field := range []string{"files", "nodes", "objects", "references", "localization", "resources", "schema_fields", "object_fields", "diagnostics", "overridden", "files_read", "files_hashed", "files_parsed", "bytes_read", "bytes_hashed", "peak_queued_results", "elapsed_ms", "changed_files", "removed_files"} {
		properties[field] = integer
	}
	return objectSchema(properties, "database", "files", "nodes", "objects", "references", "localization", "resources", "schema_fields", "object_fields", "diagnostics", "overridden", "files_read", "files_hashed", "files_parsed", "bytes_read", "bytes_hashed", "peak_queued_results", "elapsed_ms", "by_source")
}

func workspaceOutputSchema() map[string]any {
	mode := objectSchema(map[string]any{
		"id":        map[string]any{"type": "string"},
		"available": map[string]any{"type": "boolean"},
		"reason":    map[string]any{"type": "string"},
	}, "id", "available")
	capability := objectSchema(map[string]any{
		"id":                        map[string]any{"type": "string"},
		"domain":                    map[string]any{"type": "string"},
		"source":                    map[string]any{"type": "string"},
		"tools":                     arrayProperty("", map[string]any{"type": "string"}),
		"modes":                     arrayProperty("", map[string]any{"type": "string"}),
		"inputs":                    arrayProperty("", map[string]any{"type": "string"}),
		"outputs":                   arrayProperty("", map[string]any{"type": "string"}),
		"maturity":                  map[string]any{"type": "string"},
		"requires_ready_index":      map[string]any{"type": "boolean"},
		"requires_runtime_logs":     map[string]any{"type": "boolean"},
		"requires_external_process": map[string]any{"type": "boolean"},
		"side_effect":               map[string]any{"type": "string"},
		"cost":                      map[string]any{"type": "string"},
		"supports_cancellation":     map[string]any{"type": "boolean"},
		"profile":                   map[string]any{"type": "string"},
		"available":                 map[string]any{"type": "boolean"},
		"reason":                    map[string]any{"type": "string"},
		"mode_details":              arrayProperty("", mode),
	}, "id", "domain", "source", "tools", "inputs", "outputs", "maturity", "requires_ready_index", "requires_runtime_logs", "requires_external_process", "side_effect", "cost", "supports_cancellation", "profile", "available")
	capabilities := objectSchema(map[string]any{
		"operation":    map[string]any{"type": "string", "enum": []string{"capabilities"}},
		"domain":       map[string]any{"type": "string"},
		"index_status": map[string]any{"type": "string"},
		"capabilities": arrayProperty("", capability),
	}, "operation", "domain", "index_status", "capabilities")
	onActionAudit := objectSchema(map[string]any{
		"intent":                           map[string]any{"type": "string"},
		"status":                           map[string]any{"type": "string"},
		"index_status":                     map[string]any{"type": "string"},
		"engine_evidence_available":        map[string]any{"type": "boolean"},
		"documentation_evidence_available": map[string]any{"type": "boolean"},
		"documentation_evidence_status":    map[string]any{"type": "string"},
		"snapshot_source_version":          map[string]any{"type": "string"},
		"hook_count":                       map[string]any{"type": "integer", "minimum": 0},
		"findings":                         arrayProperty("", map[string]any{"type": "object", "additionalProperties": true}),
		"truncated":                        map[string]any{"type": "boolean"},
		"guidance":                         arrayProperty("", map[string]any{"type": "string"}),
	}, "intent", "status", "index_status", "engine_evidence_available", "documentation_evidence_available", "documentation_evidence_status", "snapshot_source_version", "hook_count", "guidance")
	return preciseToolOutputSchema(llmResultOutputSchema(), capabilities, onActionAudit)
}
