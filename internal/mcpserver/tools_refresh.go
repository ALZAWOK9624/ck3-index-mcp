package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"ck3-index/internal/indexer"
)

type refreshActivity struct {
	Active        bool
	LastErrorCode string
}

var refreshActivities = struct {
	sync.Mutex
	byDatabase map[string]refreshActivity
}{byDatabase: map[string]refreshActivity{}}

func refreshActivityKey(runtime *Runtime) string {
	if runtime != nil && strings.TrimSpace(runtime.DBPath) != "" {
		return runtime.DBPath
	}
	return "default"
}

func beginRefresh(runtime *Runtime) (func(error), bool) {
	key := refreshActivityKey(runtime)
	refreshActivities.Lock()
	state := refreshActivities.byDatabase[key]
	if state.Active {
		refreshActivities.Unlock()
		return nil, false
	}
	state.Active = true
	refreshActivities.byDatabase[key] = state
	refreshActivities.Unlock()
	return func(err error) {
		refreshActivities.Lock()
		defer refreshActivities.Unlock()
		state := refreshActivities.byDatabase[key]
		state.Active = false
		state.LastErrorCode = ""
		if err != nil {
			state.LastErrorCode = toolErrorFrom(err).Code
		}
		refreshActivities.byDatabase[key] = state
	}, true
}

func currentRefreshActivity(runtime *Runtime) refreshActivity {
	refreshActivities.Lock()
	defer refreshActivities.Unlock()
	return refreshActivities.byDatabase[refreshActivityKey(runtime)]
}

func refreshStatusOutput(ctx context.Context, runtime *Runtime) (map[string]any, error) {
	status, err := runtime.DB.RefreshStatus(ctx, runtime.Config)
	if err != nil {
		return nil, err
	}
	activity := currentRefreshActivity(runtime)
	result := map[string]any{
		"operation":       "status",
		"refresh_status":  status,
		"is_scanning":     activity.Active || status.Index.Status == "finalizing",
		"status":          status.Status,
		"index":           status.Index,
		"scan_generation": status.Index.Generation,
		"needs_full_scan": status.NeedsFullScan,
	}
	if activity.LastErrorCode != "" {
		// Do not retain a raw scan error: it could contain a configured source
		// path. The request that failed already received its structured detail.
		result["last_error"] = map[string]any{"code": activity.LastErrorCode, "message": "the most recent refresh request failed"}
	}
	return result, nil
}

func handleRefresh(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3RefreshArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation == "" {
		operation = "status"
	}
	switch operation {
	case "status":
		if len(args.Paths) > 0 {
			return toolOutput{}, invalidArgument("paths", "paths are only valid with operation=files")
		}
		value, err := refreshStatusOutput(ctx, runtime)
		return toolOutput{Value: value, Visibility: "private"}, err
	case "full":
		if len(args.Paths) > 0 {
			return toolOutput{}, invalidArgument("paths", "paths are only valid with operation=files")
		}
		finish, acquired := beginRefresh(runtime)
		if !acquired {
			return toolOutput{}, newToolError(ErrorConflictingGeneration, "concurrency", "another refresh is already running for this index", true, nil,
				map[string]any{"guidance": "Call ck3_refresh status and retry after is_scanning is false."})
		}
		stats, refreshErr := indexer.ScanFullStaged(ctx, runtime.Config)
		finish(refreshErr)
		if refreshErr != nil {
			return toolOutput{}, refreshErr
		}
		// The cache path is a host detail; refresh callers only need the newly
		// published generation included in status below.
		stats.Database = ""
		// Once staged publication committed, report its stable state even if a
		// cancellation notification arrived a few instructions too late to roll
		// back the completed atomic transaction.
		status, statusErr := refreshStatusOutput(context.WithoutCancel(ctx), runtime)
		if statusErr != nil {
			return toolOutput{}, statusErr
		}
		status["operation"] = "full"
		status["refresh"] = stats
		status["diagnostic_delta"] = stats.DiagnosticDelta
		return toolOutput{Value: status, Visibility: "private", Committed: true}, nil
	case "files":
		if len(args.Paths) == 0 {
			return toolOutput{}, missingArgument("paths")
		}
		normalizedPaths := make([]string, 0, len(args.Paths))
		for _, path := range args.Paths {
			if strings.TrimSpace(path) == "" {
				return toolOutput{}, invalidArgument("paths", "paths must not contain an empty value")
			}
			rel, pathErr := indexer.NormalizeRefreshPath(path)
			if pathErr != nil {
				return toolOutput{}, newToolError(ErrorPathOutsideProject, "invalid_arguments", "refresh paths must be project source-root-relative indexed paths", false,
					map[string]any{"field": "paths"}, map[string]any{"guidance": "Use a relative CK3 load-root path without parent traversal."})
			}
			normalizedPaths = append(normalizedPaths, rel)
		}
		state, err := runtime.DB.IndexState(ctx)
		if err != nil {
			return toolOutput{}, err
		}
		if state.Status == "finalizing" {
			return toolOutput{}, newToolError(ErrorIndexFinalizing, "index_state", "the index is finalizing another scan generation", true, nil,
				map[string]any{"guidance": "Wait for ck3_refresh status to report ready, then retry files."})
		}
		if !state.Ready() {
			return toolOutput{}, newToolError(ErrorIndexNotReady, "index_state", "incremental refresh requires one ready published index generation", false,
				map[string]any{"scan_status": state.Status},
				map[string]any{"guidance": "Run ck3_refresh with operation=full first."})
		}
		preflight, err := runtime.DB.RefreshStatus(ctx, runtime.Config)
		if err != nil {
			return toolOutput{}, err
		}
		if !preflight.Project.Refreshable {
			return toolOutput{}, newToolError(ErrorSourceNotFound, "configuration", "the configured project source is not accessible for refresh", false, nil,
				map[string]any{"guidance": "Restore access to the configured project source, then retry ck3_refresh status."})
		}
		if preflight.NeedsFullScan {
			return toolOutput{}, newToolError(ErrorFullScanRequired, "index_state", "incremental refresh is unsafe until a full scan refreshes the published index", false,
				map[string]any{"status": preflight.Status},
				map[string]any{"guidance": preflight.FullScanGuidance})
		}
		finish, acquired := beginRefresh(runtime)
		if !acquired {
			return toolOutput{}, newToolError(ErrorConflictingGeneration, "concurrency", "another refresh is already running for this index", true, nil,
				map[string]any{"guidance": "Call ck3_refresh status and retry after is_scanning is false."})
		}
		stats, refreshErr := indexer.ScanFiles(ctx, runtime.Config, normalizedPaths)
		finish(refreshErr)
		if refreshErr != nil {
			return toolOutput{}, refreshErr
		}
		stats.Database = ""
		status, statusErr := refreshStatusOutput(context.WithoutCancel(ctx), runtime)
		if statusErr != nil {
			return toolOutput{}, statusErr
		}
		status["operation"] = "files"
		status["refresh"] = stats
		status["changed_files"] = stats.ChangedFiles
		status["removed_files"] = stats.RemovedFiles
		status["missing_files"] = stats.MissingFiles
		status["path_outcomes"] = stats.PathOutcomes
		status["changed_symbols"] = stats.ChangedSymbols
		status["changed_symbols_truncated"] = stats.ChangedSymbolsTruncated
		status["diagnostic_delta"] = stats.DiagnosticDelta
		return toolOutput{Value: status, Visibility: "private", Committed: true}, nil
	default:
		return toolOutput{}, unknownOperation(operation)
	}
}
