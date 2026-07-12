package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"ck3-index/internal/indexer"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	hasID   bool
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func (r *rpcRequest) UnmarshalJSON(data []byte) error {
	type reqAlias rpcRequest
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var aux reqAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = rpcRequest(aux)
	_, r.hasID = raw["id"]
	return nil
}

func serveMCP(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer) error {
	db, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	r := bufio.NewReaderSize(in, 4*1024*1024)
	for {
		req, err := readMCPMessage(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !req.hasID {
			continue
		}
		res := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			res.Result = map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "ck3-index", "version": "0.2.1"},
				"instructions":    "Primary CK3/Godherja semantic index. Start broad discovery with ck3_search, one-id investigation with ck3_inspect, and code validation with ck3_review. All specialized tools remain available for precise follow-up. Call ck3-index before raw text search; use rg only to inspect exact evidence paths returned by the index. Indexed files are restricted to CK3 load roots; backups, tools, docs, and temporary root folders are excluded.",
				"capabilities": map[string]any{"tools": map[string]any{
					"listChanged": true,
				}},
			}
		case "tools/list":
			res.Result = map[string]any{"tools": mcpTools()}
		case "tools/call":
			result, err := callMCPTool(ctx, db, cfg, req.Params)
			if err != nil {
				res.Error = map[string]any{"code": -32000, "message": err.Error()}
			} else {
				res.Result = result
			}
		default:
			res.Error = map[string]any{"code": -32601, "message": "method not found"}
		}
		if err := writeMCPMessage(out, res); err != nil {
			return err
		}
	}
}

// readMCPMessage supports both framed (Content-Length: N + blank + body) and
// newline-delimited JSON envelope modes, for client compatibility.
func readMCPMessage(r *bufio.Reader) (rpcRequest, error) {
	var req rpcRequest
	for {
		line, err := r.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return req, err
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			n, perr := strconv.Atoi(strings.TrimSpace(trimmed[len("content-length:"):]))
			if perr != nil {
				return req, perr
			}
			for {
				b, err := r.ReadBytes('\n')
				if err != nil && len(b) == 0 {
					return req, err
				}
				if strings.TrimSpace(string(b)) == "" {
					break
				}
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(r, body); err != nil {
				return req, err
			}
			err = json.Unmarshal(body, &req)
			return req, err
		}
		err = json.Unmarshal([]byte(trimmed), &req)
		return req, err
	}
}

func writeMCPMessage(out io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// MCP stdio transport uses newline-delimited JSON. Older Codex clients also
	// accepted LSP-style Content-Length framing, but current clients wait for a
	// complete JSON line and will otherwise time out during initialization.
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}

func lookupScopeTool(key string) (any, error) {
	sl := indexer.LookupScope(key)
	if sl == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No trigger/effect scope rule was found; try lookup_iterator and lookup_example before using this key."},
		}, nil
	}
	return map[string]any{
		"found":           true,
		"key":             sl.Key,
		"is_trigger":      sl.IsTrigger,
		"is_effect":       sl.IsEffect,
		"scope_mask":      sl.ScopeMask,
		"scope_mask_high": sl.ScopeMaskHigh,
		"scope_names":     sl.ScopeNames,
		"scope_desc":      sl.ScopeDesc,
		"guidance":        []string{"scope_names is authoritative; use this key only in a compatible root/current scope and confirm nested syntax with lookup_shape and lookup_example."},
	}, nil
}

func lookupShapeTool(key string) (any, error) {
	sd := indexer.LookupShape(key)
	if sd == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No value-shape rule was found; confirm syntax from query_examples or game script before generating this key."},
		}, nil
	}
	return map[string]any{
		"found":    true,
		"key":      key,
		"shape":    sd.Shape,
		"desc":     sd.Desc,
		"guidance": []string{"Shape describes the value form; it does not by itself prove the surrounding trigger/effect context is legal."},
	}, nil
}

func lookupDefineTool(key string) (any, error) {
	found := indexer.IsDefine(key)
	return map[string]any{"found": found, "key": key, "guidance": []string{"Use found=false as a warning only; mods can define custom @names outside engine defines."}}, nil
}

func lookupOnActionTool(key string) (any, error) {
	found := indexer.IsOnAction(key)
	return map[string]any{"found": found, "key": key, "guidance": []string{"For on_action edits, query_object and find_refs should still be used to inspect local overrides and consumers."}}, nil
}

func lookupExampleTool(key string) (any, error) {
	ex := indexer.LookupExample(key)
	if ex == nil {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No official trigger/effect example was found; use query_examples against vanilla scripts before generating."},
		}, nil
	}
	return map[string]any{
		"found":    true,
		"key":      key,
		"desc":     ex.Desc,
		"example":  ex.Example,
		"guidance": []string{"Prefer this syntax when it is non-empty; if example is empty, use desc plus query_examples for concrete script."},
	}, nil
}

