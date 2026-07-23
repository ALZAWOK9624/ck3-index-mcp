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
	Limit          int             `json:"limit,omitempty"`
	Depth          int             `json:"depth,omitempty"`
	Mode           string          `json:"mode,omitempty"`
	AllowProject   bool            `json:"allow_project,omitempty"`
	PrivateSources map[string]bool `json:"-"`
}

type LLMResult struct {
	Query            string                   `json:"query,omitempty"`
	Intent           string                   `json:"intent"`
	Summary          string                   `json:"summary"`
	Counts           map[string]int           `json:"counts,omitempty"`
	Hotspots         map[string][]LLMEvidence `json:"hotspots,omitempty"`
	Guidance         []string                 `json:"guidance,omitempty"`
	Evidence         []LLMEvidence            `json:"evidence,omitempty"`
	NextQueries      []LLMNextQuery           `json:"next_queries,omitempty"`
	Redacted         int                      `json:"redacted,omitempty"`
	NeedsRefresh     bool                     `json:"needs_refresh,omitempty"`
	NeedsScan        bool                     `json:"needs_scan,omitempty"`
	Impact           map[string]int           `json:"impact,omitempty"`
	MissingLocKeys   []string                 `json:"missing_loc_keys,omitempty"`
	MissingResources []string                 `json:"missing_resources,omitempty"`
	ScopeFixHints    []string                 `json:"scope_fix_hints,omitempty"`
	Topology         *LLMTopology             `json:"topology,omitempty"`
	Truncated        bool                     `json:"truncated,omitempty"`
	Pagination       *LLMPagination           `json:"pagination,omitempty"`
}

// LLMPagination describes a bounded evidence page. It deliberately reports
// only page-local facts: total counts can be private-source side channels in
// public visibility, while has_more is enough for an agent to continue.
type LLMPagination struct {
	Page     int  `json:"page"`
	Limit    int  `json:"limit"`
	Returned int  `json:"returned"`
	HasMore  bool `json:"has_more"`
	NextPage int  `json:"next_page,omitempty"`
}

// LLMTopology is a compact, model-facing view over the indexed reference
// graph. It deliberately carries conclusions such as roots, leaves, cycles,
// and shortest traversal paths so clients do not need to reconstruct graph
// algorithms from a long evidence list.
type LLMTopology struct {
	Center           string            `json:"center"`
	Direction        string            `json:"direction"`
	IncludeOnActions bool              `json:"include_on_actions"`
	MaxDepth         int               `json:"max_depth"`
	Nodes            []LLMTopologyNode `json:"nodes"`
	Edges            []LLMTopologyEdge `json:"edges"`
	Roots            []string          `json:"roots,omitempty"`
	Leaves           []string          `json:"leaves,omitempty"`
	Cycles           [][]string        `json:"cycles,omitempty"`
	PathsFromCenter  []LLMTopologyPath `json:"paths_from_center,omitempty"`
	Truncated        bool              `json:"truncated,omitempty"`
}

