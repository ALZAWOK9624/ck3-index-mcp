package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ck3-index/internal/indexer"
)

type Runtime struct {
	DB                 *indexer.DB
	Config             indexer.Config
	DBPath             string
	DatabaseName       string
	DatabaseEpoch      uint64
	DatabaseController mcpDatabaseController
	ownedDB            *indexer.DB
	releaseRebound     func()
}

type toolOutput struct {
	Value      any
	Visibility string
	// Committed means the handler has already made an atomic, externally
	// visible publication. A cancellation that arrives after that commit must
	// not be reported as if the old generation had been retained.
	Committed bool
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func callMCPTool(ctx context.Context, db *indexer.DB, cfg indexer.Config, raw json.RawMessage) (any, error) {
	runtime := &Runtime{DB: db, Config: cfg}
	defer func() {
		if runtime.ownedDB != nil {
			_ = runtime.ownedDB.Close()
		}
		if runtime.releaseRebound != nil {
			runtime.releaseRebound()
		}
	}()
	if path, err := indexer.ConfiguredDatabasePath(cfg); err == nil {
		runtime.DBPath = path
	}
	if databaseContext, ok := mcpDatabaseContextFrom(ctx); ok {
		runtime.DatabaseController = databaseContext.Controller
		runtime.DatabaseName = databaseContext.Identity.Name
		runtime.DatabaseEpoch = databaseContext.Identity.Epoch
		if databaseContext.Identity.databasePath != "" {
			runtime.DBPath = databaseContext.Identity.databasePath
		}
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
	if !canonical {
		return nil, unknownToolError(call.Name)
	}
	call.Arguments = adaptRetainedCanonicalArguments(call.Name, call.Arguments)
	repairedArguments, argumentNotices := repairToolArguments(definition.Name, definition.InputSchema, call.Arguments)
	call.Arguments = repairedArguments

	if err := validateArguments(call.Arguments, definition.InputSchema, definition.CompatibilityProperties); err != nil {
		return encodeToolError(err, runtime), nil
	}
	handlerArguments, responseControl, err := splitResponseControl(call.Arguments)
	if err != nil {
		return encodeToolError(err, runtime), nil
	}
	if err := ctx.Err(); err != nil {
		return encodeToolError(err, runtime), nil
	}
	if err := ensurePublishedDatabaseBinding(ctx, runtime); err != nil {
		return encodeToolError(err, runtime), nil
	}
	before, beforeErr := runtime.DB.IndexState(ctx)
	// cacheStart is the published state whose rows the current handler
	// invocation reads. A generation-bound retry resets it below; an initial
	// state-read failure remains disqualifying instead of letting a later
	// successful read cache an answer with unknown provenance.
	cacheStart, cacheStartErr := before, beforeErr
	if beforeErr == nil && indexStatePublishing(before) && !indexStateIndependentRequest(definition.Name, handlerArguments) {
		return encodeInternalToolError(runtime, ErrorIndexFinalizing, "ck3-index is rebuilding or finalizing a new scan generation; retry this query after the index reports ready."), nil
	}
	// A deliberately invalidated index is worse than an unavailable one: its
	// rows still look complete, but they describe a project tree that has since
	// been replaced. Refuse every index-backed tool until a full refresh
	// republishes the cache, rather than answering from the old generation.
	if beforeErr == nil && before.Status == indexer.IndexStatusStale && !indexStateIndependentRequest(definition.Name, handlerArguments) {
		return encodeToolError(newToolError(ErrorIndexStale, "index_state",
			"the ck3-index cache was explicitly invalidated and no longer describes the project on disk", false,
			map[string]any{"scan_status": before.Status, "reason": before.StaleReason, "required_action": before.RequiredAction},
			map[string]any{"operation": "full", "guidance": "Run ck3_refresh with operation=full to rebuild the index before using any index-backed tool."}), runtime), nil
	}
	// In-process result cache for pure index reads. The key and the hit check
	// both carry the full published identity, not just the generation: a clean
	// reset rebuilds meta and can restart numbering at 1, so generation alone
	// cannot tell "unchanged" from "replaced by a different database that
	// happens to be on generation 1 again".
	if definition.Annotations.ReadOnlyHint && cacheableReadRequest(definition.Name, handlerArguments) && beforeErr == nil && before.Ready() && before.Revision != "" {
		key := toolCacheKey(definition.Name, runtime.DBPath, runtime.DatabaseEpoch, before.Generation, before.Revision, call.Arguments)
		if cached, ok := mcpReadToolCache.get(key); ok {
			afterState, stateErr := runtime.DB.IndexState(ctx)
			if stateErr == nil && afterState.Ready() && !indexStateChanged(before, afterState) && !indexStatePublishing(afterState) {
				var payload map[string]any
				if err := json.Unmarshal(cached, &payload); err == nil {
					finalized, finalizeErr := finalizeToolResult(payload, runtime, definition, argumentNotices,
						afterState, true, responseControl.MaxResponseBytes)
					if finalizeErr == nil {
						return finalized, nil
					}
				}
			}
			// The published state moved between the key and the hit; fall
			// through and execute normally.
		}
	}
	output, err := definition.Handler(ctx, runtime, definition, handlerArguments)
	if err != nil {
		return encodeToolError(err, runtime), nil
	}
	if output.Committed {
		markMCPCommitted(ctx)
	}
	if err := ctx.Err(); err != nil && !output.Committed {
		return encodeToolError(err, runtime), nil
	}
	resultContext := ctx
	if output.Committed {
		resultContext = context.WithoutCancel(ctx)
	}
	after, afterErr := runtime.DB.IndexState(resultContext)
	if beforeErr == nil && afterErr == nil && indexStateChanged(before, after) {
		// Refresh owns its one intentional generation change. Re-running it
		// would either duplicate work or produce a second generation, so return
		// its first transactional result directly.
		if definition.Name != "ck3_refresh" {
			if indexStatePublishing(after) && !indexStateIndependentRequest(definition.Name, handlerArguments) {
				return encodeInternalToolError(runtime, ErrorIndexFinalizing, "ck3-index began publishing a new scan generation while this query was running; retry after the index reports ready."), nil
			}
			if !definition.Annotations.ReadOnlyHint {
				// Artifact tools can be non-idempotent (migration artifacts use a
				// random id), so never execute them twice behind the caller's back.
				return encodeInternalToolError(runtime, ErrorConflictingGeneration, "The ck3-index scan generation changed while the artifact tool was running; retry the tool call."), nil
			}
			// Index reads are generation-bound; artifact-only tools never mutate the
			// database. Retry read-only tools once when a scan committed during the
			// request so one response never mixes two index generations.
			retryStart := after
			cacheStart, cacheStartErr = retryStart, nil
			output, err = definition.Handler(ctx, runtime, definition, handlerArguments)
			if err != nil {
				return encodeToolError(err, runtime), nil
			}
			if err := ctx.Err(); err != nil {
				return encodeToolError(err, runtime), nil
			}
			after, afterErr = runtime.DB.IndexState(resultContext)
			if afterErr != nil {
				return encodeInternalToolError(runtime, ErrorIndexStale, "ck3-index could not verify the scan generation after retrying the query."), nil
			}
			if indexStatePublishing(after) && !indexStateIndependentRequest(definition.Name, handlerArguments) {
				return encodeInternalToolError(runtime, ErrorIndexFinalizing, "ck3-index is still finalizing the refreshed generation; retry this query shortly."), nil
			}
			if indexStateChanged(retryStart, after) {
				return encodeInternalToolError(runtime, ErrorConflictingGeneration, "The ck3-index scan generation changed twice during one query; retry the tool call."), nil
			}
		}
	}
	result, err := encodeToolResultWithBudget(output.Value, output.Visibility, responseControl.MaxResponseBytes, definition.TrimmableFields...)
	if err != nil {
		return encodeToolError(err, runtime), nil
	}
	// Snapshot the handler payload before the per-call envelope goes on. The
	// envelope carries this caller's argument notices and this moment's index
	// state; caching it would hand a later caller a repair notice for arguments
	// it never sent, and hide the notice from the caller that earned it.
	var cachePayload []byte
	cacheEligible := definition.Annotations.ReadOnlyHint &&
		cacheableReadRequest(definition.Name, handlerArguments) &&
		cacheablePublishedTransition(cacheStart, cacheStartErr, after, afterErr)
	if cacheEligible {
		if data, marshalErr := json.Marshal(result); marshalErr == nil {
			cachePayload = data
		}
	}
	boundedResult, err := finalizeToolResult(result, runtime, definition, argumentNotices,
		after, beforeErr == nil && afterErr == nil && after.Ready(), responseControl.MaxResponseBytes)
	if err != nil {
		return encodeToolError(err, runtime), nil
	}
	if cachePayload != nil {
		mcpReadToolCache.put(toolCacheKey(definition.Name, runtime.DBPath, runtime.DatabaseEpoch, after.Generation, after.Revision, call.Arguments), cachePayload)
	}
	return boundedResult, nil
}

// finalizeToolResult attaches everything that belongs to this call rather than
// to the handler's payload: the caller's argument notices, the database
// identity, the index state the answer was produced under, and the caller's
// response budget. A cache hit runs the same steps over the stored payload, so
// a reused answer never carries another call's envelope.
func finalizeToolResult(result map[string]any, runtime *Runtime, definition *ToolDefinition, argumentNotices []string,
	state indexer.IndexState, stateVerified bool, maxResponseBytes int) (map[string]any, error) {
	result = attachArgumentNotices(result, argumentNotices)
	identity := runtime.databaseIdentity()
	if definition.Name == "ck3_database" && runtime.DatabaseController != nil {
		identity = runtime.DatabaseController.Current()
	}
	result["database"] = identity
	if definition.Name == "ck3_database" {
		// A switch is executed through a lease on the previous database. Its
		// handler already returns the new target's health/generation, so attaching
		// the old lease's index state here would make one response contradict itself.
	} else if stateVerified {
		result["indexState"] = map[string]any{
			"scan_generation":   state.Generation,
			"scan_revision":     state.Revision,
			"scan_committed_at": state.CommittedAt,
			"scan_status":       state.Status,
		}
	} else {
		result["indexState"] = map[string]any{
			"status":     "unavailable",
			"error_code": ErrorIndexStale,
			"guidance":   "The tool result was produced, but ck3-index could not verify one stable scan generation.",
		}
	}
	// encodeToolResultWithBudget bounds the payload generated by the handler.
	// Index-state metadata is attached afterwards, so verify
	// the final wire object as well rather than letting those common envelopes
	// silently exceed the caller's declared response budget.
	return enforceResponseBudget(result, maxResponseBytes, definition.TrimmableFields...)
}

func indexStateChanged(before, after indexer.IndexState) bool {
	return before.Generation != after.Generation || before.Revision != after.Revision || before.Status != after.Status
}

func cacheablePublishedTransition(before indexer.IndexState, beforeErr error, after indexer.IndexState, afterErr error) bool {
	return beforeErr == nil && afterErr == nil &&
		before.Ready() && after.Ready() &&
		before.Revision != "" && after.Revision != "" &&
		!indexStateChanged(before, after)
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
	// ck3_save reads an external file and never consults the index, so a
	// stale or missing index is no reason to refuse it.
	case "ck3_script_reference", "ck3_health", "ck3_refresh", "ck3_save", "ck3_database":
		return true
	default:
		return false
	}
}

func (runtime *Runtime) databaseIdentity() mcpDatabaseIdentity {
	if runtime == nil {
		return mcpDatabaseIdentity{}
	}
	name := strings.ToLower(strings.TrimSpace(runtime.DatabaseName))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(runtime.Config.MCPDatabaseName))
	}
	if name == "" {
		name = "default"
	}
	epoch := runtime.DatabaseEpoch
	if epoch == 0 {
		epoch = 1
	}
	return mcpDatabaseIdentity{
		Name: name, Epoch: epoch, DatabaseIdentity: redactedMCPPath(runtime.DBPath),
		ConfigIdentity: indexer.DisplayConfigPath(runtime.Config),
		databasePath:   runtime.DBPath,
	}
}

func indexStateIndependentRequest(name string, raw json.RawMessage) bool {
	if indexStateIndependentTool(name) {
		return true
	}
	if name != "ck3_workspace" {
		return false
	}
	var args struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args.Operation), "capabilities")
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

func sanitizeToolError(err error, runtime *Runtime) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if runtime == nil {
		return message
	}
	paths := []string{runtime.DBPath, runtime.Config.ConfigPath, runtime.Config.ArtifactRoot, runtime.Config.MigrationSnapshotRoot, os.Getenv("CK3_INDEX_MAP_FONT")}
	for _, source := range runtime.Config.Sources {
		paths = append(paths, source.Path)
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		// filepath.ToSlash and filepath.FromSlash only normalize the host
		// platform's separators. Error strings can still contain a Windows path
		// while the MCP server runs on Linux, so include explicit cross-platform
		// separator variants before applying the case-insensitive redaction.
		for _, variant := range []string{
			path,
			filepath.ToSlash(path),
			filepath.FromSlash(path),
			strings.ReplaceAll(path, "\\", "/"),
			strings.ReplaceAll(path, "/", "\\"),
		} {
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
