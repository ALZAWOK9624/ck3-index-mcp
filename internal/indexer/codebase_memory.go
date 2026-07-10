package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ck3-index/internal/script"
)

const architectureOverviewCacheKey = "architecture_overview_v1"

type architectureOverviewCache struct {
	Counts   map[string]int           `json:"counts"`
	Hotspots map[string][]LLMEvidence `json:"hotspots"`
}

// LLMArchitectureOverview is a compact, codebase-memory style map of the
// indexed CK3 workspace. It favors structure and hotspots over raw dumps.
func (db *DB) LLMArchitectureOverview(ctx context.Context, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	r := LLMResult{
		Intent:   "architecture_overview",
		Counts:   map[string]int{},
		Hotspots: map[string][]LLMEvidence{},
		Guidance: []string{
			"Use this as the first map of the indexed workspace before picking a narrower object or reference query.",
			"High unresolved-ref or diagnostic hotspots are better next targets than randomly opening large files.",
			"Use dependency_graph for object-centered impact once a concrete id is known.",
		},
		NextQueries: []LLMNextQuery{
			{Tool: "query_object_types", Reason: "inspect the full object type distribution"},
			{Tool: "validate_project", Reason: "inspect cached diagnostics before edits"},
		},
	}

	cache, err := db.loadArchitectureOverviewCache(ctx)
	if err != nil {
		return LLMResult{}, err
	}
	if cache == nil {
		cache, err = db.computeArchitectureOverview(ctx, 20)
		if err != nil {
			return LLMResult{}, err
		}
		r.NeedsRefresh = true
	}
	r.Counts = cache.Counts
	r.Hotspots = limitHotspots(cache.Hotspots, limit)
	for _, group := range []string{"sources", "objects", "refs", "diagnostics"} {
		r.Evidence = append(r.Evidence, r.Hotspots[group]...)
	}

	r.Summary = fmt.Sprintf("Indexed architecture: %d active file(s), %d active object(s), %d reference edge(s), %d localization row(s), %d resource row(s), %d diagnostic(s).",
		r.Counts["active_files"], r.Counts["active_objects"], r.Counts["refs"], r.Counts["localization"], r.Counts["resources"], r.Counts["diagnostics"])
	return r.withPublicFilter(opts), nil
}

func (db *DB) loadArchitectureOverviewCache(ctx context.Context) (*architectureOverviewCache, error) {
	raw, err := db.metaValue(ctx, architectureOverviewCacheKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var cache architectureOverviewCache
	if err := json.Unmarshal([]byte(raw), &cache); err != nil {
		return nil, nil
	}
	if cache.Counts == nil || cache.Hotspots == nil {
		return nil, nil
	}
	return &cache, nil
}

func (db *DB) RefreshArchitectureOverviewCache(ctx context.Context, tx *sql.Tx) error {
	cache, err := db.computeArchitectureOverviewTx(ctx, tx, 20)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, architectureOverviewCacheKey, string(data))
	return err
}

