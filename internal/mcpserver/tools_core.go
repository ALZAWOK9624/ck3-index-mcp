package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ck3-index/internal/indexer"
)

// MCP schemas advertise the shared evidence limit with a default of eight,
// but JSON Schema defaults are descriptive and are not inserted into decoded
// requests. Keep operation-specific bounded views aligned with that contract.
const defaultMCPBoundedResultLimit = 8

func boundedMCPResultLimit(limit int) int {
	if limit <= 0 {
		return defaultMCPBoundedResultLimit
	}
	return limit
}

// requirePrivateEvidenceVisibility is a conservative boundary for operations
// that either read the configured project tree directly or derive answers from
// it without source-level provenance on every intermediate value.  Do not
// weaken this to a presentation-time redaction: callers could otherwise use
// totals, diagnostics, or comparison fallbacks as an oracle for private data.
func requirePrivateEvidenceVisibility(visibility, operation string) error {
	if visibility != "public" {
		return nil
	}
	return newToolError(ErrorInvalidArguments, "privacy", "public visibility is not available for this operation because it requires private workspace evidence", false,
		map[string]any{"field": "visibility", "operation": operation},
		map[string]any{"guidance": "Use private visibility in an authorized session, or choose an operation with source-filtered public evidence."})
}

// eventChainHTMLToolResult preserves the ordinary event-chain result while
// adding a standalone, already-redacted HTML representation for clients that
// can display an interactive graph. The document is generated only from the
// same topology returned in the structured result.
type eventChainHTMLToolResult struct {
	indexer.LLMResult
	Format string                           `json:"format"`
	HTML   indexer.EventTopologyHTMLPreview `json:"html"`
}

