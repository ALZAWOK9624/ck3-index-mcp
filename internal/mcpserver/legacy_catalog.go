package mcpserver

import "ck3-index/internal/indexer"

// legacyToolCatalog preserves historical map schema seeds while the canonical
// map schemas are hardened. Its names are not registered, advertised, or
// callable in the MCP server.
func legacyToolCatalog() []map[string]any {
	schema := func(desc string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":            map[string]any{"type": "string", "description": desc},
				"limit":         map[string]any{"type": "integer", "description": "Maximum evidence items per section. Defaults to 8, caps at 20."},
				"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
				"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
			},
			"required": []string{"id"},
		}
	}
	noArgSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit":         map[string]any{"type": "integer", "description": "Maximum evidence items. Defaults to 8, caps at 20."},
			"depth":         map[string]any{"type": "integer", "description": "Dependency graph traversal depth. Defaults to 1, caps at 2."},
			"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
			"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
		},
	}
	mapIDSchema := func(desc string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":            map[string]any{"type": "string", "description": desc},
				"year":          map[string]any{"type": "integer", "description": "CK3 game year. Defaults to 1 when omitted."},
				"radius":        map[string]any{"type": "integer", "description": "Neighbor traversal radius for map_neighbors. Defaults to 1, caps at 3."},
				"limit":         map[string]any{"type": "integer", "description": "Maximum evidence items per section. Defaults to 8, caps at 20."},
				"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
				"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
			},
			"required": []string{"id"},
		}
	}
	mapAssignmentSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target":          map[string]any{"type": "string", "description": "Province id or landed title id to plan assignments for."},
			"id":              map[string]any{"type": "string", "description": "Alias for target."},
			"mode":            map[string]any{"type": "string", "description": "Assignment mode: religion, characters, or both."},
			"assignment_mode": map[string]any{"type": "string", "description": "Assignment mode alias when mode is reserved for privacy."},
			"year":            map[string]any{"type": "integer", "description": "CK3 game year. Defaults to 1 when omitted."},
			"limit":           map[string]any{"type": "integer", "description": "Maximum recommendations. Defaults to 8, caps at 20."},
			"privacy_mode":    map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
			"allow_project":   map[string]any{"type": "boolean", "description": "Set false with privacy_mode=group/public to suppress current-project evidence."},
		},
		"required": []string{"target"},
	}
	mapSpatialSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from":          map[string]any{"type": "string", "description": "Numeric source province id."},
			"to":            map[string]any{"type": "string", "description": "Numeric target province id."},
			"year":          map[string]any{"type": "integer", "description": "CK3 game year. Defaults to 1 when omitted."},
			"limit":         map[string]any{"type": "integer", "description": "Maximum supporting items. Defaults to 8, caps at 20."},
			"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
			"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
		},
		"required": []string{"from", "to"},
	}
	mapStrategicSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{"type": "string", "description": "Province id, landed title id, comma-separated targets, or all."},
			"kind":   map[string]any{"type": "string", "enum": []string{"strait", "sea_route", "river_crossing", "mountain_pass", "land_passage", "underground_internal", "underground_gateway", "offmap_gateway", "explicit_passage"}},
			"limit":  map[string]any{"type": "integer", "description": "Maximum returned passages. Defaults to 8, caps at 20."},
		},
	}
	mapMetricSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"recipe":      map[string]any{"type": "string", "description": "Built-in recipe id from map_recipe_catalog."},
			"target":      map[string]any{"type": "string", "description": "Province/title id, comma-separated ids, or all."},
			"id_prefix":   map[string]any{"type": "string", "description": "Optional entity-id prefix filter, e.g. c_c for a custom county namespace."},
			"id_pattern":  map[string]any{"type": "string", "description": "Optional bounded regular expression for exact namespace filtering, e.g. ^c_c[0-9]+$."},
			"level":       map[string]any{"type": "string", "enum": []string{"province", "barony", "county", "duchy", "kingdom", "empire"}},
			"year":        map[string]any{"type": "integer"},
			"kind":        map[string]any{"type": "string", "enum": []string{"numeric", "category"}},
			"field":       map[string]any{"type": "string"},
			"aggregate":   map[string]any{"type": "string", "enum": []string{"count", "sum", "mean", "max", "majority", "diversity", "ratio"}},
			"match_value": map[string]any{"type": "string"},
			"source_note": map[string]any{"type": "string", "description": "Required when values are model supplied."},
			"components": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"field": map[string]any{"type": "string"}, "weights": map[string]any{"type": "object"}, "default": map[string]any{"type": "number"}, "multiplier": map[string]any{"type": "number"}, "presence": map[string]any{"type": "boolean"},
			}, "required": []string{"field"}}},
			"transform": map[string]any{"type": "object", "properties": map[string]any{
				"operator": map[string]any{"type": "string", "enum": []string{"high_to_low", "neighbor_mean", "distance_decay"}}, "rounds": map[string]any{"type": "integer"}, "rates": map[string]any{"type": "array", "items": map[string]any{"type": "number"}}, "rate": map[string]any{"type": "number"}, "edge_weight": map[string]any{"type": "string"}, "cap": map[string]any{"type": "number"}, "floor": map[string]any{"type": "number"}, "terrain_absorption": map[string]any{"type": "boolean"}, "seeds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "distance_decay": map[string]any{"type": "number"},
			}},
			"values": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "string"}, "value": map[string]any{"type": "number"}, "category": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "confidence": map[string]any{"type": "number"},
			}, "required": []string{"id"}}},
			"limit": map[string]any{"type": "integer"},
		},
	}
	mapRenderSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"recipe": map[string]any{"type": "string", "enum": []string{"political_atlas", "thematic_atlas", "duchy_political_atlas", "strategic_waterways_atlas"}}, "theme": map[string]any{"type": "string", "enum": []string{"political", "culture", "faith", "development", "terrain", "custom"}}, "level": map[string]any{"type": "string", "enum": []string{"province", "barony", "county", "duchy", "kingdom", "empire"}}, "boundary_levels": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"barony", "county", "duchy", "kingdom", "empire"}}}, "title": map[string]any{"type": "string"}, "subtitle": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "year": map[string]any{"type": "integer", "description": "CK3 history year."}, "history_year": map[string]any{"type": "integer", "description": "Deprecated alias for year; conflicting values are rejected."}, "terrain_overlay": map[string]any{"type": "boolean"},
			"width": map[string]any{"type": "integer", "maximum": indexer.MapRenderMaxWidth, "description": "Optional explicit output width. Omit width and height to let the renderer auto-select a 2K-, 4K-, or 8K-class canvas from map complexity."}, "height": map[string]any{"type": "integer", "maximum": indexer.MapRenderMaxHeight, "description": "Optional explicit output height. Omit width and height for automatic aspect-preserving resolution selection."}, "padding": map[string]any{"type": "integer"}, "background": map[string]any{"type": "string"},
			"style": map[string]any{"type": "string", "enum": []string{"standard", "historical_atlas"}}, "layout": map[string]any{"type": "string", "enum": []string{"map_only", "light_frame", "full_atlas"}}, "relief_strength": map[string]any{"type": "string", "enum": []string{"none", "subtle", "strong"}}, "label_language": map[string]any{"type": "string", "enum": []string{"chinese", "english", "bilingual"}}, "color_strategy": map[string]any{"type": "string", "enum": []string{"native", "muted", "coordinated"}}, "supersample": map[string]any{"type": "integer", "enum": []int{1, 2}},
			"layers": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"type": map[string]any{"type": "string", "enum": []string{"fill", "borders", "markers", "flows", "labels"}}, "level": map[string]any{"type": "string"}, "metric": mapMetricSchema, "values": mapMetricSchema["properties"].(map[string]any)["values"], "source_note": map[string]any{"type": "string"}, "palette": map[string]any{"type": "string"}, "texture": map[string]any{"type": "string", "enum": []string{"political", "political_material"}}, "classes": map[string]any{"type": "integer", "minimum": 2, "maximum": 12}, "minimum": map[string]any{"type": "number"}, "maximum": map[string]any{"type": "number"}, "no_data": map[string]any{"type": "string"}, "color": map[string]any{"type": "string"}, "line_width": map[string]any{"type": "integer"}, "source": map[string]any{"type": "string", "description": "Constrained source such as capitals, holy_sites, special_buildings, vegetation, holdings, lakes, strategic_portals, strategic_passages, title_color, outer, metric, entities, categories, or atlas_titles."}, "ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "edges": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "limit": map[string]any{"type": "integer"}, "threshold": map[string]any{"type": "number"},
			}, "required": []string{"type"}}},
		},
		"anyOf": []map[string]any{{"required": []string{"layers"}}, {"required": []string{"recipe"}}},
	}
	patchSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"files": map[string]any{
				"type":        "array",
				"description": "Virtual source-root relative files to check without writing SQLite.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Source-root relative CK3 file path, e.g. common/decisions/example.txt."},
						"content": map[string]any{"type": "string", "description": "Complete proposed file content."},
						"op":      map[string]any{"type": "string", "description": "Patch operation: upsert, delete, or rename. Defaults to upsert."},
						"from":    map[string]any{"type": "string", "description": "Object id for rename operations."},
						"to":      map[string]any{"type": "string", "description": "New object id for rename operations."},
					},
					"required": []string{"path"},
				},
			},
			"limit":         map[string]any{"type": "integer", "description": "Maximum evidence items. Defaults to 8, caps at 20."},
			"mode":          map[string]any{"type": "string", "description": "Use public to redact patch/current-project evidence for group/non-admin contexts."},
			"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress patch/current-project evidence."},
		},
		"required": []string{"files"},
	}
	reviewSchema := map[string]any{
		"type":       "object",
		"properties": patchSchema["properties"],
	}
	searchSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":         map[string]any{"type": "string", "description": "CK3 id, key, resource path, diagnostic code, or semantic prefix."},
			"kind":          map[string]any{"type": "string", "description": "Optional category: object, reference, localization, resource, diagnostic, script_key, or datatype."},
			"source":        map[string]any{"type": "string", "description": "Optional indexed source name such as project, godherja, game, or translation."},
			"path_prefix":   map[string]any{"type": "string", "description": "Optional source-root-relative path prefix."},
			"limit":         map[string]any{"type": "integer", "description": "Maximum evidence items. Defaults to 8, caps at 20."},
			"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence."},
			"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
		},
		"required": []string{"query"},
	}
	return []map[string]any{
		{"name": "ck3_search", "description": "START HERE for broad CK3 discovery before rg. Searches ids, references, English/Chinese localization values, resources, script fields, datatypes, and FTS5 content with exact matches first.", "inputSchema": searchSchema},
		{"name": "ck3_inspect", "description": "START HERE for one CK3 id. Aggregates object, localization, resource, sound, reference, and diagnostic classification in one call.", "inputSchema": schema("Object id, localization key, resource path, sound id, or diagnostic clue.")},
		{"name": "ck3_review", "description": "START HERE for CK3 code review. Reviews proposed complete files, or current dirty project files when files are omitted, including parser, scope, refs, localization, and resources.", "inputSchema": reviewSchema},
		{"name": "query_object", "description": "Primary CK3 semantic definition lookup; use before raw text search. Returns active definitions and override context.", "inputSchema": schema("Object id or type:id.")},
		{"name": "query_object_types", "description": "List indexed CK3 object types and counts.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "find_refs", "description": "Short LLM-ready incoming/outgoing CK3 reference summary.", "inputSchema": schema("Object id or referenced id.")},
		{"name": "query_loc", "description": "Short LLM-ready localization key summary.", "inputSchema": schema("Localization key.")},
		{"name": "query_resource", "description": "Short LLM-ready resource existence and reference summary.", "inputSchema": schema("Resource path or name fragment.")},
		{"name": "query_examples", "description": "Short vanilla-first script examples by object type or type:term, snippets capped at 20 lines.", "inputSchema": schema("Object type or type:term.")},
		{"name": "query_rules", "description": "Short schema-field summary learned from local CK3 .info files.", "inputSchema": schema("Object type.")},
		{"name": "query_patterns", "description": "Short empirical field-pattern summary learned from active indexed scripts.", "inputSchema": schema("Object type.")},
		{"name": "architecture_overview", "description": "Codebase-memory style indexed workspace map: sources, object types, reference kinds, and diagnostic hotspots.", "inputSchema": noArgSchema},
		{"name": "dependency_graph", "description": "Codebase-memory style one-hop dependency graph for a CK3 object or referenced id.", "inputSchema": schema("Object id or type:id.")},
		{"name": "validate_project", "description": "Chat-fast cached parser/index/compiler diagnostic summary. Does not rescan or rerun heavy validation.", "inputSchema": noArgSchema},
		{"name": "health_check", "description": "Short ck3-index DB, schema, performance-index, and MCP registration health report.", "inputSchema": noArgSchema},
		{"name": "explain_diagnostic", "description": "Aggregated diagnostics filtered by code, source, path prefix, and confidence.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}, "path_prefix": map[string]any{"type": "string"}, "confidence": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, "required": []string{"id"}}},
		{"name": "inspect_object", "description": "Default first call for one CK3 id; aggregates definitions, refs, localization, and diagnostics before any text search.", "inputSchema": schema("Object id or type:id.")},
		{"name": "prepare_edit", "description": "Required CK3 edit preparation: object context, vanilla-first examples, schema fields, patterns, and edit risks.", "inputSchema": schema("Object id, object type, or type:term.")},
		{"name": "preflight_code", "description": "Primary CK3 generation/edit gate for definition conflicts, unresolved refs, localization candidates, resources, and diagnostics.", "inputSchema": schema("Object id, type:id, localization key, or resource/sound path.")},
		{"name": "preflight_patch", "description": "Fast temporary patch gate: validate proposed file contents with an in-memory overlay, without scanning or writing SQLite.", "inputSchema": patchSchema},
		{"name": "impact_patch", "description": "Fast temporary patch impact summary for proposed upsert/delete/rename operations, without scanning or writing SQLite.", "inputSchema": patchSchema},
		{"name": "preflight_dirty", "description": "Fast temporary preflight for current dirty project files against the SQLite cache, without scanning or writing SQLite.", "inputSchema": noArgSchema},
		{"name": "diagnose_key", "description": "Fast aggregate: check whether an id is an object, localization key, resource, reference, or diagnostic clue.", "inputSchema": schema("Any CK3 script id, localization key, or resource path.")},
		{"name": "lookup_scope", "description": "Look up the locally compiled scope rule for a CK3 trigger or effect key. Treat as a rule hint, then confirm with examples when editing.", "inputSchema": schema("Trigger or effect key (e.g. has_title_law, add_gold).")},
		{"name": "lookup_datatype", "description": "Look up functions and properties from engine logs/data_types, including signature, description, definition type, return type, and source.", "inputSchema": schema("Datatype function or property name.")},
		{"name": "lookup_shape", "description": "Look up current CK3 engine documentation and explicit usage examples for a trigger or effect key. This is not an exhaustive value grammar.", "inputSchema": schema("Trigger or effect key (e.g. is_alive, add_gold, has_trait).")},
		{"name": "lookup_define", "description": "Check whether a CK3 @define name is in the locally compiled define rules. Useful for validating define references before editing.", "inputSchema": schema("Define name with namespace prefix (e.g. @NAI|MONTHS_OF_MAINTENANCE_IN_WAR_CHEST).")},
		{"name": "lookup_on_action", "description": "Check whether a CK3 on_action name is in the locally compiled on_action rules.", "inputSchema": schema("On_action name (e.g. on_birth, yearly_playable_pulse).")},
		{"name": "lookup_iterator", "description": "Check whether a CK3 iterator/scope key is in the locally compiled iterator rules and return scope input/output hints.", "inputSchema": schema("Iterator key (e.g. any_child, every_vassal, random_county).")},
		{"name": "lookup_example", "description": "Look up the official description and usage example for a CK3 trigger or effect key (from game's effects.log/triggers.log dump). Shows the exact script syntax.", "inputSchema": schema("Trigger or effect key (e.g. add_gold, has_trait, is_alive).")},
		{"name": "lookup_modifier", "description": "Check whether a modifier tag is a valid CK3 static modifier and what scope types it applies to (from game's modifiers.log dump).", "inputSchema": schema("Modifier tag (e.g. dynasty_opinion, levy_size, monthly_prestige).")},
		{"name": "map_province_info", "description": "Precision map context for a province: centroid, bounding box, terrain, titles, and adjacent provinces with compass direction, bearing, pixel distance, water boundaries, and impassable-mountain boundaries.", "inputSchema": mapIDSchema("Numeric province id.")},
		{"name": "map_neighbors", "description": "Neighboring province context around a province or landed title, grouped by graph radius and eight-direction position, with centroid pixel distance and water/mountain boundary classification.", "inputSchema": mapIDSchema("Province id or landed title id.")},
		{"name": "map_spatial_relation", "description": "Exact spatial relation between two provinces: centroid delta, eight-direction position, compass bearing, straight-line pixel distance, direct border kind, and nearby water or impassable mountains.", "inputSchema": mapSpatialSchema},
		{"name": "map_strategic_passages", "description": "Query explicit adjacencies.csv passages separately from pixel-border neighbors, including straits, river crossings, underground links, and off-map gateways.", "inputSchema": mapStrategicSchema},
		{"name": "map_title_context", "description": "Map context for a landed title: covered provinces, holder, culture/religion distribution, and neighboring titles.", "inputSchema": mapIDSchema("Landed title id, e.g. k_k11.")},
		{"name": "map_assignment_plan", "description": "Generate review-only religion and/or placeholder-character assignment recommendations with patch file previews. Does not write files.", "inputSchema": mapAssignmentSchema},
		{"name": "map_building_candidates", "description": "Rank auditable special-building candidates for a province or landed title using slots, holdings, terrain, coast/river/lake, nearby culture, nearby special buildings, and border context.", "inputSchema": mapIDSchema("Province id or landed title id.")},
		{"name": "map_recipe_catalog", "description": "List built-in thematic map recipes, constrained metric fields/transforms, layer types, palettes, and model guidance.", "inputSchema": noArgSchema},
		{"name": "map_build_metric", "description": "Build an auditable province/title metric from indexed fields or source-noted custom values; returns values, quantiles, outliers, provenance, and warnings without rendering.", "inputSchema": mapMetricSchema},
		{"name": "map_render", "description": "Render a read-only adaptive CK3 atlas as JSON metadata plus an in-memory PNG. political_atlas supports barony through empire; thematic_atlas supports culture, faith, indexed in-game development, terrain, optional higher-rank boundary_levels, and source-noted custom layers.", "inputSchema": mapRenderSchema},
	}
}
