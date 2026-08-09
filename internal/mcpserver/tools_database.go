package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
)

func handleDatabase(ctx context.Context, runtime *Runtime, definition *ToolDefinition, raw json.RawMessage) (toolOutput, error) {
	var args ck3DatabaseArgs
	if err := decodeToolArgs(raw, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		return toolOutput{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation == "" {
		operation = "list"
	}
	controller := runtime.DatabaseController
	if controller == nil {
		controller = staticMCPDatabaseController{identity: runtime.databaseIdentity()}
	}

	switch operation {
	case "list":
		active, databases := controller.Catalog()
		return toolOutput{Value: map[string]any{
			"active":    active,
			"databases": databases,
			"guidance": []string{
				"Choose only a configured database name from this list; never invent or submit a filesystem path.",
				"A switch affects subsequent calls. Calls already running remain bound to the database lease they started with.",
			},
		}, Visibility: "private"}, nil
	case "status":
		return toolOutput{Value: map[string]any{
			"active": controller.Current(),
			"guidance": []string{
				"Use operation=list to discover configured names, then operation=switch with one exact name when a different evidence base is needed.",
			},
		}, Visibility: "private"}, nil
	case "switch":
		name := strings.ToLower(strings.TrimSpace(args.Name))
		if name == "" {
			return toolOutput{}, missingArgument("name")
		}
		result, err := controller.Switch(ctx, name)
		return toolOutput{Value: result, Visibility: "private"}, err
	default:
		return toolOutput{}, unknownOperation(operation)
	}
}

type staticMCPDatabaseController struct {
	identity mcpDatabaseIdentity
}

func (controller staticMCPDatabaseController) Current() mcpDatabaseIdentity {
	return controller.identity
}

func (controller staticMCPDatabaseController) List() []mcpDatabaseSummary {
	_, databases := controller.Catalog()
	return databases
}

func (controller staticMCPDatabaseController) Catalog() (mcpDatabaseIdentity, []mcpDatabaseSummary) {
	return controller.identity, []mcpDatabaseSummary{{
		Name: controller.identity.Name, Mode: "primary", Active: true, Available: true,
		DatabaseIdentity: controller.identity.DatabaseIdentity,
		ConfigIdentity:   controller.identity.ConfigIdentity,
	}}
}

func (controller staticMCPDatabaseController) Switch(_ context.Context, name string) (mcpDatabaseSwitchResult, error) {
	if name == controller.identity.Name {
		return mcpDatabaseSwitchResult{
			Previous: controller.identity, Active: controller.identity, Changed: false, Status: "ready",
			Guidance: []string{"The requested database was already active; subsequent calls continue to use it."},
		}, nil
	}
	return mcpDatabaseSwitchResult{}, newToolError(ErrorDatabaseSwitchUnavailable, "database",
		"runtime database switching is available only through a running MCP session with an administrator-configured database catalog", false,
		map[string]any{"name": name, "available": []string{controller.identity.Name}},
		map[string]any{"tool": "ck3_database", "operation": "list"})
}
