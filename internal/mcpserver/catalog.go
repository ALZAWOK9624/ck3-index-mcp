package mcpserver

import (
	"encoding/json"

	"ck3-index/internal/indexer"
	"ck3-index/internal/packager"
)

var legacyPrivacyProperties = []string{"mode", "privacy_mode", "allow_project"}

func buildCanonicalTools() []ToolDefinition {
	annotations := readOnlyAnnotations()
	output := genericOutputSchema()
	definitions := []ToolDefinition{
		{
			Name:        "ck3_search",
			Title:       "Search CK3 Index",
			Description: "Search when the exact CK3 id is unknown. Returns ranked object, localization, resource, reference, diagnostic, datatype, and script-key evidence.",
			InputSchema: objectSchema(map[string]any{
				"query":       stringProperty("CK3 id, localized text, resource path, diagnostic code, or semantic prefix."),
				"kind":        stringProperty("Optional evidence category.", "object", "reference", "localization", "resource", "diagnostic", "script_key", "datatype"),
				"source":      stringProperty("Optional indexed source name."),
				"path_prefix": stringProperty("Optional source-root-relative path prefix."),
				"limit":       limitProperty(),
				"page":        pageProperty(),
				"visibility":  visibilityProperty(),
			}, "query"),
			OutputSchema: output, Annotations: annotations, Handler: handleSearch,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_inspect",
			Title:       "Inspect CK3 Identifier",
			Description: "Inspect one exact CK3 id, key, or resource path after discovery. Definition views include resolution status, override provenance, event fields, and character static/history profiles; reference views include relation, phase, confidence, and unresolved reasons. compare performs a bounded read-only source-versus-upstream object comparison for an exact typed id.",
			InputSchema: objectSchema(map[string]any{
				"id":         stringProperty("Exact CK3 id, key, or resource path."),
				"operation":  stringProperty("Inspection view.", "aggregate", "definition", "references", "localization", "resource", "context", "diagnose", "compare"),
				"source":     stringProperty("Optional configured higher-precedence source for operation=compare. Defaults to the configured project/highest-priority layer in private visibility."),
				"base":       stringProperty("Optional configured lower-precedence base source for operation=compare. Defaults to the nearest lower-precedence layer."),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}, "id"),
			OutputSchema: output, Annotations: annotations, Handler: handleInspect,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_review",
			Title:       "Review CK3 Files",
			Description: "Review complete proposed CK3 files, or current dirty project files when none are supplied. Performs read-only parser, scope, reference, localization, and resource checks.",
			InputSchema: objectSchema(map[string]any{
				"files":      patchFilesProperty(),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}),
			OutputSchema: output, Annotations: annotations, Handler: handleReview,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_workspace",
			Title:       "Inspect CK3 Workspace",
			Description: "Inspect indexed workspace structure before choosing a specific object. The overview includes object/ref hotspots, override causes, event relations, dynamic refs, and true unresolved refs. on_action_evidence is a bounded read-only reconciliation of live engine, the generated CK3 1.19 snapshot, and adjacent vanilla-comment root contracts.",
			InputSchema: objectSchema(map[string]any{
				"operation":  stringProperty("Workspace view.", "overview", "object_types", "on_action_evidence", "capabilities"),
				"domain":     stringProperty("Optional capability domain filter for operation=capabilities. Empty and all return every domain.", "all", "semantic", "diagnostics", "map", "gui", "script_reference", "editing", "workspace", "dependencies", "packaging"),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}),
			OutputSchema: output, Annotations: annotations, Handler: handleWorkspace,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_dependencies",
			Title:       "Trace CK3 Dependencies",
			Description: "Trace semantic dependencies around one CK3 id. Use neighborhood for the bounded general graph or event_chain for caller/callee topology with roots, leaves, cycles, shortest paths, and unresolved calls. event_chain can additionally return a self-contained CSP-contained interactive HTML inspector.",
			InputSchema: objectSchema(map[string]any{
				"id": map[string]any{
					"type":        "string",
					"maxLength":   512,
					"description": "Center object or referenced id. event_chain accepts event:<id>, on_action:<id>, or an untyped event id.",
				},
				"operation":          stringProperty("Dependency view. neighborhood defaults to at most two hops; event_chain supports up to six.", "neighborhood", "event_chain"),
				"direction":          stringProperty("event_chain traversal direction.", "both", "callers", "callees"),
				"include_on_actions": booleanProperty("Whether event_chain includes on_action nodes and edges. Defaults to true."),
				"depth":              integerProperty("Traversal depth. event_chain defaults to 3 and caps at 6; neighborhood defaults to 1 and caps at 2.", 1, 6, 0),
				"format":             stringProperty("Response representation. html is only available for event_chain and adds an interactive no-network HTML inspector alongside the structured topology.", "json", "html"),
				"limit":              limitProperty(),
				"visibility":         visibilityProperty(),
			}, "id"),
			OutputSchema: output, Annotations: annotations, Handler: handleDependencies,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_prepare_edit",
			Title:       "Prepare CK3 Edit",
			Description: "Load edit evidence before generating CK3 script. Defaults to combined context; operation can request examples, schema rules, or empirical patterns only.",
			InputSchema: objectSchema(map[string]any{
				"id":         stringProperty("Object id, object type, or type:term."),
				"operation":  stringProperty("Preparation view.", "context", "examples", "rules", "patterns"),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}, "id"),
			OutputSchema: output, Annotations: annotations, Handler: handlePrepareEdit,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_preflight",
			Title:       "Preflight CK3 Change",
			Description: "Run a read-only gate for an indexed subject, proposed complete files, or current dirty files. Select subject, patch, or dirty with operation.",
			InputSchema: objectSchema(map[string]any{
				"operation":  stringProperty("Preflight target.", "subject", "patch", "dirty"),
				"id":         stringProperty("Required for operation=subject."),
				"files":      patchFilesProperty(),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}, "operation"),
			OutputSchema: output, Annotations: annotations, Handler: handlePreflight,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_impact",
			Title:       "Analyze CK3 Patch Impact",
			Description: "Analyze proposed upsert, delete, and rename operations before editing. Returns read-only dependency and unresolved-reference risks.",
			InputSchema: objectSchema(map[string]any{
				"files":      patchFilesProperty(),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}, "files"),
			OutputSchema: output, Annotations: annotations, Handler: handleImpact,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:        "ck3_diagnostics",
			Title:       "Inspect CK3 Diagnostics",
			Description: "Inspect cached project diagnostics without rescanning. Defaults to summary; explain filters one diagnostic code and optional provenance fields.",
			InputSchema: objectSchema(map[string]any{
				"operation":   stringProperty("Diagnostic view.", "summary", "explain"),
				"code":        stringProperty("Required for operation=explain."),
				"source":      stringProperty("Optional diagnostic source."),
				"path_prefix": stringProperty("Optional source-root-relative path prefix."),
				"confidence":  stringProperty("Optional confidence filter."),
				"limit":       limitProperty(),
				"page":        pageProperty(),
				"visibility":  visibilityProperty(),
			}),
			OutputSchema: output, Annotations: annotations, Handler: handleDiagnostics,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:         "ck3_refresh",
			Title:        "Refresh CK3 Index",
			Description:  "Refresh the configured project source after source files change. status reports index readiness without mutation; files incrementally updates explicitly named source-root-relative project files; full rebuilds in a staged cache and atomically publishes only after it is ready, never silently substituting for files.",
			InputSchema:  refreshInputSchema(),
			OutputSchema: output, Annotations: artifactAnnotations(), Handler: handleRefresh,
		},
		{
			Name:        "ck3_script_reference",
			Title:       "Look Up CK3 Script Reference",
			Description: "Look up one local engine or script-rule fact. Select scope, datatype, shape, define, on_action, iterator, example, or modifier with kind; on_action responses keep live engine rules authoritative while adding bounded review-only vanilla-comment and structured CK3 1.19 snapshot evidence when available.",
			InputSchema: objectSchema(map[string]any{
				"kind":       stringProperty("Reference family.", "scope", "datatype", "shape", "define", "on_action", "iterator", "example", "modifier"),
				"id":         stringProperty("Engine or script key."),
				"limit":      limitProperty(),
				"visibility": visibilityProperty(),
			}, "kind", "id"),
			OutputSchema: output, Annotations: annotations, Handler: handleScriptReference,
			CompatibilityProperties: legacyPrivacyProperties,
		},
		{
			Name:         "ck3_health",
			Title:        "Check CK3 Index Health",
			Description:  "Check whether the database, schema, indexes, and MCP registration are trustworthy. Returns a short path-redacted health report.",
			InputSchema:  objectSchema(map[string]any{}),
			OutputSchema: output, Annotations: annotations, Handler: handleHealth,
		},
		{
			Name:         "ck3_package",
			Title:        "Package CK3 Mod",
			Description:  "Validate proposed CK3 text and binary files, generate canonical descriptors, and create a portable manual-install ZIP in the configured temporary artifact root. Does not install or modify a live mod directory.",
			InputSchema:  packageInputSchema(),
			OutputSchema: output, Annotations: artifactAnnotations(), Handler: handlePackage,
		},
		{
			Name:        "ck3_gui",
			Title:       "Inspect CK3 GUI",
			Description: "Inspect active CK3 GUI files through the existing index. Summarize the workspace, parse one file, resolve cross-file custom type/template dependencies, or render a bounded diagnostic PNG and/or self-contained HTML preview. HTML supports tree browsing, clipped scrollbox and grid navigation, indexed dynamic-texture samples, and controlled visual behavior simulation. model_samples can instantiate bounded caller-provided datamodel rows from one exact item template; runtime_facts and action_effects never execute arbitrary Jomini code.",
			InputSchema: objectSchema(map[string]any{
				"operation":      stringProperty("GUI view.", "summary", "file", "type", "template", "preview"),
				"path":           stringProperty("Source-root-relative gui/*.gui path for operation=file."),
				"path_prefix":    stringProperty("Optional source-root-relative gui/ prefix that scopes symbol selection. Type/template dependencies still resolve against every active GUI file; responses report files and resolution_files separately."),
				"symbol":         stringProperty("Exact custom type, template, or named GUI element for preview; type/template operations keep their narrower meaning."),
				"width":          integerProperty("Optional preview width in pixels.", 64, indexer.GUIPreviewMaxWidth, 0),
				"height":         integerProperty("Optional preview height in pixels.", 64, indexer.GUIPreviewMaxHeight, 0),
				"format":         stringProperty("Preview representation. png preserves the legacy response; html returns a standalone document; both returns both.", "png", "html", "both"),
				"html_mode":      stringProperty("HTML behavior. static is script-free; inspector adds a fixed CSP-hashed tree, zoom, search, clipped scrollbox navigation, property inspector, and visual-state simulator. Only valid with format=html or both.", indexer.GUIHTMLModeStatic, indexer.GUIHTMLModeInspector),
				"language":       stringProperty("Initial GUI localization view. raw preserves script keys; English, Simplified Chinese, and bilingual values come only from the active localization index. The inspector can switch among embedded variants without network access.", indexer.GUIPreviewLanguageRaw, indexer.GUIPreviewLanguageEnglish, indexer.GUIPreviewLanguageSimpChinese, indexer.GUIPreviewLanguageBilingual),
				"sample_values":  guiScenarioSamplesProperty(),
				"model_samples":  guiModelSamplesProperty(),
				"runtime_facts":  guiRuntimeFactsProperty(),
				"action_effects": guiRuntimeActionEffectsProperty(),
				"limit":          limitProperty(),
				"visibility":     visibilityProperty(),
			}),
			OutputSchema: output, Annotations: annotations, Handler: handleGUI,
			CompatibilityProperties: legacyPrivacyProperties,
		},
	}
	definitions = append(definitions, buildMigrationTools(output)...)
	definitions = append(definitions, buildCanonicalMapTools(annotations, output)...)
	definitions = addResponseBudgetProperty(definitions)
	return standardizeCanonicalToolDescriptions(definitions)
}

