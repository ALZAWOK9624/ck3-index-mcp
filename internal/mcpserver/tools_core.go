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
	value, err := runtime.DB.LLMSearch(ctx, indexer.SearchOptions{Query: args.Query, Kind: args.Kind, Source: args.Source, PathPrefix: args.PathPrefix, LLMOptions: opts})
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
	operation := args.Operation
	if operation == "" {
		operation = "aggregate"
	}
	if operation != "compare" && (strings.TrimSpace(args.Source) != "" || strings.TrimSpace(args.Base) != "") {
		return toolOutput{}, fmt.Errorf("argument fields %q and %q are only valid with operation=compare", "source", "base")
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
		if !opts.AllowProject {
			if strings.TrimSpace(args.Source) == "" {
				return toolOutput{}, fmt.Errorf("operation=compare with public visibility requires an explicit non-project %q", "source")
			}
			if !publicObjectCompareSource(runtime.Config, args.Source) || (strings.TrimSpace(args.Base) != "" && !publicObjectCompareSource(runtime.Config, args.Base)) {
				return toolOutput{}, fmt.Errorf("operation=compare with public visibility may only use configured non-project source layers")
			}
		}
		value, err = indexer.CompareObjectAgainstBase(ctx, runtime.Config, args.ID, indexer.ObjectCompareOptions{
			Source: args.Source,
			Base:   args.Base,
			Limit:  boundedMCPResultLimit(args.Limit),
		})
	default:
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [aggregate definition references localization resource context diagnose compare]", "operation", operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func publicObjectCompareSource(cfg indexer.Config, name string) bool {
	name = strings.TrimSpace(name)
	for _, source := range cfg.Sources {
		if !strings.EqualFold(source.Name, name) {
			continue
		}
		return source.Rank > 1 && !strings.EqualFold(source.Name, "project")
	}
	return false
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
		value, err = runtime.DB.AuditOnActionEvidence(ctx, runtime.Config, boundedMCPResultLimit(args.Limit))
	default:
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [overview object_types on_action_evidence]", "operation", operation)
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
	operation := args.Operation
	if operation == "" {
		operation = "neighborhood"
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [json html]", "format", args.Format)
	}
	var value any
	switch operation {
	case "neighborhood":
		if format != "json" {
			return toolOutput{}, fmt.Errorf("argument field %q value %q requires operation=event_chain", "format", format)
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
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [neighborhood event_chain]", "operation", operation)
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
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [context examples rules patterns]", "operation", operation)
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
	var value any
	switch args.Operation {
	case "subject":
		if strings.TrimSpace(args.ID) == "" {
			return toolOutput{}, fmt.Errorf("operation=subject requires argument field %q", "id")
		}
		value, err = runtime.DB.LLMPreflight(ctx, args.ID, opts)
	case "patch":
		if len(args.Files) == 0 {
			return toolOutput{}, fmt.Errorf("operation=patch requires non-empty argument field %q", "files")
		}
		value, err = runtime.DB.LLMPreflightPatch(ctx, args.Files, opts)
	case "dirty":
		if runtime.Config.ConfigPath == "" {
			return toolOutput{}, fmt.Errorf("operation=dirty requires server configuration")
		}
		value, err = runtime.DB.LLMPreflightDirty(ctx, runtime.Config, opts)
	default:
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [subject patch dirty]", "operation", args.Operation)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleImpact(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3ImpactArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	if len(args.Files) == 0 {
		return toolOutput{}, fmt.Errorf("argument field %q must contain at least one patch file", "files")
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
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
	operation := args.Operation
	if operation == "" {
		operation = "summary"
	}
	var value any
	switch operation {
	case "summary":
		value, err = runtime.DB.LLMValidate(ctx, opts)
	case "explain":
		if strings.TrimSpace(args.Code) == "" {
			return toolOutput{}, fmt.Errorf("operation=explain requires argument field %q", "code")
		}
		value, err = runtime.DB.LLMExplainDiagnosticFiltered(ctx, indexer.DiagnosticFilter{Code: args.Code, Source: args.Source, PathPrefix: args.PathPrefix, Confidence: args.Confidence}, opts)
	default:
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [summary explain]", "operation", operation)
	}
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
	liveIndexReady := false
	if state, stateErr := runtime.DB.IndexState(ctx); stateErr == nil {
		liveIndexReady = state.Ready()
	}
	var value any
	switch args.Kind {
	case "scope":
		if !liveIndexReady {
			value, err = lookupScopeTool(args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "medium"
				result["rule_source"] = "tiger_fallback"
			}
			break
		}
		live, lookupErr := runtime.DB.LookupScopeEvidence(ctx, args.ID)
		if lookupErr != nil {
			err = lookupErr
		} else if len(live) > 0 {
			value = map[string]any{"found": true, "key": args.ID, "rules": live, "confidence": "high", "rule_source": "engine_logs", "guidance": []string{"Engine log rules take precedence; use Tiger only as fallback and investigate conflicts."}}
		} else {
			value, err = lookupScopeTool(args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "medium"
				result["rule_source"] = "tiger_fallback"
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
				value, err = lookupOnActionTool(args.ID)
				if result, ok := value.(map[string]any); ok {
					result["confidence"] = "medium"
					result["rule_source"] = "tiger_fallback"
				}
			}
		} else {
			value, err = lookupOnActionTool(args.ID)
			if result, ok := value.(map[string]any); ok {
				result["confidence"] = "medium"
				result["rule_source"] = "tiger_fallback"
				result["guidance"] = []string{"The live engine-log index is not published; this is a static Tiger fallback only."}
			}
		}
		if err == nil {
			if result, ok := value.(map[string]any); ok {
				if tiger, found := indexer.ResolveTigerOnActionContract(args.ID); found {
					// Keep the generated Tiger table as a separate static evidence
					// layer. It must never overwrite a live engine rule or create a
					// validator-facing inferred scope.
					result["tiger_contract"] = tiger
				}
				documentation, documentationErr := runtime.DB.LookupOnActionDocumentationContract(ctx, runtime.Config, args.ID, opts.Limit)
				if documentationErr != nil {
					err = documentationErr
				} else {
					// Keep vanilla comments in a distinct review-only envelope. The
					// top-level result remains the engine-first / Tiger-fallback rule
					// lookup used by existing clients.
					result["documentation_contract"] = documentation
				}
			}
		}
	case "iterator":
		value, err = lookupIteratorTool(args.ID)
	case "example":
		value, err = lookupExampleTool(args.ID)
	case "modifier":
		value, err = lookupModifierTool(args.ID)
	default:
		return toolOutput{}, fmt.Errorf("argument field %q received %q; expected one of [scope datatype shape define on_action iterator example modifier]", "kind", args.Kind)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleHealth(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3HealthArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	health, err := runtime.DB.HealthConfigured(ctx, runtime.Config)
	if err == nil {
		gis := runtime.DB.GISSidecarStatus(ctx, runtime.Config)
		health.GIS = &gis
	}
	if err != nil {
		return toolOutput{}, err
	}
	return toolOutput{Value: mcpHealthReport(health), Visibility: "private"}, nil
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
	value, err := runtime.DB.QueryGUI(ctx, indexer.GUIQueryOptions{
		Operation: args.Operation, Path: args.Path, PathPrefix: args.PathPrefix, Symbol: args.Symbol,
		AllowProject: opts.AllowProject, Limit: opts.Limit, Width: args.Width, Height: args.Height, Format: args.Format, HTMLMode: args.HTMLMode, Language: args.Language, Samples: args.Samples, ModelSamples: args.ModelSamples, RuntimeFacts: args.RuntimeFacts, ActionEffects: args.ActionEffects,
	})
	return toolOutput{Value: value, Visibility: visibility}, err
}
