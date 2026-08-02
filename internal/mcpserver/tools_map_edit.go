package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ck3-index/internal/indexer"
)

// Every other map tool answers a question. These two produce a raster, which
// makes them the first map tools that write anything, so the boundary is drawn
// tightly: output lands in the server's artifact area under a fresh directory,
// never at a path the caller chose and never inside a configured source root.
// The caller reviews the result and copies it into the Mod themselves, which
// keeps the "MCP does not write the Mod" rule the map design states.

// mapEditError keeps the two writing tools' error handling identical: a request
// the caller can correct is reported as such, and anything else stays whatever
// the indexer classified it as.
func mapEditError(field string, err error) error {
	var inputErr *indexer.MapTerrainInputError
	if errors.As(err, &inputErr) {
		return invalidArgument(field, inputErr.Error())
	}
	return err
}

func validateMapArtifactRequestKey(value string) error {
	if _, err := indexer.NormalizeArtifactRequestKey(value); err != nil {
		return invalidArgument("request_key", err.Error())
	}
	return nil
}

func handleMapTerrainEdit(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapTerrainEditArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	if err := validateMapArtifactRequestKey(args.RequestKey); err != nil {
		return toolOutput{}, err
	}
	if !args.Confirm && args.RequestKey != "" {
		return toolOutput{}, invalidArgument("request_key", "request_key is used only when confirm=true publishes an artifact")
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	// Generated relief is derived from the project's own heightmap and is
	// written to a server path, so it carries the same provenance a public
	// response must not disclose.
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	cfg.ArtifactRoot = runtime.Config.ArtifactRoot
	var result indexer.MapTerrainEditResult
	if args.Confirm {
		result, err = indexer.CreateMapTerrainEditArtifactWithRequestKeyContext(ctx, cfg, args.MapTerrainEditSpec, args.RequestKey)
	} else {
		result, err = indexer.PreviewMapTerrainEditContext(ctx, cfg, args.MapTerrainEditSpec)
	}
	if err != nil {
		return toolOutput{}, mapEditError("", err)
	}
	return toolOutput{Value: result, Visibility: visibility, Committed: args.Confirm}, nil
}

func handleMapApplySplit(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapApplySplitArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	if err := validateMapArtifactRequestKey(args.RequestKey); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	if !args.Confirm {
		return toolOutput{}, invalidArgument("confirm",
			"this tool writes a new provinces.png; run map_split_province first, read its plan, then pass confirm=true")
	}
	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	cfg.ArtifactRoot = runtime.Config.ArtifactRoot
	result, plan, planState, err := indexer.LoadMapSplitPlanArtifact(ctx, cfg, runtime.DB, args.PlanID, args.PlanHash)
	if err != nil {
		return toolOutput{}, mapSplitPlanError(err)
	}
	if len(plan.Blockers) > 0 || len(plan.MissingDependencies) > 0 {
		return toolOutput{}, newToolError(ErrorInvalidArguments, "validation",
			fmt.Sprintf("the split plan has %d blocker(s) and %d unresolved dependency item(s), so applying it would corrupt the map", len(plan.Blockers), len(plan.MissingDependencies)), false,
			map[string]any{"blockers": plan.Blockers, "missing_dependencies": plan.MissingDependencies},
			map[string]any{"guidance": "Resolve every blocker reported by map_split_province, then retry."})
	}

	sourcePath, sourceName, err := indexer.ActiveMapAsset(cfg, "map_data/provinces.png")
	if err != nil {
		return toolOutput{}, err
	}
	change, artifact, err := indexer.CreateMapSplitApplyArtifactContext(ctx, cfg, sourcePath, sourceName, result, plan, planState, args.RequestKey)
	if err != nil {
		var stale *indexer.MapSplitPlanStaleError
		if errors.As(err, &stale) {
			return toolOutput{}, mapSplitPlanError(err)
		}
		// A stale index is the usual cause and it is the caller's to fix by
		// re-scanning, so report the mismatch rather than an opaque failure.
		return toolOutput{}, newToolError(ErrorInvalidArguments, "index_state", err.Error(), true,
			map[string]any{"mismatch_count": change.MismatchCount, "mismatches": change.Mismatches},
			map[string]any{"guidance": "Re-scan so the index matches provinces.png on disk, then retry."})
	}

	value := map[string]any{
		"operation":              "apply_split",
		"plan_id":                planState.PlanID,
		"plan_hash":              planState.PlanHash,
		"artifact_id":            artifact.ArtifactID,
		"artifact_state":         artifact,
		"province_id":            result.ProvinceID,
		"source_rel":             "map_data/provinces.png",
		"source_name":            sourceName,
		"output_path":            change.OutputPath,
		"width":                  change.Width,
		"height":                 change.Height,
		"recolored_pixel_count":  change.RecoloredCount,
		"recolored_per_province": change.PerProvince,
		"new_provinces":          change.NewProvinces,
		"files":                  plan.Files,
		"applied":                false,
		"guidance": []string{
			"The Mod is unchanged. A rewritten provinces.png is in the immutable artifact; copy it in only after comparing it with the original.",
			"The raster alone is not a split: apply the definition.csv, history, and landed-title edits in files at the same time, or CK3 will load provinces that no file describes.",
			"Re-scan afterwards so the index matches the new geometry before splitting anything else.",
		},
	}
	if len(plan.Warnings) > 0 {
		value["warnings"] = plan.Warnings
	}
	return toolOutput{Value: value, Visibility: visibility, Committed: true}, nil
}

func mapSplitPlanError(err error) error {
	var stale *indexer.MapSplitPlanStaleError
	if errors.As(err, &stale) {
		return newToolError(ErrorSourceChanged, "index_state", "the immutable split plan no longer matches the current indexed map", true,
			map[string]any{"reason": stale.Reason},
			map[string]any{
				"required_action": map[string]any{"tool": "map_split_province"},
				"guidance":        "Create and review a new split plan, then apply its new plan_id and plan_hash.",
			})
	}
	return err
}

func handleMapArtifact(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapArtifactArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	opts, visibility, err := args.options(0)
	if err != nil {
		return toolOutput{}, err
	}
	if err := requireSourceTrackedMapVisibility(visibility); err != nil {
		return toolOutput{}, err
	}
	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	cfg.ArtifactRoot = runtime.Config.ArtifactRoot
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "list":
		limit := opts.Limit
		if limit <= 0 {
			limit = 8
		}
		value, err := indexer.ListMapArtifacts(ctx, cfg, limit)
		return toolOutput{Value: value, Visibility: visibility}, err
	case "status", "inspect":
		if strings.TrimSpace(args.ArtifactID) == "" {
			return toolOutput{}, invalidArgument("artifact_id", "artifact_id is required for status or inspect")
		}
		value, err := indexer.InspectMapArtifact(ctx, cfg, args.ArtifactID)
		if err != nil {
			return toolOutput{}, invalidArgument("artifact_id", err.Error())
		}
		if strings.EqualFold(args.Operation, "status") {
			value.Files = nil
		}
		return toolOutput{Value: value, Visibility: visibility}, nil
	default:
		return toolOutput{}, invalidArgument("operation", "operation must be list, status, or inspect")
	}
}
