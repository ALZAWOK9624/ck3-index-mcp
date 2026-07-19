package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ck3-index/internal/indexer"
)

type Runtime struct {
	DB     *indexer.DB
	Config indexer.Config
	DBPath string
}

type toolOutput struct {
	Value      any
	Visibility string
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func callMCPTool(ctx context.Context, db *indexer.DB, cfg indexer.Config, raw json.RawMessage) (any, error) {
	runtime := &Runtime{DB: db, Config: cfg}
	if path, err := indexer.ConfiguredDatabasePath(cfg); err == nil {
		runtime.DBPath = path
	}
	var call callToolParams
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, newProtocolError(rpcInvalidParams, "tools/call params must contain a tool name and JSON object arguments")
	}
	if strings.TrimSpace(call.Name) == "" {
		return nil, newProtocolError(rpcInvalidParams, "tools/call requires a non-empty name")
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}

	definition, canonical := findCanonicalTool(call.Name)
	deprecatedName := ""
	if canonical {
		call.Arguments = adaptRetainedCanonicalArguments(call.Name, call.Arguments)
	} else {
		alias, ok := findLegacyAlias(call.Name)
		if !ok {
			return nil, unknownToolError(call.Name)
		}
		var err error
		call.Arguments, err = adaptLegacyArguments(*alias, call.Arguments)
		if err != nil {
			return encodeToolError(sanitizeToolError(err, runtime)), nil
		}
		definition, _ = findCanonicalTool(alias.Canonical)
		deprecatedName = alias.Name
	}

	if err := validateArguments(call.Arguments, definition.InputSchema, definition.CompatibilityProperties); err != nil {
		return encodeToolError(sanitizeToolError(err, runtime)), nil
	}
	before, beforeErr := db.IndexState(ctx)
	if beforeErr == nil && indexStatePublishing(before) && !indexStateIndependentTool(definition.Name) {
		return encodeInternalToolError("INDEX_REFRESH_IN_PROGRESS", "ck3-index is rebuilding or finalizing a new scan generation; retry this query after the index reports ready."), nil
	}
	output, err := definition.Handler(ctx, runtime, definition, call.Arguments)
	if err != nil {
		return encodeToolError(sanitizeToolError(err, runtime)), nil
	}
	after, afterErr := db.IndexState(ctx)
	if beforeErr == nil && afterErr == nil && indexStateChanged(before, after) {
		if indexStatePublishing(after) && !indexStateIndependentTool(definition.Name) {
			return encodeInternalToolError("INDEX_REFRESH_IN_PROGRESS", "ck3-index began publishing a new scan generation while this query was running; retry after the index reports ready."), nil
		}
		if !definition.Annotations.ReadOnlyHint {
			// Artifact tools can be non-idempotent (migration artifacts use a
			// random id), so never execute them twice behind the caller's back.
			return encodeInternalToolError("INDEX_CHANGED_DURING_QUERY", "The ck3-index scan generation changed while the artifact tool was running; retry the tool call."), nil
		}
		// Index reads are generation-bound; artifact-only tools never mutate the
		// database. Retry read-only tools once when a scan committed during the
		// request so one response never mixes two index generations.
		retryStart := after
		output, err = definition.Handler(ctx, runtime, definition, call.Arguments)
		if err != nil {
			return encodeToolError(sanitizeToolError(err, runtime)), nil
		}
		after, afterErr = db.IndexState(ctx)
		if afterErr != nil {
			return encodeInternalToolError("INDEX_STATE_UNAVAILABLE", "ck3-index could not verify the scan generation after retrying the query."), nil
		}
		if indexStatePublishing(after) && !indexStateIndependentTool(definition.Name) {
			return encodeInternalToolError("INDEX_REFRESH_IN_PROGRESS", "ck3-index is still finalizing the refreshed generation; retry this query shortly."), nil
		}
		if indexStateChanged(retryStart, after) {
			return encodeInternalToolError("INDEX_CHANGED_DURING_QUERY", "The ck3-index scan generation changed twice during one query; retry the tool call."), nil
		}
	}
	result, err := encodeToolResult(output.Value, output.Visibility)
	if err != nil {
		return encodeInternalToolError("TOOL_RESULT_ENCODING_FAILED", "ck3-index could not encode the tool result as finite JSON."), nil
	}
	if beforeErr == nil && afterErr == nil && after.Ready() {
		result["indexState"] = map[string]any{
			"scan_generation":   after.Generation,
			"scan_revision":     after.Revision,
			"scan_committed_at": after.CommittedAt,
			"scan_status":       after.Status,
		}
	} else {
		result["indexState"] = map[string]any{
			"status":     "unavailable",
			"error_code": "INDEX_STATE_UNAVAILABLE",
			"guidance":   "The tool result was produced, but ck3-index could not verify one stable scan generation.",
		}
	}
	if deprecatedName != "" {
		result["_meta"] = map[string]any{
			"deprecated_tool": deprecatedName,
			"replacement":     definition.Name,
		}
	}
	return result, nil
}