func refreshInputSchema() map[string]any {
	operation := stringProperty("Refresh operation. status is the default and does not change the index; files incrementally refreshes named project files; full stages and atomically publishes an explicit full scan.", "status", "files", "full")
	operation["default"] = "status"
	return objectSchema(map[string]any{
		"operation": operation,
		"paths": map[string]any{
			"type":        "array",
			"minItems":    1,
			"maxItems":    64,
			"description": "Source-root-relative project paths for operation=files. Absolute paths and parent traversal are rejected.",
			"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
		},
	})
}

type catalogToolBoundary struct {
	When     string
	DoNotUse string
	Input    string
	Returns  string
	Unlike   string
}

func standardizeCanonicalToolDescriptions(definitions []ToolDefinition) []ToolDefinition {
	boundaries := map[string]catalogToolBoundary{
		"ck3_search": {
			When:     "the exact CK3 identifier, key, path, or object type is unknown",
			DoNotUse: "an exact typed identifier is already known; use ck3_inspect instead",
			Input:    "a text query with optional evidence kind, source, and path prefix",
			Returns:  "ranked indexed evidence and candidate identifiers",
			Unlike:   "ck3_inspect, it discovers candidates instead of resolving one exact subject",
		},
		"ck3_inspect": {
			When:     "an exact existing identifier, key, or indexed resource is known",
			DoNotUse: "the content is an unsaved proposal; use ck3_review instead",
			Input:    "one exact id plus an optional inspection operation",
			Returns:  "resolved definitions, references, localization, resources, or comparison evidence",
			Unlike:   "ck3_search, it is precise rather than candidate discovery",
		},
		"ck3_review": {
			When:     "diagnosing complete proposed CK3 files or the current project files",
			DoNotUse: "only a single indexed identifier needs inspection; use ck3_inspect instead",
			Input:    "complete proposed file contents when available, or no files for current dirty files",
			Returns:  "exploratory parser, scope, reference, localization, and resource findings",
			Unlike:   "ck3_preflight, it diagnoses rather than acting as a final pass/fail gate",
		},
		"ck3_workspace": {
			When:     "the question concerns workspace-wide structure, indexed object types, supported capabilities, or on_action evidence",
			DoNotUse: "an exact object or dependency graph is already the target; use ck3_inspect or ck3_dependencies instead",
			Input:    "a workspace operation and, for capabilities, an optional domain filter",
			Returns:  "bounded workspace facts, capability availability, or on_action reconciliation evidence",
			Unlike:   "ck3_search, it summarizes index-wide facts rather than finding one id",
		},
		"ck3_dependencies": {
			When:     "the semantic neighborhood or event topology around one known id is needed",
			DoNotUse: "the id itself is still unknown; use ck3_search first",
			Input:    "one event, on_action, or object id with traversal options",
			Returns:  "bounded callers, callees, paths, cycles, and unresolved dependency evidence",
			Unlike:   "ck3_impact, it describes existing graph relations rather than a proposed change risk",
		},
		"ck3_prepare_edit": {
			When:     "creating or modifying CK3 content and relevant examples, rules, or conventions are needed first",
			DoNotUse: "completed files need validation; use ck3_review or ck3_preflight instead",
			Input:    "an object id, type, or type-qualified term and an optional evidence operation",
			Returns:  "examples, schema rules, engine evidence, and project conventions",
			Unlike:   "ck3_review, it prepares authoring and does not validate completed content",
		},
		"ck3_preflight": {
			When:     "a final pass/fail gate is needed before accepting, applying, packaging, or publishing a change",
			DoNotUse: "the task is exploratory diagnosis; use ck3_review or ck3_inspect first",
			Input:    "a subject id, complete proposed files, or the explicit dirty-project operation",
			Returns:  "a bounded release-oriented validation result and blocking findings",
			Unlike:   "ck3_review, it is the final gate rather than a broad diagnostic pass",
		},
		"ck3_impact": {
			When:     "deleting, renaming, replacing, or substantially changing an existing object",
			DoNotUse: "only syntax or proposed-file validation is needed; use ck3_review instead",
			Input:    "complete proposed upsert, delete, or rename file operations",
			Returns:  "dependency, compatibility, and unresolved-reference risks before editing",
			Unlike:   "ck3_preflight, it models change impact rather than general acceptance",
		},
		"ck3_diagnostics": {
			When:     "reading diagnostics already produced by the current index generation",
			DoNotUse: "project source files changed since the last scan; use ck3_refresh first",
			Input:    "an optional diagnostic operation, code, source, path prefix, and confidence filter",
			Returns:  "cached diagnostic summaries or explained findings",
			Unlike:   "ck3_review, it does not parse new proposed content",
		},
		"ck3_refresh": {
			When:     "configured project source files changed and the index must reflect them",
			DoNotUse: "editing source content itself; refresh never writes Mod source files",
			Input:    "status, files with source-root-relative project paths, or an explicit full request",
			Returns:  "index readiness, generation, incremental change counts, diagnostics delta, and full-scan requirements",
			Unlike:   "ck3_diagnostics, it updates index evidence instead of only reading cached findings",
		},
		"ck3_script_reference": {
			When:     "a local CK3 engine or script-rule fact needs authoritative indexed evidence",
			DoNotUse: "the question is about a project object definition or dependency; use ck3_inspect or ck3_dependencies instead",
			Input:    "a reference family and one engine or script key",
			Returns:  "scope, datatype, shape, define, on_action, iterator, example, or modifier evidence",
			Unlike:   "ck3_prepare_edit, it retrieves one reference fact rather than combined authoring context",
		},
		"ck3_health": {
			When:     "checking whether the MCP registration and index database are trustworthy",
			DoNotUse: "the task is to diagnose CK3 source logic; use ck3_diagnostics or ck3_review instead",
			Input:    "no arguments",
			Returns:  "a path-redacted database, schema, and registration health report",
			Unlike:   "ck3_refresh, it observes health and does not update the index",
		},
		"ck3_package": {
			When:     "a validated set of Mod files must be packaged into a portable installation artifact",
			DoNotUse: "the goal is to modify a live Mod directory or merely inspect source; use review tools instead",
			Input:    "package metadata and complete text or binary file contents",
			Returns:  "validated descriptors and a temporary portable ZIP artifact",
			Unlike:   "ck3_preflight, it creates a controlled artifact after validation rather than only reporting a gate",
		},
		"ck3_gui": {
			When:     "inspecting indexed GUI structure, dependencies, or a bounded static preview",
			DoNotUse: "the task requires executing arbitrary Jomini UI code or a live game UI session",
			Input:    "a GUI operation with an optional indexed path, symbol, and bounded preview samples",
			Returns:  "GUI structure, cross-file resolution, or static preview artifacts",
			Unlike:   "ck3_inspect, it understands GUI syntax and visual layout-specific relationships",
		},
		"map_migration_snapshot": {
			When:     "an upstream map update needs a durable old-upstream/project migration baseline",
			DoNotUse: "only current map evidence is needed; use the read-only map tools instead",
			Input:    "configured project and old-upstream source names",
			Returns:  "a content-addressed migration snapshot in the controlled artifact area",
			Unlike:   "map_province_mapping, it persists a baseline rather than calculating one raster comparison",
		},
		"map_province_migration": {
			When:     "a previously captured map migration snapshot must be replayed against a configured new upstream",
			DoNotUse: "the mapping is still being investigated; use map_province_mapping first",
			Input:    "a snapshot id, configured target, and explicit controls or conflict resolutions",
			Returns:  "a conservative local test fork and migration validation evidence",
			Unlike:   "map_migration_snapshot, it performs the controlled replay rather than capturing the baseline",
		},
		"map_asset_audit": {
			When:     "checking active map raster, province-definition, or river-topology integrity",
			DoNotUse: "the task is to compare a specific province's political context; use map_province_info instead",
			Input:    "an optional asset-audit operation",
			Returns:  "read-only asset coverage, encoding, palette, and topology findings",
			Unlike:   "map_physical_context, it audits source assets rather than describing a selected geography",
		},
		"map_province_mapping": {
			When:     "two configured province-map versions must be compared for migration evidence",
			DoNotUse: "only one active map or one province needs inspection; use map_province_info instead",
			Input:    "configured source and target maps plus optional control points and mapping thresholds",
			Returns:  "auditable overlap shares and province split, merge, renumber, or unmapped groups",
			Unlike:   "map_province_migration, it only calculates comparison evidence and writes no converter output",
		},
		"map_province_info": {
			When:     "one province or title's exact map, title, terrain, and neighbor context is needed",
			DoNotUse: "a multi-hop topology question is needed; use map_neighbors instead",
			Input:    "one province or landed-title selector and an optional history year",
			Returns:  "read-only geometry, titles, terrain, textures, and direct boundary context",
			Unlike:   "map_physical_context, it begins with one exact administrative subject",
		},
		"map_physical_context": {
			When:     "terrain, elevation, hydrology, oceanography, materials, or physical barriers must be analyzed",
			DoNotUse: "only title holders or culture/faith history is needed; use map_title_context instead",
			Input:    "a target selector family, one or more targets, and a physical-context operation",
			Returns:  "separated observed, derived, and inferred physical-geography evidence",
			Unlike:   "map_province_info, it supports regional and physical analysis beyond one province record",
		},
		"map_neighbors": {
			When:     "a bounded geographic neighborhood around a province or title is needed",
			DoNotUse: "only a pairwise spatial comparison is needed; use map_spatial_relation instead",
			Input:    "one map subject, a bounded radius, and an optional year",
			Returns:  "neighbor groups, directions, distances, and boundary classifications",
			Unlike:   "map_strategic_passages, it traverses general adjacency rather than explicit special links",
		},
		"map_spatial_relation": {
			When:     "the exact spatial relation between two selected map subjects is needed",
			DoNotUse: "a route or a full neighborhood is needed; use map_route or map_neighbors instead",
			Input:    "two province or title selectors and an optional year",
			Returns:  "centroid delta, bearing, distance, direct-border, and barrier evidence",
			Unlike:   "map_neighbors, it compares one pair instead of traversing a neighborhood",
		},
		"map_strategic_passages": {
			When:     "explicit straits, crossings, gateways, or other special passages are relevant",
			DoNotUse: "ordinary pixel-border adjacency is sufficient; use map_neighbors instead",
			Input:    "an optional target and special-passage kind filter",
			Returns:  "read-only explicit passage evidence separated from ordinary neighbors",
			Unlike:   "map_neighbors, it only covers authored or classified special links",
		},
		"map_title_context": {
			When:     "one landed title's province coverage, holder, culture, faith, and neighbors are needed",
			DoNotUse: "the question is purely physical terrain or water; use map_physical_context instead",
			Input:    "one landed-title id and an optional history year",
			Returns:  "read-only historical, administrative, and visual title context",
			Unlike:   "map_province_info, it is title-centered rather than province-centered",
		},
		"map_assignment_plan": {
			When:     "review-only religion or placeholder-character assignment recommendations are needed",
			DoNotUse: "a live source edit should be performed; this tool never applies a patch",
			Input:    "a province or title target, assignment mode, and optional year",
			Returns:  "auditable recommendations and private patch previews when allowed",
			Unlike:   "ck3_package, it plans assignments and produces no installation artifact",
		},
		"map_building_candidates": {
			When:     "ranking special-building candidates for one province or landed title",
			DoNotUse: "a general physical map report is enough; use map_physical_context instead",
			Input:    "a province or title target and optional history year",
			Returns:  "ranked terrain, holding, water, culture, and border evidence",
			Unlike:   "map_assignment_plan, it ranks building suitability rather than assignment recommendations",
		},
		"map_recipe_catalog": {
			When:     "choosing supported map metric or render recipes before constructing a request",
			DoNotUse: "a concrete metric is already specified; use map_build_metric instead",
			Input:    "no required arguments",
			Returns:  "supported levels, transforms, layers, palettes, and guidance",
			Unlike:   "map_build_metric, it lists capabilities without calculating values",
		},
		"map_build_metric": {
			When:     "an auditable indexed or source-noted map metric must be calculated before rendering",
			DoNotUse: "only an existing recipe list is needed; use map_recipe_catalog instead",
			Input:    "a bounded metric specification, values, transforms, and output limits",
			Returns:  "metric values, quantiles, outliers, provenance, and warnings",
			Unlike:   "map_render, it calculates data rather than rendering an atlas image",
		},
		"map_route": {
			When:     "a deterministic legal land, sea, or mixed route between two map subjects is needed",
			DoNotUse: "only direct spatial relation is needed; use map_spatial_relation instead",
			Input:    "origin, destination, route mode, and bounded routing options",
			Returns:  "route points, legs, corridor context, diagnostics, and distance caveats",
			Unlike:   "map_neighbors, it solves one constrained path rather than listing local adjacency",
		},
		"map_render": {
			When:     "a read-only CK3 map visualization is needed from indexed map data",
			DoNotUse: "only raw metric data is needed; use map_build_metric instead",
			Input:    "a bounded render specification, layers, styling, and optional route context",
			Returns:  "structured render metadata and an in-memory PNG artifact",
			Unlike:   "map_build_metric, it renders a visualization instead of calculating the underlying metric alone",
		},
	}
	for i := range definitions {
		boundary, ok := boundaries[definitions[i].Name]
		if !ok {
			boundary = catalogToolBoundary{
				When:     "the documented capability is the narrowest source of evidence or computation for the task",
				DoNotUse: "a more specific canonical tool applies",
				Input:    "the documented JSON schema",
				Returns:  "the documented bounded result",
				Unlike:   "unrelated canonical tools, it serves one atomic capability",
			}
		}
		definitions[i].Description = "Use when " + boundary.When + ". Do not use " + boundary.DoNotUse + ". Input: " + boundary.Input + ". Returns: " + boundary.Returns + ". Unlike " + boundary.Unlike + ". " + definitions[i].Description
	}
	return definitions
}