func (db *DB) computeArchitectureOverview(ctx context.Context, limit int) (*architectureOverviewCache, error) {
	return db.computeArchitectureOverviewTx(ctx, db.sql, limit)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (db *DB) computeArchitectureOverviewTx(ctx context.Context, q queryer, limit int) (*architectureOverviewCache, error) {
	cache := &architectureOverviewCache{Counts: map[string]int{}, Hotspots: map[string][]LLMEvidence{}}
	if err := scalarCounts(ctx, q, cache.Counts); err != nil {
		return nil, err
	}
	var err error
	if cache.Hotspots["sources"], err = sourceSummary(ctx, q, limit); err != nil {
		return nil, err
	}
	if cache.Hotspots["objects"], err = topObjectTypes(ctx, q, limit); err != nil {
		return nil, err
	}
	if cache.Hotspots["refs"], err = topRefKinds(ctx, q, limit); err != nil {
		return nil, err
	}
	if cache.Hotspots["diagnostics"], err = diagnosticHotspots(ctx, q, limit); err != nil {
		return nil, err
	}
	return cache, nil
}

func limitHotspots(in map[string][]LLMEvidence, limit int) map[string][]LLMEvidence {
	out := map[string][]LLMEvidence{}
	for group, items := range in {
		if len(items) > limit {
			items = items[:limit]
		}
		out[group] = append([]LLMEvidence(nil), items...)
	}
	return out
}

func (db *DB) scalarCounts(ctx context.Context, counts map[string]int) error {
	return scalarCounts(ctx, db.sql, counts)
}

func scalarCounts(ctx context.Context, q queryer, counts map[string]int) error {
	queries := map[string]string{
		"active_files":     `SELECT COUNT(*) FROM files WHERE overridden=0`,
		"overridden_files": `SELECT COUNT(*) FROM files WHERE overridden!=0`,
		"active_objects":   `SELECT COUNT(*) FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0`,
		"refs":             `SELECT COUNT(*) FROM refs r JOIN files f ON f.id=r.file_id WHERE f.overridden=0`,
		"unresolved_refs":  `SELECT COUNT(*) FROM refs r JOIN files f ON f.id=r.file_id WHERE f.overridden=0 AND r.resolved=0`,
		"localization":     `SELECT COUNT(*) FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0`,
		"resources":        `SELECT COUNT(*) FROM resources r JOIN files f ON f.id=r.file_id WHERE f.overridden=0`,
		"schema_fields":    `SELECT COUNT(*) FROM schema_fields s JOIN files f ON f.id=s.file_id WHERE f.overridden=0`,
		"diagnostics":      `SELECT COUNT(*) FROM diagnostics`,
	}
	for name, query := range queries {
		var n int
		if err := q.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return err
		}
		counts[name] = n
	}
	return nil
}

func (db *DB) appendSourceSummary(ctx context.Context, r *LLMResult, limit int) error {
	items, err := sourceSummary(ctx, db.sql, limit)
	if err == nil {
		r.Evidence = append(r.Evidence, items...)
	}
	return err
}

func sourceSummary(ctx context.Context, q queryer, limit int) ([]LLMEvidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_name,source_rank,kind,COUNT(*),
		SUM(CASE WHEN overridden=0 THEN 1 ELSE 0 END)
		FROM files GROUP BY source_name,source_rank,kind
		ORDER BY source_rank,source_name,kind LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var source, kind string
		var rank, files, active int
		if err := rows.Scan(&source, &rank, &kind, &files, &active); err != nil {
			return nil, err
		}
		out = append(out, LLMEvidence{
			Kind:   "source_summary",
			Source: source,
			Detail: fmt.Sprintf("rank=%d kind=%s files=%d active=%d", rank, kind, files, active),
		})
	}
	return out, rows.Err()
}

func (db *DB) appendTopObjectTypes(ctx context.Context, r *LLMResult, limit int) error {
	items, err := topObjectTypes(ctx, db.sql, limit)
	if err == nil {
		r.Evidence = append(r.Evidence, items...)
	}
	return err
}

func topObjectTypes(ctx context.Context, q queryer, limit int) ([]LLMEvidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT o.object_type,COUNT(*)
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0
		GROUP BY o.object_type ORDER BY COUNT(*) DESC, o.object_type LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var typ string
		var count int
		if err := rows.Scan(&typ, &count); err != nil {
			return nil, err
		}
		out = append(out, LLMEvidence{
			Kind:   "object_type",
			Type:   typ,
			Detail: fmt.Sprintf("count=%d", count),
		})
	}
	return out, rows.Err()
}

func (db *DB) appendTopRefKinds(ctx context.Context, r *LLMResult, limit int) error {
	items, err := topRefKinds(ctx, db.sql, limit)
	if err == nil {
		r.Evidence = append(r.Evidence, items...)
	}
	return err
}

func topRefKinds(ctx context.Context, q queryer, limit int) ([]LLMEvidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT ref_kind,COUNT(*),
		SUM(CASE WHEN resolved=0 THEN 1 ELSE 0 END)
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0
		GROUP BY ref_kind ORDER BY COUNT(*) DESC, ref_kind LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var kind string
		var total, unresolved int
		if err := rows.Scan(&kind, &total, &unresolved); err != nil {
			return nil, err
		}
		out = append(out, LLMEvidence{
			Kind:   "ref_kind",
			Name:   kind,
			Detail: fmt.Sprintf("refs=%d unresolved=%d", total, unresolved),
		})
	}
	return out, rows.Err()
}

