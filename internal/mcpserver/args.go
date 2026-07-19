package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"ck3-index/internal/indexer"
	"ck3-index/internal/migrator"
	"ck3-index/internal/packager"
)

type visibilityArgs struct {
	Limit        int    `json:"limit,omitempty"`
	Visibility   string `json:"visibility,omitempty"`
	Mode         string `json:"mode,omitempty"`
	PrivacyMode  string `json:"privacy_mode,omitempty"`
	AllowProject *bool  `json:"allow_project,omitempty"`
}

func (a visibilityArgs) normalizedVisibility() (string, error) {
	return a.normalizedVisibilityWithDomainMode(false)
}

func (a visibilityArgs) normalizedVisibilityWithDomainMode(allowDomainMode bool) (string, error) {
	visibility := strings.ToLower(strings.TrimSpace(a.Visibility))
	if visibility != "" && visibility != "private" && visibility != "public" {
		return "", fmt.Errorf("argument field %q received %q; expected one of [private public]", "visibility", a.Visibility)
	}
	if visibility == "" {
		privacyMode := strings.ToLower(strings.TrimSpace(a.PrivacyMode))
		if privacyMode != "" && privacyMode != "private" && privacyMode != "public" && privacyMode != "group" {
			return "", fmt.Errorf("argument field %q received %q; expected one of [private public group]", "privacy_mode", a.PrivacyMode)
		}
		legacyMode := privacyMode
		if legacyMode == "" {
			legacyMode = strings.ToLower(strings.TrimSpace(a.Mode))
			if legacyMode != "" && legacyMode != "private" && legacyMode != "public" && legacyMode != "group" {
				if !allowDomainMode {
					return "", fmt.Errorf("argument field %q received %q; expected one of [private public group]", "mode", a.Mode)
				}
				legacyMode = ""
			}
		}
		if legacyMode == "public" || (legacyMode == "group" && a.AllowProject != nil && !*a.AllowProject) {
			visibility = "public"
		} else {
			visibility = "private"
		}
	}
	return visibility, nil
}

func (a visibilityArgs) options(depth int) (indexer.LLMOptions, string, error) {
	return a.optionsWithDomainMode(depth, false)
}

func (a visibilityArgs) optionsWithDomainMode(depth int, allowDomainMode bool) (indexer.LLMOptions, string, error) {
	visibility, err := a.normalizedVisibilityWithDomainMode(allowDomainMode)
	if err != nil {
		return indexer.LLMOptions{}, "", err
	}
	opts := indexer.LLMOptions{Limit: a.Limit, Depth: depth, AllowProject: true}
	if visibility == "public" {
		opts.Mode = "public"
		opts.AllowProject = false
	}
	return opts, visibility, nil
}

func decodeToolArgs(raw json.RawMessage, schema map[string]any, compatibilityProperties []string, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := validateArguments(raw, schema, compatibilityProperties); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("arguments must contain exactly one JSON object")
	}
	return nil
}

