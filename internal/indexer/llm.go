package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

const defaultLLMLimit = 8

type LLMOptions struct {
	Limit        int    `json:"limit,omitempty"`
	Mode         string `json:"mode,omitempty"`
	AllowProject bool   `json:"allow_project,omitempty"`
}

type LLMResult struct {
	Query        string         `json:"query,omitempty"`
	Intent       string         `json:"intent"`
	Summary      string         `json:"summary"`
	Counts       map[string]int `json:"counts,omitempty"`
	Guidance     []string       `json:"guidance,omitempty"`
	Evidence     []LLMEvidence  `json:"evidence,omitempty"`
	NextQueries  []LLMNextQuery `json:"next_queries,omitempty"`
	Redacted     int            `json:"redacted,omitempty"`
	NeedsRefresh bool           `json:"needs_refresh,omitempty"`
}

type LLMEvidence struct {
	Kind    string `json:"kind"`
	Type    string `json:"type,omitempty"`
	Name    string `json:"name,omitempty"`
	Source  string `json:"source,omitempty"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

type LLMNextQuery struct {
	Tool   string `json:"tool"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func (o LLMOptions) normalizedLimit() int {
	if o.Limit <= 0 {
		return defaultLLMLimit
	}
	if o.Limit > 20 {
		return 20
	}
	return o.Limit
}

func (o LLMOptions) publicMode() bool {
	return strings.EqualFold(o.Mode, "public") || (!o.AllowProject && strings.EqualFold(o.Mode, "group"))
}

func (r LLMResult) withPublicFilter(opts LLMOptions) LLMResult {
	if !opts.publicMode() {
		return r
	}
	kept := r.Evidence[:0]
	for _, ev := range r.Evidence {
		if isProjectEvidence(ev.Source, ev.Path) {
			r.Redacted++
			continue
		}
		kept = append(kept, ev)
	}
	r.Evidence = kept
	if r.Redacted > 0 {
		r.Summary = fmt.Sprintf("Public mode redacted %d private evidence item(s) for %s.", r.Redacted, r.Intent)
		if len(r.Evidence) > 0 {
			r.Summary += fmt.Sprintf(" %d public evidence item(s) remain.", len(r.Evidence))
		}
	}
	return r
}

func isProjectEvidence(source, path string) bool {
	return strings.EqualFold(source, "project")
}

func evidencePath(path string) string {
	p := filepathSlash(path)
	parts := strings.Split(p, "/")
	for i, part := range parts {
		switch part {
		case "common", "events", "history", "gui", "gfx", "localization", "map_data", "game":
			return strings.Join(parts[i:], "/")
		}
	}
	if base := filepath.Base(path); base != "." && base != "/" {
		return base
	}
	return p
}

func trimText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max > 0 && len([]rune(s)) > max {
		r := []rune(s)
		return string(r[:max]) + "..."
	}
	return s
}

func trimSnippet(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i := range lines {
		lines[i] = trimText(lines[i], 220)
	}
	return strings.Join(lines, "\n")
}

func (db *DB) LLMQueryObject(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: id, Intent: "query_object", Counts: map[string]int{"definitions": len(obj.Definitions)}}
	if len(obj.Definitions) == 0 {
		r.Summary = fmt.Sprintf("No indexed object definition matched %q.", id)
		r.NeedsRefresh = true
		r.NextQueries = []LLMNextQuery{{Tool: "find_refs", ID: id, Reason: "check whether the id is referenced under another object type"}}
		return r.withPublicFilter(opts), nil
	}
	for i, d := range obj.Definitions {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, objectEvidence("definition", d))
	}
	first := obj.Definitions[0]
	r.Summary = fmt.Sprintf("Found %d definition(s) for %q. First match is %s:%s from %s.", len(obj.Definitions), id, first.Type, first.Name, first.Source)
	r.Guidance = []string{
		"Use the first definition as the active definition unless a later query shows a project override.",
		"Before editing, inspect refs, localization, examples, and rules for this object type.",
	}
	r.NextQueries = []LLMNextQuery{
		{Tool: "find_refs", ID: first.Name, Reason: "inspect incoming and outgoing dependency edges"},
		{Tool: "query_loc", ID: first.Name, Reason: "check direct localization key"},
		{Tool: "query_examples", ID: first.Type + ":" + first.Name, Reason: "find similar indexed script examples"},
		{Tool: "query_rules", ID: first.Type, Reason: "inspect known schema fields before editing"},
	}
	return r.withPublicFilter(opts), nil
}