func guiRuntimeFactsProperty() map[string]any {
	property := arrayProperty("Optional caller-provided atomic facts for bounded And/Or/Not and comparison evaluation. Facts are labeled provided rather than observed; missing facts remain unknown.", objectSchema(map[string]any{
		"expression": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Exact atomic GUI expression, engine property, or scope path."},
		"value": map[string]any{"oneOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{"type": "number"},
			map[string]any{"type": "string", "maxLength": 512},
		}, "description": "Provided atomic boolean, number, or string value."},
	}, "expression", "value"))
	property["maxItems"] = indexer.GUIRuntimeMaxFacts
	return property
}

func guiRuntimeActionEffectsProperty() map[string]any {
	update := objectSchema(map[string]any{
		"expression": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Exact atomic fact expression to update after the matching click."},
		"operation":  stringProperty("Declarative update operation. toggle requires a known boolean value; set requires value.", "set", "toggle"),
		"value": map[string]any{"oneOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{"type": "number"},
			map[string]any{"type": "string", "maxLength": 512},
		}, "description": "Required for set and forbidden for toggle."},
	}, "expression", "operation")
	updates := arrayProperty("Bounded fact updates applied atomically by the HTML simulator.", update)
	updates["minItems"] = 1
	updates["maxItems"] = indexer.GUIRuntimeMaxActionUpdates
	effect := objectSchema(map[string]any{
		"expression": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Exact unsupported onclick expression, including its Jomini call shape."},
		"updates":    updates,
	}, "expression", "updates")
	property := arrayProperty("Optional caller-provided postconditions for exact unsupported onclick expressions. The expression is never executed; only typed fact updates are replayed and labeled provided.", effect)
	property["maxItems"] = indexer.GUIRuntimeMaxActionEffects
	return property
}