func (db *DB) appendDiagnosticHotspots(ctx context.Context, r *LLMResult, limit int) error {
	items, err := diagnosticHotspots(ctx, db.sql, limit)
	if err == nil {
		r.Evidence = append(r.Evidence, items...)
	}
	return err
}

func diagnosticHotspots(ctx context.Context, q queryer, limit int) ([]LLMEvidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT code,severity,COUNT(*)
		FROM diagnostics
		GROUP BY code,severity
		ORDER BY CASE severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, COUNT(*) DESC, code
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMEvidence
	for rows.Next() {
		var code, severity string
		var total int
		if err := rows.Scan(&code, &severity, &total); err != nil {
			return nil, err
		}
		out = append(out, LLMEvidence{
			Kind:       "diagnostic_hotspot",
			Name:       code,
			Detail:     fmt.Sprintf("%s count=%d", severity, total),
			Suggestion: diagnosticNextStep(code),
		})
	}
	return out, rows.Err()
}

func diagnosticNextStep(code string) string {
	if code == "" {
		return ""
	}
	suggestion, _ := diagnosticHint(code, "")
	if suggestion != "" {
		return suggestion
	}
	return "Use explain_diagnostic for examples, then narrow to the affected object or file."
}

// LLMDependencyGraph returns a small object-centered graph. It mirrors the
// useful part of codebase-memory graph traversal without introducing a general
// graph query language yet.
func (db *DB) LLMDependencyGraph(ctx context.Context, id string, opts LLMOptions) (LLMResult, error) {
	limit := opts.normalizedLimit()
	depth := opts.normalizedDepth()
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return LLMResult{}, err
	}
	graph, err := db.dependencyGraph(ctx, id, depth, limit)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Query:  id,
		Intent: "dependency_graph",
		Counts: map[string]int{
			"definitions":    len(obj.Definitions),
			"incoming_refs":  graph.incoming,
			"outgoing_refs":  graph.outgoing,
			"semantic_edges": graph.semantic,
			"edges":          len(graph.edges),
			"unresolved":     graph.unresolved,
			"depth":          depth,
		},
		Guidance: []string{
			"Incoming edges are impact risk for rename/delete; outgoing unresolved edges are generation or validation risks.",
			"Semantic edges are derived from CK3 script shape such as parameters, localization, resources, and scripted trigger/effect consumers.",
		},
		NextQueries: []LLMNextQuery{
			{Tool: "inspect_object", ID: id, Reason: "inspect definition, localization, and diagnostics for the center node"},
			{Tool: "impact_patch", ID: id, Reason: "use before delete or rename operations"},
		},
	}
	for i, d := range obj.Definitions {
		if i >= limit {
			break
		}
		r.Evidence = append(r.Evidence, objectEvidence("center_definition", d))
	}
	for _, ev := range graph.evidence {
		if len(r.Evidence) >= limit*4 {
			break
		}
		r.Evidence = append(r.Evidence, ev)
	}
	r.Counts["nodes"] = graph.nodes
	if len(obj.Definitions) == 0 {
		r.Summary = fmt.Sprintf("Dependency graph for %q has no center definition, but found %d incoming, %d outgoing, and %d semantic edge(s).", id, graph.incoming, graph.outgoing, graph.semantic)
		r.NeedsRefresh = true
	} else {
		r.Summary = fmt.Sprintf("Dependency graph for %q: %d definition(s), depth=%d, %d node(s), %d edge(s), %d semantic edge(s), %d unresolved edge(s).", id, len(obj.Definitions), depth, graph.nodes, len(graph.edges), graph.semantic, graph.unresolved)
	}
	return r.withPublicFilter(opts), nil
}

type graphEdge struct {
	FromType, FromName string
	ToType, ToName     string
	EdgeType           string
	Source             string
	Path               string
	Line, Column       int
	Detail             string
	Resolved           bool
	Semantic           bool
}

type graphResult struct {
	edges      []graphEdge
	evidence   []LLMEvidence
	nodes      int
	incoming   int
	outgoing   int
	semantic   int
	unresolved int
}

