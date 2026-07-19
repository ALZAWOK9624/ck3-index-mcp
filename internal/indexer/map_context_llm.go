package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type MapPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type MapBox struct {
	MinX int `json:"min_x"`
	MinY int `json:"min_y"`
	MaxX int `json:"max_x"`
	MaxY int `json:"max_y"`
}

type MapLocalizedName struct {
	Key     string `json:"key"`
	English string `json:"english,omitempty"`
	Chinese string `json:"chinese,omitempty"`
}

type MapBuildingState struct {
	HoldingType         string   `json:"holding_type,omitempty"`
	IsCountyCapital     bool     `json:"is_county_capital,omitempty"`
	SpecialBuilding     string   `json:"special_building,omitempty"`
	SpecialBuildingSlot string   `json:"special_building_slot,omitempty"`
	DuchyBuilding       string   `json:"duchy_building,omitempty"`
	Buildings           []string `json:"buildings,omitempty"`
	BuildingCount       int      `json:"building_count,omitempty"`
	HasSpecialBuilding  bool     `json:"has_special_building"`
	HasSpecialSlot      bool     `json:"has_special_building_slot"`
	SlotStatus          string   `json:"slot_status,omitempty"`
	Flags               []string `json:"flags,omitempty"`
}

type MapHolySiteRow struct {
	ID         string   `json:"id"`
	County     string   `json:"county,omitempty"`
	Barony     string   `json:"barony,omitempty"`
	ProvinceID int      `json:"province_id,omitempty"`
	Faiths     []string `json:"faiths,omitempty"`
}

type MapVisualPoint struct {
	ProvinceID int      `json:"province_id"`
	TitleID    string   `json:"title_id,omitempty"`
	Center     MapPoint `json:"center"`
}

type MapVisualSummary struct {
	Center MapPoint         `json:"center"`
	BBox   MapBox           `json:"bbox"`
	Points []MapVisualPoint `json:"points,omitempty"`
}

type MapProvinceRow struct {
	ProvinceID    int                         `json:"province_id"`
	Center        MapPoint                    `json:"center"`
	BBox          MapBox                      `json:"bbox"`
	Area          int                         `json:"area"`
	Blocked       bool                        `json:"blocked"`
	BlockKind     string                      `json:"block_kind,omitempty"`
	WaterKind     string                      `json:"water_kind,omitempty"`
	Terrain       string                      `json:"terrain,omitempty"`
	Barony        string                      `json:"barony,omitempty"`
	County        string                      `json:"county,omitempty"`
	Duchy         string                      `json:"duchy,omitempty"`
	Kingdom       string                      `json:"kingdom,omitempty"`
	Empire        string                      `json:"empire,omitempty"`
	Names         map[string]MapLocalizedName `json:"names,omitempty"`
	Culture       string                      `json:"culture,omitempty"`
	Religion      string                      `json:"religion,omitempty"`
	Holder        string                      `json:"holder,omitempty"`
	GeographyTags []string                    `json:"geography_tags,omitempty"`
	Building      MapBuildingState            `json:"building"`
	HolySites     []MapHolySiteRow            `json:"holy_sites,omitempty"`
	Regions       []string                    `json:"regions,omitempty"`
}

type MapNeighborRow struct {
	FromProvinceID int      `json:"from_province_id,omitempty"`
	ProvinceID     int      `json:"province_id"`
	Center         MapPoint `json:"center"`
	DeltaX         float64  `json:"delta_x_pixels"`
	DeltaY         float64  `json:"delta_y_pixels"`
	DistancePixels float64  `json:"distance_pixels"`
	BearingDegrees float64  `json:"bearing_degrees"`
	Direction      string   `json:"direction,omitempty"`
	BorderLen      int      `json:"border_len"`
	Blocked        bool     `json:"blocked"`
	AdjacencyKind  string   `json:"adjacency_kind"`
	EdgeWaterKind  string   `json:"edge_water_kind,omitempty"`
	BlockKind      string   `json:"block_kind,omitempty"`
	WaterKind      string   `json:"water_kind,omitempty"`
	Terrain        string   `json:"terrain,omitempty"`
	County         string   `json:"county,omitempty"`
	Culture        string   `json:"culture,omitempty"`
	Religion       string   `json:"religion,omitempty"`
	Holder         string   `json:"holder,omitempty"`
	GeographyTags  []string `json:"geography_tags,omitempty"`
}

type MapAdjacencySummary struct {
	LandNeighbors               int            `json:"land_neighbors"`
	WaterNeighbors              int            `json:"water_neighbors"`
	ImpassableMountainNeighbors int            `json:"impassable_mountain_neighbors"`
	OtherBlockedNeighbors       int            `json:"other_blocked_neighbors"`
	WaterKinds                  []MapCount     `json:"water_kinds,omitempty"`
	WaterByDirection            map[string]int `json:"water_by_direction,omitempty"`
	MountainsByDirection        map[string]int `json:"impassable_mountains_by_direction,omitempty"`
}

type MapProvinceInfoResult struct {
	Intent            string                      `json:"intent"`
	Query             string                      `json:"query"`
	Year              int                         `json:"year"`
	Summary           string                      `json:"summary"`
	Province          MapProvinceRow              `json:"province"`
	Surface           *MapSurfaceContext          `json:"surface,omitempty"`
	ResolvedSubject   MapResolvedSubject          `json:"resolved_subject"`
	Neighbors         []MapNeighborRow            `json:"neighbors,omitempty"`
	AdjacentFeatures  MapAdjacencySummary         `json:"adjacent_features"`
	StrategicPassages []MapStrategicPassageRow    `json:"strategic_passages,omitempty"`
	Physical          *MapPhysicalProvinceContext `json:"physical,omitempty"`
	Guidance          []string                    `json:"guidance,omitempty"`
}

type MapNeighborsResult struct {
	Intent           string                      `json:"intent"`
	Query            string                      `json:"query"`
	Year             int                         `json:"year"`
	Radius           int                         `json:"radius"`
	ResolvedSubject  MapResolvedSubject          `json:"resolved_subject"`
	Summary          string                      `json:"summary"`
	Counts           map[string]int              `json:"counts"`
	ByDepth          map[int][]MapNeighborRow    `json:"by_depth"`
	ByDirection      map[string][]MapNeighborRow `json:"by_direction,omitempty"`
	Cultures         []MapCount                  `json:"cultures,omitempty"`
	Religions        []MapCount                  `json:"religions,omitempty"`
	Terrains         []MapCount                  `json:"terrains,omitempty"`
	Holders          []MapCount                  `json:"holders,omitempty"`
	AdjacentFeatures MapAdjacencySummary         `json:"adjacent_features"`
}

type MapFrame struct {
	MinX           int     `json:"min_x"`
	MinY           int     `json:"min_y"`
	MaxX           int     `json:"max_x"`
	MaxY           int     `json:"max_y"`
	WidthPixels    int     `json:"width_pixels"`
	HeightPixels   int     `json:"height_pixels"`
	DiagonalPixels float64 `json:"diagonal_pixels"`
}

type MapSpatialRelationResult struct {
	Intent                   string                   `json:"intent"`
	From                     MapProvinceRow           `json:"from"`
	To                       MapProvinceRow           `json:"to"`
	ResolvedFrom             MapResolvedSubject       `json:"resolved_from"`
	ResolvedTo               MapResolvedSubject       `json:"resolved_to"`
	DeltaX                   float64                  `json:"delta_x_pixels"`
	DeltaY                   float64                  `json:"delta_y_pixels"`
	DistancePixels           float64                  `json:"distance_pixels"`
	DistanceMapDiagonalRatio float64                  `json:"distance_map_diagonal_ratio"`
	BearingDegrees           float64                  `json:"bearing_degrees"`
	Direction                string                   `json:"direction"`
	ReverseDirection         string                   `json:"reverse_direction"`
	Adjacent                 bool                     `json:"adjacent"`
	ExplicitConnection       bool                     `json:"explicit_connection"`
	BorderLen                int                      `json:"border_len,omitempty"`
	AdjacencyKind            string                   `json:"adjacency_kind"`
	StrategicPassages        []MapStrategicPassageRow `json:"strategic_passages,omitempty"`
	EdgeWaterKind            string                   `json:"edge_water_kind,omitempty"`
	MapFrame                 MapFrame                 `json:"map_frame"`
	FromAdjacentFeatures     MapAdjacencySummary      `json:"from_adjacent_features"`
	ToAdjacentFeatures       MapAdjacencySummary      `json:"to_adjacent_features"`
	Summary                  string                   `json:"summary"`
	Guidance                 []string                 `json:"guidance,omitempty"`
}

type MapStrategicPassageRow struct {
	ID               int      `json:"id"`
	FromProvince     int      `json:"from_province"`
	ToProvince       int      `json:"to_province"`
	ThroughProvince  int      `json:"through_province,omitempty"`
	ConnectionType   string   `json:"connection_type"`
	PassageKind      string   `json:"passage_kind"`
	Start            MapPoint `json:"start"`
	Stop             MapPoint `json:"stop"`
	DistancePixels   float64  `json:"distance_pixels"`
	Comment          string   `json:"comment,omitempty"`
	FromSubterranean bool     `json:"from_subterranean"`
	ToSubterranean   bool     `json:"to_subterranean"`
}