func guiScenarioSamplesProperty() map[string]any {
	property := arrayProperty("Optional caller-provided example results for exact GUI expressions. Values are labeled provided, never observed, and unmatched expressions are reported. Texture samples must name an indexed source-root-relative gfx asset.", objectSchema(map[string]any{
		"property":   stringProperty("GUI property whose expression result is sampled.", indexer.GUIScenarioPropertyText, indexer.GUIScenarioPropertyTexture, indexer.GUIScenarioPropertyVisible, indexer.GUIScenarioPropertyEnabled),
		"expression": map[string]any{"type": "string", "maxLength": 512, "description": "Exact indexed GUI expression or localized text key to match."},
		"value":      map[string]any{"type": "string", "maxLength": 512, "description": "Example display text, indexed source-root-relative gfx path for texture, or true/false for visible and enabled."},
	}, "property", "expression", "value"))
	property["maxItems"] = indexer.GUIScenarioMaxSamples
	return property
}

func guiModelSamplesProperty() map[string]any {
	sample := objectSchema(map[string]any{
		"property":   stringProperty("Row-local GUI property whose exact expression result is sampled.", indexer.GUIScenarioPropertyText, indexer.GUIScenarioPropertyTexture, indexer.GUIScenarioPropertyVisible, indexer.GUIScenarioPropertyEnabled),
		"expression": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Exact expression inside the selected item template."},
		"value":      map[string]any{"type": "string", "maxLength": 512, "description": "Provided row text, indexed source-root-relative gfx path for texture, or true/false for visible and enabled."},
	}, "property", "expression", "value")
	samples := arrayProperty("Exact row-local expression samples. Values remain isolated to this cloned row.", sample)
	samples["minItems"] = 1
	samples["maxItems"] = indexer.GUIModelSamplesMaxSamples
	row := objectSchema(map[string]any{
		"id":      map[string]any{"type": "string", "minLength": 1, "maxLength": indexer.GUIModelSamplesMaxIDLength, "description": "Caller-stable row id shown in the inspector and click log."},
		"samples": samples,
	}, "id", "samples")
	rows := arrayProperty("Bounded caller-provided rows instantiated from the selected grid item template.", row)
	rows["minItems"] = 1
	rows["maxItems"] = indexer.GUIModelSamplesMaxRows
	collection := objectSchema(map[string]any{
		"target":    map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Optional exact grid name, or the preview symbol for an unnamed root grid."},
		"datamodel": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Optional exact datamodel expression on the grid."},
		"rows":      rows,
	}, "rows")
	collection["anyOf"] = []any{
		map[string]any{"required": []string{"target"}},
		map[string]any{"required": []string{"datamodel"}},
	}
	property := arrayProperty("Optional bounded datamodel row samples. Each collection must exactly select one fixedgridbox or dynamicgridbox with one item template. At most 32 rows are accepted across all collections.", collection)
	property["maxItems"] = indexer.GUIModelSamplesMaxCollections
	return property
}

