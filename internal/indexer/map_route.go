package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	MapRouteNoPathCode      = "MAP_ROUTE_NO_PATH"
	MapRouteNodeLimitCode   = "MAP_ROUTE_NODE_LIMIT"
	MapRouteDefaultMaxNodes = 5000
	MapRouteMaxNodes        = 5000
	MapRouteMaxWaypoints    = 16
)

type MapRouteSpec struct {
	From                 string   `json:"from"`
	To                   string   `json:"to"`
	Year                 int      `json:"year,omitempty"`
	Mode                 string   `json:"mode,omitempty"`
	Objective            string   `json:"objective,omitempty"`
	Waypoints            []string `json:"waypoints,omitempty"`
	CorridorRadiusPixels int      `json:"corridor_radius_pixels,omitempty"`
	ContextLevel         string   `json:"context_level,omitempty"`
	LabelLanguage        string   `json:"label_language,omitempty"`
	MaxNodes             int      `json:"max_nodes,omitempty"`
	Verbose              bool     `json:"verbose,omitempty"`
}

type MapRoutePoint struct {
	ProvinceID              int     `json:"province_id"`
	CenterX                 float64 `json:"center_x"`
	CenterY                 float64 `json:"center_y"`
	WaterKind               string  `json:"water_kind,omitempty"`
	AdjacencyFromPrevious   string  `json:"adjacency_from_previous,omitempty"`
	DistanceFromPrevious    float64 `json:"distance_from_previous_pixels,omitempty"`
	CumulativeDistancePixel float64 `json:"cumulative_distance_pixels,omitempty"`
}