func objectEvidence(kind string, d ObjectDef) LLMEvidence {
	return LLMEvidence{Kind: kind, Type: d.Type, Name: d.Name, Source: d.Source, Path: evidencePath(d.Path), Line: d.Line, Column: d.Column}
}

func (db *DB) LLMQueryObjectTypes(ctx context.Context, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	types, err := db.QueryObjectTypes(ctx)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Intent: "query_object_types",
		Counts: map[string]int{
			"types": len(types),
		},
		Summary: fmt.Sprintf("Found %d indexed object type(s); returning top %d by object count.", len(types), minInt(limit, len(types))),
		Guidance: []string{
			"Use this only to choose a likely object type before calling prepare_edit or query_examples.",
			"Unusual low-count types may be extraction artifacts; confirm with query_object or examples before generating.",
		},
	}
	for i, item := range types {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{
			Kind:   "object_type",
			Type:   item.Type,
			Detail: fmt.Sprintf("count=%d", item.Count),
		})
	}
	return r.withPublicFilter(opts), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func refEvidence(kind string, h RefHit) LLMEvidence {
	name := h.Name
	if h.FromName != "" {
		name = h.FromName
	}
	detail := h.Kind
	if h.Raw != "" && h.Raw != h.Name {
		detail += " raw=" + h.Raw
	}
	if !h.Resolved {
		detail += " [unresolved]"
	}
	return LLMEvidence{Kind: kind, Type: h.FromType, Name: name, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Column: h.Column, Detail: detail}
}

