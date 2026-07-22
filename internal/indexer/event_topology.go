package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const (
	defaultEventChainDepth = 3
	maxEventChainDepth     = 6
)

type EventChainOptions struct {
	LLMOptions
	Direction        string
	IncludeOnActions bool
}

// LLMEventChain turns the existing typed event/on_action references into a
// bounded call hierarchy. Parsing and resolution remain owned by the normal
// index; this is only a topology and explanation layer over those facts.
func (db *DB) LLMEventChain(ctx context.Context, id string, opts EventChainOptions) (LLMResult, error) {
	direction := strings.ToLower(strings.TrimSpace(opts.Direction))
	if direction == "" {
		direction = "both"
	}
	if direction != "both" && direction != "callers" && direction != "callees" {
		return LLMResult{}, fmt.Errorf("event chain direction %q must be one of both, callers, or callees", direction)
	}
	depth := opts.Depth
	if depth <= 0 {
		depth = defaultEventChainDepth
	}
	if depth > maxEventChainDepth {
		depth = maxEventChainDepth
	}
	centerType, centerName, err := db.eventChainCenter(ctx, id, opts.LLMOptions)
	if err != nil {
		return LLMResult{}, err
	}
	center := typedTopologyID(centerType, centerName)
	limit := opts.normalizedLimit()
	nodeLimit := limit * 4
	if nodeLimit < 8 {
		nodeLimit = 8
	}
	edgeLimit := nodeLimit * 4

	topology, redacted, unresolved, err := db.buildEventTopology(ctx, centerType, centerName, direction, depth, nodeLimit, edgeLimit, opts)
	if err != nil {
		return LLMResult{}, err
	}
	r := LLMResult{
		Query:  id,
		Intent: "event_chain",
		Counts: map[string]int{
			"nodes":      len(topology.Nodes),
			"edges":      len(topology.Edges),
			"roots":      len(topology.Roots),
			"leaves":     len(topology.Leaves),
			"cycles":     len(topology.Cycles),
			"unresolved": unresolved,
			"depth":      depth,
		},
		Guidance: []string{
			"callers walks references toward objects that invoke the center; callees walks toward invoked events or on_actions; both combines them.",
			"Roots, leaves, degrees, and cycles are computed from the returned resolved subgraph. Unresolved edges remain visible but are never invented as nodes.",
		},
		NextQueries: []LLMNextQuery{
			{Tool: "ck3_inspect", ID: center, Reason: "inspect the center definition and exact call evidence"},
		},
		Redacted: redacted,
		Topology: &topology,
	}
	centerDefined := false
	for _, node := range topology.Nodes {
		if node.ID == center {
			centerDefined = node.Defined
			break
		}
	}
	if !centerDefined {
		r.Summary = fmt.Sprintf("No active event or on_action definition was found for %q.", id)
		r.NeedsRefresh = true
	} else {
		r.Summary = fmt.Sprintf("Event chain for %s: direction=%s, depth=%d, %d node(s), %d edge(s), %d root(s), %d leaf/leaves, %d cycle(s), and %d unresolved edge(s).", center, direction, depth, len(topology.Nodes), len(topology.Edges), len(topology.Roots), len(topology.Leaves), len(topology.Cycles), unresolved)
	}
	for _, edge := range topology.Edges {
		if len(r.Evidence) >= limit*2 {
			break
		}
		r.Evidence = append(r.Evidence, LLMEvidence{
			Kind: "event_chain_edge", Name: edge.To, Source: edge.Source, Path: edge.Path,
			Line: edge.Line, Column: edge.Column, EdgeType: edge.Relation,
			Detail: fmt.Sprintf("%s -> %s phase=%s confidence=%s resolution=%s", edge.From, edge.To, edge.Phase, edge.Confidence, edge.Resolution),
		})
	}
	return r, nil
}

func (db *DB) eventChainCenter(ctx context.Context, id string, opts LLMOptions) (string, string, error) {
	typ, name, typed := splitTypedID(strings.TrimSpace(id))
	if typed {
		if typ != "event" && typ != "on_action" {
			return "", "", fmt.Errorf("event chain center type %q must be event or on_action", typ)
		}
		return typ, name, nil
	}
	obj, err := db.QueryObject(ctx, id)
	if err != nil {
		return "", "", err
	}
	for _, wanted := range []string{"event", "on_action"} {
		for _, definition := range obj.Definitions {
			if definition.Type == wanted && (!opts.publicMode() || !opts.sourceIsPrivate(definition.Source)) {
				return wanted, id, nil
			}
		}
	}
	// Event ids are the more common untyped input and unresolved calls are
	// still useful evidence, so preserve the requested name as an event center.
	return "event", id, nil
}

type topologyWalkItem struct {
	Type, Name string
	Distance   int
}