type LLMTopologyNode struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Defined   bool   `json:"defined"`
	Distance  int    `json:"distance"`
	InDegree  int    `json:"in_degree"`
	OutDegree int    `json:"out_degree"`
	EventType string `json:"event_type,omitempty"`
	Title     string `json:"title,omitempty"`
	Source    string `json:"source,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
}

type LLMTopologyEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason,omitempty"`
	Source     string `json:"source,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
}

type LLMTopologyPath struct {
	To    string   `json:"to"`
	Nodes []string `json:"nodes"`
}

type LLMEvidence struct {
	Kind       string `json:"kind"`
	Type       string `json:"type,omitempty"`
	Name       string `json:"name,omitempty"`
	Source     string `json:"source,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
	Detail     string `json:"detail,omitempty"`
	EdgeType   string `json:"edge_type,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	RuleSource string `json:"rule_source,omitempty"`
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

func (o LLMOptions) normalizedDepth() int {
	if o.Depth <= 0 {
		return 1
	}
	if o.Depth > 2 {
		return 2
	}
	return o.Depth
}

func paginateLLMResult(r LLMResult, page, limit int) LLMResult {
	if page <= 0 {
		page = 1
	}
	if limit <= 0 {
		limit = defaultLLMLimit
	}
	start := (page - 1) * limit
	if start > len(r.Evidence) {
		start = len(r.Evidence)
	}
	end := start + limit
	if end > len(r.Evidence) {
		end = len(r.Evidence)
	}
	hasMore := end < len(r.Evidence)
	r.Evidence = r.Evidence[start:end]
	// A later page has intentionally omitted earlier evidence even when it is
	// the last page. Keep Truncated true for either direction of pagination so
	// clients never mistake page N for the complete result set.
	r.Truncated = r.Truncated || start > 0 || hasMore
	r.Pagination = &LLMPagination{Page: page, Limit: limit, Returned: len(r.Evidence), HasMore: hasMore}
	if hasMore {
		r.Pagination.NextPage = page + 1
	}
	return r
}

func (o LLMOptions) publicMode() bool {
	return strings.EqualFold(o.Mode, "public") || (!o.AllowProject && strings.EqualFold(o.Mode, "group"))
}

func (r LLMResult) withPublicFilter(opts LLMOptions) LLMResult {
	if !opts.publicMode() {
		return r
	}
	filterEvidence := func(items []LLMEvidence) ([]LLMEvidence, int) {
		kept := make([]LLMEvidence, 0, len(items))
		redacted := 0
		for _, item := range items {
			if opts.sourceIsPrivate(item.Source) {
				redacted++
				continue
			}
			kept = append(kept, item)
		}
		return kept, redacted
	}
	var redacted int
	r.Evidence, redacted = filterEvidence(r.Evidence)
	r.Redacted += redacted
	if r.Hotspots != nil {
		filtered := make(map[string][]LLMEvidence, len(r.Hotspots))
		for group, items := range r.Hotspots {
			kept, count := filterEvidence(items)
			r.Redacted += count
			// Do not retain an empty keyed group: callers may use the group key
			// as an object id, source label, or other provenance-bearing hint.
			if len(kept) > 0 {
				filtered[group] = kept
			}
		}
		r.Hotspots = filtered
	}
	if r.Topology != nil {
		r.Topology, redacted = filterPublicTopology(*r.Topology, opts)
		r.Redacted += redacted
	}
	// Most LLMResult aggregates are calculated before evidence filtering. They
	// can reveal private-source existence or cardinality even when every raw
	// row was removed. Public results therefore expose only provenance-qualified
	// evidence and topology; never retain aggregate counts, missing-id lists, or
	// legacy follow-up hints that could contain a private identifier.
	r.Counts = nil
	r.Impact = nil
	r.MissingLocKeys = nil
	r.MissingResources = nil
	r.ScopeFixHints = nil
	r.NextQueries = nil
	r.Redacted = 0
	r.Summary = "Public visibility returns only evidence with a configured non-private source."
	r.Guidance = []string{"Public visibility omits private-source findings and aggregate counts."}
	return r
}

// filterPublicTopology is defensive: event-chain construction already applies
// public visibility while walking the graph, but a result may be assembled by
// another index query in the future. Keep every nested evidence surface behind
// the same Source.Private policy rather than assuming top-level evidence is
// the only provenance-bearing field.
func filterPublicTopology(topology LLMTopology, opts LLMOptions) (*LLMTopology, int) {
	filtered := topology
	filtered.Nodes = make([]LLMTopologyNode, 0, len(topology.Nodes))
	removed := map[string]bool{}
	redacted := 0
	for _, node := range topology.Nodes {
		if opts.sourceIsPrivate(node.Source) {
			removed[node.ID] = true
			redacted++
			continue
		}
		filtered.Nodes = append(filtered.Nodes, node)
	}
	if removed[topology.Center] {
		return nil, redacted
	}
	filtered.Edges = make([]LLMTopologyEdge, 0, len(topology.Edges))
	for _, edge := range topology.Edges {
		if opts.sourceIsPrivate(edge.Source) || removed[edge.From] || removed[edge.To] {
			redacted++
			continue
		}
		filtered.Edges = append(filtered.Edges, edge)
	}
	filterIDs := func(ids []string) []string {
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if removed[id] {
				continue
			}
			kept = append(kept, id)
		}
		return kept
	}
	filtered.Roots = filterIDs(topology.Roots)
	filtered.Leaves = filterIDs(topology.Leaves)
	filtered.Cycles = make([][]string, 0, len(topology.Cycles))
	for _, cycle := range topology.Cycles {
		keep := true
		for _, id := range cycle {
			if removed[id] {
				keep = false
				break
			}
		}
		if keep {
			filtered.Cycles = append(filtered.Cycles, cycle)
		}
	}
	filtered.PathsFromCenter = make([]LLMTopologyPath, 0, len(topology.PathsFromCenter))
	for _, path := range topology.PathsFromCenter {
		keep := !removed[path.To]
		for _, id := range path.Nodes {
			if removed[id] {
				keep = false
				break
			}
		}
		if keep {
			filtered.PathsFromCenter = append(filtered.PathsFromCenter, path)
		}
	}
	return &filtered, redacted
}

func (o LLMOptions) sourceIsPrivate(source string) bool {
	name := strings.ToLower(strings.TrimSpace(source))
	if name == "" {
		// Public evidence must retain a positive source provenance. A missing
		// source is indistinguishable from a future producer forgetting to set
		// it, so fail closed instead of treating it as public by accident.
		return true
	}
	if name == "patch" {
		return true
	}
	if len(o.PrivateSources) == 0 {
		// Direct indexer callers and legacy caches did not carry source policy.
		// Retain their former project/patch behavior until an MCP runtime supplies
		// the configured policy map.
		return name == "project"
	}
	// Unknown provenance is private by default. Public filtering must never
	// become permissive merely because a stale cache lacks a policy record.
	private, known := o.PrivateSources[name]
	return !known || private
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
	obj, preRedacted := filterObjectQueryForPublic(obj, opts)
	r := LLMResult{Query: id, Intent: "query_object", Counts: map[string]int{
		"definitions":        len(obj.Definitions),
		"resolution_groups":  len(obj.Resolution),
		"file_overrides":     len(obj.FileOverrides),
		"event_profiles":     len(obj.EventProfiles),
		"character_profiles": len(obj.CharacterProfiles),
		"diagnostics":        0,
	}, Redacted: preRedacted}
	staticFields, historyDates, historyFields := characterProfileCounts(obj.CharacterProfiles)
	r.Counts["character_static_fields"] = staticFields
	r.Counts["character_history_dates"] = historyDates
	r.Counts["character_history_fields"] = historyFields
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
	for i, resolution := range obj.Resolution {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{
			Kind: "definition_resolution", Type: resolution.Type, Name: resolution.Name,
			Detail: fmt.Sprintf("mode=%s status=%s candidates=%d active=%d reason=%s", resolution.Mode, resolution.Status, resolution.CandidateCount, resolution.ActiveCount, resolution.Reason),
		})
	}
	liveDiagnostics := titleAmbiguityDiagnostics(obj)
	r.Counts["diagnostics"] = len(liveDiagnostics)
	for _, diagnostic := range liveDiagnostics {
		r.Evidence = append(r.Evidence, diagnosticEvidence(diagnostic))
	}
	for i, file := range obj.FileOverrides {
		if i >= limit {
			break
		}
		detail := fmt.Sprintf("logical_path=%s rank=%d overridden=%t", file.LogicalPath, file.Rank, file.Overridden)
		if file.OverrideReason != "" {
			detail += fmt.Sprintf(" reason=%s by=%s rule=%s", file.OverrideReason, file.OverrideBySource, file.OverrideRule)
		}
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "file_override", Source: file.Source, Path: evidencePath(file.Path), Detail: detail})
	}
	for _, profile := range obj.EventProfiles {
		for i, field := range profile.Fields {
			if i >= limit {
				break
			}
			r.Evidence = append(r.Evidence, LLMEvidence{
				Kind: "event_field", Type: "event", Name: profile.Name, Source: profile.Source,
				Path: evidencePath(profile.Path), Line: field.Line,
				Detail: fmt.Sprintf("field=%s shape=%s raw=%s", field.Field, field.Shape, field.Raw),
			})
		}
	}
	r.Evidence = append(r.Evidence, characterProfileEvidence(obj.CharacterProfiles, limit)...)
	first := obj.Definitions[0]
	resolution := "unclassified"
	if len(obj.Resolution) > 0 {
		resolution = obj.Resolution[0].Status
	}
	r.Summary = fmt.Sprintf("Found %d definition candidate(s) for %q; resolution=%s. First match is %s:%s from %s.", len(obj.Definitions), id, resolution, first.Type, first.Name, first.Source)
	r.Guidance = []string{
		"Use definition status and resolution evidence; do not assume the first row is active when status is ambiguous or merged.",
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

// filterObjectQueryForPublic removes non-public object families before any
// aggregate/resolution metadata is calculated. Top-level evidence filtering is
// not enough here: DefinitionResolution and count fields have no source field
// of their own, so they could otherwise disclose that a private definition
// exists even after its individual evidence row was redacted.
func filterObjectQueryForPublic(obj ObjectQuery, opts LLMOptions) (ObjectQuery, int) {
	if !opts.publicMode() {
		return obj, 0
	}
	filtered := obj
	filtered.Definitions = make([]ObjectDef, 0, len(obj.Definitions))
	redacted := 0
	for _, definition := range obj.Definitions {
		if opts.sourceIsPrivate(definition.Source) {
			redacted++
			continue
		}
		filtered.Definitions = append(filtered.Definitions, definition)
	}
	filtered.Overrides = append([]ObjectDef(nil), filtered.Definitions...)
	filtered.Resolution = resolveDefinitionCandidates(filtered.Definitions)
	// Override chains can mention the private source that hid a public file via
	// OverrideBySource. Keep them private-only until that provenance is modeled
	// as a first-class, independently filterable field.
	filtered.FileOverrides = nil
	filtered.EventProfiles = make([]EventProfile, 0, len(obj.EventProfiles))
	for _, profile := range obj.EventProfiles {
		if opts.sourceIsPrivate(profile.Source) {
			redacted++
			continue
		}
		filtered.EventProfiles = append(filtered.EventProfiles, profile)
	}
	filtered.CharacterProfiles = make([]CharacterProfile, 0, len(obj.CharacterProfiles))
	for _, profile := range obj.CharacterProfiles {
		if opts.sourceIsPrivate(profile.Source) {
			redacted++
			continue
		}
		filtered.CharacterProfiles = append(filtered.CharacterProfiles, profile)
	}
	return filtered, redacted
}

func objectEvidence(kind string, d ObjectDef) LLMEvidence {
	ev := LLMEvidence{Kind: kind, Type: d.Type, Name: d.Name, Source: d.Source, Path: evidencePath(d.Path), Line: d.Line, Column: d.Column}
	if d.Status != "" {
		ev.Detail = "status=" + d.Status
	}
	if d.Type == "scripted_variable" && d.Value != "" {
		if ev.Detail != "" {
			ev.Detail += " "
		}
		ev.Detail += "value=" + d.Value
	}
	return ev
}

func characterProfileCounts(profiles []CharacterProfile) (staticFields, historyDates, historyFields int) {
	for _, profile := range profiles {
		staticFields += len(profile.StaticFields)
		historyDates += len(profile.Timeline)
		for _, entry := range profile.Timeline {
			historyFields += len(entry.Fields)
		}
	}
	return staticFields, historyDates, historyFields
}

func characterProfileEvidence(profiles []CharacterProfile, limit int) []LLMEvidence {
	if limit <= 0 {
		limit = defaultLLMLimit
	}
	var staticEvidence, historyEvidence []LLMEvidence
	for _, profile := range profiles {
		for _, field := range profile.StaticFields {
			staticEvidence = append(staticEvidence, LLMEvidence{
				Kind: "character_field", Type: "character", Name: profile.Name, Source: profile.Source,
				Path: evidencePath(profile.Path), Line: field.Line,
				Detail: fmt.Sprintf("field=%s shape=%s raw=%s", field.Field, field.Shape, field.Raw),
			})
		}
		for _, entry := range profile.Timeline {
			for _, field := range entry.Fields {
				historyEvidence = append(historyEvidence, LLMEvidence{
					Kind: "character_history", Type: "character", Name: profile.Name, Source: profile.Source,
					Path: evidencePath(profile.Path), Line: field.Line,
					Detail: fmt.Sprintf("date=%s field=%s shape=%s raw=%s", entry.Date, field.Field, field.Shape, field.Raw),
				})
			}
		}
	}
	if len(staticEvidence) == 0 {
		return historyEvidence[:minInt(limit, len(historyEvidence))]
	}
	if len(historyEvidence) == 0 {
		return staticEvidence[:minInt(limit, len(staticEvidence))]
	}
	// Keep both identity and lifecycle evidence visible at the default limit;
	// otherwise long character headers hide every dated entry.
	staticLimit := (limit + 1) / 2
	historyLimit := limit - staticLimit
	out := append([]LLMEvidence(nil), staticEvidence[:minInt(staticLimit, len(staticEvidence))]...)
	out = append(out, historyEvidence[:minInt(historyLimit, len(historyEvidence))]...)
	if len(out) < limit {
		for _, evidence := range staticEvidence[minInt(staticLimit, len(staticEvidence)):] {
			if len(out) >= limit {
				break
			}
			out = append(out, evidence)
		}
	}
	if len(out) < limit {
		for _, evidence := range historyEvidence[minInt(historyLimit, len(historyEvidence)):] {
			if len(out) >= limit {
				break
			}
			out = append(out, evidence)
		}
	}
	return out
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
	if h.Name != "" {
		detail += " ref=" + h.Name
	}
	if h.Raw != "" && h.Raw != h.Name {
		detail += " raw=" + h.Raw
	}
	if h.Relation != "" {
		detail += " relation=" + h.Relation
	}
	if h.Phase != "" {
		detail += " phase=" + h.Phase
	}
	if h.Resolution != "" {
		detail += " resolution=" + h.Resolution
	}
	if h.ResolutionReason != "" {
		detail += " reason=" + h.ResolutionReason
	}
	ev := LLMEvidence{Kind: kind, Type: h.FromType, Name: name, Source: h.Source, Path: evidencePath(h.Path), Line: h.Line, Column: h.Column, Detail: detail}
	if h.Resolution == "unresolved" {
		ev.Suggestion, ev.RuleSource = refHint(h.Kind)
	}
	return ev
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
			"incoming":          refs.IncomingTotal,
			"outgoing":          refs.OutgoingTotal,
			"incoming_returned": len(refs.Incoming),
			"outgoing_returned": len(refs.Outgoing),
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
	r.Summary = fmt.Sprintf("%q has %d incoming and %d outgoing indexed reference(s).", id, refs.IncomingTotal, refs.OutgoingTotal)
	r.Guidance = []string{
		"Incoming refs are scripts that may break if this id is renamed or removed.",
		"Outgoing unresolved refs are the first things to fix before validating generated code.",
	}
	if refs.IncomingTruncated || refs.OutgoingTruncated {
		r.Guidance = append(r.Guidance, "Reference totals are exact; evidence is capped. Narrow with a typed id or inspect matching files when more evidence is needed.")
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
		r.Evidence = append(r.Evidence, LLMEvidence{Kind: "sound_event", Name: strings.Trim(id, `"`), Detail: "known from compiled CK3 sound-event evidence"})
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
		r.Guidance = []string{"Sound events are validated from compiled CK3 sound-event evidence, not from filesystem resource files."}
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
	projectSource, err := db.projectSourceName(ctx)
	if err != nil {
		return LLMResult{}, err
	}
	if projectSource == "" {
		// Source-role metadata was introduced after older published caches. Those
		// caches used the conventional project source name, so preserve their
		// read-only diagnostic behavior until the next successful scan persists
		// source_layers. Current caches always resolve this through SourceRole.
		projectSource = "project"
	}
	rep, err := db.CachedValidationForSource(ctx, projectSource)
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
		"This summary is limited to the current project source plus global diagnostics; upstream game and Godherja files remain searchable as reference evidence.",
		"This MCP tool reads diagnostics refreshed by full or incremental scan; ambiguous title definitions are also synthesized live by ck3_inspect.",
		"After editing files, run ck3-index scan --files for small changes or ck3-index scan for a full refresh before trusting a clean result.",
	}
	if len(rep.Diagnostics) > 0 {
		r.NextQueries = []LLMNextQuery{{Tool: "explain_diagnostic", ID: rep.Diagnostics[0].Code, Reason: "inspect the most severe diagnostic class"}}
	}
	return r.withPublicFilter(opts), nil
}