func buildMigrationTools(output map[string]any) []ToolDefinition {
	controlPoint := objectSchema(map[string]any{
		"source_x": map[string]any{"type": "number"}, "source_y": map[string]any{"type": "number"},
		"target_x": map[string]any{"type": "number"}, "target_y": map[string]any{"type": "number"},
	}, "source_x", "source_y", "target_x", "target_y")
	resolution := objectSchema(map[string]any{
		"conflict_id":       stringProperty("Stable conflict id returned by a previous migration attempt."),
		"source_province":   integerProperty("Optional old province id for a reusable province resolution.", 1, 0, 0),
		"action":            stringProperty("Explicit conflict action.", "select_target", "expand", "prefer_project", "prefer_target", "drop"),
		"target_provinces":  map[string]any{"type": "array", "maxItems": 128, "items": map[string]any{"type": "integer", "minimum": 1}},
		"allow_type_change": booleanProperty("Acknowledge an intentional land-water type change."),
	}, "action")
	resolution["anyOf"] = []any{map[string]any{"required": []string{"conflict_id"}}, map[string]any{"required": []string{"source_province"}}}
	return []ToolDefinition{
		{Name: "map_migration_snapshot", Title: "Snapshot CK3 Map Migration Baseline",
			Description:  "Persist a content-addressed old-upstream/current-project baseline from configured private sources before an upstream map update. Does not accept client paths or modify either source.",
			InputSchema:  objectSchema(map[string]any{"project": stringProperty("Configured current Mod source name."), "base": stringProperty("Configured old-upstream source name.")}, "project", "base"),
			OutputSchema: output, Annotations: artifactAnnotations(), Handler: handleMapMigrationSnapshot},
		{Name: "map_province_migration", Title: "Migrate CK3 Mod Across Map Versions",
			Description: "Build a complete local test fork from a new configured upstream, replay project changes by conservative three-way merge, rewrite proven province references, and publish only after strict validation. Writes only the configured artifact area.",
			InputSchema: objectSchema(map[string]any{
				"snapshot_id": stringProperty("Persistent id returned by map_migration_snapshot."), "target": stringProperty("Configured new-upstream source name."),
				"output_name":    stringProperty("Optional safe directory name for the local test fork."),
				"control_points": map[string]any{"type": "array", "minItems": 3, "maxItems": 128, "items": controlPoint},
				"resolutions":    map[string]any{"type": "array", "maxItems": 4096, "items": resolution},
				"delete_paths":   map[string]any{"type": "array", "maxItems": 1024, "items": stringProperty("Explicit source-root-relative path to omit from the fork.")},
			}, "snapshot_id", "target"), OutputSchema: output, Annotations: artifactAnnotations(), Handler: handleMapProvinceMigration},
	}
}

func packageInputSchema() map[string]any {
	metadata := objectSchema(map[string]any{
		"name":              stringProperty("Human-readable mod name."),
		"slug":              stringProperty("Portable lowercase ASCII directory id matching [a-z0-9][a-z0-9_-]{2,63}."),
		"version":           stringProperty("Mod version."),
		"supported_version": stringProperty("Supported CK3 version or wildcard."),
		"tags": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 32,
			"items": stringProperty("Launcher tag."),
		},
		"kind": stringProperty("Package kind. replace_paths require total_conversion; submod requires dependencies.", "addon", "submod", "total_conversion"),
		"dependencies": map[string]any{
			"type": "array", "maxItems": 64, "items": stringProperty("Exact launcher dependency name."),
		},
		"replace_paths": map[string]any{
			"type": "array", "maxItems": 128, "items": stringProperty("Explicit total-conversion replace_path."),
		},
	}, "name", "slug", "version", "supported_version", "tags")
	base64MaxLength := ((packager.MCPLimits.MaxFileBytes + 2) / 3) * 4
	file := objectSchema(map[string]any{
		"path": map[string]any{
			"type": "string", "minLength": 1, "maxLength": 1024,
			"description": "Path relative to the mod folder.",
		},
		"content": map[string]any{
			"type": "string", "maxLength": packager.MCPLimits.MaxFileBytes,
			"description": "Complete UTF-8 text content. Decoded UTF-8 bytes are limited to 8 MiB per file and 32 MiB per package.",
		},
		"content_base64": map[string]any{
			"type": "string", "maxLength": base64MaxLength,
			"description": "Base64-encoded binary content. Decoded bytes are limited to 8 MiB per file and 32 MiB per package.",
		},
	}, "path")
	file["oneOf"] = []any{map[string]any{"required": []string{"content"}}, map[string]any{"required": []string{"content_base64"}}}
	return objectSchema(map[string]any{
		"metadata": metadata,
		"files": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 256, "items": file,
		},
	}, "metadata", "files")
}