func lookupModifierTool(key string) (any, error) {
	ml := indexer.LookupModifier(key)
	if !ml.Found {
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"Do not use this as a static modifier unless query_object or vanilla examples confirm it exists."},
		}, nil
	}
	return map[string]any{
		"found":     true,
		"key":       key,
		"use_areas": ml.UseAreas,
		"guidance":  []string{"Use only in modifier blocks that apply to one of these scope/use areas."},
	}, nil
}

func lookupIteratorTool(key string) (any, error) {
	il := indexer.LookupIterator(key)
	if il == nil {
		if ex := indexer.LookupExample(key); ex != nil && strings.Contains(strings.ToLower(ex.Example+" "+ex.Desc), "iterate") {
			return map[string]any{
				"found":     true,
				"key":       key,
				"source":    "example_log",
				"scope_in":  "",
				"scope_out": "",
				"example":   ex.Example,
				"desc":      ex.Desc,
				"guidance":  []string{"Iterator exists in official examples, but scope input/output was not in the compact rule table; verify with vanilla examples before complex nesting."},
			}, nil
		}
		return map[string]any{
			"found":    false,
			"key":      key,
			"guidance": []string{"No iterator rule was found; try lookup_example or query_examples before generating this block."},
		}, nil
	}
	return map[string]any{
		"found":     true,
		"key":       il.Key,
		"scope_in":  il.ScopeIn,
		"scope_out": il.ScopeOut,
		"guidance":  []string{"Iterator block syntax is usually key = { limit = { <triggers> } <effects> }; scope_out is the current scope inside the block."},
	}, nil
}

func mcpTools() []map[string]any {
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
		{"name": "lookup_shape", "description": "Look up the locally compiled value-shape rule for a CK3 trigger or effect key: boolean, compare, scope, item, block, etc.", "inputSchema": schema("Trigger or effect key (e.g. is_alive, add_gold, has_trait).")},
		{"name": "lookup_define", "description": "Check whether a CK3 @define name is in the locally compiled define rules. Useful for validating define references before editing.", "inputSchema": schema("Define name with namespace prefix (e.g. @NAI|MONTHS_OF_MAINTENANCE_IN_WAR_CHEST).")},
		{"name": "lookup_on_action", "description": "Check whether a CK3 on_action name is in the locally compiled on_action rules.", "inputSchema": schema("On_action name (e.g. on_birth, yearly_playable_pulse).")},
		{"name": "lookup_iterator", "description": "Check whether a CK3 iterator/scope key is in the locally compiled iterator rules and return scope input/output hints.", "inputSchema": schema("Iterator key (e.g. any_child, every_vassal, random_county).")},
		{"name": "lookup_example", "description": "Look up the official description and usage example for a CK3 trigger or effect key (from game's effects.log/triggers.log dump). Shows the exact script syntax.", "inputSchema": schema("Trigger or effect key (e.g. add_gold, has_trait, is_alive).")},
		{"name": "lookup_modifier", "description": "Check whether a modifier tag is a valid CK3 static modifier and what scope types it applies to (from game's modifiers.log dump).", "inputSchema": schema("Modifier tag (e.g. dynasty_opinion, levy_size, monthly_prestige).")},
		{"name": "map_province_info", "description": "Map context for a province: location, blocked status, de-jure chain, culture, religion, holder, and adjacent provinces.", "inputSchema": mapIDSchema("Numeric province id.")},
		{"name": "map_neighbors", "description": "Neighboring province context around a province or landed title, grouped by traversal radius.", "inputSchema": mapIDSchema("Province id or landed title id.")},
		{"name": "map_title_context", "description": "Map context for a landed title: covered provinces, holder, culture/religion distribution, and neighboring titles.", "inputSchema": mapIDSchema("Landed title id, e.g. k_k11.")},
		{"name": "map_assignment_plan", "description": "Generate review-only religion and/or placeholder-character assignment recommendations with patch file previews. Does not write files.", "inputSchema": mapAssignmentSchema},
		{"name": "map_building_candidates", "description": "Rank auditable special-building candidates for a province or landed title using slots, holdings, terrain, coast/river/lake, nearby culture, nearby special buildings, and border context.", "inputSchema": mapIDSchema("Province id or landed title id.")},
	}
}