func handleSearch(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3SearchArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if len(args.Queries) > 0 {
		if strings.TrimSpace(args.Query) != "" {
			return toolOutput{}, invalidArgument("queries", "send either query or queries, not both")
		}
		value, err := runtime.DB.LLMSearchBatch(ctx, args.Queries, indexer.SearchOptions{
			Kind: args.Kind, Source: args.Source, PathPrefix: args.PathPrefix, LLMOptions: opts,
		})
		if err != nil {
			return toolOutput{}, invalidArgument("queries", err.Error())
		}
		return toolOutput{Value: value, Visibility: visibility}, nil
	}
	value, err := runtime.DB.LLMSearch(ctx, indexer.SearchOptions{Query: args.Query, Kind: args.Kind, Source: args.Source, PathPrefix: args.PathPrefix, Page: args.Page, LLMOptions: opts})
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleInspect(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3InspectArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	operation := args.Operation
	if operation == "" {
		operation = "aggregate"
	}
	if operation != "compare" && (strings.TrimSpace(args.Source) != "" || strings.TrimSpace(args.Base) != "") {
		return toolOutput{}, invalidArgument("operation", "source and base are only valid with operation=compare")
	}
	var value any
	switch operation {
	case "aggregate":
		value, err = runtime.DB.LLMInspectSmart(ctx, args.ID, opts)
	case "definition":
		value, err = runtime.DB.LLMQueryObject(ctx, args.ID, opts)
	case "references":
		value, err = runtime.DB.LLMFindRefs(ctx, args.ID, opts)
	case "localization":
		value, err = runtime.DB.LLMQueryLocalization(ctx, args.ID, opts)
	case "resource":
		value, err = runtime.DB.LLMQueryResource(ctx, args.ID, opts)
	case "context":
		value, err = runtime.DB.LLMInspectObject(ctx, args.ID, opts)
	case "diagnose":
		value, err = runtime.DB.LLMDiagnoseKey(ctx, args.ID, opts)
	case "compare":
		if err := requirePrivateEvidenceVisibility(visibility, "compare"); err != nil {
			return toolOutput{}, err
		}
		value, err = indexer.CompareObjectAgainstBase(ctx, runtime.Config, args.ID, indexer.ObjectCompareOptions{
			Source: args.Source,
			Base:   args.Base,
			Limit:  boundedMCPResultLimit(args.Limit),
		})
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleReview(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3ReviewArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	if err := requirePrivateEvidenceVisibility(visibility, "review"); err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	value, err := runtime.DB.LLMReview(ctx, runtime.Config, args.Files, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleWorkspace(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3WorkspaceArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	operation := args.Operation
	if operation == "" {
		operation = "overview"
	}
	var value any
	switch operation {
	case "overview":
		value, err = runtime.DB.LLMArchitectureOverview(ctx, opts)
	case "object_types":
		value, err = runtime.DB.LLMQueryObjectTypes(ctx, opts)
	case "on_action_evidence":
		if err := requirePrivateEvidenceVisibility(visibility, "on_action_evidence"); err != nil {
			return toolOutput{}, err
		}
		value, err = runtime.DB.AuditOnActionEvidence(ctx, runtime.Config, boundedMCPResultLimit(args.Limit))
	case "capabilities":
		value, err = workspaceCapabilities(ctx, runtime, args.Domain)
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleDependencies(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3DependenciesArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(args.Depth)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	operation := args.Operation
	if operation == "" {
		operation = "neighborhood"
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		return toolOutput{}, invalidArgument("format", "format must be json or html")
	}
	var value any
	switch operation {
	case "neighborhood":
		if format != "json" {
			return toolOutput{}, invalidArgument("format", "format=html requires operation=event_chain")
		}
		value, err = runtime.DB.LLMDependencyGraph(ctx, args.ID, opts)
	case "event_chain":
		includeOnActions := true
		if args.IncludeOnActions != nil {
			includeOnActions = *args.IncludeOnActions
		}
		var chain indexer.LLMResult
		chain, err = runtime.DB.LLMEventChain(ctx, args.ID, indexer.EventChainOptions{
			LLMOptions: opts, Direction: args.Direction, IncludeOnActions: includeOnActions,
		})
		if err != nil {
			break
		}
		if format == "json" {
			value = chain
			break
		}
		if chain.Topology == nil {
			return toolOutput{}, fmt.Errorf("event_chain returned no topology for HTML rendering")
		}
		preview, renderErr := indexer.RenderEventTopologyHTML(*chain.Topology)
		if renderErr != nil {
			return toolOutput{}, renderErr
		}
		value = eventChainHTMLToolResult{LLMResult: chain, Format: "html", HTML: preview}
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handlePrepareEdit(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3PrepareEditArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	operation := args.Operation
	if operation == "" {
		operation = "context"
	}
	var value any
	switch operation {
	case "context":
		value, err = runtime.DB.LLMPrepareEdit(ctx, args.ID, opts)
	case "examples":
		typ, contains := indexer.SplitExampleID(args.ID)
		value, err = runtime.DB.LLMQueryExamples(ctx, typ, contains, opts)
	case "rules":
		value, err = runtime.DB.LLMQueryRules(ctx, args.ID, opts)
	case "patterns":
		value, err = runtime.DB.LLMQueryPatterns(ctx, args.ID, opts)
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handlePreflight(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3PreflightArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	if err := requirePrivateEvidenceVisibility(visibility, "preflight"); err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	var value any
	switch args.Operation {
	case "subject":
		if strings.TrimSpace(args.ID) == "" {
			return toolOutput{}, missingArgument("id")
		}
		value, err = runtime.DB.LLMPreflight(ctx, args.ID, opts)
	case "patch":
		if len(args.Files) == 0 {
			return toolOutput{}, missingArgument("files")
		}
		value, err = runtime.DB.LLMPreflightPatch(ctx, args.Files, opts)
	case "dirty":
		if runtime.Config.ConfigPath == "" {
			return toolOutput{}, newToolError(ErrorSourceNotFound, "configuration", "operation=dirty requires a configured project source", false, nil,
				map[string]any{"guidance": "Start MCP with a ck3-index configuration that declares one project source."})
		}
		value, err = runtime.DB.LLMPreflightDirty(ctx, runtime.Config, opts)
	default:
		return toolOutput{}, unknownOperation(args.Operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleImpact(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3ImpactArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	if len(args.Files) == 0 {
		return toolOutput{}, missingArgument("files")
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	if err := requirePrivateEvidenceVisibility(visibility, "impact"); err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	value, err := runtime.DB.LLMImpactPatch(ctx, args.Files, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleDiagnostics(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3DiagnosticsArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	operation := args.Operation
	if operation == "" {
		operation = "summary"
	}
	var value any
	switch operation {
	case "summary":
		if args.Page > 0 {
			return toolOutput{}, invalidArgument("page", "page is only valid with operation=explain")
		}
		value, err = runtime.DB.LLMValidateSinceBaseline(ctx, args.Baseline, opts)
	case "explain":
		if strings.TrimSpace(args.Code) == "" {
			return toolOutput{}, missingArgument("code")
		}
		value, err = runtime.DB.LLMExplainDiagnosticFiltered(ctx, indexer.DiagnosticFilter{Code: args.Code, Source: args.Source, PathPrefix: args.PathPrefix, Confidence: args.Confidence, Baseline: args.Baseline, Page: args.Page}, opts)
	// The baseline operations moved to ck3_diagnostic_baseline when they
	// stopped being read-only. No redirect branch for the old names: the
	// operation enum rejects them at decode time and names the two that remain,
	// so the branch would never run.
	default:
		return toolOutput{}, unknownOperation(operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

// handleDiagnosticBaseline records, lists and forgets diagnostic baselines.
//
// save and clear write, so this tool is advertised non-read-only and never
// enters the read cache. They also invalidate the cached ck3_diagnostics
// responses: the cache key carries the index generation, and recording a
// baseline changes which findings a summary reports without moving it.
func handleDiagnosticBaseline(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3DiagnosticBaselineArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation == "" {
		return toolOutput{}, missingArgument("operation")
	}
	switch operation {
	case "save":
		saved, err := runtime.DB.SaveDiagnosticBaseline(ctx, args.Baseline)
		if err != nil {
			return toolOutput{}, err
		}
		mcpReadToolCache.invalidateTool(indexer.ToolDiagnostics)
		return toolOutput{Value: map[string]any{
			"operation": operation,
			"baseline":  saved,
			"guidance": []string{
				"Findings recorded here are hidden from later ck3_diagnostics summary and explain calls that pass the same baseline name.",
				"The baseline survives ck3_refresh operation=full; re-record it after deliberately accepting new upstream findings.",
			},
		}, Visibility: "private", Committed: true}, nil
	case "list":
		baselines, err := runtime.DB.ListDiagnosticBaselines(ctx)
		if err != nil {
			return toolOutput{}, err
		}
		if baselines == nil {
			baselines = []indexer.DiagnosticBaseline{}
		}
		return toolOutput{Value: map[string]any{
			"operation": operation,
			"baselines": baselines,
		}, Visibility: "private"}, nil
	case "clear":
		cleared, err := runtime.DB.ClearDiagnosticBaseline(ctx, args.Baseline)
		if err != nil {
			return toolOutput{}, err
		}
		mcpReadToolCache.invalidateTool(indexer.ToolDiagnostics)
		value := map[string]any{"operation": operation, "baseline": cleared}
		if !cleared.Existed {
			value["guidance"] = []string{
				"No baseline was recorded under this name; nothing was forgotten. Use operation=list to see the recorded names.",
			}
		}
		return toolOutput{Value: value, Visibility: "private", Committed: cleared.Existed}, nil
	default:
		return toolOutput{}, unknownOperation(operation)
	}
}

func handleCoatOfArms(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3CoatOfArmsArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	value, err := runtime.DB.LLMCoatOfArms(ctx, indexer.CoatOfArmsSpec{
		Operation: args.Operation,
		ID:        args.ID,
		Size:      args.Size,
		Filter:    args.Filter,
		Limit:     opts.Limit,
	}, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleScriptReference(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3ScriptReferenceArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	liveIndexReady := false
	if state, stateErr := runtime.DB.IndexState(ctx); stateErr == nil {
		liveIndexReady = state.Ready()
	}
	var value any
	switch args.Kind {
	case "scope":
		if !liveIndexReady {
			value, err = lookupScopeTool(runtime.DB, args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "high"
				result["rule_source"] = "engine_1_19_snapshot"
			}
			break
		}
		live, lookupErr := runtime.DB.LookupScopeEvidence(ctx, args.ID)
		if lookupErr != nil {
			err = lookupErr
		} else if len(live) > 0 {
			value = map[string]any{"found": true, "key": args.ID, "rules": live, "confidence": "high", "rule_source": "engine_logs", "guidance": []string{"Live engine log rules take precedence; the generated CK3 1.19 snapshot is used only while live evidence is unavailable."}}
		} else {
			value, err = lookupScopeTool(runtime.DB, args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "high"
				result["rule_source"] = "engine_1_19_snapshot"
			}
		}
	case "datatype":
		if !liveIndexReady {
			value = map[string]any{"query": args.ID, "found": false, "items": []indexer.DatatypeInfo{}, "rule_source": "engine_logs_unavailable", "guidance": []string{"The engine-log cache is not yet published; retry after the index reports ready."}}
			break
		}
		var items []indexer.DatatypeInfo
		items, err = runtime.DB.LookupDatatype(ctx, args.ID, opts.Limit)
		value = map[string]any{"query": args.ID, "found": len(items) > 0, "items": items, "rule_source": "engine_logs/data_types"}
	case "shape":
		value, err = lookupShapeTool(args.ID)
	case "define":
		value, err = lookupDefineTool(args.ID)
	case "on_action":
		if liveIndexReady {
			live, lookupErr := runtime.DB.LookupOnActionEvidence(ctx, args.ID)
			if lookupErr != nil {
				err = lookupErr
			} else if len(live) > 0 {
				value = map[string]any{"found": true, "key": args.ID, "rules": live, "confidence": "high", "rule_source": "engine_logs", "guidance": []string{"Live on_action logs take precedence; Expected Scope: none means this hook has no implicit root scope."}}
			} else {
				value, err = lookupOnActionTool(runtime.DB, args.ID)
				if result, ok := value.(map[string]any); ok {
					result["confidence"] = "high"
					result["rule_source"] = "engine_1_19_snapshot"
				}
			}
		} else {
			value, err = lookupOnActionTool(runtime.DB, args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "high"
				result["rule_source"] = "engine_1_19_snapshot"
				result["guidance"] = []string{"The live engine-log index is not published; this result uses the generated CK3 1.19 snapshot."}
			}
		}
		if err == nil {
			if result, ok := value.(map[string]any); ok {
				if snapshot, found := indexer.ResolveOnActionSnapshotContract(args.ID); found {
					// Keep the generated CK3 1.19 snapshot as a separate contract
					// layer. It must never overwrite a live engine rule or create a
					// validator-facing inferred scope.
					result["snapshot_contract"] = snapshot
				}
				game, gameConfigured := indexer.GameSource(runtime.Config)
				if !opts.AllowProject && (!gameConfigured || game.Private) {
					result["documentation_contract"] = map[string]any{
						"status":   "unavailable",
						"guidance": []string{"Public visibility excludes the configured documentation source."},
					}
				} else {
					documentation, documentationErr := runtime.DB.LookupOnActionDocumentationContract(ctx, runtime.Config, args.ID, opts.Limit)
					if documentationErr != nil {
						err = documentationErr
					} else {
						// Keep vanilla comments in a distinct review-only envelope. The
						// top-level result remains the engine-first / 1.19-snapshot rule
						// lookup used by existing clients.
						result["documentation_contract"] = documentation
					}
				}
			}
		}
	case "iterator":
		value, err = lookupIteratorTool(args.ID)
	case "example":
		value, err = lookupExampleTool(args.ID)
	case "modifier":
		value, err = lookupModifierTool(runtime.DB, args.ID)
	default:
		return toolOutput{}, invalidArgument("kind", "kind must be one of the documented script reference families")
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleHealth(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3HealthArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	// An agent calls ck3_health to decide whether the server is usable, which no
	// table total answers. Deep stays available for the times someone is
	// actually inspecting the database.
	depth := indexer.HealthQuick
	if args.Mode == "deep" {
		depth = indexer.HealthDeep
	}
	health, err := runtime.DB.HealthConfiguredDepth(ctx, runtime.Config, depth)
	var databaseUsage *mcpDatabaseResourceUsage
	if err == nil {
		gis := runtime.DB.CachedGISSidecarStatus(ctx, runtime.Config)
		if depth == indexer.HealthDeep {
			gis = runtime.DB.GISSidecarStatus(ctx, runtime.Config)
		}
		health.GIS = &gis
		usage := currentMCPTaskUsage()
		health.ActiveTasks = usage.Active
		health.ActiveExpensiveTasks = usage.Heavy + usage.Raster
		health.ActiveHeavyTasks = usage.Heavy
		health.ActiveRasterTasks = usage.Raster
		health.QueuedTasks = usage.Queued
		health.QueuedExpensiveTasks = usage.QueuedHeavy + usage.QueuedRaster
		health.QueuedHeavyTasks = usage.QueuedHeavy
		health.QueuedRasterTasks = usage.QueuedRaster
		health.EstimatedTaskMemoryMB = usage.EstimatedMemoryMB
		if resources, ok := runtime.DatabaseController.(mcpDatabaseResourceReporter); ok {
			usage := resources.ResourceUsage()
			databaseUsage = &usage
			health.LoadedDatabaseCount = usage.LoadedDatabaseCount
			health.RetiredDatabaseCount = usage.RetiredDatabaseCount
			health.AggregateSQLiteCacheBudgetMB = usage.AggregateSQLiteCacheBudgetMB
			health.MaxOpenDatabasePools = usage.MaxOpenDatabasePools
			health.MaxSQLiteCacheBudgetMB = usage.MaxSQLiteCacheBudgetMB
		}
	}
	if err != nil {
		return toolOutput{}, err
	}
	report := mcpHealthReport(health)
	if databaseUsage != nil {
		report["queried_database"] = runtime.databaseIdentity()
		report["active_database"] = databaseUsage.Active
		report["configured_database_count"] = databaseUsage.ConfiguredDatabaseCount
	} else if runtime.DatabaseController != nil {
		active, databases := runtime.DatabaseController.Catalog()
		report["queried_database"] = runtime.databaseIdentity()
		report["active_database"] = active
		report["configured_database_count"] = len(databases)
	} else {
		report["queried_database"] = runtime.databaseIdentity()
		report["active_database"] = runtime.databaseIdentity()
		report["configured_database_count"] = 1
	}
	report["tool_cache"] = mcpReadToolCache.stats()
	return toolOutput{Value: report, Visibility: "private"}, nil
}

func handleGUI(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3GUIArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	value, err := runtime.DB.QueryGUI(ctx, indexer.GUIQueryOptions{
		Operation: args.Operation, Path: args.Path, PathPrefix: args.PathPrefix, Symbol: args.Symbol,
		AllowProject: opts.AllowProject, Limit: opts.Limit, Width: args.Width, Height: args.Height, Format: args.Format, HTMLMode: args.HTMLMode, Language: args.Language, Samples: args.Samples, ModelSamples: args.ModelSamples, RuntimeFacts: args.RuntimeFacts, ActionEffects: args.ActionEffects,
	})
	return toolOutput{Value: value, Visibility: visibility}, err
}