type MapRouteLeg struct {
	Kind       string `json:"kind"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

type MapRouteCorridorTargets struct {
	ProvinceIDs []int    `json:"province_ids"`
	CountyIDs   []string `json:"county_ids,omitempty"`
	DuchyIDs    []string `json:"duchy_ids,omitempty"`
}

type MapRouteTimings struct {
	ResolveMS   int64 `json:"resolve_ms"`
	GraphLoadMS int64 `json:"graph_load_ms"`
	RouteMS     int64 `json:"route_ms"`
	CorridorMS  int64 `json:"corridor_ms"`
}

type MapRouteFailure struct {
	Code                    string                `json:"code"`
	Message                 string                `json:"message"`
	FromComponentSize       int                   `json:"from_component_size,omitempty"`
	ToComponentSize         int                   `json:"to_component_size,omitempty"`
	RejectedBoundaryTypes   map[string]int        `json:"rejected_boundary_types,omitempty"`
	ResolutionCandidates    []MapSubjectCandidate `json:"resolution_candidates,omitempty"`
	ExpandedNodesBeforeStop int                   `json:"expanded_nodes_before_stop,omitempty"`
}

type MapRouteResult struct {
	Status          string                  `json:"status"`
	Intent          string                  `json:"intent"`
	ResolvedFrom    MapResolvedSubject      `json:"resolved_from"`
	ResolvedTo      MapResolvedSubject      `json:"resolved_to"`
	Mode            string                  `json:"mode"`
	Objective       string                  `json:"objective"`
	Path            []MapRoutePoint         `json:"path,omitempty"`
	Legs            []MapRouteLeg           `json:"legs,omitempty"`
	DistancePixels  float64                 `json:"distance_pixels,omitempty"`
	CorridorTargets MapRouteCorridorTargets `json:"corridor_targets"`
	Warnings        []string                `json:"warnings,omitempty"`
	Error           *MapRouteFailure        `json:"error,omitempty"`
	Timings         MapRouteTimings         `json:"timings_ms"`
	Evidence        []string                `json:"evidence,omitempty"`
}

type mapRouteNode struct {
	ID        int
	Center    MapPoint
	Blocked   bool
	BlockKind string
	WaterKind string
	County    string
	Duchy     string
	Area      int
}

type mapRouteEdge struct {
	To        int
	Distance  float64
	Kind      string
	Blocked   bool
	Strategic bool
}

type mapRouteGraph struct {
	Nodes    map[int]mapRouteNode
	Edges    map[int][]mapRouteEdge
	Rejected map[string]int
}

type mapRouteQueueItem struct {
	ID       int
	Cost     float64
	Distance float64
	Order    int
}

type mapRouteQueue []mapRouteQueueItem

func (q mapRouteQueue) Len() int { return len(q) }
func (q mapRouteQueue) Less(i, j int) bool {
	if math.Abs(q[i].Cost-q[j].Cost) > 1e-9 {
		return q[i].Cost < q[j].Cost
	}
	if q[i].ID != q[j].ID {
		return q[i].ID < q[j].ID
	}
	return q[i].Order < q[j].Order
}
func pushMapRouteQueue(queue *mapRouteQueue, item mapRouteQueueItem) {
	*queue = append(*queue, item)
	for index := len(*queue) - 1; index > 0; {
		parent := (index - 1) / 2
		if !queue.Less(index, parent) {
			break
		}
		(*queue)[index], (*queue)[parent] = (*queue)[parent], (*queue)[index]
		index = parent
	}
}

func popMapRouteQueue(queue *mapRouteQueue) mapRouteQueueItem {
	root := (*queue)[0]
	last := (*queue)[len(*queue)-1]
	*queue = (*queue)[:len(*queue)-1]
	if len(*queue) == 0 {
		return root
	}
	(*queue)[0] = last
	for index := 0; ; {
		left := index*2 + 1
		if left >= len(*queue) {
			break
		}
		right, smallest := left+1, left
		if right < len(*queue) && queue.Less(right, left) {
			smallest = right
		}
		if !queue.Less(smallest, index) {
			break
		}
		(*queue)[index], (*queue)[smallest] = (*queue)[smallest], (*queue)[index]
		index = smallest
	}
	return root
}

func (db *DB) LLMMapRoute(ctx context.Context, spec MapRouteSpec, _ LLMOptions) (MapRouteResult, error) {
	result := MapRouteResult{Status: "blocked", Intent: "map_route", CorridorTargets: MapRouteCorridorTargets{ProvinceIDs: []int{}}}
	if status, err := db.MapDatabaseStatus(ctx); err != nil {
		return result, err
	} else if !status.Complete {
		result.Error = &MapRouteFailure{Code: MapDatabaseIncompleteCode, Message: (&MapDatabaseError{Status: status}).Error()}
		return result, nil
	}
	if err := normalizeMapRouteSpec(&spec); err != nil {
		return result, err
	}
	result.Mode, result.Objective = spec.Mode, spec.Objective

	started := time.Now()
	resolvedFrom, err := db.ResolveMapSubject(ctx, spec.From, spec.Year)
	if err != nil {
		result.Error = routeResolutionFailure(err)
		result.Timings.ResolveMS = time.Since(started).Milliseconds()
		return result, nil
	}
	result.ResolvedFrom = resolvedFrom
	resolvedTo, err := db.ResolveMapSubject(ctx, spec.To, spec.Year)
	if err != nil {
		result.Error = routeResolutionFailure(err)
		result.Timings.ResolveMS = time.Since(started).Milliseconds()
		return result, nil
	}
	result.ResolvedTo = resolvedTo
	waypoints := make([]MapResolvedSubject, 0, len(spec.Waypoints))
	for _, waypoint := range spec.Waypoints {
		resolved, resolveErr := db.ResolveMapSubject(ctx, waypoint, spec.Year)
		if resolveErr != nil {
			result.Error = routeResolutionFailure(resolveErr)
			result.Timings.ResolveMS = time.Since(started).Milliseconds()
			return result, nil
		}
		waypoints = append(waypoints, resolved)
	}
	result.Timings.ResolveMS = time.Since(started).Milliseconds()

	started = time.Now()
	graph, err := db.loadMapRouteGraph(ctx)
	if err != nil {
		return result, err
	}
	result.Timings.GraphLoadMS = time.Since(started).Milliseconds()

	started = time.Now()
	pathIDs, edgeKinds, edgeDistances, expanded, err := routeThroughSubjects(graph, resolvedFrom, resolvedTo, waypoints, spec)
	result.Timings.RouteMS = time.Since(started).Milliseconds()
	if err != nil {
		code := MapRouteNoPathCode
		if errors.Is(err, errMapRouteNodeLimit) {
			code = MapRouteNodeLimitCode
		}
		fromStarts := graph.routeSubjectNodes(resolvedFrom.ProvinceID, spec.Mode)
		toStarts := graph.routeSubjectNodes(resolvedTo.ProvinceID, spec.Mode)
		result.Error = &MapRouteFailure{
			Code: code, Message: err.Error(), FromComponentSize: graph.componentSize(fromStarts, spec.Mode),
			ToComponentSize: graph.componentSize(toStarts, spec.Mode), RejectedBoundaryTypes: graph.Rejected,
			ExpandedNodesBeforeStop: expanded,
		}
		return result, nil
	}

	var cumulative float64
	for index, provinceID := range pathIDs {
		node := graph.Nodes[provinceID]
		point := MapRoutePoint{ProvinceID: provinceID, CenterX: node.Center.X, CenterY: node.Center.Y, WaterKind: node.WaterKind}
		if index > 0 {
			point.AdjacencyFromPrevious = edgeKinds[index-1]
			point.DistanceFromPrevious = roundMapRouteDistance(edgeDistances[index-1])
			cumulative += edgeDistances[index-1]
		}
		point.CumulativeDistancePixel = roundMapRouteDistance(cumulative)
		result.Path = append(result.Path, point)
	}
	result.DistancePixels = roundMapRouteDistance(cumulative)
	result.Legs = buildMapRouteLegs(result.Path, spec.Mode)
	result.Warnings = []string{"Pixel distance is a source-map centroid-path measurement, not in-game travel time or real-world distance."}
	if spec.Objective == "scenic" {
		result.Warnings = append(result.Warnings, "Scenic routing applies a bounded preference for coastal and island water provinces while limiting detour over the shortest route.")
	}

	started = time.Now()
	result.CorridorTargets = graph.corridorTargets(pathIDs, spec.CorridorRadiusPixels)
	result.Timings.CorridorMS = time.Since(started).Milliseconds()
	if spec.Verbose {
		result.Evidence = append(result.Evidence,
			fmt.Sprintf("loaded_nodes=%d", len(graph.Nodes)),
			fmt.Sprintf("expanded_nodes=%d", expanded),
			fmt.Sprintf("corridor_radius_pixels=%d", spec.CorridorRadiusPixels),
		)
	}
	result.Status = "ready"
	return result, nil
}

func normalizeMapRouteSpec(spec *MapRouteSpec) error {
	if strings.TrimSpace(spec.From) == "" || strings.TrimSpace(spec.To) == "" {
		return fmt.Errorf("map_route requires non-empty from and to subjects")
	}
	if spec.Year <= 0 {
		spec.Year = 1
	}
	spec.Mode = strings.ToLower(strings.TrimSpace(spec.Mode))
	if spec.Mode == "" {
		spec.Mode = "mixed"
	}
	if spec.Mode != "sea" && spec.Mode != "land" && spec.Mode != "mixed" {
		return fmt.Errorf("map_route mode must be sea, land, or mixed")
	}
	spec.Objective = strings.ToLower(strings.TrimSpace(spec.Objective))
	if spec.Objective == "" {
		spec.Objective = "shortest"
	}
	if spec.Objective != "shortest" && spec.Objective != "scenic" {
		return fmt.Errorf("map_route objective must be shortest or scenic")
	}
	if len(spec.Waypoints) > MapRouteMaxWaypoints {
		return fmt.Errorf("map_route accepts at most %d waypoints", MapRouteMaxWaypoints)
	}
	if spec.CorridorRadiusPixels <= 0 {
		spec.CorridorRadiusPixels = 120
	}
	if spec.CorridorRadiusPixels > 2048 {
		return fmt.Errorf("corridor_radius_pixels must not exceed 2048")
	}
	if spec.ContextLevel == "" {
		spec.ContextLevel = "duchy"
	}
	if spec.ContextLevel != "county" && spec.ContextLevel != "duchy" {
		return fmt.Errorf("context_level must be county or duchy")
	}
	if spec.LabelLanguage == "" {
		spec.LabelLanguage = "bilingual"
	}
	if spec.LabelLanguage != "en" && spec.LabelLanguage != "zh" && spec.LabelLanguage != "bilingual" {
		return fmt.Errorf("label_language must be en, zh, or bilingual")
	}
	if spec.MaxNodes <= 0 {
		spec.MaxNodes = MapRouteDefaultMaxNodes
	}
	if spec.MaxNodes > MapRouteMaxNodes {
		return fmt.Errorf("max_nodes must not exceed %d", MapRouteMaxNodes)
	}
	return nil
}

func routeResolutionFailure(err error) *MapRouteFailure {
	var resolution *MapSubjectResolutionError
	if errors.As(err, &resolution) {
		return &MapRouteFailure{Code: resolution.Code, Message: resolution.Message, ResolutionCandidates: resolution.Candidates}
	}
	var database *MapDatabaseError
	if errors.As(err, &database) {
		return &MapRouteFailure{Code: MapDatabaseIncompleteCode, Message: database.Error()}
	}
	return &MapRouteFailure{Code: "MAP_ROUTE_RESOLUTION_FAILED", Message: err.Error()}
}

func (db *DB) loadMapRouteGraph(ctx context.Context) (*mapRouteGraph, error) {
	graph := &mapRouteGraph{Nodes: map[int]mapRouteNode{}, Edges: map[int][]mapRouteEdge{}, Rejected: map[string]int{}}
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,COALESCE(center_x,0),COALESCE(center_y,0),blocked,COALESCE(block_kind,''),COALESCE(water_kind,''),COALESCE(county,''),COALESCE(duchy,''),COALESCE(area,0) FROM map_provinces`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var node mapRouteNode
		var blocked int
		if err := rows.Scan(&node.ID, &node.Center.X, &node.Center.Y, &blocked, &node.BlockKind, &node.WaterKind, &node.County, &node.Duchy, &node.Area); err != nil {
			rows.Close()
			return nil, err
		}
		node.Blocked = blocked != 0
		graph.Nodes[node.ID] = node
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	type pair struct{ A, B int }
	edges := map[pair]mapRouteEdge{}
	rows, err = db.sql.QueryContext(ctx, `SELECT province_id,neighbor_id,border_len,blocked FROM map_adjacencies`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var from, to, borderLen, blocked int
		if err := rows.Scan(&from, &to, &borderLen, &blocked); err != nil {
			rows.Close()
			return nil, err
		}
		if from == to || graph.Nodes[from].ID == 0 || graph.Nodes[to].ID == 0 {
			continue
		}
		key := pair{A: minInt(from, to), B: maxInt(from, to)}
		distance := mapPointDistance(graph.Nodes[from].Center, graph.Nodes[to].Center)
		kind := classifyMapRouteBoundary(graph.Nodes[from], graph.Nodes[to], blocked != 0)
		candidate := mapRouteEdge{Distance: distance, Kind: kind, Blocked: blocked != 0}
		if previous, ok := edges[key]; !ok || borderLen > 0 && previous.Blocked && blocked == 0 {
			edges[key] = candidate
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for key, edge := range edges {
		forward := edge
		forward.To = key.B
		backward := edge
		backward.To = key.A
		graph.Edges[key.A] = append(graph.Edges[key.A], forward)
		graph.Edges[key.B] = append(graph.Edges[key.B], backward)
	}

	rows, err = db.sql.QueryContext(ctx, `SELECT from_province,to_province,passage_kind,distance_pixels FROM map_strategic_adjacencies`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var from, to int
		var kind string
		var distance sql.NullFloat64
		if err := rows.Scan(&from, &to, &kind, &distance); err != nil {
			rows.Close()
			return nil, err
		}
		if from == to || graph.Nodes[from].ID == 0 || graph.Nodes[to].ID == 0 || kind == "offmap_gateway" || kind == "underground_gateway" {
			continue
		}
		d := distance.Float64
		if d <= 0 {
			d = mapPointDistance(graph.Nodes[from].Center, graph.Nodes[to].Center)
		}
		graph.Edges[from] = append(graph.Edges[from], mapRouteEdge{To: to, Distance: d, Kind: kind, Strategic: true})
		graph.Edges[to] = append(graph.Edges[to], mapRouteEdge{To: from, Distance: d, Kind: kind, Strategic: true})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for id := range graph.Edges {
		sort.Slice(graph.Edges[id], func(i, j int) bool {
			if graph.Edges[id][i].To != graph.Edges[id][j].To {
				return graph.Edges[id][i].To < graph.Edges[id][j].To
			}
			return graph.Edges[id][i].Kind < graph.Edges[id][j].Kind
		})
	}
	return graph, nil
}

func classifyMapRouteBoundary(from, to mapRouteNode, blocked bool) string {
	// CK3 marks navigable water provinces as blocked land. The adjacency row
	// therefore carries blocked=1 at ordinary coast and sea boundaries; water
	// classification must take precedence over the land-only blocked flag.
	if isMapRouteWater(from) || isMapRouteWater(to) {
		return "water_boundary"
	}
	if blocked {
		if strings.Contains(from.BlockKind, "mountain") || strings.Contains(to.BlockKind, "mountain") {
			return "impassable_mountain_boundary"
		}
		return "blocked_boundary"
	}
	return "land_boundary"
}

func isMapRouteSea(node mapRouteNode) bool {
	switch strings.ToLower(node.WaterKind) {
	case "sea", "coastal_sea":
		return !strings.Contains(strings.ToLower(node.BlockKind), "impassable")
	default:
		return false
	}
}

func isMapRouteWater(node mapRouteNode) bool { return strings.TrimSpace(node.WaterKind) != "" }
func isMapRouteLand(node mapRouteNode) bool  { return !node.Blocked && !isMapRouteWater(node) }

func (graph *mapRouteGraph) routeSubjectNodes(provinceID int, mode string) []int {
	node := graph.Nodes[provinceID]
	switch mode {
	case "sea":
		if isMapRouteSea(node) {
			return []int{provinceID}
		}
		set := map[int]bool{}
		for _, edge := range graph.Edges[provinceID] {
			if isMapRouteSea(graph.Nodes[edge.To]) && edge.Kind == "water_boundary" {
				set[edge.To] = true
			}
		}
		return sortedMapRouteIDs(set)
	case "land":
		if isMapRouteLand(node) {
			return []int{provinceID}
		}
	case "mixed":
		if isMapRouteLand(node) || isMapRouteSea(node) {
			return []int{provinceID}
		}
	}
	return nil
}

var errMapRouteNodeLimit = errors.New("map route search exceeded max_nodes")

func routeThroughSubjects(graph *mapRouteGraph, from, to MapResolvedSubject, waypoints []MapResolvedSubject, spec MapRouteSpec) ([]int, []string, []float64, int, error) {
	subjects := append([]MapResolvedSubject{from}, waypoints...)
	subjects = append(subjects, to)
	var fullPath []int
	var fullKinds []string
	var fullDistances []float64
	expandedTotal := 0
	for index := 0; index < len(subjects)-1; index++ {
		starts := graph.routeSubjectNodes(subjects[index].ProvinceID, spec.Mode)
		ends := graph.routeSubjectNodes(subjects[index+1].ProvinceID, spec.Mode)
		if index > 0 && len(fullPath) > 0 {
			starts = []int{fullPath[len(fullPath)-1]}
		}
		path, kinds, distances, expanded, err := graph.shortestRoute(starts, ends, spec.Mode, spec.Objective, spec.MaxNodes-expandedTotal)
		expandedTotal += expanded
		if err != nil {
			return nil, nil, nil, expandedTotal, err
		}
		if len(fullPath) > 0 && len(path) > 0 && fullPath[len(fullPath)-1] == path[0] {
			path = path[1:]
		}
		fullPath = append(fullPath, path...)
		fullKinds = append(fullKinds, kinds...)
		fullDistances = append(fullDistances, distances...)
	}
	return fullPath, fullKinds, fullDistances, expandedTotal, nil
}

func (graph *mapRouteGraph) shortestRoute(starts, ends []int, mode, objective string, maxNodes int) ([]int, []string, []float64, int, error) {
	if len(starts) == 0 || len(ends) == 0 {
		return nil, nil, nil, 0, fmt.Errorf("%s: an endpoint has no valid %s access province", MapRouteNoPathCode, mode)
	}
	shortest, shortestKinds, shortestDistances, expanded, err := graph.dijkstra(starts, ends, mode, false, maxNodes)
	if err != nil || objective != "scenic" || mode == "land" {
		return shortest, shortestKinds, shortestDistances, expanded, err
	}
	remaining := maxNodes - expanded
	if remaining <= 0 {
		return shortest, shortestKinds, shortestDistances, expanded, nil
	}
	scenic, scenicKinds, scenicDistances, scenicExpanded, scenicErr := graph.dijkstra(starts, ends, mode, true, remaining)
	expanded += scenicExpanded
	if scenicErr != nil {
		return shortest, shortestKinds, shortestDistances, expanded, nil
	}
	if sumFloat64(scenicDistances) <= sumFloat64(shortestDistances)*1.25+1e-6 {
		return scenic, scenicKinds, scenicDistances, expanded, nil
	}
	return shortest, shortestKinds, shortestDistances, expanded, nil
}

func (graph *mapRouteGraph) dijkstra(starts, ends []int, mode string, scenic bool, maxNodes int) ([]int, []string, []float64, int, error) {
	if maxNodes <= 0 {
		return nil, nil, nil, 0, errMapRouteNodeLimit
	}
	endSet := map[int]bool{}
	for _, id := range ends {
		endSet[id] = true
	}
	cost := map[int]float64{}
	distance := map[int]float64{}
	previous := map[int]int{}
	previousEdge := map[int]mapRouteEdge{}
	queue := &mapRouteQueue{}
	order := 0
	for _, id := range starts {
		if !graph.nodeAllowed(id, mode) {
			continue
		}
		cost[id], distance[id] = 0, 0
		pushMapRouteQueue(queue, mapRouteQueueItem{ID: id, Order: order})
		order++
	}
	expanded := 0
	goal := 0
	for queue.Len() > 0 {
		item := popMapRouteQueue(queue)
		if known, ok := cost[item.ID]; !ok || item.Cost > known+1e-9 {
			continue
		}
		expanded++
		if expanded > maxNodes {
			return nil, nil, nil, expanded, errMapRouteNodeLimit
		}
		if endSet[item.ID] {
			goal = item.ID
			break
		}
		for _, edge := range graph.Edges[item.ID] {
			allowed, rejectedKind := graph.edgeAllowed(item.ID, edge, mode)
			if !allowed {
				if rejectedKind != "" {
					graph.Rejected[rejectedKind]++
				}
				continue
			}
			step := edge.Distance
			if mode == "mixed" && isMapRouteSea(graph.Nodes[item.ID]) != isMapRouteSea(graph.Nodes[edge.To]) {
				step += 250
			}
			weighted := step
			if scenic && isMapRouteSea(graph.Nodes[edge.To]) {
				if strings.EqualFold(graph.Nodes[edge.To].WaterKind, "coastal_sea") {
					weighted *= 0.88
				} else if graph.hasLandNeighbor(edge.To) {
					weighted *= 0.93
				}
			}
			candidate := cost[item.ID] + weighted
			known, exists := cost[edge.To]
			if !exists || candidate < known-1e-9 || math.Abs(candidate-known) <= 1e-9 && item.ID < previous[edge.To] {
				cost[edge.To] = candidate
				distance[edge.To] = distance[item.ID] + edge.Distance
				previous[edge.To] = item.ID
				previousEdge[edge.To] = edge
				pushMapRouteQueue(queue, mapRouteQueueItem{ID: edge.To, Cost: candidate, Distance: distance[edge.To], Order: order})
				order++
			}
		}
	}
	if goal == 0 {
		return nil, nil, nil, expanded, fmt.Errorf("%s: no legal %s route connects the resolved endpoints", MapRouteNoPathCode, mode)
	}
	path := []int{goal}
	for {
		previousID, ok := previous[path[len(path)-1]]
		if !ok {
			break
		}
		path = append(path, previousID)
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	kinds := make([]string, 0, len(path)-1)
	distances := make([]float64, 0, len(path)-1)
	for index := 1; index < len(path); index++ {
		edge := previousEdge[path[index]]
		kinds = append(kinds, edge.Kind)
		distances = append(distances, edge.Distance)
	}
	return path, kinds, distances, expanded, nil
}

func (graph *mapRouteGraph) nodeAllowed(id int, mode string) bool {
	node, ok := graph.Nodes[id]
	if !ok {
		return false
	}
	switch mode {
	case "sea":
		return isMapRouteSea(node)
	case "land":
		return isMapRouteLand(node)
	case "mixed":
		return isMapRouteLand(node) || isMapRouteSea(node)
	default:
		return false
	}
}

func (graph *mapRouteGraph) edgeAllowed(from int, edge mapRouteEdge, mode string) (bool, string) {
	if !graph.nodeAllowed(from, mode) || !graph.nodeAllowed(edge.To, mode) {
		if edge.Kind != "" {
			return false, edge.Kind
		}
		return false, "invalid_endpoint_type"
	}
	if edge.Blocked && edge.Kind != "water_boundary" {
		return false, edge.Kind
	}
	if edge.Strategic {
		switch edge.Kind {
		case "offmap_gateway", "underground_gateway":
			return false, edge.Kind
		case "sea_route":
			return mode != "land" && isMapRouteSea(graph.Nodes[from]) && isMapRouteSea(graph.Nodes[edge.To]), edge.Kind
		case "strait", "river_crossing", "mountain_pass", "land_passage", "underground_internal", "explicit_passage":
			return mode != "sea", edge.Kind
		default:
			return false, edge.Kind
		}
	}
	switch mode {
	case "sea":
		return edge.Kind == "water_boundary" && isMapRouteSea(graph.Nodes[from]) && isMapRouteSea(graph.Nodes[edge.To]), edge.Kind
	case "land":
		return edge.Kind == "land_boundary", edge.Kind
	case "mixed":
		return edge.Kind == "land_boundary" || edge.Kind == "water_boundary", edge.Kind
	}
	return false, edge.Kind
}

func (graph *mapRouteGraph) hasLandNeighbor(id int) bool {
	for _, edge := range graph.Edges[id] {
		if !edge.Strategic && isMapRouteLand(graph.Nodes[edge.To]) {
			return true
		}
	}
	return false
}

func (graph *mapRouteGraph) componentSize(starts []int, mode string) int {
	seen := map[int]bool{}
	queue := append([]int(nil), starts...)
	for len(queue) > 0 && len(seen) <= MapRouteMaxNodes {
		id := queue[0]
		queue = queue[1:]
		if seen[id] || !graph.nodeAllowed(id, mode) {
			continue
		}
		seen[id] = true
		for _, edge := range graph.Edges[id] {
			if ok, _ := graph.edgeAllowed(id, edge, mode); ok && !seen[edge.To] {
				queue = append(queue, edge.To)
			}
		}
	}
	return len(seen)
}

func (graph *mapRouteGraph) corridorTargets(path []int, radius int) MapRouteCorridorTargets {
	provinceSet := map[int]bool{}
	countySet := map[string]bool{}
	duchySet := map[string]bool{}
	for id, node := range graph.Nodes {
		if len(path) == 0 {
			break
		}
		within := false
		if len(path) == 1 {
			within = mapPointDistance(node.Center, graph.Nodes[path[0]].Center) <= float64(radius)
		} else {
			for index := 1; index < len(path); index++ {
				if pointSegmentDistance(node.Center, graph.Nodes[path[index-1]].Center, graph.Nodes[path[index]].Center) <= float64(radius) {
					within = true
					break
				}
			}
		}
		if !within {
			continue
		}
		provinceSet[id] = true
		if node.County != "" {
			countySet[node.County] = true
		}
		if node.Duchy != "" {
			duchySet[node.Duchy] = true
		}
	}
	return MapRouteCorridorTargets{ProvinceIDs: sortedMapRouteIDs(provinceSet), CountyIDs: sortedStringSet(countySet), DuchyIDs: sortedStringSet(duchySet)}
}

func buildMapRouteLegs(path []MapRoutePoint, mode string) []MapRouteLeg {
	if len(path) == 0 {
		return nil
	}
	if mode == "sea" {
		return []MapRouteLeg{{Kind: "embark", StartIndex: 0, EndIndex: 0}, {Kind: "sea", StartIndex: 0, EndIndex: len(path) - 1}, {Kind: "disembark", StartIndex: len(path) - 1, EndIndex: len(path) - 1}}
	}
	legKind := func(point MapRoutePoint) string {
		if point.WaterKind != "" {
			return "sea"
		}
		return "land"
	}
	legs := []MapRouteLeg{{Kind: legKind(path[0]), StartIndex: 0}}
	for index := 1; index < len(path); index++ {
		kind := legKind(path[index])
		if kind != legs[len(legs)-1].Kind {
			legs[len(legs)-1].EndIndex = index - 1
			if kind == "sea" {
				legs = append(legs, MapRouteLeg{Kind: "embark", StartIndex: index - 1, EndIndex: index})
			} else {
				legs = append(legs, MapRouteLeg{Kind: "disembark", StartIndex: index - 1, EndIndex: index})
			}
			legs = append(legs, MapRouteLeg{Kind: kind, StartIndex: index})
		}
	}
	legs[len(legs)-1].EndIndex = len(path) - 1
	return legs
}

func sortedMapRouteIDs(values map[int]bool) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func sortedStringSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func mapPointDistance(a, b MapPoint) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func pointSegmentDistance(point, start, end MapPoint) float64 {
	dx, dy := end.X-start.X, end.Y-start.Y
	if dx == 0 && dy == 0 {
		return mapPointDistance(point, start)
	}
	ratio := ((point.X-start.X)*dx + (point.Y-start.Y)*dy) / (dx*dx + dy*dy)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	projection := MapPoint{X: start.X + ratio*dx, Y: start.Y + ratio*dy}
	return mapPointDistance(point, projection)
}

func sumFloat64(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total
}

func roundMapRouteDistance(value float64) float64 { return math.Round(value*100) / 100 }