func (db *DB) LLMFindRefs(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	refs, err := db.QueryRefs(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Query:  id,
		Intent: "find_refs",
		Counts: map[string]int{
			"incoming": len(refs.Incoming),
			"outgoing": len(refs.Outgoing),
		},
	}
	for i, h := range refs.Incoming {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, refEvidence("incoming_ref", h))
	}
	for i, h := range refs.Outgoing {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, refEvidence("outgoing_ref", h))
	}
	r.Summary = fmt.Sprintf("%q has %d incoming and %d outgoing indexed reference(s).", id, len(refs.Incoming), len(refs.Outgoing))
	r.Guidance = []string{
		"Incoming refs are scripts that may break if this id is renamed or removed.",
		"Outgoing unresolved refs are the first things to fix before validating generated code.",
	}
	r.NextQueries = []LLMNextQuery{{Tool: "query_object", ID: id, Reason: "confirm definition and override chain"}}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMQueryLocalization(ctx context.Context, key string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	loc, err := db.QueryLocalization(ctx, key)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: key, Intent: "query_loc", Counts: map[string]int{"values": len(loc.Values)}}
	for i, h := range loc.Values {
		if i >= limit {
			break
		}
		detail := h.Language + ": " + trimText(h.Value, 180)
		if h.Replace {
			detail += " (replace)"
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "localization", Name: key, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: detail})
	}
	if len(loc.Values) == 0 {
		r.Summary = fmt.Sprintf("No indexed localization value matched %q.", key)
		r.NeedsRefresh = true
		r.Guidance = []string{"Generated or edited script that references this key should add localization before final validation."}
	} else {
		r.Summary = fmt.Sprintf("Found %d localization value(s) for %q.", len(loc.Values), key)
		r.Guidance = []string{"Use localization as display text evidence only; do not infer mechanics from it."}
	}
	r.NextQueries = []LLMNextQuery{{Tool: "find_refs", ID: key, Reason: "find scripts referencing this localization key"}}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMQueryResource(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	res, err := db.QueryResource(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	soundKnown := strings.HasPrefix(strings.Trim(id, `"`), "event:/") && IsSound(strings.Trim(id, `"`))
	soundCount := 0
	if soundKnown {
		soundCount = 1
	}
	r := LLMResult{Query: id, Intent: "query_resource", Counts: map[string]int{"resources": len(res.Resources), "references": len(res.References), "known_sounds": soundCount}}
	if soundKnown {
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "sound_event", Name: strings.Trim(id, `"`), Detail: "known from compiled CK3/Tiger sound logs"})
	}
	for i, h := range res.Resources {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "resource", Name: h.ResourcePath, Source: h.Source, Path: evidencePath(h.Path), Detail: h.Kind})
	}
	for i, h := range res.References {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, refEvidence("resource_ref", h))
	}
	if soundKnown && len(res.Resources) == 0 {
		r.Summary = fmt.Sprintf("%q is a known indexed sound event; %d reference(s) may mention it.", id, len(res.References))
		r.Guidance = []string{"Sound events are validated from compiled game/Tiger log seeds, not from filesystem resource files."}
	} else if len(res.Resources) == 0 {
		r.Summary = fmt.Sprintf("No indexed resource matched %q; %d reference(s) may still mention it.", id, len(res.References))
		r.Guidance = []string{"Missing indexed resources should be fixed by adding the file or using an existing vanilla/Godherja path."}
	} else {
		r.Summary = fmt.Sprintf("Found %d resource file(s) and %d reference(s) for %q.", len(res.Resources), len(res.References), id)
		r.Guidance = []string{"Prefer existing indexed resources over inventing paths."}
	}
	r.NextQueries = []LLMNextQuery{{Tool: "find_refs", ID: id, Reason: "inspect all references to this resource path or id"}}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMQueryExamples(ctx context.Context, typ, contains string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	ex, err := db.QueryExamples(ctx, typ, contains, limit)
	if err != nil {
		return LLMResult{}, err
	}
	query := typ
	if contains != "" {
		query += ":" + contains
	}
	r := LLMResult{Query: query, Intent: "query_examples", Counts: map[string]int{"examples": len(ex.Examples)}}
	for _, h := range ex.Examples {
		detail := ""
		line := h.Line
		if h.MatchLine > 0 {
			line = h.MatchLine
			detail = "matched: " + trimText(h.Match, 180)
		}
		r.Evidence = append(r.Evidence, LLMEvidence{
			Kind: "example", Type: h.Type, Name: h.Name, Source: h.Source,
			Path: evidencePath(h.Path), Line: line, Detail: detail, Snippet: trimSnippet(h.Snippet, 20),
		})
	}
	if contains != "" && len(ex.Examples) > 0 {
		r.Summary = fmt.Sprintf("Found %d vanilla-first example(s) for %q, including object-body matches.", len(ex.Examples), query)
	} else {
		r.Summary = fmt.Sprintf("Found %d vanilla-first example(s) for %q.", len(ex.Examples), query)
	}
	r.Guidance = []string{
		"Copy structure, not names or flavor text.",
		"If the requested term appears in a snippet, prefer that syntax over memory.",
	}
	r.NextQueries = []LLMNextQuery{{Tool: "query_rules", ID: typ, Reason: "check schema fields used by this object type"}}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMQueryRules(ctx context.Context, typ string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	rules, err := db.QueryRules(ctx, typ)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: typ, Intent: "query_rules", Counts: map[string]int{"fields": len(rules.Fields)}}
	for i, h := range rules.Fields {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "schema_field", Name: h.Field, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: trimText(h.Raw, 180)})
	}
	r.Summary = fmt.Sprintf("Found %d indexed schema field example(s) for object type %q.", len(rules.Fields), typ)
	r.Guidance = []string{
		"Fields listed here are allowed top-level schema hints, not full trigger/effect validity proofs.",
		"Use lookup_scope, lookup_shape, lookup_example, and query_examples for nested script syntax.",
	}
	r.NextQueries = []LLMNextQuery{{Tool: "query_examples", ID: typ, Reason: "compare schema fields against vanilla script examples"}}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMQueryPatterns(ctx context.Context, typ string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	patterns, err := db.QueryPatterns(ctx, typ)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: typ, Intent: "query_patterns", Counts: map[string]int{"field_shapes": len(patterns.Fields)}}
	for i, h := range patterns.Fields {
		if i >= limit {
			break
		}
		detail := fmt.Sprintf("%s count=%d sample=%s", h.Shape, h.Count, trimText(h.Raw, 120))
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "field_pattern", Type: typ, Name: h.Field, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: detail})
	}
	r.Summary = fmt.Sprintf("Found %d empirical field pattern(s) for object type %q from indexed scripts.", len(patterns.Fields), typ)
	r.Guidance = []string{
		"These are empirical patterns from active indexed files, not official engine schema.",
		"High-count patterns are good generation defaults; low-count patterns should be confirmed with query_examples.",
	}
	r.NextQueries = []LLMNextQuery{
		{Tool: "query_examples", ID: typ, Reason: "inspect concrete vanilla-first object bodies"},
		{Tool: "query_rules", ID: typ, Reason: "compare empirical usage with .info schema hints"},
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMValidate(ctx context.Context, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	rep, err := db.CachedValidation(ctx)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Intent: "validate_project", Counts: rep.Counts}
	for i, d := range rep.Diagnostics {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, diagnosticEvidence(d))
	}
	r.Summary = fmt.Sprintf("Cached validation has %d error(s), %d warning(s), and %d info diagnostic(s).", rep.Counts["error"], rep.Counts["warning"], rep.Counts["info"])
	r.Guidance = []string{
		"This MCP tool is chat-fast and reads cached diagnostics only.",
		"After editing files, run ck3-index scan or CLI validate to refresh diagnostics before trusting a clean result.",
	}
	if len(rep.Diagnostics) > 0 {
		r.NextQueries = []LLMNextQuery{{Tool: "explain_diagnostic", ID: rep.Diagnostics[0].Code, Reason: "inspect the most severe diagnostic class"}}
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMExplainDiagnostic(ctx context.Context, code string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	diags, err := db.ExplainDiagnostic(ctx, code)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: code, Intent: "explain_diagnostic", Counts: map[string]int{"diagnostics": len(diags)}}
	for i, d := range diags {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, diagnosticEvidence(d))
	}
	r.Summary = fmt.Sprintf("Found %d diagnostic(s) with code %q.", len(diags), code)
	return r.withPublicFilter(opts), nil
}