type topologyNodeState struct {
	Type, Name string
	Distance   int
	Source     string
	Path       string
	Line       int
	EventType  string
	Title      string
	Defined    bool
}

func (db *DB) buildEventTopology(ctx context.Context, centerType, centerName, direction string, depth, nodeLimit, edgeLimit int, opts EventChainOptions) (LLMTopology, int, int, error) {
	center := typedTopologyID(centerType, centerName)
	topology := LLMTopology{Center: center, Direction: direction, IncludeOnActions: opts.IncludeOnActions, MaxDepth: depth}
	states := map[string]*topologyNodeState{center: {Type: centerType, Name: centerName, Distance: 0}}
	queue := []topologyWalkItem{{Type: centerType, Name: centerName, Distance: 0}}
	predecessor := map[string]string{}
	seenEdges := map[string]bool{}
	redacted := 0
	unresolved := 0

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.Distance >= depth || len(topology.Edges) >= edgeLimit {
			if len(topology.Edges) >= edgeLimit {
				topology.Truncated = true
			}
			continue
		}
		current := typedTopologyID(item.Type, item.Name)
		refs, err := db.QueryRefs(ctx, current)
		if err != nil {
			return LLMTopology{}, 0, 0, err
		}
		if refs.IncomingTruncated || refs.OutgoingTruncated {
			topology.Truncated = true
		}
		var hits []RefHit
		if direction == "both" || direction == "callers" {
			hits = append(hits, refs.Incoming...)
		}
		if direction == "both" || direction == "callees" {
			hits = append(hits, refs.Outgoing...)
		}
		for _, hit := range hits {
			if hit.FromName == "" || (hit.FromType != "event" && hit.FromType != "on_action") || (hit.Kind != "event" && hit.Kind != "on_action") {
				continue
			}
			if !opts.IncludeOnActions && (hit.FromType == "on_action" || hit.Kind == "on_action") {
				continue
			}
			from := typedTopologyID(hit.FromType, hit.FromName)
			to := typedTopologyID(hit.Kind, hit.Name)
			edgeKey := strings.Join([]string{from, to, hit.Relation, hit.Phase, hit.Path, fmt.Sprint(hit.Line), fmt.Sprint(hit.Column)}, "|")
			if seenEdges[edgeKey] {
				continue
			}
			seenEdges[edgeKey] = true
			if opts.publicMode() && opts.sourceIsPrivate(hit.Source) {
				redacted++
				continue
			}
			topology.Edges = append(topology.Edges, LLMTopologyEdge{
				From: from, To: to, Relation: hit.Relation, Phase: hit.Phase, Confidence: hit.Confidence,
				Resolution: hit.Resolution, Reason: hit.ResolutionReason, Source: hit.Source,
				Path: evidencePath(hit.Path), Line: hit.Line, Column: hit.Column,
			})
			if !hit.Resolved {
				unresolved++
			}
			if len(topology.Edges) >= edgeLimit {
				topology.Truncated = true
				break
			}

			nextType, nextName, nextID := hit.Kind, hit.Name, to
			if direction == "callers" || (direction == "both" && current == to) {
				nextType, nextName, nextID = hit.FromType, hit.FromName, from
			}
			if !hit.Resolved && nextID == to {
				continue
			}
			if _, exists := states[nextID]; exists {
				continue
			}
			if len(states) >= nodeLimit {
				topology.Truncated = true
				continue
			}
			states[nextID] = &topologyNodeState{Type: nextType, Name: nextName, Distance: item.Distance + 1}
			predecessor[nextID] = current
			queue = append(queue, topologyWalkItem{Type: nextType, Name: nextName, Distance: item.Distance + 1})
		}
	}

	for id, state := range states {
		if err := db.enrichTopologyNode(ctx, state, opts.LLMOptions); err != nil {
			return LLMTopology{}, 0, 0, err
		}
		states[id] = state
	}
	buildTopologyConclusions(&topology, states, predecessor, opts.normalizedLimit())
	return topology, redacted, unresolved, nil
}

func (db *DB) enrichTopologyNode(ctx context.Context, state *topologyNodeState, opts LLMOptions) error {
	obj, err := db.QueryObject(ctx, typedTopologyID(state.Type, state.Name))
	if err != nil {
		return err
	}
	for _, definition := range obj.Definitions {
		if opts.publicMode() && opts.sourceIsPrivate(definition.Source) {
			continue
		}
		state.Source = definition.Source
		state.Path = evidencePath(definition.Path)
		state.Line = definition.Line
		state.Defined = true
		break
	}
	if state.Type != "event" {
		return nil
	}
	for _, profile := range obj.EventProfiles {
		if opts.publicMode() && opts.sourceIsPrivate(profile.Source) {
			continue
		}
		for _, field := range profile.Fields {
			value := directFieldValue(field.Raw)
			switch field.Field {
			case "type":
				state.EventType = value
			case "title":
				state.Title = value
			}
		}
		break
	}
	return nil
}

