package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ck3-index/internal/indexer"
)

// Every other map tool answers a question. These two produce a raster, which
// makes them the first map tools that write anything, so the boundary is drawn
// tightly: output lands in the server's artifact area under a fresh directory,
// never at a path the caller chose and never inside a configured source root.
// The caller reviews the result and copies it into the Mod themselves, which
// keeps the "MCP does not write the Mod" rule the map design states.

// mapEditArtifactDir reserves an empty directory for one edit. A fresh
// directory per call means a generated raster can never quietly replace the
// previous run's, which is the evidence a caller compares against.
func mapEditArtifactDir(runtime *Runtime, cfg indexer.Config, operation string) (string, error) {
	root := strings.TrimSpace(runtime.Config.ArtifactRoot)
	if root == "" {
		return "", newToolError(ErrorInvalidArguments, "index_state", "no artifact root is configured, so there is nowhere to write generated map rasters", false, nil,
			map[string]any{"guidance": "Set artifact_root in the ck3-index configuration, then retry."})
	}
	base := filepath.Join(root, "map-edits")
	// Check containment before creating anything: a misconfigured artifact root
	// pointing into the Mod must not leave a directory behind on its way to
	// being refused.
	if err := indexer.EnsureMapOutputOutsideSources(base, cfg.Sources); err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	// os.MkdirTemp rejects a pattern containing a path separator, so the
	// operation name cannot escape the artifact area even if it were unvalidated.
	pattern := fmt.Sprintf("%s-%s-*", time.Now().UTC().Format("20060102T150405Z"), operation)
	return os.MkdirTemp(base, pattern)
}

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

func handleMapTerrainEdit(_ context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapTerrainEditArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
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
		result, err = indexer.CreateMapTerrainEditArtifact(cfg, args.MapTerrainEditSpec)
	} else {
		result, err = indexer.PreviewMapTerrainEdit(cfg, args.MapTerrainEditSpec)
	}
	if err != nil {
		return toolOutput{}, mapEditError("", err)
	}
	return toolOutput{Value: result, Visibility: visibility}, nil
}

func handleMapApplySplit(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapApplySplitArgs
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
	if !args.Confirm {
		return toolOutput{}, invalidArgument("confirm",
			"this tool writes a new provinces.png; run map_split_province first, read its plan, then pass confirm=true")
	}
	weight := 3.0
	if args.TerrainWeight != nil {
		weight = *args.TerrainWeight
	}
	result, plan, err := runtime.DB.SplitProvince(ctx,
		indexer.MapSplitRequest{ProvinceID: args.ProvinceID, Seeds: args.Seeds, TerrainWeight: weight},
		indexer.MapSplitEmit{
			Definition:   args.EmitDefinition,
			History:      args.EmitHistory,
			LandedTitles: args.EmitLandedTitles,
		})
	if err != nil {
		var inputErr *indexer.MapSplitInputError
		if errors.As(err, &inputErr) {
			return toolOutput{}, invalidArgument("seeds", inputErr.Error())
		}
		return toolOutput{}, err
	}
	if len(plan.Blockers) > 0 {
		return toolOutput{}, newToolError(ErrorInvalidArguments, "validation",
			fmt.Sprintf("the split plan has %d unresolved blocker(s), so applying it would corrupt the map", len(plan.Blockers)), false,
			map[string]any{"blockers": plan.Blockers},
			map[string]any{"guidance": "Resolve every blocker reported by map_split_province, then retry."})
	}

	cfg, err := mapSourcesForVisibility(runtime, opts.AllowProject)
	if err != nil {
		return toolOutput{}, err
	}
	sourcePath, sourceName, err := indexer.ActiveMapAsset(cfg, "map_data/provinces.png")
	if err != nil {
		return toolOutput{}, err
	}
	dir, err := mapEditArtifactDir(runtime, cfg, "split")
	if err != nil {
		return toolOutput{}, err
	}
	// The output is laid out under its CK3-relative path so the directory can be
	// copied into a Mod wholesale; ApplyProvinceSplitToImage creates the parent.
	outputPath := filepath.Join(dir, "map_data", "provinces.png")
	change, err := indexer.ApplyProvinceSplitToImage(sourcePath, outputPath, result, plan)
	if err != nil {
		os.RemoveAll(dir)
		// A stale index is the usual cause and it is the caller's to fix by
		// re-scanning, so report the mismatch rather than an opaque failure.
		return toolOutput{}, newToolError(ErrorInvalidArguments, "index_state", err.Error(), true,
			map[string]any{"mismatches": change.Mismatches},
			map[string]any{"guidance": "Re-scan so the index matches provinces.png on disk, then retry."})
	}

	value := map[string]any{
		"operation":              "apply_split",
		"province_id":            result.ProvinceID,
		"source_rel":             "map_data/provinces.png",
		"source_name":            sourceName,
		"artifact_dir":           dir,
		"output_path":            change.OutputPath,
		"width":                  change.Width,
		"height":                 change.Height,
		"recolored_pixel_count":  change.RecoloredCount,
		"recolored_per_province": change.PerProvince,
		"new_provinces":          change.NewProvinces,
		"files":                  plan.Files,
		"applied":                false,
		"guidance": []string{
			"The Mod is unchanged. A rewritten provinces.png is in artifact_dir; copy it in only after comparing it with the original.",
			"The raster alone is not a split: apply the definition.csv, history, and landed-title edits in files at the same time, or CK3 will load provinces that no file describes.",
			"Re-scan afterwards so the index matches the new geometry before splitting anything else.",
		},
	}
	if len(plan.Warnings) > 0 {
		value["warnings"] = plan.Warnings
	}
	return toolOutput{Value: value, Visibility: visibility}, nil
}