type ck3SearchArgs struct {
	visibilityArgs
	Query      string `json:"query"`
	Kind       string `json:"kind,omitempty"`
	Source     string `json:"source,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
}

type ck3InspectArgs struct {
	visibilityArgs
	ID        string `json:"id"`
	Operation string `json:"operation,omitempty"`
	Source    string `json:"source,omitempty"`
	Base      string `json:"base,omitempty"`
}

type ck3ReviewArgs struct {
	visibilityArgs
	Files []indexer.PatchFileInput `json:"files,omitempty"`
}

type ck3WorkspaceArgs struct {
	visibilityArgs
	Operation string `json:"operation,omitempty"`
}

type ck3DependenciesArgs struct {
	visibilityArgs
	ID               string `json:"id"`
	Operation        string `json:"operation,omitempty"`
	Direction        string `json:"direction,omitempty"`
	Depth            int    `json:"depth,omitempty"`
	IncludeOnActions *bool  `json:"include_on_actions,omitempty"`
	Format           string `json:"format,omitempty"`
}

type ck3PrepareEditArgs struct {
	visibilityArgs
	ID        string `json:"id"`
	Operation string `json:"operation,omitempty"`
}

type ck3PreflightArgs struct {
	visibilityArgs
	Operation string                   `json:"operation"`
	ID        string                   `json:"id,omitempty"`
	Files     []indexer.PatchFileInput `json:"files,omitempty"`
}

type ck3ImpactArgs struct {
	visibilityArgs
	Files []indexer.PatchFileInput `json:"files"`
}

type ck3DiagnosticsArgs struct {
	visibilityArgs
	Operation  string `json:"operation,omitempty"`
	Code       string `json:"code,omitempty"`
	Source     string `json:"source,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

type ck3ScriptReferenceArgs struct {
	visibilityArgs
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type ck3HealthArgs struct{}

type ck3PackageArgs struct {
	Metadata packager.Metadata    `json:"metadata"`
	Files    []packager.FileInput `json:"files"`
}

type ck3GUIArgs struct {
	visibilityArgs
	Operation     string                                `json:"operation,omitempty"`
	Path          string                                `json:"path,omitempty"`
	PathPrefix    string                                `json:"path_prefix,omitempty"`
	Symbol        string                                `json:"symbol,omitempty"`
	Width         int                                   `json:"width,omitempty"`
	Height        int                                   `json:"height,omitempty"`
	Format        string                                `json:"format,omitempty"`
	HTMLMode      string                                `json:"html_mode,omitempty"`
	Language      string                                `json:"language,omitempty"`
	Samples       []indexer.GUIScenarioSample           `json:"sample_values,omitempty"`
	ModelSamples  []indexer.GUIModelSampleCollection    `json:"model_samples,omitempty"`
	RuntimeFacts  []indexer.GUIRuntimeFactInput         `json:"runtime_facts,omitempty"`
	ActionEffects []indexer.GUIRuntimeActionEffectInput `json:"action_effects,omitempty"`
}

type mapAssetAuditArgs struct {
	visibilityArgs
	Operation string `json:"operation,omitempty"`
}

type mapProvinceMappingArgs struct {
	indexer.MapProvinceMappingSpec
	visibilityArgs
}

type mapMigrationSnapshotArgs struct {
	Project string `json:"project"`
	Base    string `json:"base"`
}

type mapProvinceMigrationArgs struct {
	SnapshotID    string                    `json:"snapshot_id"`
	Target        string                    `json:"target"`
	OutputName    string                    `json:"output_name,omitempty"`
	ControlPoints []indexer.MapControlPoint `json:"control_points,omitempty"`
	Resolutions   []migrator.Resolution     `json:"resolutions,omitempty"`
	DeletePaths   []string                  `json:"delete_paths,omitempty"`
}

type mapProvinceInfoArgs struct {
	visibilityArgs
	ID   string `json:"id"`
	Year int    `json:"year,omitempty"`
}

type mapPhysicalContextArgs struct {
	indexer.MapPhysicalContextSpec
	visibilityArgs
}

type mapNeighborsArgs struct {
	visibilityArgs
	ID     string `json:"id"`
	Radius int    `json:"radius,omitempty"`
	Year   int    `json:"year,omitempty"`
}

type mapSpatialRelationArgs struct {
	visibilityArgs
	From string `json:"from"`
	To   string `json:"to"`
	Year int    `json:"year,omitempty"`
}

type mapStrategicPassagesArgs struct {
	visibilityArgs
	Target string `json:"target,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type mapTitleContextArgs struct {
	visibilityArgs
	ID   string `json:"id"`
	Year int    `json:"year,omitempty"`
}

type mapAssignmentPlanArgs struct {
	visibilityArgs
	Target         string `json:"target"`
	ID             string `json:"id,omitempty"`
	AssignmentMode string `json:"assignment_mode,omitempty"`
	Year           int    `json:"year,omitempty"`
}

func (a mapAssignmentPlanArgs) assignmentMode() string {
	if a.AssignmentMode != "" {
		return a.AssignmentMode
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode != "public" && mode != "group" && mode != "private" {
		return mode
	}
	return ""
}

type mapBuildingCandidatesArgs struct {
	visibilityArgs
	Target string `json:"target"`
	ID     string `json:"id,omitempty"`
	Year   int    `json:"year,omitempty"`
}

type mapRecipeCatalogArgs struct {
	visibilityArgs
	Depth int `json:"depth,omitempty"`
}

type mapBuildMetricArgs struct {
	indexer.MapMetricSpec
	visibilityArgs
}

type mapRouteArgs struct {
	indexer.MapRouteSpec
	Limit        int    `json:"limit,omitempty"`
	Visibility   string `json:"visibility,omitempty"`
	PrivacyMode  string `json:"privacy_mode,omitempty"`
	AllowProject *bool  `json:"allow_project,omitempty"`
}

func (a mapRouteArgs) options(depth int) (indexer.LLMOptions, string, error) {
	return visibilityArgs{Limit: a.Limit, Visibility: a.Visibility, PrivacyMode: a.PrivacyMode, AllowProject: a.AllowProject}.options(depth)
}

type mapRenderArgs struct {
	indexer.MapRenderSpec
	visibilityArgs
}
