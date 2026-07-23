package mcpserver

import (
	"context"
	"strings"

	"ck3-index/internal/indexer"
)

type workspaceCapability struct {
	ID                      string                    `json:"id"`
	Domain                  string                    `json:"domain"`
	Source                  string                    `json:"source"`
	Tools                   []string                  `json:"tools"`
	Modes                   []string                  `json:"modes,omitempty"`
	Inputs                  []string                  `json:"inputs"`
	Outputs                 []string                  `json:"outputs"`
	Maturity                string                    `json:"maturity"`
	RequiresReadyIndex      bool                      `json:"requires_ready_index"`
	RequiresRuntimeLogs     bool                      `json:"requires_runtime_logs"`
	RequiresExternalProcess bool                      `json:"requires_external_process"`
	SideEffect              string                    `json:"side_effect"`
	Cost                    string                    `json:"cost"`
	SupportsCancellation    bool                      `json:"supports_cancellation"`
	Profile                 string                    `json:"profile"`
	Available               bool                      `json:"available"`
	Reason                  string                    `json:"reason,omitempty"`
	ModeDetails             []workspaceCapabilityMode `json:"mode_details,omitempty"`
}

// workspaceCapabilityMode keeps a capability query truthful when one operation
// on an otherwise available tool is intentionally unavailable.
type workspaceCapabilityMode struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func workspaceCapabilities(ctx context.Context, runtime *Runtime, requestedDomain string) (map[string]any, error) {
	state, err := runtime.DB.IndexState(ctx)
	if err != nil {
		return nil, err
	}
	bootstrapConfig := len(runtime.Config.Sources) == 0
	var refresh indexer.RefreshStatus
	if bootstrapConfig {
		// Capability discovery is intentionally available before init/first
		// scan. With no loaded source configuration there is no project state
		// to probe yet, but the full-refresh mode remains discoverable.
		refresh = indexer.RefreshStatus{
			Status:            "full_scan_required",
			Index:             state,
			EngineRules:       indexer.RefreshEngineStatus{Available: true, Current: true},
			NeedsFullScan:     true,
			FullScanAvailable: true,
		}
	} else {
		refresh, err = runtime.DB.RefreshStatus(ctx, runtime.Config)
		if err != nil {
			return nil, err
		}
	}
	refreshActivity := currentRefreshActivity(runtime)
	indexAvailable := state.Ready()
	indexReason := ""
	if !indexAvailable {
		indexReason = "a ready published index generation is required"
	}
	mapAvailable := runtime.DB.RequireMapDatabase(ctx) == nil
	mapReason := ""
	if !mapAvailable {
		mapReason = "the current index has no published map database"
	}
	gis := runtime.DB.GISSidecarStatus(ctx, runtime.Config)
	gisAvailable := mapAvailable && gis.Available && gis.AnalysisStatus == "ready"
	gisReason := ""
	if !gisAvailable {
		gisReason = "a published map database with verified GIS analysis is required"
	}
	filesAvailable := true
	filesReason := ""
	switch {
	case refreshActivity.Active:
		filesAvailable = false
		filesReason = "another refresh is already running for this index"
	case !state.Ready():
		filesAvailable = false
		filesReason = "a ready published index generation is required"
	case !refresh.Project.Refreshable:
		filesAvailable = false
		filesReason = "the configured project source is not accessible"
	case refresh.NeedsFullScan:
		filesAvailable = false
		filesReason = "a full scan is required before incremental refresh"
	}
	fullAvailable := true
	fullReason := ""
	switch {
	case refreshActivity.Active:
		fullAvailable = false
		fullReason = "another refresh is already running for this index"
	case !bootstrapConfig && !refresh.Project.Refreshable:
		fullAvailable = false
		fullReason = "the configured project source is not accessible"
	case !refresh.EngineRules.Available && strings.TrimSpace(runtime.Config.EngineLogs) != "":
		fullAvailable = false
		fullReason = "the configured engine rule bundle is unavailable"
	}
	items := []workspaceCapability{
		{ID: "semantic_search", Domain: "semantic", Source: "sqlite_index", Tools: []string{"ck3_search", "ck3_inspect"}, Inputs: []string{"query or exact id"}, Outputs: []string{"ranked evidence and resolved objects"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "low", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "workspace_overview", Domain: "workspace", Source: "sqlite_index", Tools: []string{"ck3_workspace"}, Modes: []string{"overview", "object_types", "on_action_evidence"}, Inputs: []string{"operation"}, Outputs: []string{"workspace structure and evidence"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "low", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "dependency_graph", Domain: "dependencies", Source: "semantic_graph", Tools: []string{"ck3_dependencies"}, Modes: []string{"neighborhood", "event_chain"}, Inputs: []string{"exact id and traversal options"}, Outputs: []string{"bounded graph and topology"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "medium", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "editing_evidence", Domain: "editing", Source: "parser_and_index", Tools: []string{"ck3_prepare_edit", "ck3_review", "ck3_preflight", "ck3_impact"}, Inputs: []string{"id or complete proposed files"}, Outputs: []string{"rules, diagnostics, and impact risks"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "medium", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "diagnostics", Domain: "diagnostics", Source: "sqlite_index", Tools: []string{"ck3_diagnostics"}, Modes: []string{"summary", "explain"}, Inputs: []string{"optional code and provenance filters"}, Outputs: []string{"cached diagnostic findings"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "low", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "script_reference", Domain: "script_reference", Source: "engine_logs_and_snapshot", Tools: []string{"ck3_script_reference"}, Inputs: []string{"reference kind and id"}, Outputs: []string{"engine or generated snapshot facts"}, Maturity: "stable", RequiresReadyIndex: false, SideEffect: "none", Cost: "low", Profile: "default", Available: true},
		{ID: "gui_inspection", Domain: "gui", Source: "sqlite_index", Tools: []string{"ck3_gui"}, Inputs: []string{"GUI operation, path, or symbol"}, Outputs: []string{"GUI structure and bounded previews"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "medium", Profile: "default", Available: indexAvailable, Reason: indexReason},
		{ID: "map_queries", Domain: "map", Source: "map_cache", Tools: []string{"map_province_info", "map_neighbors", "map_spatial_relation", "map_title_context", "map_route", "map_render"}, Inputs: []string{"province, title, or map query"}, Outputs: []string{"map context and rendered layers"}, Maturity: "stable", RequiresReadyIndex: true, SideEffect: "none", Cost: "medium", Profile: "default", Available: mapAvailable, Reason: mapReason},
		{ID: "map_gis", Domain: "map", Source: "verified_gis_cache", Tools: []string{"map_physical_context", "map_build_metric"}, Inputs: []string{"province or metric request"}, Outputs: []string{"terrain and hydrology-derived facts"}, Maturity: "experimental", RequiresReadyIndex: true, RequiresExternalProcess: true, SideEffect: "none", Cost: "high", Profile: "experimental", Available: gisAvailable, Reason: gisReason},
		{ID: "packaging", Domain: "packaging", Source: "artifact_builder", Tools: []string{"ck3_package"}, Inputs: []string{"metadata and complete file contents"}, Outputs: []string{"validated portable artifact"}, Maturity: "stable", RequiresReadyIndex: false, SideEffect: "temporary artifact only", Cost: "medium", Profile: "default", Available: true},
		{ID: "index_refresh", Domain: "workspace", Source: "transactional_indexer", Tools: []string{"ck3_refresh"}, Modes: []string{"status", "files", "full"}, Inputs: []string{"optional project-relative paths"}, Outputs: []string{"index readiness and refresh deltas"}, Maturity: "beta", RequiresReadyIndex: false, SideEffect: "updates rebuildable index cache only", Cost: "high", SupportsCancellation: true, Profile: "default", Available: true, ModeDetails: []workspaceCapabilityMode{
			{ID: "status", Available: true},
			{ID: "files", Available: filesAvailable, Reason: filesReason},
			{ID: "full", Available: fullAvailable, Reason: fullReason},
		}},
	}
	domain := strings.ToLower(strings.TrimSpace(requestedDomain))
	if domain == "" {
		domain = "all"
	}
	filtered := make([]workspaceCapability, 0, len(items))
	for _, item := range items {
		if domain == "all" || item.Domain == domain {
			filtered = append(filtered, item)
		}
	}
	return map[string]any{
		"operation":    "capabilities",
		"domain":       domain,
		"index_status": state.Status,
		"capabilities": filtered,
	}, nil
}
