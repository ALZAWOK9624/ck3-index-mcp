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

func serveMCP(ctx context.Context, dbPath string, in io.Reader, out io.Writer) error {
	db, err := indexer.Open(dbPath)
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
				"serverInfo":      map[string]any{"name": "ck3-index", "version": "0.2.0"},
				"capabilities": map[string]any{"tools": map[string]any{
					"listChanged": true,
				}},
			}
		case "tools/list":
			res.Result = map[string]any{"tools": mcpTools()}
		case "tools/call":
			result, err := callMCPTool(ctx, db, req.Params)
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
	header := []byte("Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n")
	if _, err := out.Write(header); err != nil {
		return err
	}
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
		"found":      true,
		"key":        sl.Key,
		"is_trigger": sl.IsTrigger,
		"is_effect":  sl.IsEffect,
		"scope_mask": sl.ScopeMask,
		"scope_desc": sl.ScopeDesc,
		"guidance":   []string{"Use this key only in a compatible root/current scope; confirm nested syntax with lookup_shape and lookup_example."},
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
			"mode":          map[string]any{"type": "string", "description": "Use public to redact current-project evidence for group/non-admin contexts."},
			"allow_project": map[string]any{"type": "boolean", "description": "Set false with mode=group/public to suppress current-project evidence."},
		},
	}
	return []map[string]any{
		{"name": "query_object", "description": "Short LLM-ready active CK3 object definition summary.", "inputSchema": schema("Object id or type:id.")},
		{"name": "query_object_types", "description": "List indexed CK3 object types and counts.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "find_refs", "description": "Short LLM-ready incoming/outgoing CK3 reference summary.", "inputSchema": schema("Object id or referenced id.")},
		{"name": "query_loc", "description": "Short LLM-ready localization key summary.", "inputSchema": schema("Localization key.")},
		{"name": "query_resource", "description": "Short LLM-ready resource existence and reference summary.", "inputSchema": schema("Resource path or name fragment.")},
		{"name": "query_examples", "description": "Short vanilla-first script examples by object type or type:term, snippets capped at 20 lines.", "inputSchema": schema("Object type or type:term.")},
		{"name": "query_rules", "description": "Short schema-field summary learned from local CK3 .info files.", "inputSchema": schema("Object type.")},
		{"name": "query_patterns", "description": "Short empirical field-pattern summary learned from active indexed scripts.", "inputSchema": schema("Object type.")},
		{"name": "validate_project", "description": "Chat-fast cached parser/index/compiler diagnostic summary. Does not rescan or rerun heavy validation.", "inputSchema": noArgSchema},
		{"name": "explain_diagnostic", "description": "Short diagnostics summary filtered by code.", "inputSchema": schema("Diagnostic code.")},
		{"name": "inspect_object", "description": "Fast aggregate: definitions, refs, localization, and related diagnostics for one id.", "inputSchema": schema("Object id or type:id.")},
		{"name": "prepare_edit", "description": "Fast aggregate: object context, vanilla-first examples, schema fields, and edit risks.", "inputSchema": schema("Object id, object type, or type:term.")},
		{"name": "preflight_code", "description": "Fast generation/edit gate: definition conflicts, unresolved refs, localization candidates, and related diagnostics.", "inputSchema": schema("Object id, type:id, localization key, or resource/sound path.")},
		{"name": "diagnose_key", "description": "Fast aggregate: check whether an id is an object, localization key, resource, reference, or diagnostic clue.", "inputSchema": schema("Any CK3 script id, localization key, or resource path.")},
		{"name": "lookup_scope", "description": "Look up the locally compiled scope rule for a CK3 trigger or effect key. Treat as a rule hint, then confirm with examples when editing.", "inputSchema": schema("Trigger or effect key (e.g. has_title_law, add_gold).")},
		{"name": "lookup_shape", "description": "Look up the locally compiled value-shape rule for a CK3 trigger or effect key: boolean, compare, scope, item, block, etc.", "inputSchema": schema("Trigger or effect key (e.g. is_alive, add_gold, has_trait).")},
		{"name": "lookup_define", "description": "Check whether a CK3 @define name is in the locally compiled define rules. Useful for validating define references before editing.", "inputSchema": schema("Define name with namespace prefix (e.g. @NAI|MONTHS_OF_MAINTENANCE_IN_WAR_CHEST).")},
		{"name": "lookup_on_action", "description": "Check whether a CK3 on_action name is in the locally compiled on_action rules.", "inputSchema": schema("On_action name (e.g. on_birth, yearly_playable_pulse).")},
		{"name": "lookup_iterator", "description": "Check whether a CK3 iterator/scope key is in the locally compiled iterator rules and return scope input/output hints.", "inputSchema": schema("Iterator key (e.g. any_child, every_vassal, random_county).")},
		{"name": "lookup_example", "description": "Look up the official description and usage example for a CK3 trigger or effect key (from game's effects.log/triggers.log dump). Shows the exact script syntax.", "inputSchema": schema("Trigger or effect key (e.g. add_gold, has_trait, is_alive).")},
		{"name": "lookup_modifier", "description": "Check whether a modifier tag is a valid CK3 static modifier and what scope types it applies to (from game's modifiers.log dump).", "inputSchema": schema("Modifier tag (e.g. dynasty_opinion, levy_size, monthly_prestige).")},
	}
}

func callMCPTool(ctx context.Context, db *indexer.DB, raw json.RawMessage) (any, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	var args struct {
		ID           string `json:"id"`
		Limit        int    `json:"limit"`
		Mode         string `json:"mode"`
		AllowProject *bool  `json:"allow_project"`
	}
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return nil, err
		}
	}
	opts := indexer.LLMOptions{Limit: args.Limit, Mode: args.Mode, AllowProject: true}
	if args.AllowProject != nil {
		opts.AllowProject = *args.AllowProject
	}
	var v any
	var err error
	switch p.Name {
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
	case "validate_project":
		v, err = db.LLMValidate(ctx, opts)
	case "explain_diagnostic":
		if args.ID == "" {
			return nil, fmt.Errorf("explain_diagnostic requires id")
		}
		v, err = db.LLMExplainDiagnostic(ctx, args.ID, opts)
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
	case "diagnose_key":
		if args.ID == "" {
			return nil, fmt.Errorf("diagnose_key requires id")
		}
		v, err = db.LLMDiagnoseKey(ctx, args.ID, opts)
	case "lookup_scope":
		if args.ID == "" {
			return nil, fmt.Errorf("lookup_scope requires id")
		}
		v, err = lookupScopeTool(args.ID)
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
	default:
		err = fmt.Errorf("unknown tool %q", p.Name)
	}
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(v)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}, nil
}