type MapStrategicPassagesResult struct {
	Intent   string                   `json:"intent"`
	Target   string                   `json:"target"`
	Kind     string                   `json:"kind,omitempty"`
	Summary  string                   `json:"summary"`
	Counts   []MapCount               `json:"counts"`
	Passages []MapStrategicPassageRow `json:"passages,omitempty"`
	Guidance []string                 `json:"guidance,omitempty"`
}

type MapTitleContextResult struct {
	Intent          string                  `json:"intent"`
	Query           string                  `json:"query"`
	Year            int                     `json:"year"`
	Summary         string                  `json:"summary"`
	Title           MapTitleRow             `json:"title"`
	Counts          map[string]int          `json:"counts"`
	Cultures        []MapCount              `json:"cultures,omitempty"`
	Religions       []MapCount              `json:"religions,omitempty"`
	Terrains        []MapCount              `json:"terrains,omitempty"`
	Geography       []MapCount              `json:"geography,omitempty"`
	Regions         []MapCount              `json:"regions,omitempty"`
	Holders         []MapCount              `json:"holders,omitempty"`
	HolySites       []MapHolySiteRow        `json:"holy_sites,omitempty"`
	Buildings       MapTitleBuildingSummary `json:"buildings"`
	Visual          MapVisualSummary        `json:"visual"`
	NeighborTitles  []MapTitleBorder        `json:"neighbor_titles,omitempty"`
	CoarseGeography MapCoarseGeography      `json:"coarse_geography"`
	Physical        *MapPhysicalAggregate   `json:"physical,omitempty"`
	Guidance        []string                `json:"guidance,omitempty"`
	IntegrityStatus string                  `json:"integrity_status"`
	IntegrityIssues []MapIntegrityIssue     `json:"integrity_warnings,omitempty"`
}

type MapTitleRow struct {
	TitleID       string           `json:"title_id"`
	Type          string           `json:"type"`
	ParentID      string           `json:"parent_id,omitempty"`
	CapitalTitle  string           `json:"capital_title,omitempty"`
	Name          MapLocalizedName `json:"name,omitempty"`
	Holder        string           `json:"holder,omitempty"`
	ProvinceCount int              `json:"province_count"`
	Center        MapPoint         `json:"center"`
	BBox          MapBox           `json:"bbox"`
}

type MapTitleBuildingSummary struct {
	HoldingTypes         []MapCount `json:"holding_types,omitempty"`
	SpecialBuildings     []MapCount `json:"special_buildings,omitempty"`
	SpecialBuildingSlots []MapCount `json:"special_building_slots,omitempty"`
	DuchyBuildings       []MapCount `json:"duchy_buildings,omitempty"`
	EmptySpecialSlots    int        `json:"empty_special_slots,omitempty"`
	OccupiedSpecialSlots int        `json:"occupied_special_slots,omitempty"`
	RuinHoldings         int        `json:"ruin_holdings,omitempty"`
	PlaceholderHoldings  int        `json:"placeholder_holdings,omitempty"`
	CountyCapitals       int        `json:"county_capitals,omitempty"`
}

type MapTitleBorder struct {
	TitleID   string `json:"title_id"`
	BorderLen int    `json:"border_len"`
	Holder    string `json:"holder,omitempty"`
	Direction string `json:"direction,omitempty"`
}

type MapCoarseGeography struct {
	MapRegion               string              `json:"map_region,omitempty"`
	Coastal                 bool                `json:"coastal"`
	Inland                  bool                `json:"inland"`
	IslandTendency          bool                `json:"island_tendency"`
	TerrainSummary          []MapCount          `json:"terrain_summary,omitempty"`
	MajorNeighborDirections map[string][]string `json:"major_neighbor_directions,omitempty"`
}

type MapCount struct {
	ID     string `json:"id"`
	Count  int    `json:"count"`
	Weight int    `json:"weight,omitempty"`
}

type MapAssignmentPlanResult struct {
	Intent          string              `json:"intent"`
	Query           string              `json:"query"`
	Year            int                 `json:"year"`
	Mode            string              `json:"mode"`
	Summary         string              `json:"summary"`
	Recommendations []MapRecommendation `json:"recommendations,omitempty"`
	PatchFiles      []PatchFileInput    `json:"patch_files,omitempty"`
	Guidance        []string            `json:"guidance,omitempty"`
}