func (db *DB) dependencyGraph(ctx context.Context, id string, depth, limit int) (graphResult, error) {
	seenIDs := map[string]int{id: 0}
	frontier := []string{id}
	seenEdges := map[string]bool{}
	seenNodes := map[string]bool{nodeKey("", id): true}
	var result graphResult
	for level := 0; level < depth && len(frontier) > 0; level++ {
		var next []string
		for _, cur := range frontier {
			refs, err := db.QueryRefs(ctx, cur)
			if err != nil {
				return graphResult{}, err
			}
			for _, e := range refGraphEdges(refs) {
				if addGraphEdge(&result, seenEdges, seenNodes, e) {
					if !e.Resolved {
						result.unresolved++
					}
					if e.ToName != "" && seenIDs[e.ToName] == 0 && e.ToName != id {
						seenIDs[e.ToName] = level + 1
						next = append(next, e.ToName)
					}
					if e.FromName != "" && seenIDs[e.FromName] == 0 && e.FromName != id {
						seenIDs[e.FromName] = level + 1
						next = append(next, e.FromName)
					}
				}
			}
			sem, err := db.semanticGraphEdges(ctx, cur)
			if err != nil {
				return graphResult{}, err
			}
			for _, e := range sem {
				if addGraphEdge(&result, seenEdges, seenNodes, e) {
					if e.Semantic {
						result.semantic++
					}
					if e.ToName != "" && seenIDs[e.ToName] == 0 && e.ToName != id {
						seenIDs[e.ToName] = level + 1
						next = append(next, e.ToName)
					}
					if e.FromName != "" && seenIDs[e.FromName] == 0 && e.FromName != id {
						seenIDs[e.FromName] = level + 1
						next = append(next, e.FromName)
					}
				}
			}
		}
		frontier = next
	}
	for _, e := range result.edges {
		if len(result.evidence) >= limit*4 {
			break
		}
		if e.FromName != "" {
			result.evidence = append(result.evidence, LLMEvidence{Kind: "graph_node", Type: e.FromType, Name: e.FromName, Source: e.Source, Path: evidencePath(e.Path), Line: e.Line})
		}
		result.evidence = append(result.evidence, edgeEvidence(e))
		if e.ToName != "" {
			result.evidence = append(result.evidence, LLMEvidence{Kind: "graph_node", Type: e.ToType, Name: e.ToName, Source: e.Source, Path: evidencePath(e.Path), Line: e.Line})
		}
	}
	result.nodes = len(seenNodes)
	return result, nil
}

func refGraphEdges(refs RefQuery) []graphEdge {
	var out []graphEdge
	for _, h := range refs.Incoming {
		out = append(out, graphEdge{
			FromType: h.FromType, FromName: h.FromName, ToType: h.Kind, ToName: h.Name,
			EdgeType: "incoming:" + h.Kind, Source: h.Source, Path: h.Path, Line: h.Line, Column: h.Column,
			Detail: refDetail(h), Resolved: h.Resolved,
		})
	}
	for _, h := range refs.Outgoing {
		out = append(out, graphEdge{
			FromType: h.FromType, FromName: h.FromName, ToType: h.Kind, ToName: h.Name,
			EdgeType: "outgoing:" + h.Kind, Source: h.Source, Path: h.Path, Line: h.Line, Column: h.Column,
			Detail: refDetail(h), Resolved: h.Resolved,
		})
	}
	return out
}

func refDetail(h RefHit) string {
	detail := h.Kind
	if h.Raw != "" && h.Raw != h.Name {
		detail += " raw=" + h.Raw
	}
	if !h.Resolved {
		detail += " [unresolved]"
	}
	return detail
}

func addGraphEdge(result *graphResult, seenEdges, seenNodes map[string]bool, e graphEdge) bool {
	if e.FromName == "" && e.ToName == "" {
		return false
	}
	key := e.FromType + ":" + e.FromName + ">" + e.EdgeType + ">" + e.ToType + ":" + e.ToName + "@" + e.Path + fmt.Sprint(e.Line)
	if seenEdges[key] {
		return false
	}
	seenEdges[key] = true
	result.edges = append(result.edges, e)
	if e.EdgeType != "" && strings.HasPrefix(e.EdgeType, "incoming:") {
		result.incoming++
	} else if e.EdgeType != "" && strings.HasPrefix(e.EdgeType, "outgoing:") {
		result.outgoing++
	}
	if e.FromName != "" {
		seenNodes[nodeKey(e.FromType, e.FromName)] = true
	}
	if e.ToName != "" {
		seenNodes[nodeKey(e.ToType, e.ToName)] = true
	}
	return true
}