func buildCanonicalMapTools(annotations ToolAnnotations, output map[string]any) []ToolDefinition {
	mapLevels := append([]string(nil), indexer.MapRecipeCatalog().Levels...)
	controlPoint := objectSchema(map[string]any{
		"source_x": map[string]any{"type": "number", "description": "Source-map pixel X coordinate."},
		"source_y": map[string]any{"type": "number", "description": "Source-map pixel Y coordinate."},
		"target_x": map[string]any{"type": "number", "description": "Matching target-map pixel X coordinate."},
		"target_y": map[string]any{"type": "number", "description": "Matching target-map pixel Y coordinate."},
	}, "source_x", "source_y", "target_x", "target_y")
	controlPoints := arrayProperty("Optional geographic control-point pairs used to build a piecewise-affine warp.", controlPoint)
	controlPoints["minItems"] = 3
	controlPoints["maxItems"] = 128
	return []ToolDefinition{
		mapTool("map_asset_audit", "Audit CK3 Map Assets", "Audit active CK3 map rasters for province-definition coverage, PNG encoding, river palette-index semantics, and orthogonal river topology. This absorbs AzgaarToCK3's distinctive validators without duplicating ck3-index parsing or geometry.", objectSchema(map[string]any{
			"operation": stringProperty("Asset family to audit.", "summary", "provinces", "rivers"), "limit": limitProperty(), "visibility": visibilityProperty(),
		}), handleMapAssetAudit, legacyPrivacyProperties),
		mapTool("map_province_mapping", "Compare Province Map Versions", "Compare two configured CK3 province rasters through a Delaunay piecewise-affine warp. Returns auditable overlap shares and one-to-one, renumber, split, merge, complex, and unmapped groups without writing converter files.", objectSchema(map[string]any{
			"source":            stringProperty("Configured source-map name, or active."),
			"target":            stringProperty("Configured target-map name, or active."),
			"control_points":    controlPoints,
			"min_share":         map[string]any{"type": "number", "minimum": 0.000001, "maximum": 1, "default": 0.05, "description": "Minimum source or target overlap share retained as a mapping edge."},
			"max_candidates":    integerProperty("Maximum target candidates returned per source province.", 1, 20, 5),
			"allow_cross_water": booleanProperty("Allow land provinces to map to water provinces and vice versa."),
			"limit":             limitProperty(),
			"visibility":        visibilityProperty(),
		}, "source", "target"), handleMapProvinceMapping, legacyPrivacyProperties),
		mapTool("map_province_info", "Inspect Map Province", "Inspect one province's exact geometry, titles, scripted terrain, observed surface-material blend, texture resources, and direct boundaries. Returns read-only precision context and classified neighbors.", objectSchema(map[string]any{
			"id": stringProperty("Map subject: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name."), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "id"), handleMapProvinceInfo, legacyPrivacyProperties),
		mapTool("map_physical_context", "Inspect Physical Geography", "Inspect normalized elevation, terrain, observed gfx/map/terrain surface-material blends and texture resources, composite rivers, water bodies, relative bathymetry, and physical barriers without modifying map assets. Region coast queries can include adjacent water in one bounded cached-database call. Observed, derived, and inferred facts remain explicitly separated.", objectSchema(map[string]any{
			"target_type":            stringProperty("Target selector family.", "province", "title", "region", "targets", "all"),
			"target":                 stringProperty("One numeric province id, landed-title id, region:<id>, exact region id with target_type=region, or all."),
			"targets":                map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "items": map[string]any{"type": "string"}, "description": "Up to 16 province, title, or region:<id> targets."},
			"operation":              stringProperty("Physical geography view. surface returns observed material blend weights plus configured mask and DDS resources without requiring WhiteboxTools.", "summary", "terrain", "surface", "hydrology", "oceanography", "barriers"),
			"include_adjacent_water": booleanProperty("Include a bounded coast-to-adjacent-water aggregate. Lakes and major-river provinces remain separate from the ocean-depth verdict."),
			"limit":                  limitProperty(), "visibility": visibilityProperty(),
		}), handleMapPhysicalContext, legacyPrivacyProperties),
		mapTool("map_neighbors", "Inspect Map Neighborhood", "Inspect the bounded graph neighborhood around a province or landed title. Returns radius groups, direction, distance, and boundary classifications.", objectSchema(map[string]any{
			"id": stringProperty("Map subject: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name."), "radius": integerProperty("Traversal radius.", 1, 3, 1), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "id"), handleMapNeighbors, legacyPrivacyProperties),
		mapTool("map_spatial_relation", "Compare Map Provinces", "Compare the exact spatial relation between two provinces. Returns centroid delta, bearing, distance, direct border, and nearby barriers.", objectSchema(map[string]any{
			"from": stringProperty("Source map subject: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name."), "to": stringProperty("Target map subject in the same forms as from."), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "from", "to"), handleMapSpatialRelation, legacyPrivacyProperties),
		mapTool("map_strategic_passages", "Inspect Strategic Passages", "Inspect explicit adjacencies separately from pixel-border neighbors. Returns straits, crossings, underground links, and off-map gateways.", objectSchema(map[string]any{
			"target": stringProperty("Province id, landed-title id, comma-separated targets, or all."),
			"kind":   stringProperty("Optional passage family.", "strait", "sea_route", "river_crossing", "mountain_pass", "land_passage", "underground_internal", "underground_gateway", "offmap_gateway", "explicit_passage"),
			"limit":  limitProperty(), "visibility": visibilityProperty(),
		}), handleMapStrategicPassages, legacyPrivacyProperties),
		mapTool("map_title_context", "Inspect Map Title", "Inspect province coverage, holder, culture, faith, and neighboring titles for one landed title. Returns read-only historical and visual context.", objectSchema(map[string]any{
			"id": stringProperty("Landed-title id."), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "id"), handleMapTitleContext, legacyPrivacyProperties),
		mapTool("map_assignment_plan", "Plan Map Assignments", "Generate review-only religion or placeholder-character assignment recommendations. Returns patch previews privately and removes them when visibility=public.", objectSchema(map[string]any{
			"target": stringProperty("Province or landed-title target."), "assignment_mode": stringProperty("Assignment family.", "religion", "characters", "both"), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "target"), handleMapAssignmentPlan, []string{"id", "mode", "privacy_mode", "allow_project"}),
		mapTool("map_building_candidates", "Rank Map Building Candidates", "Rank auditable special-building candidates for a province or landed title. Returns terrain, holding, water, culture, and border evidence without writing files.", objectSchema(map[string]any{
			"target": stringProperty("Province or landed-title target."), "year": integerProperty("CK3 history year.", 1, 0, 1), "limit": limitProperty(), "visibility": visibilityProperty(),
		}, "target"), handleMapBuildingCandidates, []string{"id", "mode", "privacy_mode", "allow_project"}),
		mapTool("map_recipe_catalog", "List Map Recipes", "List supported map recipes, levels, transforms, layers, palettes, and guidance. Use this before building a custom metric or render specification.", objectSchema(map[string]any{
			"visibility": visibilityProperty(),
		}), handleMapRecipeCatalog, []string{"limit", "depth", "mode", "privacy_mode", "allow_project"}),
		mapTool("map_build_metric", "Build Map Metric", "Build an auditable indexed or source-noted map metric before rendering. Returns values, quantiles, outliers, provenance, and warnings.", canonicalMetricSchema(mapLevels), handleMapBuildMetric, legacyPrivacyProperties),
		mapTool("map_route", "Calculate Map Route", "Resolve CK3 places and calculate a deterministic legal land, sea, or mixed route over indexed province topology. Returns compact route points, legs, corridor context, diagnostics, and pixel-distance caveats.", mapRouteSchema(), handleMapRoute, []string{"privacy_mode", "allow_project"}),
		mapTool("map_render", "Render CK3 Map", "Render a read-only adaptive CK3 atlas with automatic resolution when dimensions are omitted. Returns structured metadata and an in-memory PNG without accepting client file paths.", canonicalRenderSchema(mapLevels), handleMapRender, []string{"mode", "privacy_mode", "allow_project", "font_path"}),
	}
}

func mapRouteSchema() map[string]any {
	properties := map[string]any{
		"from":                   stringProperty("Origin: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name."),
		"to":                     stringProperty("Destination: numeric province id, b_/c_/d_/k_/e_ title id, or an exact unique English or Chinese localized name."),
		"year":                   integerProperty("CK3 history year.", 1, 0, 1),
		"mode":                   stringProperty("Traversal mode.", "sea", "land", "mixed"),
		"objective":              stringProperty("Route objective.", "shortest", "scenic"),
		"waypoints":              map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxWaypoints, "items": map[string]any{"type": "string"}, "description": "Optional exact map subjects that the route must visit in order."},
		"corridor_radius_pixels": integerProperty("Source-map corridor radius used to select nearby context.", 1, 2048, 120),
		"context_level":          stringProperty("Political context level returned with the corridor.", "county", "duchy"),
		"label_language":         stringProperty("Preferred context-label language.", "en", "zh", "bilingual"),
		"max_nodes":              integerProperty("Maximum graph nodes expanded before returning a bounded failure.", 1, indexer.MapRouteMaxNodes, indexer.MapRouteDefaultMaxNodes),
		"verbose":                booleanProperty("Include bounded graph-load and expansion evidence."),
		"limit":                  limitProperty(),
		"visibility":             visibilityProperty(),
	}
	properties["mode"].(map[string]any)["default"] = "mixed"
	properties["objective"].(map[string]any)["default"] = "shortest"
	properties["context_level"].(map[string]any)["default"] = "duchy"
	properties["label_language"].(map[string]any)["default"] = "bilingual"
	properties["verbose"].(map[string]any)["default"] = false
	schema := objectSchema(properties, "from", "to")
	schema["additionalProperties"] = false
	return schema
}

func mapTool(name, title, description string, input map[string]any, handler ToolHandler, compatibility []string) ToolDefinition {
	return ToolDefinition{Name: name, Title: title, Description: description, InputSchema: input, OutputSchema: genericOutputSchema(), Annotations: readOnlyAnnotations(), Handler: handler, CompatibilityProperties: compatibility}
}

func patchFilesProperty() map[string]any {
	return arrayProperty("Complete source-root-relative files analyzed only in memory.", objectSchema(map[string]any{
		"path":    stringProperty("Source-root-relative CK3 path."),
		"content": stringProperty("Complete proposed file content."),
		"op":      stringProperty("Patch operation.", "upsert", "delete", "rename"),
		"from":    stringProperty("Existing object id for rename."),
		"to":      stringProperty("Replacement object id for rename."),
	}, "path"))
}

func canonicalMetricSchema(levels []string) map[string]any {
	schema := cloneSchema(legacyInputSchema("map_build_metric"))
	hardenMetricSpecSchema(schema, levels)
	properties := schema["properties"].(map[string]any)
	properties["limit"] = limitProperty()
	properties["visibility"] = visibilityProperty()
	return schema
}

func canonicalRenderSchema(levels []string) map[string]any {
	schema := cloneSchema(legacyInputSchema("map_render"))
	properties := schema["properties"].(map[string]any)
	properties["limit"] = limitProperty()
	properties["visibility"] = visibilityProperty()
	properties["level"] = stringProperty("Primary render level.", levels...)
	properties["year"] = integerProperty("Displayed atlas year.", 1, 0, 0)
	properties["history_year"] = integerProperty("Deprecated alias for year; conflicting values are rejected.", 1, 0, 0)
	properties["history_year"].(map[string]any)["deprecated"] = true
	properties["width"] = integerProperty("Optional explicit output width. Omit width and height for automatic sizing.", 1, indexer.MapRenderMaxWidth, 0)
	properties["height"] = integerProperty("Optional explicit output height. Omit width and height for automatic sizing.", 1, indexer.MapRenderMaxHeight, 0)
	properties["padding"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 1024, "description": "Outer map padding in final-output pixels."}
	properties["route_province_ids"] = map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": map[string]any{"type": "integer", "minimum": 1}, "description": "Ordered route and endpoint province ids to include in the render viewport."}
	properties["auto_context"] = booleanProperty("Expand a route into a bounded county- or duchy-level map corridor instead of rendering isolated route nodes.")
	properties["corridor_radius_pixels"] = integerProperty("Source-map route corridor radius.", 1, 2048, 120)
	properties["context_level"] = stringProperty("Political context expansion level.", "county", "duchy")
	properties["verbose"] = booleanProperty("Include full metric values and recipe targets. Route renders default to compact metadata.")
	properties["route"] = mapRenderRouteProperty()
	properties["boundary_levels"].(map[string]any)["maxItems"] = 5
	layers := properties["layers"].(map[string]any)
	layers["minItems"] = 1
	layers["maxItems"] = 32
	layerItems := layers["items"].(map[string]any)
	layerItems["additionalProperties"] = false
	layerProperties := layerItems["properties"].(map[string]any)
	layerProperties["level"] = stringProperty("Layer aggregation level.", levels...)
	nestedMetric := cloneSchema(legacyInputSchema("map_build_metric"))
	hardenMetricSpecSchema(nestedMetric, levels)
	delete(nestedMetric["properties"].(map[string]any), "limit")
	layerProperties["metric"] = nestedMetric
	layerProperties["values"] = cloneSchemaProperty(nestedMetric["properties"].(map[string]any)["values"])
	layerProperties["line_width"] = integerProperty("Line or marker size in final-output pixels.", 1, 64, 1)
	layerProperties["limit"] = integerProperty("Maximum markers, labels, or passages for the layer.", 1, 20, 8)
	layerProperties["ids"].(map[string]any)["maxItems"] = 200000
	layerProperties["edges"] = arrayProperty("Explicit source-to-target flow edges.", objectSchema(map[string]any{
		"from":  stringProperty("Source entity id."),
		"to":    stringProperty("Target entity id."),
		"value": map[string]any{"type": "number"},
		"label": stringProperty("Optional edge label."),
	}, "from", "to"))
	layerProperties["edges"].(map[string]any)["maxItems"] = 200000
	schema["additionalProperties"] = false
	return schema
}

func mapRenderRouteProperty() map[string]any {
	subject := objectSchema(map[string]any{
		"input": stringProperty("Original subject input."), "province_id": map[string]any{"type": "integer", "minimum": 1},
		"barony": stringProperty("Barony id."), "county": stringProperty("County id."), "duchy": stringProperty("Duchy id."),
		"kingdom": stringProperty("Kingdom id."), "empire": stringProperty("Empire id."), "name_en": stringProperty("English name."), "name_zh": stringProperty("Chinese name."),
	}, "input", "province_id")
	point := objectSchema(map[string]any{
		"province_id": map[string]any{"type": "integer", "minimum": 1}, "center_x": map[string]any{"type": "number"}, "center_y": map[string]any{"type": "number"},
		"water_kind": stringProperty("Indexed water kind."), "adjacency_from_previous": stringProperty("Legal edge classification."),
		"distance_from_previous_pixels": map[string]any{"type": "number", "minimum": 0}, "cumulative_distance_pixels": map[string]any{"type": "number", "minimum": 0},
	}, "province_id", "center_x", "center_y")
	leg := objectSchema(map[string]any{
		"kind":        stringProperty("Route leg kind.", "embark", "sea", "land", "disembark"),
		"start_index": map[string]any{"type": "integer", "minimum": 0}, "end_index": map[string]any{"type": "integer", "minimum": 0},
	}, "kind", "start_index", "end_index")
	corridor := objectSchema(map[string]any{
		"province_ids": map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": map[string]any{"type": "integer", "minimum": 1}},
		"county_ids":   map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": map[string]any{"type": "string"}},
		"duchy_ids":    map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": map[string]any{"type": "string"}},
	})
	candidate := objectSchema(map[string]any{
		"input":       stringProperty("Original ambiguous subject input."),
		"title_id":    stringProperty("Candidate landed-title id."),
		"province_id": map[string]any{"type": "integer", "minimum": 1},
		"name_en":     stringProperty("Candidate English name."),
		"name_zh":     stringProperty("Candidate Chinese name."),
	}, "input", "province_id")
	failure := objectSchema(map[string]any{
		"code":                       stringProperty("Stable route failure code."),
		"message":                    stringProperty("Bounded route failure explanation."),
		"from_component_size":        map[string]any{"type": "integer", "minimum": 0},
		"to_component_size":          map[string]any{"type": "integer", "minimum": 0},
		"rejected_boundary_types":    map[string]any{"type": "object", "maxProperties": 32, "additionalProperties": map[string]any{"type": "integer", "minimum": 0}},
		"resolution_candidates":      map[string]any{"type": "array", "maxItems": 64, "items": candidate},
		"expanded_nodes_before_stop": map[string]any{"type": "integer", "minimum": 0},
	}, "code", "message")
	timings := objectSchema(map[string]any{
		"resolve_ms":    map[string]any{"type": "integer", "minimum": 0},
		"graph_load_ms": map[string]any{"type": "integer", "minimum": 0},
		"route_ms":      map[string]any{"type": "integer", "minimum": 0},
		"corridor_ms":   map[string]any{"type": "integer", "minimum": 0},
	})
	return objectSchema(map[string]any{
		"status": stringProperty("Route status.", "ready", "blocked"), "intent": stringProperty("Route result intent."),
		"resolved_from": subject, "resolved_to": subject, "mode": stringProperty("Route mode.", "sea", "land", "mixed"),
		"objective":       stringProperty("Route objective.", "shortest", "scenic"),
		"path":            map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": point},
		"legs":            map[string]any{"type": "array", "maxItems": indexer.MapRouteMaxNodes, "items": leg},
		"distance_pixels": map[string]any{"type": "number", "minimum": 0}, "corridor_targets": corridor,
		"warnings": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}},
		"evidence": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}},
		"error":    failure, "timings_ms": timings,
	}, "status", "resolved_from", "resolved_to", "path")
}