type MapRecommendation struct {
	Kind       string   `json:"kind"`
	Target     string   `json:"target"`
	Value      string   `json:"value,omitempty"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

type MapBuildingCandidatesResult struct {
	Intent     string                 `json:"intent"`
	Query      string                 `json:"query"`
	Year       int                    `json:"year"`
	Summary    string                 `json:"summary"`
	Candidates []MapBuildingCandidate `json:"candidates,omitempty"`
	Visual     MapVisualSummary       `json:"visual"`
	Guidance   []string               `json:"guidance,omitempty"`
}

type MapBuildingCandidate struct {
	Province               MapProvinceRow `json:"province"`
	Score                  int            `json:"score"`
	Reasons                []string       `json:"reasons,omitempty"`
	Warnings               []string       `json:"warnings,omitempty"`
	DistanceToWater        int            `json:"distance_to_water,omitempty"`
	NearbySameCulture      int            `json:"nearby_same_culture,omitempty"`
	NearbySpecialBuildings int            `json:"nearby_special_buildings,omitempty"`
	Border                 bool           `json:"border"`
	EmptySpecialSlot       bool           `json:"empty_special_slot"`
}

func (db *DB) LLMMapProvinceInfo(ctx context.Context, id string, year int, opts LLMOptions) (MapProvinceInfoResult, error) {
	if year <= 0 {
		year = 1
	}
	resolved, err := db.ResolveMapSubject(ctx, id, year)
	if err != nil {
		return MapProvinceInfoResult{}, err
	}
	pid := resolved.ProvinceID
	prov, err := db.mapProvinceAt(ctx, pid, yearDateKey(year))
	if err != nil {
		return MapProvinceInfoResult{}, err
	}
	if prov.ProvinceID == 0 {
		return MapProvinceInfoResult{}, fmt.Errorf("province %d not found in map cache; run ck3-index scan", pid)
	}
	neighbors, err := db.mapNeighborRows(ctx, pid, yearDateKey(year), opts.normalizedLimit())
	if err != nil {
		return MapProvinceInfoResult{}, err
	}
	passages, err := db.mapStrategicPassagesForProvinces(ctx, map[int]bool{pid: true}, "", opts.normalizedLimit())
	if err != nil {
		return MapProvinceInfoResult{}, err
	}
	var physical *MapPhysicalProvinceContext
	if value, physicalErr := db.physicalProvinceContext(ctx, pid, "summary", db.cachedGISSidecarStatus(ctx)); physicalErr == nil {
		physical = &value
	}
	surface, err := db.mapSurfaceProvinceContext(ctx, pid, 4)
	if err != nil {
		return MapProvinceInfoResult{}, err
	}
	summary := fmt.Sprintf("Province %d is terrain %s, block_kind %s, county %s, holding %s, slot_status %s, culture %s, religion %s, holder %s.", pid, prov.Terrain, prov.BlockKind, prov.County, prov.Building.HoldingType, prov.Building.SlotStatus, prov.Culture, prov.Religion, prov.Holder)
	if surface.Available {
		summary += fmt.Sprintf(" Its dominant observed map-surface material is %s.", surface.DominantMaterialID)
	}
	return MapProvinceInfoResult{
		Intent: "map_province_info", Query: id, Year: year, Province: prov, Surface: surface, ResolvedSubject: resolved, Neighbors: neighbors,
		AdjacentFeatures:  summarizeMapAdjacencies(neighbors),
		StrategicPassages: passages,
		Physical:          physical,
		Summary:           summary,
		Guidance:          []string{"Neighbor direction and pixel distance are measured from this province centroid; positive Y points south on the source image.", "Water and impassable-mountain boundaries are classified separately in adjacent_features and adjacency_kind.", "Surface-material shares come from indexed terrain blend rasters and are not the same as scripted common/province_terrain categories.", "Use map_spatial_relation for an exact two-province centroid comparison."},
	}, nil
}

func (db *DB) LLMMapNeighbors(ctx context.Context, id string, radius int, year int, opts LLMOptions) (MapNeighborsResult, error) {
	if year <= 0 {
		year = 1
	}
	if radius <= 0 {
		radius = 1
	}
	if radius > 3 {
		radius = 3
	}
	resolved, err := db.ResolveMapSubject(ctx, id, year)
	if err != nil {
		return MapNeighborsResult{}, err
	}
	start := []int{resolved.ProvinceID}
	date := yearDateKey(year)
	seen := map[int]bool{}
	frontier := map[int]bool{}
	for _, pid := range start {
		seen[pid] = true
		frontier[pid] = true
	}
	byDepth := map[int][]MapNeighborRow{}
	byDirection := map[string][]MapNeighborRow{}
	refCenter, _ := db.averageProvinceCenter(ctx, start)
	cultures, religions, terrains, holders := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	var discovered []MapNeighborRow
	for depth := 1; depth <= radius; depth++ {
		next := map[int]bool{}
		for pid := range frontier {
			rows, err := db.mapNeighborRows(ctx, pid, date, 1000)
			if err != nil {
				return MapNeighborsResult{}, err
			}
			for _, n := range rows {
				if seen[n.ProvinceID] {
					continue
				}
				seen[n.ProvinceID] = true
				if c, ok := db.provinceCenter(ctx, n.ProvinceID); ok {
					setNeighborSpatial(&n, refCenter, c)
					byDirection[n.Direction] = append(byDirection[n.Direction], n)
				}
				discovered = append(discovered, n)
				next[n.ProvinceID] = true
				byDepth[depth] = append(byDepth[depth], n)
				if n.Culture != "" {
					cultures[n.Culture]++
				}
				if n.Religion != "" {
					religions[n.Religion]++
				}
				if n.Terrain != "" {
					terrains[n.Terrain]++
				}
				if n.Holder != "" {
					holders[n.Holder]++
				}
			}
		}
		frontier = next
	}
	limitDepthRows(byDepth, opts.normalizedLimit())
	for k := range byDirection {
		if len(byDirection[k]) > opts.normalizedLimit() {
			byDirection[k] = byDirection[k][:opts.normalizedLimit()]
		}
	}
	return MapNeighborsResult{
		Intent: "map_neighbors", Query: id, Year: year, Radius: radius, ResolvedSubject: resolved,
		Summary:          fmt.Sprintf("Found %d province(s) within radius %d of %s.", len(seen)-len(start), radius, id),
		Counts:           map[string]int{"start_provinces": len(start), "seen_provinces": len(seen)},
		ByDepth:          byDepth,
		ByDirection:      byDirection,
		Cultures:         topMapCounts(cultures, opts.normalizedLimit()),
		Religions:        topMapCounts(religions, opts.normalizedLimit()),
		Terrains:         topMapCounts(terrains, opts.normalizedLimit()),
		Holders:          topMapCounts(holders, opts.normalizedLimit()),
		AdjacentFeatures: summarizeMapAdjacencies(discovered),
	}, nil
}

func (db *DB) LLMMapSpatialRelation(ctx context.Context, fromID, toID string, year int, opts LLMOptions) (MapSpatialRelationResult, error) {
	if year <= 0 {
		year = 1
	}
	resolvedFrom, err := db.ResolveMapSubject(ctx, fromID, year)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	resolvedTo, err := db.ResolveMapSubject(ctx, toID, year)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	fromPID, toPID := resolvedFrom.ProvinceID, resolvedTo.ProvinceID
	date := yearDateKey(year)
	from, err := db.mapProvinceAt(ctx, fromPID, date)
	if err != nil || from.ProvinceID == 0 {
		if err == nil {
			err = fmt.Errorf("province %d not found in map cache", fromPID)
		}
		return MapSpatialRelationResult{}, err
	}
	to, err := db.mapProvinceAt(ctx, toPID, date)
	if err != nil || to.ProvinceID == 0 {
		if err == nil {
			err = fmt.Errorf("province %d not found in map cache", toPID)
		}
		return MapSpatialRelationResult{}, err
	}
	dx, dy, distance, bearing, direction := spatialMetrics(from.Center, to.Center)
	frame, err := db.mapFrame(ctx)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	result := MapSpatialRelationResult{
		Intent: "map_spatial_relation", From: from, To: to, ResolvedFrom: resolvedFrom, ResolvedTo: resolvedTo,
		DeltaX: dx, DeltaY: dy, DistancePixels: distance, BearingDegrees: bearing,
		Direction: direction, ReverseDirection: coarseDirection(-dx, -dy),
		AdjacencyKind: "not_adjacent", MapFrame: frame,
		Guidance: []string{"Bearing uses compass convention: 0=north, 90=east, 180=south, 270=west.", "Pixel distance is straight-line distance between province centroids, not travel distance.", "Positive delta_y means the target lies south on provinces.png."},
	}
	if frame.DiagonalPixels > 0 {
		result.DistanceMapDiagonalRatio = roundMapMetric(distance / frame.DiagonalPixels)
	}
	var blocked int
	err = db.sql.QueryRowContext(ctx, `SELECT border_len,blocked FROM map_adjacencies WHERE province_id=? AND neighbor_id=?`, fromPID, toPID).Scan(&result.BorderLen, &blocked)
	if err == nil {
		result.Adjacent = true
		result.AdjacencyKind, result.EdgeWaterKind = mapAdjacencyKind(from, to, blocked != 0)
	} else if err != sql.ErrNoRows {
		return MapSpatialRelationResult{}, err
	}
	result.StrategicPassages, err = db.mapStrategicPassagesBetween(ctx, fromPID, toPID)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	if len(result.StrategicPassages) > 0 {
		result.ExplicitConnection = true
		if !result.Adjacent {
			result.AdjacencyKind = result.StrategicPassages[0].PassageKind
		}
	}
	fromNeighbors, err := db.mapNeighborRows(ctx, fromPID, date, 10000)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	toNeighbors, err := db.mapNeighborRows(ctx, toPID, date, 10000)
	if err != nil {
		return MapSpatialRelationResult{}, err
	}
	result.FromAdjacentFeatures = summarizeMapAdjacencies(fromNeighbors)
	result.ToAdjacentFeatures = summarizeMapAdjacencies(toNeighbors)
	result.Summary = fmt.Sprintf("Province %d is %s of province %d at %.2f pixels; adjacent=%t, explicit_connection=%t, adjacency_kind=%s.", toPID, direction, fromPID, distance, result.Adjacent, result.ExplicitConnection, result.AdjacencyKind)
	return result, nil
}

func (db *DB) LLMMapStrategicPassages(ctx context.Context, target, kind string, opts LLMOptions) (MapStrategicPassagesResult, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "all"
	}
	pids, err := db.mapRenderTargetProvinces(ctx, target)
	if err != nil {
		return MapStrategicPassagesResult{}, err
	}
	selected := map[int]bool{}
	for _, pid := range pids {
		selected[pid] = true
	}
	all, err := db.mapStrategicPassagesForProvinces(ctx, selected, strings.TrimSpace(kind), 0)
	if err != nil {
		return MapStrategicPassagesResult{}, err
	}
	counts := map[string]int{}
	for _, passage := range all {
		counts[passage.PassageKind]++
	}
	limit := opts.normalizedLimit()
	passages := all
	if len(passages) > limit {
		passages = passages[:limit]
	}
	return MapStrategicPassagesResult{
		Intent: "map_strategic_passages", Target: target, Kind: kind,
		Summary: fmt.Sprintf("Found %d explicit strategic passage(s) touching %s; returning %d.", len(all), target, len(passages)),
		Counts:  topMapCounts(counts, 20), Passages: passages,
		Guidance: []string{"Strategic passages come from adjacencies.csv and are separate from pixel-border neighbors.", "underground_gateway and offmap_gateway should be rendered as portal stubs, not full straight lines.", "Graph diffusion does not traverse strategic passages unless a caller explicitly opts in."},
	}, nil
}

func (db *DB) mapStrategicPassagesBetween(ctx context.Context, fromPID, toPID int) ([]MapStrategicPassageRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id,from_province,to_province,through_province,connection_type,passage_kind,start_x,start_y,stop_x,stop_y,distance_pixels,comment,from_subterranean,to_subterranean
		FROM map_strategic_adjacencies WHERE (from_province=? AND to_province=?) OR (from_province=? AND to_province=?) ORDER BY id`, fromPID, toPID, toPID, fromPID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMapStrategicPassageRows(rows)
}