func edgeEvidence(e graphEdge) LLMEvidence {
	kind := "semantic_edge"
	if strings.HasPrefix(e.EdgeType, "incoming:") {
		kind = "incoming_edge"
	} else if strings.HasPrefix(e.EdgeType, "outgoing:") {
		kind = "outgoing_edge"
	}
	name := e.ToName
	typ := e.ToType
	if kind == "incoming_edge" {
		name = e.FromName
		typ = e.FromType
	}
	return LLMEvidence{
		Kind: kind, Type: typ, Name: name, Source: e.Source, Path: evidencePath(e.Path),
		Line: e.Line, Column: e.Column, Detail: e.Detail, EdgeType: e.EdgeType,
	}
}

func (db *DB) semanticGraphEdges(ctx context.Context, id string) ([]graphEdge, error) {
	obj, err := db.QueryObject(ctx, id)
	if err != nil || len(obj.Definitions) == 0 {
		return nil, err
	}
	var out []graphEdge
	for _, d := range obj.Definitions {
		edges, err := db.semanticEdgesForDefinition(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, edges...)
		incoming, err := db.semanticIncomingEdges(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, incoming...)
	}
	return out, nil
}

func (db *DB) semanticEdgesForDefinition(ctx context.Context, d ObjectDef) ([]graphEdge, error) {
	root, err := parseObjectDefinition(d)
	if err != nil || root == nil {
		return nil, err
	}
	var out []graphEdge
	walkScript(root.Children, func(n *script.Node) {
		raw := strings.Trim(n.Value, `"`)
		switch {
		case d.Type == "men_at_arms_type" && n.Key == "PARAMETER" && raw != "":
			out = append(out, graphEdge{FromType: d.Type, FromName: d.Name, ToType: "parameter", ToName: raw, EdgeType: "uses_parameter", Source: d.Source, Path: d.Path, Line: n.Line, Column: n.Col, Detail: "valid_for_maa_trigger PARAMETER", Semantic: true, Resolved: true})
		case d.Type == "culture_tradition" && strings.HasPrefix(d.Name, "tradition_") && n.Parent != 0 && n.Key != "" && (n.Value == "yes" || n.Value == "no") && underBlock(root, n, "parameters"):
			out = append(out, graphEdge{FromType: d.Type, FromName: d.Name, ToType: "parameter", ToName: n.Key, EdgeType: "defines_parameter", Source: d.Source, Path: d.Path, Line: n.Line, Column: n.Col, Detail: "tradition parameters", Semantic: true, Resolved: true})
		case n.Key != "" && locKeys[n.Key] && raw != "" && !strings.Contains(raw, " ") && !strings.Contains(raw, "$"):
			out = append(out, graphEdge{FromType: d.Type, FromName: d.Name, ToType: "localization", ToName: raw, EdgeType: "uses_localization", Source: d.Source, Path: d.Path, Line: n.Line, Column: n.Col, Detail: n.Key, Semantic: true, Resolved: true})
		case raw != "" && (strings.Contains(raw, "gfx/") || resourceExt.MatchString(raw)):
			out = append(out, graphEdge{FromType: d.Type, FromName: d.Name, ToType: "resource", ToName: normalizeResource(raw), EdgeType: "uses_resource", Source: d.Source, Path: d.Path, Line: n.Line, Column: n.Col, Detail: n.Key, Semantic: true, Resolved: true})
		case d.Type == "scripted_trigger" || d.Type == "scripted_effect":
			// Consumer edges are collected by semanticIncomingEdges.
		}
	})
	return out, nil
}