func diagnosticEvidence(d Diagnostic) LLMEvidence {
	return LLMEvidence{Kind: "diagnostic", Source: d.Source, Path: evidencePath(d.Path), Line: d.Line, Column: d.Column, Detail: d.Severity + " " + d.Code + ": " + d.Message}
}

func (db *DB) LLMInspectObject(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	refs, err := db.QueryRefs(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	loc, err := db.QueryLocalization(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	diags, err := db.diagnosticsFor(ctx, id, limit)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Query:  id,
		Intent: "inspect_object",
		Counts: map[string]int{
			"definitions":   len(obj.Definitions),
			"incoming_refs": len(refs.Incoming),
			"outgoing_refs": len(refs.Outgoing),
			"localization":  len(loc.Values),
			"diagnostics":   len(diags),
		},
	}
	for i, d := range obj.Definitions {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, objectEvidence("definition", d))
	}
	for i, h := range refs.Incoming {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, refEvidence("incoming_ref", h))
	}
	for i, h := range refs.Outgoing {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, refEvidence("outgoing_ref", h))
	}
	for i, h := range loc.Values {
		if i >= 2 {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "localization", Name: id, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: h.Language + ": " + trimText(h.Value, 180)})
	}
	for _, d := range diags {
		r.Evidence = append(r.Evidence, diagnosticEvidence(d))
	}
	if len(obj.Definitions) == 0 {
		r.Summary = fmt.Sprintf("No definition matched %q. References, localization, and diagnostics were still checked.", id)
		r.Guidance = []string{"If this is generated code, create the definition or correct the id before referencing it."}
	} else {
		r.Summary = fmt.Sprintf("%q summary: %d definition(s), %d incoming ref(s), %d outgoing ref(s), %d localization value(s), %d related diagnostic(s).", id, len(obj.Definitions), len(refs.Incoming), len(refs.Outgoing), len(loc.Values), len(diags))
		r.Guidance = []string{
			"Use this as the compact object briefing before editing.",
			"Resolve diagnostics and missing localization/resource refs before considering generated code complete.",
		}
	}
	if len(obj.Definitions) > 0 {
		typ := obj.Definitions[0].Type
		r.NextQueries = []LLMNextQuery{
			{Tool: "query_examples", ID: typ + ":" + obj.Definitions[0].Name, Reason: "find similar scripts before editing"},
			{Tool: "query_rules", ID: typ, Reason: "inspect schema fields before editing"},
		}
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMPrepareEdit(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	typ, contains := SplitExampleID(id)
	typeOnly, err := db.objectTypeExists(ctx, typ)
	if err != nil {
		return LLMResult{}, err
	}
	useObjectDefs := len(obj.Definitions) > 0 && !(typeOnly && contains == "")
	if useObjectDefs {
		typ = obj.Definitions[0].Type
		contains = obj.Definitions[0].Name
	}
	ex, err := db.QueryExamples(ctx, typ, contains, limit)
	if err != nil {
		return LLMResult{}, err
	}
	rules, err := db.QueryRules(ctx, typ)
	if err != nil {
		return LLMResult{}, err
	}
	patterns, err := db.QueryPatterns(ctx, typ)
	if err != nil {
		return LLMResult{}, err
	}
	var refs RefQuery
	if contains != "" {
		refs, err = db.QueryRefs(ctx, contains)
		if err != nil {
			return LLMResult{}, err
		}
	}
	r := LLMResult{
		Query:  id,
		Intent: "prepare_edit",
		Counts: map[string]int{
			"definitions": 0,
			"examples":    len(ex.Examples),
			"rules":       len(rules.Fields),
			"patterns":    len(patterns.Fields),
			"refs":        len(refs.Incoming) + len(refs.Outgoing),
		},
	}
	if useObjectDefs {
		r.Counts["definitions"] = len(obj.Definitions)
		for i, d := range obj.Definitions {
			if i >= 3 {
				break
			}
			r.Evidence = append(r.Evidence, objectEvidence("definition", d))
		}
	}
	for i, h := range ex.Examples {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "example", Type: h.Type, Name: h.Name, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Snippet: trimSnippet(h.Snippet, 20)})
	}
	for i, h := range rules.Fields {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "schema_field", Name: h.Field, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: trimText(h.Raw, 180)})
	}
	for i, h := range patterns.Fields {
		if i >= limit {
			break
		}
		detail := fmt.Sprintf("%s count=%d sample=%s", h.Shape, h.Count, trimText(h.Raw, 120))
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "field_pattern", Type: typ, Name: h.Field, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: detail})
	}
	r.Summary = fmt.Sprintf("Edit prep for %q: object type %q, %d definition(s), %d example(s), %d schema field(s), %d empirical pattern(s), %d related ref(s).", id, typ, r.Counts["definitions"], len(ex.Examples), len(rules.Fields), len(patterns.Fields), len(refs.Incoming)+len(refs.Outgoing))
	r.Guidance = []string{
		"Generation workflow: follow vanilla-first examples, empirical field patterns, then schema fields; use lookup_scope/lookup_shape for every unfamiliar trigger or effect.",
		"Use existing scripted triggers/effects and modifiers when indexed; invent new ids only after diagnose_key returns no definition/ref conflict.",
		"Generated code must include matching localization and must be refreshed with scan or CLI validate before use.",
	}
	r.NextQueries = []LLMNextQuery{
		{Tool: "query_patterns", ID: typ, Reason: "inspect empirical field shapes and sample locations"},
		{Tool: "validate_project", Reason: "run after script, localization, GUI, or resource changes"},
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMPreflight(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	refs, err := db.QueryRefs(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	typ, name := preflightTypeAndName(id, obj)
	locFound, locChecked, locEvidence, err := db.preflightLocalization(ctx, typ, name, limit)
	if err != nil {
		return LLMResult{}, err
	}
	diags, err := db.diagnosticsFor(ctx, name, limit)
	if err != nil {
		return LLMResult{}, err
	}

	unresolved := 0
	blockers := 0
	warnings := 0
	r := LLMResult{
		Query:  id,
		Intent: "preflight_code",
		Counts: map[string]int{
			"definitions":       len(obj.Definitions),
			"incoming_refs":     len(refs.Incoming),
			"outgoing_refs":     len(refs.Outgoing),
			"loc_candidates":    locChecked,
			"localization":      locFound,
			"diagnostics":       len(diags),
			"unresolved_refs":   0,
			"blocking_risks":    0,
			"nonblocking_risks": 0,
		},
	}
	add := func(ev LLMEvidence) {
		if len(r.Evidence) < limit {
			r.Evidence = append(r.Evidence, ev)
		}
	}
	for i, d := range obj.Definitions {
		if i >= 3 {
			break
		}
		add(objectEvidence("definition", d))
	}
	for _, ev := range locEvidence {
		add(ev)
	}
	for _, h := range refs.Outgoing {
		if h.Resolved {
			continue
		}
		unresolved++
		if preflightBlockingRef(h.Kind) {
			blockers++
		} else {
			warnings++
		}
		add(refEvidence("unresolved_outgoing_ref", h))
	}
	for _, d := range diags {
		if d.Severity == "error" {
			blockers++
		} else {
			warnings++
		}
		add(diagnosticEvidence(d))
	}
	if len(obj.Definitions) == 0 && (len(refs.Incoming) > 0 || len(refs.Outgoing) > 0) {
		warnings++
	}
	if locChecked > 0 && locFound == 0 {
		warnings++
	}
	r.Counts["unresolved_refs"] = unresolved
	r.Counts["blocking_risks"] = blockers
	r.Counts["nonblocking_risks"] = warnings
	if blockers == 0 && warnings == 0 {
		r.Summary = fmt.Sprintf("Preflight for %q found no immediate indexed blockers. Definitions=%d, outgoing refs=%d, localization hits=%d.", id, len(obj.Definitions), len(refs.Outgoing), locFound)
	} else {
		r.Summary = fmt.Sprintf("Preflight for %q found %d blocking risk(s), %d warning risk(s), and %d unresolved outgoing ref(s).", id, blockers, warnings, unresolved)
	}
	r.Guidance = []string{
		"Use preflight before generating or editing code; fix blocking risks before trusting generated CK3 script.",
		"Missing object/resource/sound refs are stronger evidence than missing localization; localization is still required for player-facing content.",
		"After edits, refresh the index with ck3-index scan and re-run preflight or validate_project.",
	}
	r.NextQueries = []LLMNextQuery{
		{Tool: "prepare_edit", ID: preflightPrepareID(typ, name), Reason: "get vanilla-first examples and schema hints before writing code"},
		{Tool: "find_refs", ID: name, Reason: "inspect dependency edges in more detail"},
		{Tool: "validate_project", Reason: "check cached project diagnostics"},
	}
	return r.withPublicFilter(opts), nil
}

func preflightTypeAndName(id string, obj ObjectQuery) (string, string) {
	if len(obj.Definitions) > 0 {
		return obj.Definitions[0].Type, obj.Definitions[0].Name
	}
	if typ, name, ok := strings.Cut(id, ":"); ok {
		return typ, name
	}
	return "", id
}

func preflightPrepareID(typ, name string) string {
	if typ != "" && name != "" {
		return typ + ":" + name
	}
	if name != "" {
		return name
	}
	return typ
}

func preflightBlockingRef(kind string) bool {
	switch kind {
	case "localization", "define", "scope", "global_var", "flag":
		return false
	default:
		return true
	}
}

func (db *DB) preflightLocalization(ctx context.Context, typ, name string, limit int) (int, int, []LLMEvidence, error) {
	keys := preflightLocalizationKeys(typ, name)
	found := 0
	var evidence []LLMEvidence
	for _, key := range keys {
		loc, err := db.QueryLocalization(ctx, key)
		if err != nil {
			return 0, len(keys), nil, err
		}
		found += len(loc.Values)
		for _, h := range loc.Values {
			if len(evidence) >= limit {
				break
			}
			evidence = append(evidence, LLMEvidence{Kind: "localization_candidate", Name: key, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Detail: h.Language + ": " + trimText(h.Value, 180)})
		}
	}
	return found, len(keys), evidence, nil
}

func preflightLocalizationKeys(typ, name string) []string {
	if name == "" {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	add(name)
	switch typ {
	case "event":
		add(name + ".t")
		add(name + ".desc")
		add(name + ".a")
		add(name + ".b")
		add(name + ".tt")
	case "decision":
		add(name + "_desc")
		add(name + "_tooltip")
		add(name + "_confirm")
	case "trait", "modifier", "opinion_modifier":
		add(name + "_desc")
	default:
		add(name + "_desc")
		add(name + "_tooltip")
	}
	return keys
}

func (db *DB) objectTypeExists(ctx context.Context, typ string) (bool, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE o.object_type=? AND f.overridden=0 LIMIT 1`, typ).Scan(&n)
	return n > 0, err
}

func (db *DB) LLMDiagnoseKey(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	obj, err := db.LLMQueryObject(ctx, id, opts)
	if err != nil {
		return LLMResult{}, err
	}
	loc, err := db.LLMQueryLocalization(ctx, id, opts)
	if err != nil {
		return LLMResult{}, err
	}
	res, err := db.LLMQueryResource(ctx, id, opts)
	if err != nil {
		return LLMResult{}, err
	}
	refs, err := db.LLMFindRefs(ctx, id, opts)
	if err != nil {
		return LLMResult{}, err
	}
	diags, err := db.diagnosticsFor(ctx, id, opts.normalizedLimit())
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Query:  id,
		Intent: "diagnose_key",
		Counts: map[string]int{
			"definitions":  obj.Counts["definitions"],
			"localization": loc.Counts["values"],
			"resources":    res.Counts["resources"],
			"known_sounds": res.Counts["known_sounds"],
			"incoming":     refs.Counts["incoming"],
			"outgoing":     refs.Counts["outgoing"],
			"diagnostics":  len(diags),
		},
	}
	r.Evidence = append(r.Evidence, obj.Evidence...)
	r.Evidence = append(r.Evidence, loc.Evidence...)
	r.Evidence = append(r.Evidence, res.Evidence...)
	r.Evidence = append(r.Evidence, refs.Evidence...)
	for _, d := range diags {
		r.Evidence = append(r.Evidence, diagnosticEvidence(d))
	}
	r.Summary = fmt.Sprintf("%q diagnosis: %d definition(s), %d localization value(s), %d resource hit(s), %d known sound(s), %d incoming/%d outgoing ref(s), %d related diagnostic(s).", id, r.Counts["definitions"], r.Counts["localization"], r.Counts["resources"], r.Counts["known_sounds"], r.Counts["incoming"], r.Counts["outgoing"], r.Counts["diagnostics"])
	r.Guidance = []string{
		"Use diagnosis for ambiguous ids before deciding whether a token is an object, loc key, resource, or missing reference.",
		"If every count is zero, the id is probably safe for a new generated object but still needs a prefix.",
	}
	if r.Counts["known_sounds"] > 0 {
		r.Guidance = []string{"This id is a known sound event from compiled game/Tiger logs; it is not expected to appear as a filesystem resource."}
	}
	r.NextQueries = []LLMNextQuery{
		{Tool: "inspect_object", ID: id, Reason: "get object-centered context if this is a script object"},
		{Tool: "validate_project", Reason: "check current project diagnostics"},
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) diagnosticsFor(ctx context.Context, id string, limit int) ([]Diagnostic, error) {
	if limit <= 0 {
		limit = defaultLLMLimit
	}
	needle := "%" + id + "%"
	rows, err := db.sql.QueryContext(ctx, `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0)
		FROM diagnostics
		WHERE message LIKE ? OR path LIKE ?
		ORDER BY CASE severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, path,line
		LIMIT ?`, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return out, nil
}