func (db *DB) LLMExplainDiagnostic(ctx context.Context, code string, opts LLMOptions) (LLMResult, error) {
	return db.LLMExplainDiagnosticFiltered(ctx, DiagnosticFilter{Code: code}, opts)
}

func (db *DB) LLMExplainDiagnosticFiltered(ctx context.Context, filter DiagnosticFilter, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	fetchLimit := page*limit + 1
	if fetchLimit > 501 {
		fetchLimit = 501
	}
	diags, err := db.ExplainDiagnosticFiltered(ctx, filter)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{Query: filter.Code, Intent: "explain_diagnostic", Counts: map[string]int{"diagnostics": len(diags)}}
	for _, d := range diags {
		if opts.publicMode() && opts.sourceIsPrivate(d.SourceLayer) {
			continue
		}
		r.Evidence = append(r.Evidence, diagnosticEvidence(d))
		if len(r.Evidence) >= fetchLimit {
			break
		}
	}
	r = r.withPublicFilter(opts)
	r = paginateLLMResult(r, page, limit)
	r.Summary = fmt.Sprintf("Found diagnostic evidence for code %q; page %d returned %d item(s).", filter.Code, page, len(r.Evidence))
	return r, nil
}

func diagnosticEvidence(d Diagnostic) LLMEvidence {
	suggestion, ruleSource := diagnosticHint(d.Code, d.Message)
	detail := fmt.Sprintf("%s %s confidence=%s occurrences=%d: %s", d.Severity, d.Code, d.Confidence, d.Occurrences, d.Message)
	return LLMEvidence{Kind: "diagnostic", Source: d.SourceLayer, Path: evidencePath(d.Path), Line: d.Line, Column: d.Column, Detail: detail, Suggestion: suggestion, RuleSource: ruleSource}
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
	diags = aggregateDiagnostics(append(diags, titleAmbiguityDiagnostics(obj)...))
	r := LLMResult{
		Query:  id,
		Intent: "inspect_object",
		Counts: map[string]int{
			"definitions":        len(obj.Definitions),
			"incoming_refs":      refs.IncomingTotal,
			"outgoing_refs":      refs.OutgoingTotal,
			"localization":       len(loc.Values),
			"diagnostics":        len(diags),
			"character_profiles": len(obj.CharacterProfiles),
		},
	}
	staticFields, historyDates, historyFields := characterProfileCounts(obj.CharacterProfiles)
	r.Counts["character_static_fields"] = staticFields
	r.Counts["character_history_dates"] = historyDates
	r.Counts["character_history_fields"] = historyFields
	for i, d := range obj.Definitions {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, objectEvidence("definition", d))
	}
	r.Evidence = append(r.Evidence, characterProfileEvidence(obj.CharacterProfiles, limit)...)
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
		r.Summary = fmt.Sprintf("%q summary: %d definition(s), %d incoming ref(s), %d outgoing ref(s), %d localization value(s), %d related diagnostic(s).", id, len(obj.Definitions), refs.IncomingTotal, refs.OutgoingTotal, len(loc.Values), len(diags))
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
			"refs":        refs.IncomingTotal + refs.OutgoingTotal,
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
	r.Summary = fmt.Sprintf("Edit prep for %q: object type %q, %d definition(s), %d example(s), %d schema field(s), %d empirical pattern(s), %d related ref(s).", id, typ, r.Counts["definitions"], len(ex.Examples), len(rules.Fields), len(patterns.Fields), refs.IncomingTotal+refs.OutgoingTotal)
	r.Guidance = []string{
		"Generation workflow: follow vanilla-first examples, empirical field patterns, then schema fields; use lookup_scope/lookup_shape for every unfamiliar trigger or effect.",
		"Use existing scripted triggers/effects and modifiers when indexed; invent new ids only after diagnose_key returns no definition/ref conflict.",
		"Generated code must include matching localization and must be refreshed with scan or CLI validate before use.",
	}
	if typ == "casus_belli_type" {
		r.Guidance = append(r.Guidance, "For casus belli ai_score and ai_score_mult blocks, initialize with value = ... and then use add/multiply operations; base = ... is not a valid current CK3 script-value field.")
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
			"incoming_refs":     refs.IncomingTotal,
			"outgoing_refs":     refs.OutgoingTotal,
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
	for _, d := range diags {
		if d.Severity == "error" {
			blockers++
		} else {
			warnings++
		}
		add(diagnosticEvidence(d))
	}
	for _, h := range refs.Outgoing {
		if h.Resolved {
			continue
		}
		if preflightRuntimeRef(h.Kind) {
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
	for _, h := range refs.Outgoing {
		if !h.Resolved {
			continue
		}
		if preflightRuntimeRef(h.Kind) {
			continue
		}
		add(refEvidence("outgoing_ref", h))
	}
	for _, ev := range locEvidence {
		add(ev)
	}
	if len(obj.Definitions) == 0 && (refs.IncomingTotal > 0 || refs.OutgoingTotal > 0) {
		warnings++
	}
	if locChecked > 0 && locFound == 0 {
		warnings++
	}
	r.Counts["unresolved_refs"] = unresolved
	r.Counts["blocking_risks"] = blockers
	r.Counts["nonblocking_risks"] = warnings
	if blockers == 0 && warnings == 0 {
		r.Summary = fmt.Sprintf("Preflight for %q found no immediate indexed blockers. Definitions=%d, outgoing refs=%d, localization hits=%d.", id, len(obj.Definitions), refs.OutgoingTotal, locFound)
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
	case "localization", "define", "scope":
		return false
	default:
		return true
	}
}

func preflightRuntimeRef(kind string) bool {
	switch kind {
	case "flag", "global_var":
		return true
	default:
		return false
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
		add(name + ".t")
		add(name + ".desc")
		add(name + ".tt")
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
		Query:    id,
		Intent:   "diagnose_key",
		Redacted: obj.Redacted + loc.Redacted + res.Redacted + refs.Redacted,
		Counts: map[string]int{
			"definitions":              obj.Counts["definitions"],
			"character_profiles":       obj.Counts["character_profiles"],
			"character_static_fields":  obj.Counts["character_static_fields"],
			"character_history_dates":  obj.Counts["character_history_dates"],
			"character_history_fields": obj.Counts["character_history_fields"],
			"localization":             loc.Counts["values"],
			"resources":                res.Counts["resources"],
			"known_sounds":             res.Counts["known_sounds"],
			"incoming":                 refs.Counts["incoming"],
			"outgoing":                 refs.Counts["outgoing"],
			"diagnostics":              len(diags),
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
		r.Guidance = []string{"This id is a known sound event from compiled CK3 sound-event evidence; it is not expected to appear as a filesystem resource."}
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