func (db *DB) mapStrategicPassagesForProvinces(ctx context.Context, selected map[int]bool, kind string, limit int) ([]MapStrategicPassageRow, error) {
	query := `SELECT id,from_province,to_province,through_province,connection_type,passage_kind,start_x,start_y,stop_x,stop_y,distance_pixels,comment,from_subterranean,to_subterranean FROM map_strategic_adjacencies`
	args := []any{}
	if kind != "" {
		query += ` WHERE passage_kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY CASE passage_kind WHEN 'underground_gateway' THEN 0 WHEN 'offmap_gateway' THEN 1 WHEN 'underground_internal' THEN 2 ELSE 3 END,distance_pixels DESC,id`
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanMapStrategicPassageRows(rows)
	if err != nil {
		return nil, err
	}
	out := make([]MapStrategicPassageRow, 0, len(all))
	for _, passage := range all {
		if selected[passage.FromProvince] || selected[passage.ToProvince] {
			out = append(out, passage)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func scanMapStrategicPassageRows(rows *sql.Rows) ([]MapStrategicPassageRow, error) {
	var out []MapStrategicPassageRow
	for rows.Next() {
		var item MapStrategicPassageRow
		var fromSub, toSub int
		if err := rows.Scan(&item.ID, &item.FromProvince, &item.ToProvince, &item.ThroughProvince, &item.ConnectionType, &item.PassageKind,
			&item.Start.X, &item.Start.Y, &item.Stop.X, &item.Stop.Y, &item.DistancePixels, &item.Comment, &fromSub, &toSub); err != nil {
			return nil, err
		}
		item.FromSubterranean = fromSub != 0
		item.ToSubterranean = toSub != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) LLMMapTitleContext(ctx context.Context, id string, year int, opts LLMOptions) (MapTitleContextResult, error) {
	if year <= 0 {
		year = 1
	}
	date := yearDateKey(year)
	title, err := db.mapTitleRow(ctx, id, date)
	if err != nil {
		return MapTitleContextResult{}, err
	}
	provinces, err := db.titleProvinceIDs(ctx, id)
	if err != nil {
		return MapTitleContextResult{}, err
	}
	if len(provinces) == 0 {
		return MapTitleContextResult{}, fmt.Errorf("title %s has no mapped provinces in map cache", id)
	}
	cultures, religions, terrains, holders := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	geography, regions := map[string]int{}, map[string]int{}
	holySites := map[string]MapHolySiteRow{}
	holdingTypes, specialBuildings, specialSlots, duchyBuildings := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	buildingSummary := MapTitleBuildingSummary{}
	provinceSet := map[int]bool{}
	var visualPoints []MapVisualPoint
	for _, pid := range provinces {
		provinceSet[pid] = true
		p, err := db.mapProvinceAt(ctx, pid, date)
		if err != nil {
			return MapTitleContextResult{}, err
		}
		if p.Culture != "" {
			cultures[p.Culture]++
		}
		if p.Religion != "" {
			religions[p.Religion]++
		}
		if p.Terrain != "" {
			terrains[p.Terrain]++
		}
		if p.Holder != "" {
			holders[p.Holder]++
		}
		for _, tag := range p.GeographyTags {
			geography[tag]++
		}
		for _, region := range p.Regions {
			regions[region]++
		}
		for _, site := range p.HolySites {
			holySites[site.ID] = site
		}
		if p.Building.HoldingType != "" {
			holdingTypes[p.Building.HoldingType]++
		}
		if p.Building.SpecialBuilding != "" {
			specialBuildings[p.Building.SpecialBuilding]++
		}
		if p.Building.SpecialBuildingSlot != "" {
			specialSlots[p.Building.SpecialBuildingSlot]++
		}
		if p.Building.DuchyBuilding != "" {
			duchyBuildings[p.Building.DuchyBuilding]++
		}
		switch p.Building.SlotStatus {
		case "empty_special_slot":
			buildingSummary.EmptySpecialSlots++
		case "occupied_special_slot":
			buildingSummary.OccupiedSpecialSlots++
		}
		if containsString(p.Building.Flags, "ruin_holding") {
			buildingSummary.RuinHoldings++
		}
		if containsString(p.Building.Flags, "placeholder_holding") {
			buildingSummary.PlaceholderHoldings++
		}
		if p.Building.IsCountyCapital {
			buildingSummary.CountyCapitals++
		}
		if len(visualPoints) < opts.normalizedLimit() {
			visualPoints = append(visualPoints, MapVisualPoint{ProvinceID: p.ProvinceID, TitleID: p.Barony, Center: p.Center})
		}
	}
	neighborTitles, err := db.neighborTitleBorders(ctx, provinceSet, date, opts.normalizedLimit())
	if err != nil {
		return MapTitleContextResult{}, err
	}
	integrityIssues, err := db.mapIntegrityIssues(ctx, id, provinceSet)
	if err != nil {
		return MapTitleContextResult{}, err
	}
	integrityStatus := "ok"
	if len(integrityIssues) > 0 {
		integrityStatus = "warning"
	}
	title.ProvinceCount = len(provinces)
	holySiteList := make([]MapHolySiteRow, 0, len(holySites))
	for _, site := range holySites {
		holySiteList = append(holySiteList, site)
	}
	sort.Slice(holySiteList, func(i, j int) bool { return holySiteList[i].ID < holySiteList[j].ID })
	buildingSummary.HoldingTypes = topMapCounts(holdingTypes, opts.normalizedLimit())
	buildingSummary.SpecialBuildings = topMapCounts(specialBuildings, opts.normalizedLimit())
	buildingSummary.SpecialBuildingSlots = topMapCounts(specialSlots, opts.normalizedLimit())
	buildingSummary.DuchyBuildings = topMapCounts(duchyBuildings, opts.normalizedLimit())
	coarse := MapCoarseGeography{MapRegion: db.coarseMapRegion(ctx, title.Center), Coastal: geography["coastal"] > 0, IslandTendency: geography["island_like"] > 0, TerrainSummary: topMapCounts(terrains, 5), MajorNeighborDirections: map[string][]string{}}
	coarse.Inland = !coarse.Coastal
	for i := range neighborTitles {
		if nt, err := db.mapTitleRow(ctx, neighborTitles[i].TitleID, date); err == nil {
			neighborTitles[i].Direction = coarseDirection(nt.Center.X-title.Center.X, nt.Center.Y-title.Center.Y)
		}
		d := neighborTitles[i].Direction
		if d != "" {
			coarse.MajorNeighborDirections[d] = append(coarse.MajorNeighborDirections[d], neighborTitles[i].TitleID)
		}
	}
	var physical *MapPhysicalAggregate
	if ids, physicalErr := db.physicalProvinceIDs(ctx, provinceSet); physicalErr == nil && len(ids) > 0 {
		if value, aggregateErr := db.physicalAggregate(ctx, ids); aggregateErr == nil {
			physical = &value
		}
	}
	return MapTitleContextResult{
		Intent: "map_title_context", Query: id, Year: year, Title: title,
		Summary:         fmt.Sprintf("%s covers %d province(s); holder=%s.", id, len(provinces), title.Holder),
		Counts:          map[string]int{"provinces": len(provinces), "neighbor_titles": len(neighborTitles)},
		Cultures:        topMapCounts(cultures, opts.normalizedLimit()),
		Religions:       topMapCounts(religions, opts.normalizedLimit()),
		Terrains:        topMapCounts(terrains, opts.normalizedLimit()),
		Geography:       topMapCounts(geography, opts.normalizedLimit()),
		Regions:         topMapCounts(regions, opts.normalizedLimit()),
		Holders:         topMapCounts(holders, opts.normalizedLimit()),
		HolySites:       holySiteList,
		Buildings:       buildingSummary,
		Visual:          MapVisualSummary{Center: title.Center, BBox: title.BBox, Points: visualPoints},
		NeighborTitles:  neighborTitles,
		CoarseGeography: coarse,
		Physical:        physical,
		Guidance:        []string{"Use this title context before generating religion or character assignment patches.", "Use map_building_candidates for special-building placement candidates and review reasons."},
		IntegrityStatus: integrityStatus,
		IntegrityIssues: integrityIssues,
	}, nil
}

func coarseDirection(dx, dy float64) string {
	ax, ay := dx, dy
	if ax < 0 {
		ax = -ax
	}
	if ay < 0 {
		ay = -ay
	}
	if ax < 0.45*ay {
		if dy < 0 {
			return "north"
		}
		return "south"
	}
	if ay < 0.45*ax {
		if dx < 0 {
			return "west"
		}
		return "east"
	}
	if dx >= 0 && dy < 0 {
		return "northeast"
	}
	if dx >= 0 && dy >= 0 {
		return "southeast"
	}
	if dx < 0 && dy >= 0 {
		return "southwest"
	}
	return "northwest"
}

func roundMapMetric(v float64) float64 {
	return math.Round(v*100) / 100
}

func spatialMetrics(from, to MapPoint) (dx, dy, distance, bearing float64, direction string) {
	dx = roundMapMetric(to.X - from.X)
	dy = roundMapMetric(to.Y - from.Y)
	distance = roundMapMetric(math.Hypot(to.X-from.X, to.Y-from.Y))
	bearing = math.Atan2(to.X-from.X, -(to.Y-from.Y)) * 180 / math.Pi
	if bearing < 0 {
		bearing += 360
	}
	bearing = roundMapMetric(bearing)
	direction = coarseDirection(to.X-from.X, to.Y-from.Y)
	return
}

func setNeighborSpatial(n *MapNeighborRow, from, to MapPoint) {
	n.Center = to
	n.DeltaX, n.DeltaY, n.DistancePixels, n.BearingDegrees, n.Direction = spatialMetrics(from, to)
}

func mapAdjacencyKind(from, to MapProvinceRow, blocked bool) (string, string) {
	if from.BlockKind == "impassable_mountain" || to.BlockKind == "impassable_mountain" {
		return "impassable_mountain_boundary", ""
	}
	if from.BlockKind == "water" || to.BlockKind == "water" {
		waterKind := to.WaterKind
		if waterKind == "" {
			waterKind = from.WaterKind
		}
		return "water_boundary", waterKind
	}
	if blocked {
		return "other_blocked_boundary", ""
	}
	return "land_border", ""
}

func summarizeMapAdjacencies(rows []MapNeighborRow) MapAdjacencySummary {
	s := MapAdjacencySummary{}
	waterKinds := map[string]int{}
	for _, n := range rows {
		switch n.AdjacencyKind {
		case "water_boundary":
			s.WaterNeighbors++
			kind := n.EdgeWaterKind
			if kind == "" {
				kind = "water"
			}
			waterKinds[kind]++
			if s.WaterByDirection == nil {
				s.WaterByDirection = map[string]int{}
			}
			s.WaterByDirection[n.Direction]++
		case "impassable_mountain_boundary":
			s.ImpassableMountainNeighbors++
			if s.MountainsByDirection == nil {
				s.MountainsByDirection = map[string]int{}
			}
			s.MountainsByDirection[n.Direction]++
		case "other_blocked_boundary":
			s.OtherBlockedNeighbors++
		default:
			s.LandNeighbors++
		}
	}
	s.WaterKinds = topMapCounts(waterKinds, 20)
	return s
}

func (db *DB) mapFrame(ctx context.Context) (MapFrame, error) {
	var f MapFrame
	err := db.sql.QueryRowContext(ctx, `SELECT MIN(min_x),MIN(min_y),MAX(max_x),MAX(max_y) FROM map_provinces WHERE area>0`).Scan(&f.MinX, &f.MinY, &f.MaxX, &f.MaxY)
	if err != nil {
		return f, err
	}
	f.WidthPixels = f.MaxX - f.MinX + 1
	f.HeightPixels = f.MaxY - f.MinY + 1
	f.DiagonalPixels = roundMapMetric(math.Hypot(float64(f.WidthPixels), float64(f.HeightPixels)))
	return f, nil
}

func (db *DB) provinceCenter(ctx context.Context, pid int) (MapPoint, bool) {
	var p MapPoint
	if db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_provinces WHERE province_id=?`, pid).Scan(&p.X, &p.Y) != nil {
		return p, false
	}
	return p, true
}
func (db *DB) averageProvinceCenter(ctx context.Context, pids []int) (MapPoint, error) {
	var p MapPoint
	if len(pids) == 0 {
		return p, nil
	}
	sum := MapPoint{}
	n := 0
	for _, id := range pids {
		if c, ok := db.provinceCenter(ctx, id); ok {
			sum.X += c.X
			sum.Y += c.Y
			n++
		}
	}
	if n > 0 {
		p.X = sum.X / float64(n)
		p.Y = sum.Y / float64(n)
	}
	return p, nil
}
func (db *DB) coarseMapRegion(ctx context.Context, p MapPoint) string {
	var minx, miny, maxx, maxy float64
	if db.sql.QueryRowContext(ctx, `SELECT MIN(center_x),MIN(center_y),MAX(center_x),MAX(center_y) FROM map_provinces`).Scan(&minx, &miny, &maxx, &maxy) != nil {
		return ""
	}
	thirdX := (maxx - minx) / 3
	thirdY := (maxy - miny) / 3
	ew := "central"
	ns := "central"
	if p.X < minx+thirdX {
		ew = "western"
	} else if p.X > maxx-thirdX {
		ew = "eastern"
	}
	if p.Y < miny+thirdY {
		ns = "northern"
	} else if p.Y > maxy-thirdY {
		ns = "southern"
	}
	if ns == "central" && ew == "central" {
		return "central"
	}
	if ns == "central" {
		return ew
	}
	if ew == "central" {
		return ns
	}
	return ns + "-" + ew
}

func (db *DB) LLMMapAssignmentPlan(ctx context.Context, assignmentMode, target string, year int, opts LLMOptions) (MapAssignmentPlanResult, error) {
	if year <= 0 {
		year = 1
	}
	if assignmentMode == "" {
		assignmentMode = "both"
	}
	assignmentMode = strings.ToLower(assignmentMode)
	if assignmentMode != "religion" && assignmentMode != "characters" && assignmentMode != "both" {
		return MapAssignmentPlanResult{}, fmt.Errorf("assignment mode must be religion, characters, or both")
	}
	provinces, err := db.targetProvinceIDs(ctx, target)
	if err != nil {
		return MapAssignmentPlanResult{}, err
	}
	date := yearDateKey(year)
	limit := opts.normalizedLimit()
	var recs []MapRecommendation
	var patches []PatchFileInput
	if assignmentMode == "religion" || assignmentMode == "both" {
		rRecs, patch, err := db.religionAssignmentPreview(ctx, provinces, year, date, limit)
		if err != nil {
			return MapAssignmentPlanResult{}, err
		}
		recs = append(recs, rRecs...)
		if patch.Content != "" {
			patches = append(patches, patch)
		}
	}
	if assignmentMode == "characters" || assignmentMode == "both" {
		cRecs, cPatches, err := db.characterAssignmentPreview(ctx, provinces, year, date, limit)
		if err != nil {
			return MapAssignmentPlanResult{}, err
		}
		recs = append(recs, cRecs...)
		patches = append(patches, cPatches...)
	}
	for i := range recs {
		pids, _ := db.targetProvinceIDs(ctx, recs[i].Target)
		if c, err := db.averageProvinceCenter(ctx, pids); err == nil && len(pids) > 0 {
			if region := db.coarseMapRegion(ctx, c); region != "" {
				recs[i].Evidence = append(recs[i].Evidence, "coarse map region: "+region)
			}
		}
	}
	return MapAssignmentPlanResult{
		Intent: "map_assignment_plan", Query: target, Year: year, Mode: assignmentMode,
		Summary:         fmt.Sprintf("Generated %d recommendation(s) and %d patch preview file(s) for %s.", len(recs), len(patches), target),
		Recommendations: recs, PatchFiles: patches,
		Guidance: []string{"Patch files are previews only; run preflight_patch before applying.", "Low-confidence recommendations should be manually reviewed."},
	}, nil
}

func (db *DB) mapProvinceAt(ctx context.Context, pid int, date int) (MapProvinceRow, error) {
	var p MapProvinceRow
	var blocked, countyCapital int
	err := db.sql.QueryRowContext(ctx, `SELECT province_id,center_x,center_y,min_x,min_y,max_x,max_y,area,blocked,COALESCE(block_kind,''),COALESCE(water_kind,''),COALESCE(terrain,''),COALESCE(barony,''),COALESCE(county,''),COALESCE(duchy,''),COALESCE(kingdom,''),COALESCE(empire,''),is_county_capital
		FROM map_provinces WHERE province_id=?`, pid).Scan(&p.ProvinceID, &p.Center.X, &p.Center.Y, &p.BBox.MinX, &p.BBox.MinY, &p.BBox.MaxX, &p.BBox.MaxY, &p.Area, &blocked, &p.BlockKind, &p.WaterKind, &p.Terrain, &p.Barony, &p.County, &p.Duchy, &p.Kingdom, &p.Empire, &countyCapital)
	if err == sql.ErrNoRows {
		return MapProvinceRow{}, nil
	}
	if err != nil {
		return MapProvinceRow{}, err
	}
	p.Blocked = blocked != 0
	p.Building.IsCountyCapital = countyCapital != 0
	p.Culture, _ = db.resolveProvinceField(ctx, pid, p.County, "culture", date, true)
	p.Religion, _ = db.resolveProvinceField(ctx, pid, p.County, "religion", date, true)
	p.Building.HoldingType, _ = db.resolveProvinceField(ctx, pid, p.County, "holding", date, false)
	p.Building.SpecialBuilding, _ = db.resolveProvinceField(ctx, pid, p.County, "special_building", date, false)
	p.Building.SpecialBuildingSlot, _ = db.resolveProvinceField(ctx, pid, p.County, "special_building_slot", date, false)
	p.Building.DuchyBuilding, _ = db.resolveProvinceField(ctx, pid, p.County, "duchy_building", date, false)
	buildings, _ := db.resolveProvinceField(ctx, pid, p.County, "buildings", date, false)
	p.Building.Buildings = splitMapList(buildings)
	p.Building.BuildingCount = len(p.Building.Buildings)
	p.Building.HasSpecialBuilding = p.Building.SpecialBuilding != ""
	p.Building.HasSpecialSlot = p.Building.SpecialBuildingSlot != "" || p.Building.SpecialBuilding != ""
	p.Building.SlotStatus = buildingSlotStatus(p.Building)
	p.Building.Flags = buildingFlags(p.Building)
	if p.County != "" {
		p.Holder, _ = db.resolveEffectiveTitleHolder(ctx, p.County, date)
	}
	p.GeographyTags, _ = db.mapGeographyTags(ctx, p)
	p.HolySites, _ = db.mapHolySitesForProvince(ctx, pid)
	p.Regions, _ = db.mapRegionsForProvince(ctx, pid)
	p.Names = db.mapProvinceNames(ctx, p)
	return p, nil
}

func (db *DB) mapNeighborRows(ctx context.Context, pid int, date int, limit int) ([]MapNeighborRow, error) {
	origin, err := db.mapProvinceAt(ctx, pid, date)
	if err != nil {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT neighbor_id,border_len,blocked FROM map_adjacencies WHERE province_id=? ORDER BY border_len DESC, neighbor_id LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapNeighborRow
	for rows.Next() {
		var n MapNeighborRow
		var blocked int
		if err := rows.Scan(&n.ProvinceID, &n.BorderLen, &blocked); err != nil {
			return nil, err
		}
		n.Blocked = blocked != 0
		n.FromProvinceID = pid
		p, err := db.mapProvinceAt(ctx, n.ProvinceID, date)
		if err != nil {
			return nil, err
		}
		n.County, n.Terrain, n.Culture, n.Religion, n.Holder = p.County, p.Terrain, p.Culture, p.Religion, p.Holder
		n.BlockKind, n.WaterKind, n.GeographyTags = p.BlockKind, p.WaterKind, p.GeographyTags
		n.AdjacencyKind, n.EdgeWaterKind = mapAdjacencyKind(origin, p, n.Blocked)
		setNeighborSpatial(&n, origin.Center, p.Center)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (db *DB) resolveProvinceField(ctx context.Context, pid int, county, field string, date int, allowCountyFallback bool) (string, error) {
	var v string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM map_province_history WHERE province_id=? AND field=? AND date_key<=? ORDER BY date_key DESC LIMIT 1`, pid, field, date).Scan(&v)
	if err != sql.ErrNoRows {
		return v, err
	}
	if !allowCountyFallback || county == "" {
		return "", nil
	}
	var capital int
	err = db.sql.QueryRowContext(ctx, `SELECT province_id FROM map_provinces WHERE county=? AND blocked=0 ORDER BY is_county_capital DESC, province_id LIMIT 1`, county).Scan(&capital)
	if err != nil || capital == pid {
		return "", nil
	}
	return db.resolveProvinceField(ctx, capital, county, field, date, false)
}

func (db *DB) resolveTitleField(ctx context.Context, title, field string, date int) (string, error) {
	var v string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM map_title_history WHERE title_id=? AND field=? AND date_key<=? ORDER BY date_key DESC LIMIT 1`, title, field, date).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (db *DB) resolveEffectiveTitleHolder(ctx context.Context, title string, date int) (string, error) {
	seen := map[string]bool{}
	for title != "" && !seen[title] {
		seen[title] = true
		holder, err := db.resolveTitleField(ctx, title, "holder", date)
		if err != nil {
			return "", err
		}
		if isValidMapHolder(holder) {
			return holder, nil
		}
		var parent sql.NullString
		err = db.sql.QueryRowContext(ctx, `SELECT parent_id FROM map_titles WHERE title_id=?`, title).Scan(&parent)
		if err == sql.ErrNoRows {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		title = parent.String
	}
	return "", nil
}

func (db *DB) titleProvinceIDs(ctx context.Context, title string) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id FROM map_title_provinces WHERE title_id=? ORDER BY province_id`, title)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		out = append(out, pid)
	}
	return out, rows.Err()
}

func (db *DB) targetProvinceIDs(ctx context.Context, target string) ([]int, error) {
	selector, err := parseMapTargetSelector(target, "")
	if err != nil {
		return nil, err
	}
	pids, err := db.mapTargetProvinceIDs(ctx, selector)
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("target %s has no mapped provinces", target)
	}
	return pids, nil
}

func (db *DB) mapTitleRow(ctx context.Context, title string, date int) (MapTitleRow, error) {
	var t MapTitleRow
	err := db.sql.QueryRowContext(ctx, `SELECT title_id,title_type,COALESCE(parent_id,''),COALESCE(capital_title,''),province_count,COALESCE(center_x,0),COALESCE(center_y,0),COALESCE(min_x,0),COALESCE(min_y,0),COALESCE(max_x,0),COALESCE(max_y,0) FROM map_titles WHERE title_id=?`, title).
		Scan(&t.TitleID, &t.Type, &t.ParentID, &t.CapitalTitle, &t.ProvinceCount, &t.Center.X, &t.Center.Y, &t.BBox.MinX, &t.BBox.MinY, &t.BBox.MaxX, &t.BBox.MaxY)
	if err != nil {
		return MapTitleRow{}, err
	}
	t.Holder, _ = db.resolveEffectiveTitleHolder(ctx, title, date)
	t.Name = db.localizedName(ctx, title)
	return t, nil
}

func (db *DB) neighborTitleBorders(ctx context.Context, provinceSet map[int]bool, date int, limit int) ([]MapTitleBorder, error) {
	border := map[string]int{}
	for pid := range provinceSet {
		rows, err := db.sql.QueryContext(ctx, `SELECT neighbor_id,border_len FROM map_adjacencies WHERE province_id=?`, pid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var nid, bl int
			if err := rows.Scan(&nid, &bl); err != nil {
				rows.Close()
				return nil, err
			}
			if provinceSet[nid] {
				continue
			}
			p, err := db.mapProvinceAt(ctx, nid, date)
			if err != nil {
				rows.Close()
				return nil, err
			}
			title := p.County
			if title != "" {
				border[title] += bl
			}
		}
		rows.Close()
	}
	var out []MapTitleBorder
	for title, bl := range border {
		holder, _ := db.resolveEffectiveTitleHolder(ctx, title, date)
		out = append(out, MapTitleBorder{TitleID: title, BorderLen: bl, Holder: holder})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BorderLen > out[j].BorderLen })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func splitMapList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.Fields(v)
}

