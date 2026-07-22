package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"ck3-index/internal/indexer"
)

func requireMapDatabase(ctx context.Context, runtime *Runtime) error {
	if err := runtime.DB.RequireMapDatabase(ctx); err != nil {
		return newToolError(ErrorMapDatabaseUnavailable, "index_state", "the current index has no published map database", true, nil,
			map[string]any{"guidance": "Run a full ck3-index scan that includes map inputs, then retry the map tool."})
	}
	return nil
}

// Map cache tables currently combine active source layers without retaining
// enough per-cell provenance to prove that an answer excludes private project
// data. Refuse public cache-derived map requests rather than making a vague
// "best effort" redaction claim. Source-file map audits/mappings use their
// separate filtered configuration path below and remain safely public.
func requireSourceTrackedMapVisibility(visibility string) error {
	if visibility != "public" {
		return nil
	}
	return newToolError(ErrorInvalidArguments, "privacy", "public visibility is not available for map-cache queries until the map cache records source-level provenance", false,
		map[string]any{"field": "visibility"},
		map[string]any{"guidance": "Use private visibility, or use map_asset_audit/map_province_mapping for source-filtered map evidence."})
}

func mapSourcesForVisibility(runtime *Runtime, allowPrivate bool) (indexer.Config, error) {
	cfg, err := indexer.NormalizeConfig(runtime.Config)
	if err != nil || allowPrivate {
		return cfg, err
	}
	filtered := make([]indexer.Source, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if !source.Private {
			filtered = append(filtered, source)
		}
	}
	cfg.Sources = filtered
	return cfg, nil
}

func handleMapAssetAudit(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapAssetAuditArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	value, err := indexer.AuditMapAssets(ctx, cfg, args.Operation, opts.Limit)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapProvinceMapping(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapProvinceMappingArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	value, err := indexer.MapProvinceMapping(ctx, cfg, args.MapProvinceMappingSpec)
	if err == nil {
		limit := opts.Limit
		if limit <= 0 {
			limit = 8
		}
		if len(value.Groups) > limit {
			value.Groups = value.Groups[:limit]
		}
		if len(value.Sources) > limit {
			value.Sources = value.Sources[:limit]
		}
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapProvinceInfo(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapProvinceInfoArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapProvinceInfo(ctx, args.ID, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapPhysicalContext(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapPhysicalContextArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapPhysicalContext(ctx, args.MapPhysicalContextSpec, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapNeighbors(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapNeighborsArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapNeighbors(ctx, args.ID, args.Radius, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapSpatialRelation(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapSpatialRelationArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapSpatialRelation(ctx, args.From, args.To, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapStrategicPassages(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapStrategicPassagesArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapStrategicPassages(ctx, args.Target, args.Kind, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapTitleContext(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapTitleContextArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapTitleContext(ctx, args.ID, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapAssignmentPlan(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapAssignmentPlanArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	target, err := normalizedTargetAlias(args.Target, args.ID)
	if err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.visibilityArgs.optionsWithDomainMode(0, true)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapAssignmentPlan(ctx, args.assignmentMode(), target, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapBuildingCandidates(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapBuildingCandidatesArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	target, err := normalizedTargetAlias(args.Target, args.ID)
	if err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapBuildingCandidates(ctx, target, args.Year, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapRecipeCatalog(_ context.Context, _ *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapRecipeCatalogArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	visibility, err := args.normalizedVisibility()
	if err != nil {
		return toolOutput{}, err
	}
	return toolOutput{Value: mapRecipeCatalog(), Visibility: visibility}, nil
}

func mapRecipeCatalog() any {
	// Kept behind a function to make the MCP adapter explicit while leaving the
	// indexer-owned recipe catalog as the single source of map capabilities.
	return indexer.MapRecipeCatalog()
}

func handleMapBuildMetric(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapBuildMetricArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if err := requireMapDatabase(ctx, runtime); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapBuildMetric(ctx, args.MapMetricSpec, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapRoute(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapRouteArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapRoute(ctx, args.MapRouteSpec, opts)
	return toolOutput{Value: value, Visibility: visibility}, err
}

func handleMapRender(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapRenderArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	if strings.TrimSpace(args.FontPath) != "" {
		return toolOutput{}, invalidArgument("font_path", "map_render does not accept argument field \"font_path\"; configure CK3_INDEX_MAP_FONT on the server")
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	opts = configureRuntimeOptions(runtime, opts)
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	value, err := runtime.DB.LLMMapRender(ctx, args.MapRenderSpec, opts)
	if err == nil && !args.Verbose {
		value = compactMapRenderResult(value)
	}
	return toolOutput{Value: value, Visibility: visibility}, err
}

func normalizedTargetAlias(target, legacyID string) (string, error) {
	target = strings.TrimSpace(target)
	legacyID = strings.TrimSpace(legacyID)
	if target != "" && legacyID != "" && target != legacyID {
		return "", invalidArgument("target", "argument fields \"target\" and deprecated alias \"id\" conflict")
	}
	if target == "" {
		target = legacyID
	}
	if target == "" {
		return "", missingArgument("target")
	}
	return target, nil
}

func compactMapRenderResult(value indexer.MapRenderResult) indexer.MapRenderResult {
	value.Metrics = append([]indexer.MapMetricResult(nil), value.Metrics...)
	for index := range value.Metrics {
		value.Metrics[index].Values = nil
		value.Metrics[index].Categories = nil
		value.Metrics[index].Outliers = nil
		if value.Metrics[index].RecipeSpec != nil {
			spec := *value.Metrics[index].RecipeSpec
			spec.Values = nil
			value.Metrics[index].RecipeSpec = &spec
		}
	}
	return value
}
