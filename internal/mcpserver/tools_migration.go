package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"ck3-index/internal/migrator"
)

func handleMapMigrationSnapshot(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapMigrationSnapshotArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	result, err := migrator.CreateSnapshot(ctx, runtime.Config, migrator.SnapshotSpec{Project: args.Project, Base: args.Base})
	if err != nil {
		return toolOutput{}, err
	}
	return toolOutput{Value: result, Visibility: "private"}, nil
}

func handleMapProvinceMigration(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args mapProvinceMigrationArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	result, err := migrator.BuildMigration(ctx, runtime.Config, migrator.MigrationSpec{
		SnapshotID: args.SnapshotID, Target: args.Target, OutputName: args.OutputName, ControlPoints: args.ControlPoints,
		Resolutions: args.Resolutions, DeletePaths: args.DeletePaths,
	}, migrator.BuildOptions{ArtifactRoot: runtime.Config.ArtifactRoot, Retention: time.Duration(runtime.Config.ArtifactRetentionHours) * time.Hour, DB: runtime.DB})
	if err != nil {
		return toolOutput{}, err
	}
	if len(result.Files) > 100 {
		result.Files = result.Files[:100]
	}
	if len(result.Conflicts) > 100 {
		result.Conflicts = result.Conflicts[:100]
	}
	if len(result.Diagnostics) > 100 {
		result.Diagnostics = result.Diagnostics[:100]
	}
	return toolOutput{Value: result, Visibility: "private"}, nil
}
