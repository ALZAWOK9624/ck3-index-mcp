package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"ck3-index/internal/packager"
)

func handlePackage(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3PackageArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	result, err := packager.Build(ctx, packager.Request{Metadata: args.Metadata, Files: args.Files}, packager.BuildOptions{
		ArtifactRoot: runtime.Config.ArtifactRoot,
		Retention:    time.Duration(runtime.Config.ArtifactRetentionHours) * time.Hour,
		Limits:       packager.MCPLimits,
		Validator:    packager.IndexerValidator{DB: runtime.DB},
	})
	if err != nil {
		return toolOutput{}, err
	}
	return toolOutput{Value: result, Visibility: "private"}, nil
}