func indexStateChanged(before, after indexer.IndexState) bool {
	return before.Generation != after.Generation || before.Revision != after.Revision || before.Status != after.Status
}

func indexStatePublishing(state indexer.IndexState) bool {
	return state.Status == "finalizing"
}

// These operations can return useful static or health information even before
// the first scan. All index-backed tools are held until the published state is
// ready, which prevents the finalizing window from leaking partial rows. An
// initializing/absent cache is left to each tool's existing unavailable-index
// behavior; it has no previously published rows to accidentally expose.
func indexStateIndependentTool(name string) bool {
	switch name {
	case "ck3_script_reference", "ck3_health":
		return true
	default:
		return false
	}
}

func adaptRetainedCanonicalArguments(name string, raw json.RawMessage) json.RawMessage {
	if name != "map_assignment_plan" && name != "map_building_candidates" {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	if _, hasTarget := fields["target"]; !hasTarget {
		if id, hasID := fields["id"]; hasID {
			fields["target"] = id
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return data
}

func adaptLegacyArguments(alias LegacyAlias, raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("legacy tool %s arguments must be a JSON object", alias.Name)
	}
	if fields == nil {
		return nil, fmt.Errorf("legacy tool %s arguments must be a JSON object", alias.Name)
	}
	if alias.Operation != "" {
		fields["operation"] = mustJSON(alias.Operation)
	}
	if alias.Kind != "" {
		fields["kind"] = mustJSON(alias.Kind)
	}
	if alias.Name == "explain_diagnostic" {
		if value, ok := fields["id"]; ok {
			fields["code"] = value
			delete(fields, "id")
		}
	}
	canonical, ok := findCanonicalTool(alias.Canonical)
	if !ok {
		return nil, fmt.Errorf("legacy tool %s has no canonical target", alias.Name)
	}
	properties, _ := canonical.InputSchema["properties"].(map[string]any)
	compatibility := make(map[string]bool, len(canonical.CompatibilityProperties))
	for _, name := range canonical.CompatibilityProperties {
		compatibility[name] = true
	}
	for name := range fields {
		if _, known := properties[name]; !known && !compatibility[name] {
			delete(fields, name)
		}
	}
	return json.Marshal(fields)
}

func mustJSON(value string) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func sanitizeToolError(err error, runtime *Runtime) string {
	message := err.Error()
	paths := []string{runtime.DBPath, runtime.Config.ConfigPath, runtime.Config.ArtifactRoot, runtime.Config.MigrationSnapshotRoot, os.Getenv("CK3_INDEX_MAP_FONT")}
	for _, source := range runtime.Config.Sources {
		paths = append(paths, source.Path)
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		for _, variant := range []string{path, filepath.ToSlash(path), filepath.FromSlash(path)} {
			message = replaceAllCaseInsensitive(message, variant, "<redacted-path>")
		}
	}
	return message
}

func replaceAllCaseInsensitive(value, old, replacement string) string {
	if old == "" {
		return value
	}
	return regexp.MustCompile(`(?i)`+regexp.QuoteMeta(old)).ReplaceAllString(value, replacement)
}