func directFieldValue(raw string) string {
	_, value, ok := strings.Cut(raw, "=")
	if !ok {
		return ""
	}
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if strings.ContainsAny(value, "{}\n\r") {
		return ""
	}
	return value
}

func typedTopologyID(typ, name string) string {
	return typ + ":" + name
}

func buildTopologyConclusions(topology *LLMTopology, states map[string]*topologyNodeState, predecessor map[string]string, pathLimit int) {
	adjacency := make(map[string]map[string]bool, len(states))
	reverse := make(map[string]map[string]bool, len(states))
	for id := range states {
		adjacency[id] = map[string]bool{}
		reverse[id] = map[string]bool{}
	}
	for _, edge := range topology.Edges {
		if _, fromOK := states[edge.From]; !fromOK {
			continue
		}
		if _, toOK := states[edge.To]; !toOK {
			continue
		}
		adjacency[edge.From][edge.To] = true
		reverse[edge.To][edge.From] = true
	}
	for id, state := range states {
		topology.Nodes = append(topology.Nodes, LLMTopologyNode{
			ID: id, Type: state.Type, Name: state.Name, Defined: state.Defined, Distance: state.Distance,
			InDegree: len(reverse[id]), OutDegree: len(adjacency[id]), EventType: state.EventType,
			Title: state.Title, Source: state.Source, Path: state.Path, Line: state.Line,
		})
		if len(reverse[id]) == 0 {
			topology.Roots = append(topology.Roots, id)
		}
		if len(adjacency[id]) == 0 {
			topology.Leaves = append(topology.Leaves, id)
		}
	}
	sort.Slice(topology.Nodes, func(i, j int) bool {
		if topology.Nodes[i].Distance != topology.Nodes[j].Distance {
			return topology.Nodes[i].Distance < topology.Nodes[j].Distance
		}
		return topology.Nodes[i].ID < topology.Nodes[j].ID
	})
	sort.Slice(topology.Edges, func(i, j int) bool {
		a, b := topology.Edges[i], topology.Edges[j]
		return strings.Join([]string{a.From, a.To, a.Relation, a.Phase, a.Path, fmt.Sprint(a.Line)}, "|") < strings.Join([]string{b.From, b.To, b.Relation, b.Phase, b.Path, fmt.Sprint(b.Line)}, "|")
	})
	sort.Strings(topology.Roots)
	sort.Strings(topology.Leaves)
	topology.Cycles = topologyCycles(adjacency)

	var targets []LLMTopologyNode
	for _, node := range topology.Nodes {
		if node.ID != topology.Center {
			targets = append(targets, node)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Distance != targets[j].Distance {
			return targets[i].Distance > targets[j].Distance
		}
		return targets[i].ID < targets[j].ID
	})
	for _, target := range targets {
		if len(topology.PathsFromCenter) >= pathLimit {
			break
		}
		path := []string{target.ID}
		for current := target.ID; current != topology.Center; {
			previous, ok := predecessor[current]
			if !ok {
				path = nil
				break
			}
			path = append(path, previous)
			current = previous
		}
		if len(path) == 0 {
			continue
		}
		for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
			path[left], path[right] = path[right], path[left]
		}
		topology.PathsFromCenter = append(topology.PathsFromCenter, LLMTopologyPath{To: target.ID, Nodes: path})
	}
}

func topologyCycles(adjacency map[string]map[string]bool) [][]string {
	index := 0
	indices := map[string]int{}
	lowlink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var cycles [][]string
	var visit func(string)
	visit = func(node string) {
		indices[node] = index
		lowlink[node] = index
		index++
		stack = append(stack, node)
		onStack[node] = true
		var targets []string
		for target := range adjacency[node] {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			if _, seen := indices[target]; !seen {
				visit(target)
				if lowlink[target] < lowlink[node] {
					lowlink[node] = lowlink[target]
				}
			} else if onStack[target] && indices[target] < lowlink[node] {
				lowlink[node] = indices[target]
			}
		}
		if lowlink[node] != indices[node] {
			return
		}
		var component []string
		for len(stack) > 0 {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		if len(component) > 1 || (len(component) == 1 && adjacency[component[0]][component[0]]) {
			sort.Strings(component)
			cycles = append(cycles, component)
		}
	}
	var nodes []string
	for node := range adjacency {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		if _, seen := indices[node]; !seen {
			visit(node)
		}
	}
	sort.Slice(cycles, func(i, j int) bool { return strings.Join(cycles[i], "|") < strings.Join(cycles[j], "|") })
	return cycles
}