func hardenMetricSpecSchema(schema map[string]any, levels []string) {
	properties := schema["properties"].(map[string]any)
	properties["level"] = stringProperty("Aggregation level.", levels...)
	properties["year"] = integerProperty("CK3 history year.", 1, 0, 1)
	componentList := properties["components"].(map[string]any)
	componentList["maxItems"] = 32
	components := componentList["items"].(map[string]any)
	components["additionalProperties"] = false
	componentProperties := components["properties"].(map[string]any)
	componentProperties["weights"] = map[string]any{
		"type":                 "object",
		"maxProperties":        512,
		"additionalProperties": map[string]any{"type": "number"},
	}
	transform := properties["transform"].(map[string]any)
	transform["additionalProperties"] = false
	transformProperties := transform["properties"].(map[string]any)
	transformProperties["rounds"] = integerProperty("Graph transform rounds.", 1, 16, 1)
	transformProperties["rates"].(map[string]any)["maxItems"] = 16
	transformProperties["rates"].(map[string]any)["items"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	transformProperties["rate"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	transformProperties["distance_decay"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	transformProperties["seeds"].(map[string]any)["maxItems"] = 20000
	transformProperties["only_higher_to_lower"] = booleanProperty("Restrict diffusion to higher-to-lower values.")
	valueList := properties["values"].(map[string]any)
	valueList["maxItems"] = 200000
	values := valueList["items"].(map[string]any)
	values["additionalProperties"] = false
	values["properties"].(map[string]any)["confidence"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	schema["additionalProperties"] = false
}

func cloneSchemaProperty(value any) map[string]any {
	data, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

func legacyInputSchema(name string) map[string]any {
	for _, tool := range legacyToolCatalog() {
		if tool["name"] == name {
			return tool["inputSchema"].(map[string]any)
		}
	}
	return objectSchema(map[string]any{})
}

func buildLegacyAliases() []LegacyAlias {
	type aliasSpec struct{ name, canonical, operation, kind string }
	specs := []aliasSpec{
		{"query_object", "ck3_inspect", "definition", ""},
		{"find_refs", "ck3_inspect", "references", ""},
		{"query_loc", "ck3_inspect", "localization", ""},
		{"query_resource", "ck3_inspect", "resource", ""},
		{"inspect_object", "ck3_inspect", "context", ""},
		{"diagnose_key", "ck3_inspect", "diagnose", ""},
		{"query_object_types", "ck3_workspace", "object_types", ""},
		{"architecture_overview", "ck3_workspace", "overview", ""},
		{"dependency_graph", "ck3_dependencies", "", ""},
		{"prepare_edit", "ck3_prepare_edit", "context", ""},
		{"query_examples", "ck3_prepare_edit", "examples", ""},
		{"query_rules", "ck3_prepare_edit", "rules", ""},
		{"query_patterns", "ck3_prepare_edit", "patterns", ""},
		{"preflight_code", "ck3_preflight", "subject", ""},
		{"preflight_patch", "ck3_preflight", "patch", ""},
		{"preflight_dirty", "ck3_preflight", "dirty", ""},
		{"impact_patch", "ck3_impact", "", ""},
		{"validate_project", "ck3_diagnostics", "summary", ""},
		{"explain_diagnostic", "ck3_diagnostics", "explain", ""},
		{"lookup_scope", "ck3_script_reference", "", "scope"},
		{"lookup_datatype", "ck3_script_reference", "", "datatype"},
		{"lookup_shape", "ck3_script_reference", "", "shape"},
		{"lookup_define", "ck3_script_reference", "", "define"},
		{"lookup_on_action", "ck3_script_reference", "", "on_action"},
		{"lookup_iterator", "ck3_script_reference", "", "iterator"},
		{"lookup_example", "ck3_script_reference", "", "example"},
		{"lookup_modifier", "ck3_script_reference", "", "modifier"},
		{"health_check", "ck3_health", "", ""},
	}
	legacy := legacyToolCatalog()
	byName := map[string]map[string]any{}
	for _, tool := range legacy {
		byName[tool["name"].(string)] = tool
	}
	aliases := make([]LegacyAlias, 0, len(specs))
	for _, spec := range specs {
		tool := byName[spec.name]
		aliases = append(aliases, LegacyAlias{
			Name: spec.name, Canonical: spec.canonical, Operation: spec.operation, Kind: spec.kind,
			Description: tool["description"].(string), InputSchema: tool["inputSchema"].(map[string]any),
		})
	}
	return aliases
}