func callMCPTool(ctx context.Context, db *indexer.DB, cfg indexer.Config, raw json.RawMessage) (any, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	var args struct {
		ID             string                   `json:"id"`
		Query          string                   `json:"query"`
		Kind           string                   `json:"kind"`
		Source         string                   `json:"source"`
		PathPrefix     string                   `json:"path_prefix"`
		Confidence     string                   `json:"confidence"`
		Files          []indexer.PatchFileInput `json:"files"`
		Limit          int                      `json:"limit"`
		Depth          int                      `json:"depth"`
		Year           int                      `json:"year"`
		Radius         int                      `json:"radius"`
		Target         string                   `json:"target"`
		AssignmentMode string                   `json:"assignment_mode"`
		PrivacyMode    string                   `json:"privacy_mode"`
		Mode           string                   `json:"mode"`
		AllowProject   *bool                    `json:"allow_project"`
	}
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return nil, err
		}
	}
	privacyMode := args.Mode
	assignmentMode := args.AssignmentMode
	if p.Name == "map_assignment_plan" {
		if assignmentMode == "" && args.Mode != "public" && args.Mode != "group" {
			assignmentMode = args.Mode
		}
		privacyMode = args.PrivacyMode
		if privacyMode == "" && (args.Mode == "public" || args.Mode == "group") {
			privacyMode = args.Mode
		}
	}
	opts := indexer.LLMOptions{Limit: args.Limit, Depth: args.Depth, Mode: privacyMode, AllowProject: true}
	if args.AllowProject != nil {
		opts.AllowProject = *args.AllowProject
	}
	var v any
	var err error
	switch p.Name {
	case "ck3_search":
		v, err = db.LLMSearch(ctx, indexer.SearchOptions{Query: args.Query, Kind: args.Kind, Source: args.Source, PathPrefix: args.PathPrefix, LLMOptions: opts})
	case "ck3_inspect":
		if args.ID == "" {
			return nil, fmt.Errorf("ck3_inspect requires id")
		}
		v, err = db.LLMInspectSmart(ctx, args.ID, opts)
	case "ck3_review":
		v, err = db.LLMReview(ctx, cfg, args.Files, opts)
	case "query_object":
		if args.ID == "" {
			return nil, fmt.Errorf("query_object requires id")
		}
		v, err = db.LLMQueryObject(ctx, args.ID, opts)
	case "query_object_types":
		v, err = db.LLMQueryObjectTypes(ctx, opts)
	case "find_refs":
		if args.ID == "" {
			return nil, fmt.Errorf("find_refs requires id")
		}
		v, err = db.LLMFindRefs(ctx, args.ID, opts)
	case "query_loc":
		if args.ID == "" {
			return nil, fmt.Errorf("query_loc requires id")
		}
		v, err = db.LLMQueryLocalization(ctx, args.ID, opts)
	case "query_resource":
		if args.ID == "" {
			return nil, fmt.Errorf("query_resource requires id")
		}
		v, err = db.LLMQueryResource(ctx, args.ID, opts)
	case "query_examples":
		if args.ID == "" {
			return nil, fmt.Errorf("query_examples requires id")
		}
		typ, contains := indexer.SplitExampleID(args.ID)
		v, err = db.LLMQueryExamples(ctx, typ, contains, opts)
	case "query_rules":
		if args.ID == "" {
			return nil, fmt.Errorf("query_rules requires id")
		}
		v, err = db.LLMQueryRules(ctx, args.ID, opts)
	case "query_patterns":
		if args.ID == "" {
			return nil, fmt.Errorf("query_patterns requires id")
		}
		v, err = db.LLMQueryPatterns(ctx, args.ID, opts)
	case "architecture_overview":
		v, err = db.LLMArchitectureOverview(ctx, opts)
	case "dependency_graph":
		if args.ID == "" {
			return nil, fmt.Errorf("dependency_graph requires id")
		}
		v, err = db.LLMDependencyGraph(ctx, args.ID, opts)
	case "validate_project":
		v, err = db.LLMValidate(ctx, opts)
	case "health_check":
		var health indexer.HealthReport
		health, err = db.Health(ctx)
		if err == nil {
			v = mcpHealthReport(health)
		}
	case "explain_diagnostic":
		if args.ID == "" {
			return nil, fmt.Errorf("explain_diagnostic requires id")
		}
		v, err = db.LLMExplainDiagnosticFiltered(ctx, indexer.DiagnosticFilter{Code: args.ID, Source: args.Source, PathPrefix: args.PathPrefix, Confidence: args.Confidence}, opts)
	case "inspect_object":
		if args.ID == "" {
			return nil, fmt.Errorf("inspect_object requires id")
		}
		v, err = db.LLMInspectObject(ctx, args.ID, opts)
	case "prepare_edit":
		if args.ID == "" {
			return nil, fmt.Errorf("prepare_edit requires id")
		}
		v, err = db.LLMPrepareEdit(ctx, args.ID, opts)
	case "preflight_code":
		if args.ID == "" {
			return nil, fmt.Errorf("preflight_code requires id")
		}
		v, err = db.LLMPreflight(ctx, args.ID, opts)
	case "preflight_patch":
		if len(args.Files) == 0 {
			return nil, fmt.Errorf("preflight_patch requires files")
		}
		v, err = db.LLMPreflightPatch(ctx, args.Files, opts)
	case "impact_patch":
		if len(args.Files) == 0 {
			return nil, fmt.Errorf("impact_patch requires files")
		}
		v, err = db.LLMImpactPatch(ctx, args.Files, opts)
	case "preflight_dirty":
		if cfg.ConfigPath == "" {
			return nil, fmt.Errorf("preflight_dirty requires server config")
		}
		v, err = db.LLMPreflightDirty(ctx, cfg, opts)
	case "diagnose_key":
		if args.ID == "" {
			return nil, fmt.Errorf("diagnose_key requires id")
		}
		v, err = db.LLMDiagnoseKey(ctx, args.ID, opts)
	case "lookup_scope":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_scope requires id")
		}
		var live []indexer.ScopeEvidence
		live, err = db.LookupScopeEvidence(ctx, args.ID)
		if err == nil && len(live) > 0 {
			v = map[string]any{"found": true, "key": args.ID, "rules": live, "confidence": "high", "rule_source": "engine_logs", "guidance": []string{"Engine log rules take precedence; use Tiger only as fallback and investigate conflicts."}}
		} else if err == nil {
			v, err = lookupScopeTool(args.ID)
			if m, ok := v.(map[string]any); ok {
				m["confidence"] = "medium"
				m["rule_source"] = "tiger_fallback"
			}
		}
	case "lookup_datatype":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_datatype requires id")
		}
		var items []indexer.DatatypeInfo
		items, err = db.LookupDatatype(ctx, args.ID, opts.Limit)
		v = map[string]any{"query": args.ID, "found": len(items) > 0, "items": items, "rule_source": "engine_logs/data_types"}
	case "lookup_shape":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_shape requires id")
		}
		v, err = lookupShapeTool(args.ID)
	case "lookup_define":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_define requires id")
		}
		v, err = lookupDefineTool(args.ID)
	case "lookup_on_action":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_on_action requires id")
		}
		v, err = lookupOnActionTool(args.ID)
	case "lookup_iterator":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_iterator requires id")
		}
		v, err = lookupIteratorTool(args.ID)
	case "lookup_example":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_example requires id")
		}
		v, err = lookupExampleTool(args.ID)
	case "lookup_modifier":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_modifier requires id")
		}
		v, err = lookupModifierTool(args.ID)
	case "map_province_info":
		if args.ID == "" {
			return nil, fmt.Errorf("map_province_info requires id")
		}
		v, err = db.LLMMapProvinceInfo(ctx, args.ID, args.Year, opts)
	case "map_neighbors":
		if args.ID == "" {
			return nil, fmt.Errorf("map_neighbors requires id")
		}
		v, err = db.LLMMapNeighbors(ctx, args.ID, args.Radius, args.Year, opts)
	case "map_title_context":
		if args.ID == "" {
			return nil, fmt.Errorf("map_title_context requires id")
		}
		v, err = db.LLMMapTitleContext(ctx, args.ID, args.Year, opts)
	case "map_assignment_plan":
		target := args.Target
		if target == "" {
			target = args.ID
		}
		if target == "" {
			return nil, fmt.Errorf("map_assignment_plan requires target")
		}
		v, err = db.LLMMapAssignmentPlan(ctx, assignmentMode, target, args.Year, opts)
	case "map_building_candidates":
		target := args.Target
		if target == "" {
			target = args.ID
		}
		if target == "" {
			return nil, fmt.Errorf("map_building_candidates requires target")
		}
		v, err = db.LLMMapBuildingCandidates(ctx, target, args.Year, opts)
	default:
		err = fmt.Errorf("unknown tool %q", p.Name)
	}
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(v)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}, nil
}

func mcpHealthReport(h indexer.HealthReport) map[string]any {
	wal := make([]map[string]any, 0, len(h.WALFiles))
	for _, f := range h.WALFiles {
		name := "db-sidecar"
		switch {
		case strings.HasSuffix(f.Path, "-wal"):
			name = "wal"
		case strings.HasSuffix(f.Path, "-shm"):
			name = "shm"
		}
		item := map[string]any{"name": name, "exists": f.Exists}
		if f.SizeMB > 0 {
			item["size_mb"] = f.SizeMB
		}
		wal = append(wal, item)
	}
	return map[string]any{
		"status":             h.Status,
		"database_mb":        h.DatabaseMB,
		"tables":             h.Tables,
		"index_rule_version": h.IndexRuleVersion,
		"missing_indexes":    h.MissingIndexes,
		"wal_files":          wal,
		"mcp_configured":     h.MCPConfigured,
		"mcp_serving":        true,
		"guidance":           h.Guidance,
	}
}