func buildingSlotStatus(b MapBuildingState) string {
	switch {
	case b.SpecialBuilding != "":
		return "occupied_special_slot"
	case b.SpecialBuildingSlot != "":
		return "empty_special_slot"
	default:
		return "no_known_special_slot"
	}
}

func buildingFlags(b MapBuildingState) []string {
	var flags []string
	holding := strings.ToLower(b.HoldingType)
	switch {
	case strings.Contains(holding, "ruin"):
		flags = append(flags, "ruin_holding")
	case strings.Contains(holding, "wasteland") || strings.Contains(holding, "empty") || holding == "none":
		flags = append(flags, "placeholder_holding")
	}
	for _, id := range append([]string{b.SpecialBuilding, b.SpecialBuildingSlot, b.DuchyBuilding}, b.Buildings...) {
		lower := strings.ToLower(id)
		if strings.Contains(lower, "ruin") && !containsString(flags, "ruin_building") {
			flags = append(flags, "ruin_building")
		}
		if (strings.Contains(lower, "placeholder") || strings.Contains(lower, "empty")) && !containsString(flags, "placeholder_building") {
			flags = append(flags, "placeholder_building")
		}
	}
	return flags
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func (db *DB) localizedName(ctx context.Context, key string) MapLocalizedName {
	name := MapLocalizedName{Key: key}
	if key == "" {
		return name
	}
	q, err := db.QueryLocalization(ctx, key)
	if err != nil {
		return name
	}
	for _, h := range q.Values {
		lang := strings.ToLower(h.Language)
		switch {
		case name.Chinese == "" && (strings.Contains(lang, "simp_chinese") || lang == "chinese" || strings.Contains(lang, "zh")):
			name.Chinese = h.Value
		case name.English == "" && strings.Contains(lang, "english"):
			name.English = h.Value
		}
	}
	return name
}

func (db *DB) mapProvinceNames(ctx context.Context, p MapProvinceRow) map[string]MapLocalizedName {
	out := map[string]MapLocalizedName{}
	for kind, key := range map[string]string{
		"barony": p.Barony, "county": p.County, "duchy": p.Duchy, "kingdom": p.Kingdom, "empire": p.Empire,
	} {
		if key == "" {
			continue
		}
		name := db.localizedName(ctx, key)
		if name.English != "" || name.Chinese != "" {
			out[kind] = name
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (db *DB) mapHolySitesForProvince(ctx context.Context, pid int) ([]MapHolySiteRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT holy_site_id,COALESCE(county,''),COALESCE(barony,''),COALESCE(province_id,0) FROM map_holy_sites WHERE province_id=? ORDER BY holy_site_id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapHolySiteRow
	for rows.Next() {
		var h MapHolySiteRow
		if err := rows.Scan(&h.ID, &h.County, &h.Barony, &h.ProvinceID); err != nil {
			return nil, err
		}
		h.Faiths, _ = db.faithsForHolySite(ctx, h.ID)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (db *DB) faithsForHolySite(ctx context.Context, site string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT faith_id FROM map_holy_site_faiths WHERE holy_site_id=? ORDER BY faith_id`, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var faith string
		if err := rows.Scan(&faith); err != nil {
			return nil, err
		}
		out = append(out, faith)
	}
	return out, rows.Err()
}

func (db *DB) mapRegionsForProvince(ctx context.Context, pid int) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT region_id FROM map_province_regions WHERE province_id=? ORDER BY region_id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			return nil, err
		}
		out = append(out, region)
	}
	return out, rows.Err()
}

func (db *DB) mapGeographyTags(ctx context.Context, p MapProvinceRow) ([]string, error) {
	tags := map[string]bool{}
	if p.Blocked {
		if p.BlockKind != "" {
			tags[p.BlockKind] = true
		}
		if p.WaterKind != "" {
			tags[p.WaterKind] = true
		}
		return sortedTagKeys(tags), nil
	}
	if terrainIsMountain(p.Terrain) {
		tags["mountainous"] = true
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT mp.blocked,COALESCE(mp.block_kind,''),COALESCE(mp.water_kind,''),COALESCE(mp.terrain,''),COALESCE(mp.county,''),a.border_len
		FROM map_adjacencies a JOIN map_provinces mp ON mp.province_id=a.neighbor_id
		WHERE a.province_id=?`, p.ProvinceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	landNeighbors, waterNeighbors, mountainBlocks, borderNeighbors := 0, 0, 0, 0
	for rows.Next() {
		var blocked, borderLen int
		var blockKind, waterKind, terrain, county string
		if err := rows.Scan(&blocked, &blockKind, &waterKind, &terrain, &county, &borderLen); err != nil {
			return nil, err
		}
		if blocked != 0 {
			switch waterKind {
			case "sea", "coastal_sea", "impassable_sea":
				tags["coastal"] = true
				waterNeighbors++
			case "lake":
				tags["lakeside"] = true
				waterNeighbors++
			case "river":
				tags["riverside"] = true
				waterNeighbors++
			}
			if blockKind == "impassable_mountain" {
				tags["near_impassable_mountains"] = true
				mountainBlocks++
			}
			continue
		}
		landNeighbors++
		if county != "" && p.County != "" && county != p.County {
			borderNeighbors++
		}
		if terrainIsMountain(terrain) {
			tags["near_mountains"] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if waterNeighbors > 0 && landNeighbors == 0 {
		tags["island_like"] = true
	}
	if waterNeighbors > 0 && landNeighbors <= 2 {
		tags["peninsula_like"] = true
	}
	if (terrainIsMountain(p.Terrain) || mountainBlocks > 0) && landNeighbors >= 2 {
		tags["mountain_pass_candidate"] = true
	}
	if landNeighbors <= 2 && (waterNeighbors > 0 || mountainBlocks > 0) {
		tags["chokepoint_candidate"] = true
	}
	if waterNeighbors == 0 && strings.Contains(strings.ToLower(p.Terrain), "desert") && mountainBlocks > 0 {
		tags["inland_basin_candidate"] = true
	}
	if borderNeighbors > 0 {
		tags["county_border"] = true
	}
	return sortedTagKeys(tags), nil
}

func terrainIsMountain(terrain string) bool {
	t := strings.ToLower(terrain)
	return strings.Contains(t, "mountain") || strings.Contains(t, "hills")
}

func sortedTagKeys(tags map[string]bool) []string {
	out := make([]string, 0, len(tags))
	for tag := range tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func (db *DB) LLMMapBuildingCandidates(ctx context.Context, target string, year int, opts LLMOptions) (MapBuildingCandidatesResult, error) {
	if year <= 0 {
		year = 1
	}
	provinces, err := db.targetProvinceIDs(ctx, target)
	if err != nil {
		return MapBuildingCandidatesResult{}, err
	}
	date := yearDateKey(year)
	limit := opts.normalizedLimit()
	var candidates []MapBuildingCandidate
	var visualPoints []MapVisualPoint
	visual := MapVisualSummary{BBox: MapBox{MinX: int(^uint(0) >> 1), MinY: int(^uint(0) >> 1)}}
	for _, pid := range provinces {
		p, err := db.mapProvinceAt(ctx, pid, date)
		if err != nil {
			return MapBuildingCandidatesResult{}, err
		}
		if p.ProvinceID == 0 {
			continue
		}
		visual = expandVisualSummary(visual, p)
		if len(visualPoints) < limit {
			visualPoints = append(visualPoints, MapVisualPoint{ProvinceID: p.ProvinceID, TitleID: p.Barony, Center: p.Center})
		}
		if p.Blocked {
			continue
		}
		c, err := db.scoreBuildingCandidate(ctx, p, date)
		if err != nil {
			return MapBuildingCandidatesResult{}, err
		}
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Province.ProvinceID < candidates[j].Province.ProvinceID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	visual.Points = visualPoints
	if visual.BBox.MinX == int(^uint(0)>>1) {
		visual.BBox = MapBox{}
	}
	return MapBuildingCandidatesResult{
		Intent:     "map_building_candidates",
		Query:      target,
		Year:       year,
		Summary:    fmt.Sprintf("Ranked %d non-blocked province candidate(s) for %s.", len(candidates), target),
		Candidates: candidates,
		Visual:     visual,
		Guidance:   []string{"Scores are heuristics for review, not automatic edit decisions.", "Existing special buildings and blocked provinces are deliberately penalized or skipped.", "Use preflight_patch before applying any generated building edits."},
	}, nil
}

func expandVisualSummary(v MapVisualSummary, p MapProvinceRow) MapVisualSummary {
	if v.BBox.MinX > p.BBox.MinX {
		v.BBox.MinX = p.BBox.MinX
	}
	if v.BBox.MinY > p.BBox.MinY {
		v.BBox.MinY = p.BBox.MinY
	}
	if v.BBox.MaxX < p.BBox.MaxX {
		v.BBox.MaxX = p.BBox.MaxX
	}
	if v.BBox.MaxY < p.BBox.MaxY {
		v.BBox.MaxY = p.BBox.MaxY
	}
	v.Center.X = (float64(v.BBox.MinX) + float64(v.BBox.MaxX)) / 2
	v.Center.Y = (float64(v.BBox.MinY) + float64(v.BBox.MaxY)) / 2
	return v
}

func (db *DB) scoreBuildingCandidate(ctx context.Context, p MapProvinceRow, date int) (MapBuildingCandidate, error) {
	c := MapBuildingCandidate{Province: p}
	if region := db.coarseMapRegion(ctx, p.Center); region != "" {
		c.Reasons = append(c.Reasons, "coarse map region: "+region)
	}
	score := 50
	if p.Building.SlotStatus == "empty_special_slot" {
		score += 30
		c.EmptySpecialSlot = true
		c.Reasons = append(c.Reasons, "has an empty known special_building_slot")
	}
	if p.Building.SpecialBuilding != "" {
		score -= 60
		c.Warnings = append(c.Warnings, "already has special_building")
	}
	if p.Building.IsCountyCapital {
		score += 12
		c.Reasons = append(c.Reasons, "county capital")
	}
	switch {
	case strings.Contains(p.Building.HoldingType, "city"):
		score += 10
		c.Reasons = append(c.Reasons, "city holding")
	case strings.Contains(p.Building.HoldingType, "castle"):
		score += 6
		c.Reasons = append(c.Reasons, "castle holding")
	case strings.Contains(p.Building.HoldingType, "temple") || strings.Contains(p.Building.HoldingType, "church"):
		score += 6
		c.Reasons = append(c.Reasons, "temple/church holding")
	case containsString(p.Building.Flags, "ruin_holding"):
		score -= 12
		c.Warnings = append(c.Warnings, "ruin holding")
	case containsString(p.Building.Flags, "placeholder_holding"):
		score -= 20
		c.Warnings = append(c.Warnings, "placeholder or empty holding")
	}
	if containsString(p.GeographyTags, "coastal") {
		score += 8
		c.Reasons = append(c.Reasons, "coastal")
	}
	if containsString(p.GeographyTags, "riverside") || containsString(p.GeographyTags, "lakeside") {
		score += 5
		c.Reasons = append(c.Reasons, "riverside/lakeside")
	}
	if containsString(p.GeographyTags, "mountain_pass_candidate") || containsString(p.GeographyTags, "chokepoint_candidate") {
		score += 8
		c.Reasons = append(c.Reasons, "pass or chokepoint candidate")
	}
	c.DistanceToWater, _ = db.distanceToWater(ctx, p.ProvinceID, 5)
	if c.DistanceToWater == 0 {
		score += 4
	} else if c.DistanceToWater > 0 && c.DistanceToWater <= 2 {
		score += 2
	}
	c.NearbySameCulture, _ = db.nearbySameCulture(ctx, p.ProvinceID, p.Culture, date, 2)
	score += c.NearbySameCulture * 2
	if c.NearbySameCulture > 0 {
		c.Reasons = append(c.Reasons, fmt.Sprintf("%d same-culture province(s) nearby", c.NearbySameCulture))
	}
	c.NearbySpecialBuildings, _ = db.nearbySpecialBuildings(ctx, p.ProvinceID, date, 3)
	if c.NearbySpecialBuildings > 0 {
		score -= c.NearbySpecialBuildings * 8
		c.Warnings = append(c.Warnings, fmt.Sprintf("%d nearby special building(s)", c.NearbySpecialBuildings))
	}
	c.Border, _ = db.isBorderProvince(ctx, p, date)
	if c.Border {
		score += 4
		c.Reasons = append(c.Reasons, "county/culture/religion/holder border")
	}
	if score < 0 {
		score = 0
	}
	c.Score = score
	return c, nil
}

func (db *DB) distanceToWater(ctx context.Context, start int, maxRadius int) (int, error) {
	seen := map[int]bool{start: true}
	frontier := map[int]bool{start: true}
	for depth := 0; depth <= maxRadius; depth++ {
		next := map[int]bool{}
		for pid := range frontier {
			rows, err := db.sql.QueryContext(ctx, `SELECT mp.province_id,mp.blocked,COALESCE(mp.block_kind,''),COALESCE(mp.water_kind,'')
				FROM map_adjacencies a JOIN map_provinces mp ON mp.province_id=a.neighbor_id
				WHERE a.province_id=?`, pid)
			if err != nil {
				return -1, err
			}
			for rows.Next() {
				var nid, blocked int
				var blockKind, waterKind string
				if err := rows.Scan(&nid, &blocked, &blockKind, &waterKind); err != nil {
					rows.Close()
					return -1, err
				}
				if blocked != 0 && blockKind == "water" {
					rows.Close()
					return depth, nil
				}
				if !seen[nid] {
					seen[nid] = true
					next[nid] = true
				}
			}
			rows.Close()
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return -1, nil
}

func (db *DB) nearbySameCulture(ctx context.Context, start int, culture string, date int, radius int) (int, error) {
	if culture == "" {
		return 0, nil
	}
	pids, err := db.provincesWithinRadius(ctx, start, radius)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, pid := range pids {
		p, err := db.mapProvinceAt(ctx, pid, date)
		if err != nil {
			return 0, err
		}
		if !p.Blocked && p.Culture == culture {
			count++
		}
	}
	return count, nil
}

func (db *DB) nearbySpecialBuildings(ctx context.Context, start int, date int, radius int) (int, error) {
	pids, err := db.provincesWithinRadius(ctx, start, radius)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, pid := range pids {
		v, err := db.resolveProvinceField(ctx, pid, "", "special_building", date, false)
		if err != nil {
			return 0, err
		}
		if v != "" {
			count++
		}
	}
	return count, nil
}

func (db *DB) isBorderProvince(ctx context.Context, p MapProvinceRow, date int) (bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT neighbor_id FROM map_adjacencies WHERE province_id=?`, p.ProvinceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid int
		if err := rows.Scan(&nid); err != nil {
			return false, err
		}
		n, err := db.mapProvinceAt(ctx, nid, date)
		if err != nil {
			return false, err
		}
		if n.Blocked {
			continue
		}
		if (p.County != "" && n.County != "" && p.County != n.County) ||
			(p.Culture != "" && n.Culture != "" && p.Culture != n.Culture) ||
			(p.Religion != "" && n.Religion != "" && p.Religion != n.Religion) ||
			(p.Holder != "" && n.Holder != "" && p.Holder != n.Holder) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (db *DB) provincesWithinRadius(ctx context.Context, start int, radius int) ([]int, error) {
	seen := map[int]bool{start: true}
	frontier := map[int]bool{start: true}
	var out []int
	for depth := 1; depth <= radius; depth++ {
		next := map[int]bool{}
		for pid := range frontier {
			rows, err := db.sql.QueryContext(ctx, `SELECT neighbor_id FROM map_adjacencies WHERE province_id=?`, pid)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var nid int
				if err := rows.Scan(&nid); err != nil {
					rows.Close()
					return nil, err
				}
				if seen[nid] {
					continue
				}
				seen[nid] = true
				next[nid] = true
				out = append(out, nid)
			}
			rows.Close()
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	sort.Ints(out)
	return out, nil
}

func (db *DB) religionAssignmentPreview(ctx context.Context, provinces []int, year, date, limit int) ([]MapRecommendation, PatchFileInput, error) {
	var recs []MapRecommendation
	var b strings.Builder
	b.WriteString("# Generated preview by ck3-index map_assignment_plan. Review before applying.\n\n")
	changes := 0
	for _, pid := range provinces {
		if len(recs) >= limit {
			break
		}
		p, err := db.mapProvinceAt(ctx, pid, date)
		if err != nil {
			return nil, PatchFileInput{}, err
		}
		if p.Blocked || p.Religion != "" {
			continue
		}
		faith, weight, evidence, err := db.bestNeighborReligion(ctx, pid, p.Culture, date)
		if err != nil {
			return nil, PatchFileInput{}, err
		}
		if faith == "" {
			continue
		}
		conf := mathConfidence(weight)
		recs = append(recs, MapRecommendation{Kind: "religion", Target: strconv.Itoa(pid), Value: faith, Confidence: conf, Evidence: evidence})
		fmt.Fprintf(&b, "%d = {\n\t%d.1.1 = { religion = %s }\n}\n\n", pid, year, faith)
		changes++
	}
	if changes == 0 {
		return recs, PatchFileInput{}, nil
	}
	return recs, PatchFileInput{Path: "history/provinces/zz_map_context_generated_provinces.txt", Content: b.String()}, nil
}

func (db *DB) bestNeighborReligion(ctx context.Context, pid int, culture string, date int) (string, int, []string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT neighbor_id,border_len,blocked FROM map_adjacencies WHERE province_id=? ORDER BY border_len DESC`, pid)
	if err != nil {
		return "", 0, nil, err
	}
	defer rows.Close()
	weights := map[string]int{}
	for rows.Next() {
		var nid, bl, blocked int
		if err := rows.Scan(&nid, &bl, &blocked); err != nil {
			return "", 0, nil, err
		}
		if blocked != 0 {
			continue
		}
		n, err := db.mapProvinceAt(ctx, nid, date)
		if err != nil {
			return "", 0, nil, err
		}
		if n.Religion == "" || n.Blocked {
			continue
		}
		w := bl
		if culture != "" && n.Culture == culture {
			w *= 2
		}
		weights[n.Religion] += w
	}
	best, weight := "", 0
	for k, v := range weights {
		if v > weight {
			best, weight = k, v
		}
	}
	if best == "" {
		return "", 0, nil, nil
	}
	return best, weight, []string{fmt.Sprintf("best neighboring faith by weighted border: %s=%d", best, weight)}, nil
}

func (db *DB) characterAssignmentPreview(ctx context.Context, provinces []int, year, date, limit int) ([]MapRecommendation, []PatchFileInput, error) {
	counties := map[string]int{}
	for _, pid := range provinces {
		p, err := db.mapProvinceAt(ctx, pid, date)
		if err != nil {
			return nil, nil, err
		}
		if p.Blocked || p.County == "" {
			continue
		}
		if _, ok := counties[p.County]; !ok {
			counties[p.County] = pid
		}
	}
	var recs []MapRecommendation
	var chars, titles strings.Builder
	chars.WriteString("# Generated preview by ck3-index map_assignment_plan. Review before applying.\n\n")
	titles.WriteString("# Generated preview by ck3-index map_assignment_plan. Review before applying.\n\n")
	keys := make([]string, 0, len(counties))
	for c := range counties {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, county := range keys {
		if len(recs) >= limit {
			break
		}
		holder, err := db.resolveEffectiveTitleHolder(ctx, county, date)
		if err != nil {
			return nil, nil, err
		}
		if isValidMapHolder(holder) {
			continue
		}
		p, err := db.mapProvinceAt(ctx, counties[county], date)
		if err != nil {
			return nil, nil, err
		}
		culture, religion := p.Culture, p.Religion
		if religion == "" {
			religion, _, _, _ = db.bestNeighborReligion(ctx, p.ProvinceID, culture, date)
		}
		if culture == "" {
			culture = "unknown_culture"
		}
		if religion == "" {
			religion = "unknown_faith"
		}
		charID := "map_context_" + strings.NewReplacer("-", "_").Replace(county) + "_" + strconv.Itoa(year)
		name := "Generated " + county
		birthYear := year - 30
		if birthYear < 1 {
			birthYear = 1
		}
		fmt.Fprintf(&chars, "%s = {\n\tname = \"%s\"\n\tculture = %s\n\treligion = %s\n\t%d.1.1 = { birth = yes }\n}\n\n", charID, name, culture, religion, birthYear)
		liegeLine := ""
		if liege, _ := db.inferredLiegeTitle(ctx, county, date); liege != "" {
			liegeLine = fmt.Sprintf("\n\t\tliege = %s", liege)
		}
		fmt.Fprintf(&titles, "%s = {\n\t%d.1.1 = {\n\t\tholder = %s%s\n\t}\n}\n\n", county, year, charID, liegeLine)
		recs = append(recs, MapRecommendation{Kind: "character", Target: county, Value: charID, Confidence: 0.75, Evidence: []string{"county has no effective holder", "culture and religion inferred from local province context"}})
	}
	var patches []PatchFileInput
	if len(recs) > 0 {
		patches = append(patches,
			PatchFileInput{Path: "history/characters/zz_map_context_generated_characters.txt", Content: chars.String()},
			PatchFileInput{Path: "history/titles/zz_map_context_generated_titles.txt", Content: titles.String()},
		)
	}
	return recs, patches, nil
}

func (db *DB) inferredLiegeTitle(ctx context.Context, county string, date int) (string, error) {
	liege, err := db.resolveTitleField(ctx, county, "liege", date)
	if err != nil || liege != "" {
		return liege, err
	}
	var parent sql.NullString
	err = db.sql.QueryRowContext(ctx, `SELECT parent_id FROM map_titles WHERE title_id=?`, county).Scan(&parent)
	if err == sql.ErrNoRows || !parent.Valid {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	holder, err := db.resolveEffectiveTitleHolder(ctx, parent.String, date)
	if err != nil {
		return "", err
	}
	if isValidMapHolder(holder) {
		return parent.String, nil
	}
	return "", nil
}

func mathConfidence(weight int) float64 {
	switch {
	case weight >= 20:
		return 0.9
	case weight >= 10:
		return 0.8
	case weight >= 5:
		return 0.65
	default:
		return 0.5
	}
}

func limitDepthRows(m map[int][]MapNeighborRow, limit int) {
	for d, rows := range m {
		sort.Slice(rows, func(i, j int) bool { return rows[i].BorderLen > rows[j].BorderLen })
		if len(rows) > limit {
			rows = rows[:limit]
		}
		m[d] = rows
	}
}

func topMapCounts(m map[string]int, limit int) []MapCount {
	out := make([]MapCount, 0, len(m))
	for k, v := range m {
		if k != "" {
			out = append(out, MapCount{ID: k, Count: v})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].ID < out[j].ID
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