func (db *DB) semanticIncomingEdges(ctx context.Context, d ObjectDef) ([]graphEdge, error) {
	var out []graphEdge
	if d.Type == "men_at_arms_type" {
		params, err := db.parametersForMAA(ctx, d)
		if err != nil {
			return nil, err
		}
		for _, p := range params {
			trads, err := db.traditionsDefiningParameter(ctx, p)
			if err != nil {
				return nil, err
			}
			for _, tr := range trads {
				out = append(out,
					graphEdge{FromType: tr.Type, FromName: tr.Name, ToType: "parameter", ToName: p, EdgeType: "defines_parameter", Source: tr.Source, Path: tr.Path, Line: tr.Line, Column: tr.Column, Detail: "tradition parameters", Semantic: true, Resolved: true},
					graphEdge{FromType: "parameter", FromName: p, ToType: d.Type, ToName: d.Name, EdgeType: "unlocks_men_at_arms", Source: d.Source, Path: d.Path, Line: d.Line, Column: d.Column, Detail: "MAA can_recruit valid_for_maa_trigger", Semantic: true, Resolved: true},
				)
			}
		}
	}
	if d.Type == "scripted_trigger" || d.Type == "scripted_effect" {
		refs, err := db.QueryRefs(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		for _, h := range refs.Incoming {
			out = append(out, graphEdge{FromType: h.FromType, FromName: h.FromName, ToType: d.Type, ToName: d.Name, EdgeType: "consumes_" + d.Type, Source: h.Source, Path: h.Path, Line: h.Line, Column: h.Column, Detail: h.Raw, Semantic: true, Resolved: h.Resolved})
		}
	}
	return out, nil
}

func (db *DB) parametersForMAA(ctx context.Context, d ObjectDef) ([]string, error) {
	root, err := parseObjectDefinition(d)
	if err != nil || root == nil {
		return nil, err
	}
	seen := map[string]bool{}
	var params []string
	walkScript(root.Children, func(n *script.Node) {
		if n.Key == "PARAMETER" {
			p := strings.Trim(n.Value, `"`)
			if p != "" && !seen[p] {
				seen[p] = true
				params = append(params, p)
			}
		}
	})
	return params, nil
}

func (db *DB) traditionsDefiningParameter(ctx context.Context, param string) ([]ObjectDef, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
		FROM object_fields of
		JOIN objects o ON o.name=of.object_name AND o.object_type=of.object_type AND o.file_id=of.file_id
		JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0 AND of.field='parameters' AND o.name LIKE 'tradition_%'
		ORDER BY o.source_rank,o.path,o.line LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectDef
	for rows.Next() {
		var d ObjectDef
		if err := rows.Scan(&d.Type, &d.Name, &d.Source, &d.Rank, &d.Path, &d.Line, &d.Column); err != nil {
			return nil, err
		}
		root, err := parseObjectDefinition(d)
		if err != nil || root == nil {
			continue
		}
		found := false
		walkScript(root.Children, func(n *script.Node) {
			if underBlock(root, n, "parameters") && n.Key == param {
				found = true
			}
		})
		if found {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

func parseObjectDefinition(d ObjectDef) (*script.Node, error) {
	data, err := os.ReadFile(d.Path)
	if err != nil {
		return nil, err
	}
	parsed := script.Parse(string(data))
	var best *script.Node
	walkScript(parsed.Nodes, func(n *script.Node) {
		if best == nil && n.Kind == "block" && n.Key == d.Name {
			best = n
		}
	})
	return best, nil
}

func walkScript(nodes []*script.Node, fn func(*script.Node)) {
	for _, n := range nodes {
		fn(n)
		walkScript(n.Children, fn)
	}
}

func underBlock(root, target *script.Node, key string) bool {
	if root == nil || target == nil {
		return false
	}
	var found bool
	var walk func(nodes []*script.Node, inside bool)
	walk = func(nodes []*script.Node, inside bool) {
		for _, n := range nodes {
			now := inside || n.Key == key
			if n == target && inside {
				found = true
			}
			walk(n.Children, now)
		}
	}
	walk(root.Children, false)
	return found
}

func nodeKey(typ, name string) string {
	return strings.ToLower(typ + ":" + name)
}

func countUnresolvedRefs(refs RefQuery) int {
	n := 0
	for _, h := range refs.Incoming {
		if !h.Resolved {
			n++
		}
	}
	for _, h := range refs.Outgoing {
		if !h.Resolved {
			n++
		}
	}
	return n
}
