package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type MapMetricComponent struct {
	Field      string             `json:"field"`
	Weights    map[string]float64 `json:"weights,omitempty"`
	Default    float64            `json:"default,omitempty"`
	Multiplier float64            `json:"multiplier,omitempty"`
	Presence   bool               `json:"presence,omitempty"`
}

type MapGraphTransform struct {
	Operator          string    `json:"operator,omitempty"`
	Rounds            int       `json:"rounds,omitempty"`
	Rates             []float64 `json:"rates,omitempty"`
	Rate              float64   `json:"rate,omitempty"`
	EdgeWeight        string    `json:"edge_weight,omitempty"`
	Cap               float64   `json:"cap,omitempty"`
	Floor             *float64  `json:"floor,omitempty"`
	TerrainAbsorption bool      `json:"terrain_absorption,omitempty"`
	Seeds             []string  `json:"seeds,omitempty"`
	DistanceDecay     float64   `json:"distance_decay,omitempty"`
	OnlyHigherToLower bool      `json:"only_higher_to_lower,omitempty"`
}

type MapMetricValue struct {
	ID         string  `json:"id"`
	Value      float64 `json:"value,omitempty"`
	Category   string  `json:"category,omitempty"`
	Label      string  `json:"label,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

type MapMetricSpec struct {
	Recipe     string               `json:"recipe,omitempty"`
	Target     string               `json:"target,omitempty"`
	IDPrefix   string               `json:"id_prefix,omitempty"`
	IDPattern  string               `json:"id_pattern,omitempty"`
	Level      string               `json:"level,omitempty"`
	Year       int                  `json:"year,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Field      string               `json:"field,omitempty"`
	Aggregate  string               `json:"aggregate,omitempty"`
	MatchValue string               `json:"match_value,omitempty"`
	Components []MapMetricComponent `json:"components,omitempty"`
	Transform  MapGraphTransform    `json:"transform,omitempty"`
	Values     []MapMetricValue     `json:"values,omitempty"`
	SourceNote string               `json:"source_note,omitempty"`
	Provenance string               `json:"provenance,omitempty"`
}

type MapMetricStats struct {
	Count    int     `json:"count"`
	Missing  int     `json:"missing"`
	Minimum  float64 `json:"minimum,omitempty"`
	Maximum  float64 `json:"maximum,omitempty"`
	Mean     float64 `json:"mean,omitempty"`
	P10      float64 `json:"p10,omitempty"`
	Median   float64 `json:"median,omitempty"`
	P90      float64 `json:"p90,omitempty"`
	Coverage float64 `json:"coverage"`
}

type MapMetricResult struct {
	Intent          string              `json:"intent"`
	Recipe          string              `json:"recipe,omitempty"`
	Target          string              `json:"target"`
	Level           string              `json:"level"`
	Year            int                 `json:"year"`
	Kind            string              `json:"kind"`
	Provenance      string              `json:"provenance"`
	SourceNote      string              `json:"source_note"`
	Summary         string              `json:"summary"`
	Stats           MapMetricStats      `json:"stats"`
	Values          []MapMetricValue    `json:"values"`
	Categories      []MapCount          `json:"categories,omitempty"`
	Outliers        []MapMetricValue    `json:"outliers,omitempty"`
	Warnings        []string            `json:"warnings,omitempty"`
	IntegrityStatus string              `json:"integrity_status"`
	IntegrityIssues []MapIntegrityIssue `json:"integrity_warnings,omitempty"`
	RecipeSpec      *MapMetricSpec      `json:"resolved_recipe,omitempty"`
}

type MapRecipe struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Spec        MapMetricSpec  `json:"spec"`
	Layers      []string       `json:"suggested_layers"`
	RenderSpec  *MapRenderSpec `json:"render_spec,omitempty"`
}

type MapRecipeCatalogResult struct {
	Intent        string      `json:"intent"`
	Summary       string      `json:"summary"`
	Levels        []string    `json:"levels"`
	Aggregates    []string    `json:"aggregates"`
	Transforms    []string    `json:"transforms"`
	Fields        []string    `json:"fields"`
	LayerTypes    []string    `json:"layer_types"`
	MarkerSources []string    `json:"marker_sources"`
	FlowSources   []string    `json:"flow_sources"`
	Palettes      []string    `json:"palettes"`
	Styles        []string    `json:"styles"`
	Layouts       []string    `json:"layouts"`
	Themes        []string    `json:"themes"`
	RenderRecipes []string    `json:"render_recipes"`
	Recipes       []MapRecipe `json:"recipes"`
	Guidance      []string    `json:"guidance"`
}

var developmentHoldingWeights = map[string]float64{
	"great_city_holding": 8, "great_city_holding_e": 8, "great_city_holding_m": 8,
	"metro_holding": 6, "city_holding": 5, "church_holding": 4,
	"castle_holding": 3, "tribal_holding": 2, "none": 0.5,
}

var developmentTerrainWeights = map[string]float64{
	"farmlands": 2.5, "floodplains": 2, "oasis": 2, "plains": 1,
	"drylands": 0.25, "forest": 0, "steppe": 0, "taiga": -0.25,
	"hills": -0.25, "jungle": -0.5, "wetlands": -0.75,
	"desert": -1, "desert_mountains": -1.25, "mountains": -1.5,
}

var zeroMetricFloor = 0.0

func builtInMapRecipes() []MapRecipe {
	return []MapRecipe{
		{ID: "development_network", Name: "Development Network", Description: "Holding and terrain potential with capped high-to-low adjacency diffusion.", Layers: []string{"fill", "borders", "markers", "flows", "labels"}, Spec: MapMetricSpec{Level: "county", Kind: "numeric", Aggregate: "sum", Provenance: "derived", SourceNote: "Indexed holding and terrain facts with an explicit diffusion model.", Components: []MapMetricComponent{{Field: "holding", Weights: developmentHoldingWeights}, {Field: "terrain", Weights: developmentTerrainWeights}}, Transform: MapGraphTransform{Operator: "high_to_low", Rounds: 3, Rates: []float64{0.25, 0.10, 0.04}, EdgeWeight: "uniform", Cap: 50, Floor: &zeroMetricFloor, TerrainAbsorption: true}}},
		{ID: "cultural_frontier", Name: "Cultural Frontier", Description: "Dominant county culture; use a diversity metric as an optional second fill layer.", Layers: []string{"fill", "borders", "labels"}, Spec: MapMetricSpec{Level: "county", Kind: "category", Field: "culture", Aggregate: "majority", Provenance: "indexed", SourceNote: "Indexed province culture at the selected year."}},
		{ID: "faith_and_holy_sites", Name: "Faith and Holy Sites", Description: "Dominant faith with holy-site markers.", Layers: []string{"fill", "borders", "markers", "labels"}, Spec: MapMetricSpec{Level: "county", Kind: "category", Field: "religion", Aggregate: "majority", Provenance: "indexed", SourceNote: "Indexed province religion and holy sites at the selected year."}},
		{ID: "special_building_constellation", Name: "Special Building Constellation", Description: "Special-building counts with special-building markers.", Layers: []string{"fill", "borders", "markers"}, Spec: MapMetricSpec{Level: "county", Kind: "numeric", Aggregate: "sum", Provenance: "indexed", SourceNote: "Indexed special-building assignments at the selected year.", Components: []MapMetricComponent{{Field: "special_building", Presence: true}}}},
		{ID: "capital_gravity", Name: "Capital Gravity", Description: "Settlement weight around county capitals.", Layers: []string{"fill", "borders", "markers", "flows"}, Spec: MapMetricSpec{Level: "county", Kind: "numeric", Aggregate: "sum", Provenance: "derived", SourceNote: "Indexed holdings weighted by type, with county capitals shown as nodes.", Components: []MapMetricComponent{{Field: "holding", Weights: developmentHoldingWeights}, {Field: "is_county_capital", Presence: true, Multiplier: 3}}, Transform: MapGraphTransform{Operator: "neighbor_mean", Rounds: 1, Rate: 0.15, EdgeWeight: "border_len", Cap: 50}}},
		{ID: "settlement_terrain_gap", Name: "Settlement Terrain Gap", Description: "Terrain carrying potential minus realized holding intensity.", Layers: []string{"fill", "borders", "labels"}, Spec: MapMetricSpec{Level: "county", Kind: "numeric", Aggregate: "sum", Provenance: "derived", SourceNote: "Difference between indexed terrain potential and indexed holding intensity.", Components: []MapMetricComponent{{Field: "terrain", Weights: developmentTerrainWeights}, {Field: "holding", Weights: developmentHoldingWeights, Multiplier: -0.45}}}},
		{ID: "duchy_political_atlas", Name: "Duchy Political Atlas", Description: "A bilingual dark-sea historical atlas using coordinated native title colors, cached relief, vegetation, holdings, rivers, and political hierarchy.", Layers: []string{"fill", "borders", "markers", "labels"}, Spec: MapMetricSpec{Level: "duchy", Kind: "category", Field: "entity_id", Aggregate: "majority", Provenance: "indexed", SourceNote: "Indexed de jure duchies and landed-title colors."}, RenderSpec: &MapRenderSpec{Recipe: "duchy_political_atlas", Year: 1254, Style: "historical_atlas", Layout: "full_atlas", ReliefStrength: "subtle", LabelLanguage: "bilingual", ColorStrategy: "coordinated", Supersample: 2}},
		{ID: "political_atlas", Name: "Adaptive Political Atlas", Description: "Political atlas from barony through empire level with automatically composed higher-rank boundaries, vegetation, and dated holdings.", Layers: []string{"fill", "borders", "markers", "labels"}, Spec: MapMetricSpec{Kind: "category", Field: "entity_id", Aggregate: "majority", Provenance: "indexed"}, RenderSpec: &MapRenderSpec{Recipe: "political_atlas", Theme: "political", Level: "county", Year: 1254, HistoryYear: 6254, Style: "historical_atlas", Layout: "full_atlas", ReliefStrength: "subtle", LabelLanguage: "bilingual", ColorStrategy: "coordinated", Supersample: 2}},
		{ID: "thematic_atlas", Name: "Adaptive Thematic Atlas", Description: "Culture, faith, in-game development, terrain, or source-noted custom atlas with vegetation and dated holdings at a selected administrative level.", Layers: []string{"fill", "borders", "markers", "labels"}, Spec: MapMetricSpec{Level: "county", Provenance: "indexed"}, RenderSpec: &MapRenderSpec{Recipe: "thematic_atlas", Theme: "culture", Level: "county", Year: 1254, HistoryYear: 6254, Style: "historical_atlas", Layout: "full_atlas", ReliefStrength: "subtle", LabelLanguage: "bilingual", Supersample: 2}},
		{ID: "strategic_waterways_atlas", Name: "Strategic Waterways Atlas", Description: "Clean duchy political base with indexed lake bodies, straits, river crossings, sea routes, and portal-style underground or off-map gateways.", Layers: []string{"fill", "borders", "markers", "flows", "labels"}, Spec: MapMetricSpec{Level: "duchy", Kind: "category", Field: "entity_id", Aggregate: "majority", Provenance: "indexed", SourceNote: "Indexed de jure titles, connected lake bodies, and explicit adjacencies.csv passages."}, RenderSpec: &MapRenderSpec{Recipe: "strategic_waterways_atlas", Level: "duchy", Year: 1254, HistoryYear: 6254, Style: "historical_atlas", Layout: "full_atlas", ReliefStrength: "strong", LabelLanguage: "bilingual", ColorStrategy: "coordinated", Supersample: 2}},
		{ID: "elevation_relief", Name: "Relative Elevation", Description: "Normalized heightmap elevation aggregated by province or title; values are not metres.", Layers: []string{"fill", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Field: "elevation", Aggregate: "mean", Provenance: "derived", SourceNote: "Normalized active heightmap zonal statistics."}},
		{ID: "ridge_network", Name: "Ridges and Divides", Description: "Relative ridge strength from local curvature.", Layers: []string{"fill", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Field: "ridge_score", Aggregate: "mean", Provenance: "inferred", SourceNote: "Heightmap curvature inference; no real-world vertical scale is claimed."}},
		{ID: "watershed_flow", Name: "Watershed Flow", Description: "WhiteboxTools catchment pixels when verified full GIS analysis is cached.", Layers: []string{"fill", "flows", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Field: "catchment_area", Aggregate: "max", Provenance: "derived", SourceNote: "Verified D8 catchment pixel counts; unavailable when the GIS sidecar has not run."}},
		{ID: "composite_rivers", Name: "Composite River Network", Description: "Ordinary river pixels plus observed default.map major-river provinces.", Layers: []string{"fill", "flows", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Aggregate: "sum", Provenance: "derived", SourceNote: "rivers.png pixels and river_provinces remain separately auditable.", Components: []MapMetricComponent{{Field: "river_pixel_count"}, {Field: "major_river", Multiplier: 25}}}},
		{ID: "relative_bathymetry", Name: "Relative Bathymetry", Description: "Relative seabed depth below an estimated map-local water surface; never metres.", Layers: []string{"fill", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Field: "relative_depth", Aggregate: "mean", Provenance: "derived", SourceNote: "Heightmap depth relative to ocean or local-lake boundary reference."}},
		{ID: "continental_shelf", Name: "Continental Shelf", Description: "Inferred shallow, low-gradient seabed score.", Layers: []string{"fill", "borders"}, Spec: MapMetricSpec{Level: "province", Kind: "numeric", Field: "shelf_score", Aggregate: "mean", Provenance: "inferred", SourceNote: "Relative depth and seabed-gradient classification."}},
		{ID: "ocean_basins", Name: "Ocean Basins", Description: "Connected ocean and lake body identifiers derived from province topology.", Layers: []string{"fill", "borders", "labels"}, Spec: MapMetricSpec{Level: "province", Kind: "category", Field: "ocean_basin_id", Aggregate: "majority", Provenance: "derived", SourceNote: "Water province connected components; river_provinces are excluded."}},
	}
}

func MapRecipeCatalog() MapRecipeCatalogResult {
	return MapRecipeCatalogResult{
		Intent: "map_recipe_catalog", Summary: "Seventeen built-in metric and adaptive atlas recipes, including physical geography, composite rivers, and relative bathymetry.",
		Levels:        []string{"province", "barony", "county", "duchy", "kingdom", "empire", "region"},
		Aggregates:    []string{"count", "sum", "mean", "max", "majority", "diversity", "ratio"},
		Transforms:    []string{"high_to_low", "neighbor_mean", "distance_decay"},
		Fields:        []string{"entity_id", "holding", "terrain", "surface_material", "surface_material_diversity", "culture", "religion", "development", "special_building", "building_count", "is_county_capital", "area", "elevation", "slope", "ruggedness", "ridge_score", "flow_accumulation", "catchment_area", "river_order", "river_pixel_count", "major_river", "relative_depth", "seabed_slope", "seabed_ruggedness", "shelf_score", "trench_score", "coastal_dropoff", "strait_sill_depth", "ocean_basin_id"},
		LayerTypes:    []string{"fill", "borders", "markers", "flows", "labels"},
		MarkerSources: []string{"capitals", "holy_sites", "special_buildings", "vegetation", "holdings", "lakes", "strategic_portals"},
		FlowSources:   []string{"metric", "strategic_passages", "custom_edges"},
		Palettes:      []string{"political", "political_muted", "political_coordinated", "development", "viridis", "magma", "blue_red", "categorical20", "parchment"},
		Styles:        []string{"standard", "historical_atlas"},
		Layouts:       []string{"map_only", "light_frame", "full_atlas"},
		Themes:        []string{"political", "culture", "faith", "development", "terrain", "custom"},
		RenderRecipes: []string{"political_atlas", "thematic_atlas", "duchy_political_atlas", "strategic_waterways_atlas"},
		Recipes:       builtInMapRecipes(),
		Guidance:      []string{"Omit width and height for automatic 2K-, 4K-, or 8K-class sizing based on province coverage, label granularity, detail layers, relief, and layout; explicit dimensions remain an override.", "Use political_atlas with level=barony..empire for adaptive political maps.", "Use thematic_atlas with theme=culture, faith, development, or terrain; boundary_levels is optional and defaults to all higher title ranks.", "Use strategic_waterways_atlas to display lakes and adjacencies.csv without drawing underground or off-map gateways as full straight lines.", "Physical height, slope, depth, flow, and width values use normalized height or pixels; never describe them as metres or cubic metres per second.", "river_provinces are observed major river channels and remain excluded from ocean bathymetry.", "Custom values require source_note and are visibly marked as model supplied.", "Use map_build_metric before map_render when you need to inspect outliers or tune a formula."},
	}
}

func resolveMapRecipe(spec MapMetricSpec) (MapMetricSpec, error) {
	if spec.Recipe == "" {
		return spec, nil
	}
	if spec.Recipe == "political_atlas" || spec.Recipe == "thematic_atlas" || spec.Recipe == "strategic_waterways_atlas" {
		return spec, fmt.Errorf("map recipe %q is render-only; use it with map_render", spec.Recipe)
	}
	for _, recipe := range builtInMapRecipes() {
		if recipe.ID != spec.Recipe {
			continue
		}
		resolved := recipe.Spec
		resolved.Recipe = recipe.ID
		if spec.Target != "" {
			resolved.Target = spec.Target
		}
		if spec.IDPrefix != "" {
			resolved.IDPrefix = spec.IDPrefix
		}
		if spec.IDPattern != "" {
			resolved.IDPattern = spec.IDPattern
		}
		if spec.Level != "" {
			resolved.Level = spec.Level
		}
		if spec.Year > 0 {
			resolved.Year = spec.Year
		}
		if spec.SourceNote != "" {
			resolved.SourceNote = spec.SourceNote
		}
		if len(spec.Values) > 0 {
			resolved.Values = spec.Values
		}
		return resolved, nil
	}
	return spec, fmt.Errorf("unknown map recipe %q", spec.Recipe)
}

func normalizeMapLevel(level string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "province", "p":
		return "province", nil
	case "region", "geographical_region", "governorate", "governate":
		return "region", nil
	case "barony", "b":
		return "barony", nil
	case "county", "c":
		return "county", nil
	case "duchy", "d":
		return "duchy", nil
	case "kingdom", "k":
		return "kingdom", nil
	case "empire", "e":
		return "empire", nil
	default:
		return "", fmt.Errorf("unsupported map level %q", level)
	}
}

func mapLevelColumn(level string) string {
	switch level {
	case "barony":
		return "barony"
	case "county":
		return "county"
	case "duchy":
		return "duchy"
	case "kingdom":
		return "kingdom"
	case "empire":
		return "empire"
	}
	return ""
}

func (db *DB) LLMMapBuildMetric(ctx context.Context, input MapMetricSpec, opts LLMOptions) (MapMetricResult, error) {
	spec, err := resolveMapRecipe(input)
	if err != nil {
		return MapMetricResult{}, err
	}
	level, err := normalizeMapLevel(spec.Level)
	if err != nil {
		return MapMetricResult{}, err
	}
	spec.Level = level
	if spec.Year <= 0 {
		spec.Year = 1
	}
	if spec.Target == "" {
		spec.Target = "all"
	}
	if spec.Kind == "" {
		spec.Kind = "numeric"
	}
	if spec.Provenance == "" {
		spec.Provenance = "derived"
	}
	if len(spec.Values) > 0 {
		spec.Provenance = "model"
		if strings.TrimSpace(spec.SourceNote) == "" {
			return MapMetricResult{}, fmt.Errorf("custom map values require source_note")
		}
	}
	entities, entityProvinces, err := db.mapMetricEntitiesWithBlocked(ctx, spec.Target, level, mapMetricUsesPhysicalFields(spec))
	if err != nil {
		return MapMetricResult{}, err
	}
	if len(entities) == 0 {
		return MapMetricResult{}, fmt.Errorf("target %q selected no %s entities", spec.Target, level)
	}
	var idPattern *regexp.Regexp
	if spec.IDPattern != "" {
		if len(spec.IDPattern) > 128 {
			return MapMetricResult{}, fmt.Errorf("id_pattern is too long")
		}
		idPattern, err = regexp.Compile(spec.IDPattern)
		if err != nil {
			return MapMetricResult{}, fmt.Errorf("invalid id_pattern: %w", err)
		}
	}
	if spec.IDPrefix != "" || idPattern != nil {
		filtered := entities[:0]
		for _, id := range entities {
			prefixOK := spec.IDPrefix == "" || strings.HasPrefix(id, spec.IDPrefix)
			patternOK := idPattern == nil || idPattern.MatchString(id)
			if prefixOK && patternOK {
				filtered = append(filtered, id)
			} else {
				delete(entityProvinces, id)
			}
		}
		entities = filtered
		if len(entities) == 0 {
			return MapMetricResult{}, fmt.Errorf("id filters removed every selected entity")
		}
	}

	values := map[string]MapMetricValue{}
	warnings := []string{}
	if len(spec.Values) > 0 {
		known := map[string]bool{}
		for _, id := range entities {
			known[id] = true
		}
		for _, item := range spec.Values {
			if !known[item.ID] {
				return MapMetricResult{}, fmt.Errorf("unknown or out-of-target map id %q", item.ID)
			}
			if _, exists := values[item.ID]; exists {
				return MapMetricResult{}, fmt.Errorf("duplicate custom map id %q", item.ID)
			}
			if math.IsNaN(item.Value) || math.IsInf(item.Value, 0) {
				return MapMetricResult{}, fmt.Errorf("non-finite custom value for %q", item.ID)
			}
			values[item.ID] = item
		}
	} else {
		date := yearDateKey(spec.Year)
		for _, id := range entities {
			pids := entityProvinces[id]
			item, ok, err := db.calculateMapMetricEntity(ctx, id, pids, date, spec)
			if err != nil {
				return MapMetricResult{}, err
			}
			if ok {
				values[id] = item
			}
		}
	}
	if spec.Kind == "numeric" && spec.Transform.Operator != "" {
		values, err = db.transformMapMetric(ctx, level, values, entityProvinces, spec.Transform)
		if err != nil {
			return MapMetricResult{}, err
		}
	}
	ordered := make([]MapMetricValue, 0, len(values))
	for _, id := range entities {
		if item, ok := values[id]; ok {
			ordered = append(ordered, item)
		}
	}
	stats, categories := mapMetricStats(ordered, len(entities), spec.Kind)
	outliers := mapMetricOutliers(ordered, spec.Kind, opts.normalizedLimit())
	provinceSet := map[int]bool{}
	for _, pids := range entityProvinces {
		for _, pid := range pids {
			provinceSet[pid] = true
		}
	}
	integrityIssues, err := db.mapIntegrityIssues(ctx, "", provinceSet)
	if err != nil {
		return MapMetricResult{}, err
	}
	integrityStatus := "ok"
	if len(integrityIssues) > 0 {
		integrityStatus = "warning"
		warnings = append(warnings, integrityMessages(integrityIssues)...)
	}
	result := MapMetricResult{
		Intent: "map_build_metric", Recipe: spec.Recipe, Target: spec.Target, Level: level, Year: spec.Year,
		Kind: spec.Kind, Provenance: spec.Provenance, SourceNote: spec.SourceNote, Stats: stats,
		Values: ordered, Categories: categories, Outliers: outliers, Warnings: warnings, RecipeSpec: &spec,
		IntegrityStatus: integrityStatus, IntegrityIssues: integrityIssues,
	}
	result.Summary = fmt.Sprintf("Built %s metric for %d/%d %s entities; provenance=%s.", spec.Kind, len(ordered), len(entities), level, spec.Provenance)
	return result, nil
}

func (db *DB) mapMetricEntities(ctx context.Context, target, level string) ([]string, map[string][]int, error) {
	return db.mapMetricEntitiesWithBlocked(ctx, target, level, false)
}

func (db *DB) mapMetricEntitiesWithBlocked(ctx context.Context, target, level string, includeBlocked bool) ([]string, map[string][]int, error) {
	targetProvinces := map[int]bool{}
	provinceQuery := `SELECT mp.province_id FROM map_provinces mp WHERE mp.area>0`
	if !includeBlocked {
		provinceQuery += ` AND mp.blocked=0`
	}
	args := []any{}
	targets := splitMapTargets(target)
	if len(targets) > 0 && !(len(targets) == 1 && targets[0] == "all") {
		provinceIDs := make([]int, 0, len(targets))
		titleIDs := make([]string, 0, len(targets))
		regionIDs := make([]string, 0, len(targets))
		for _, item := range targets {
			selector, err := parseMapTargetSelector(item, "")
			if err != nil {
				return nil, nil, err
			}
			if selector.Kind == "all" {
				provinceIDs = nil
				titleIDs = nil
				regionIDs = nil
				break
			}
			switch selector.Kind {
			case "province":
				pid, _ := strconv.Atoi(selector.Value)
				provinceIDs = append(provinceIDs, pid)
			case "region":
				regionIDs = append(regionIDs, selector.Value)
			case "title":
				titleIDs = append(titleIDs, selector.Value)
			}
		}
		clauses := make([]string, 0, 2)
		if len(provinceIDs) > 0 {
			clauses = append(clauses, `mp.province_id IN (`+sqlPlaceholders(len(provinceIDs))+`)`)
			for _, pid := range provinceIDs {
				args = append(args, pid)
			}
		}
		if len(titleIDs) > 0 {
			clauses = append(clauses, `mp.province_id IN (SELECT province_id FROM map_title_provinces WHERE title_id IN (`+sqlPlaceholders(len(titleIDs))+`))`)
			for _, titleID := range titleIDs {
				args = append(args, titleID)
			}
		}
		if len(regionIDs) > 0 {
			clauses = append(clauses, `mp.province_id IN (SELECT province_id FROM map_province_regions WHERE region_id IN (`+sqlPlaceholders(len(regionIDs))+`))`)
			for _, regionID := range regionIDs {
				args = append(args, regionID)
			}
		}
		if len(clauses) > 0 {
			provinceQuery += ` AND (` + strings.Join(clauses, ` OR `) + `)`
		}
	}
	provinceRows, err := db.sql.QueryContext(ctx, provinceQuery, args...)
	if err != nil {
		return nil, nil, err
	}
	for provinceRows.Next() {
		var pid int
		if err := provinceRows.Scan(&pid); err != nil {
			provinceRows.Close()
			return nil, nil, err
		}
		targetProvinces[pid] = true
	}
	if err := provinceRows.Close(); err != nil {
		return nil, nil, err
	}
	groups := map[string][]int{}
	if level == "province" {
		for pid := range targetProvinces {
			groups[strconv.Itoa(pid)] = []int{pid}
		}
	} else if level == "region" {
		regionIDs := make([]string, 0, len(targets))
		for _, item := range targets {
			if regionID, ok := mapRegionTargetID(item); ok {
				regionIDs = append(regionIDs, regionID)
			}
		}
		if len(regionIDs) == 0 {
			return nil, nil, fmt.Errorf("region map level requires one or more region:<id> targets")
		}
		regionArgs := make([]any, 0, len(regionIDs))
		for _, regionID := range regionIDs {
			regionArgs = append(regionArgs, regionID)
		}
		rows, err := db.sql.QueryContext(ctx, `SELECT region_id,province_id
			FROM map_province_regions
			WHERE region_id IN (`+sqlPlaceholders(len(regionIDs))+`)
			ORDER BY region_id,province_id`, regionArgs...)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var pid int
			if err := rows.Scan(&id, &pid); err != nil {
				return nil, nil, err
			}
			if targetProvinces[pid] {
				groups[id] = append(groups[id], pid)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	} else {
		levelCode := map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[level]
		rows, err := db.sql.QueryContext(ctx, `SELECT mtp.title_id,mtp.province_id
			FROM map_title_provinces mtp JOIN map_titles mt ON mt.title_id=mtp.title_id
			JOIN map_provinces mp ON mp.province_id=mtp.province_id
			WHERE mt.title_type=? AND mp.area>0 AND mp.blocked=0
			ORDER BY mtp.title_id,mtp.province_id`, levelCode)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var pid int
			if err := rows.Scan(&id, &pid); err != nil {
				return nil, nil, err
			}
			if targetProvinces[pid] {
				groups[id] = append(groups[id], pid)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, groups, nil
}

func sqlPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func splitMapTargets(target string) []string {
	parts := strings.Split(target, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func mapRegionTargetID(target string) (string, bool) {
	target = strings.TrimSpace(target)
	const prefix = "region:"
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	region := strings.TrimSpace(strings.TrimPrefix(target, prefix))
	return region, region != ""
}

func (db *DB) calculateMapMetricEntity(ctx context.Context, id string, pids []int, date int, spec MapMetricSpec) (MapMetricValue, bool, error) {
	if spec.Kind == "category" || spec.Aggregate == "majority" {
		if spec.Field == "entity_id" {
			return MapMetricValue{ID: id, Category: id}, true, nil
		}
		counts := map[string]int{}
		for _, pid := range pids {
			value, err := db.mapMetricField(ctx, pid, spec.Field, date)
			if err != nil {
				return MapMetricValue{}, false, err
			}
			if value != "" {
				counts[value]++
			}
		}
		best, count := "", 0
		for value, n := range counts {
			if n > count || n == count && value < best {
				best, count = value, n
			}
		}
		return MapMetricValue{ID: id, Category: best}, best != "", nil
	}
	if spec.Aggregate == "diversity" {
		counts, total := map[string]int{}, 0
		for _, pid := range pids {
			value, err := db.mapMetricField(ctx, pid, spec.Field, date)
			if err != nil {
				return MapMetricValue{}, false, err
			}
			if value != "" {
				counts[value]++
				total++
			}
		}
		if total == 0 {
			return MapMetricValue{}, false, nil
		}
		sum := 0.0
		for _, n := range counts {
			p := float64(n) / float64(total)
			sum -= p * math.Log(p)
		}
		if len(counts) > 1 {
			sum /= math.Log(float64(len(counts)))
		}
		return MapMetricValue{ID: id, Value: sum}, true, nil
	}
	if spec.Aggregate == "ratio" {
		matches, total := 0, 0
		for _, pid := range pids {
			value, err := db.mapMetricField(ctx, pid, spec.Field, date)
			if err != nil {
				return MapMetricValue{}, false, err
			}
			if value == "" {
				continue
			}
			total++
			if value == spec.MatchValue {
				matches++
			}
		}
		if total == 0 {
			return MapMetricValue{}, false, nil
		}
		return MapMetricValue{ID: id, Value: float64(matches) / float64(total)}, true, nil
	}
	if spec.Field == "development" || spec.Field == "development_level" {
		values := []float64{}
		seenCounties := map[string]bool{}
		for _, pid := range pids {
			var county string
			_ = db.sql.QueryRowContext(ctx, `SELECT COALESCE(county,'') FROM map_provinces WHERE province_id=?`, pid).Scan(&county)
			key := county
			if key == "" {
				key = strconv.Itoa(pid)
			}
			if seenCounties[key] {
				continue
			}
			seenCounties[key] = true
			value, err := db.mapMetricField(ctx, pid, "development", date)
			if err != nil {
				return MapMetricValue{}, false, err
			}
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				values = append(values, parsed)
			}
		}
		if len(values) == 0 {
			return MapMetricValue{}, false, nil
		}
		result := values[0]
		switch spec.Aggregate {
		case "sum":
			result = 0
			for _, value := range values {
				result += value
			}
		case "mean":
			result = 0
			for _, value := range values {
				result += value
			}
			result /= float64(len(values))
		default: // CK3 development is county-level; max avoids diluting a capital entry across baronies.
			for _, value := range values[1:] {
				if value > result {
					result = value
				}
			}
		}
		return MapMetricValue{ID: id, Value: result}, true, nil
	}
	components := spec.Components
	if len(components) == 0 && spec.Field != "" {
		components = []MapMetricComponent{{Field: spec.Field, Presence: spec.Aggregate == "count"}}
	}
	if len(components) == 0 {
		return MapMetricValue{}, false, fmt.Errorf("numeric metric requires components or field")
	}
	total, observations, maximum := 0.0, 0, math.Inf(-1)
	for _, pid := range pids {
		provinceScore := 0.0
		provinceHasData := false
		for _, component := range components {
			value, err := db.mapMetricField(ctx, pid, component.Field, date)
			if err != nil {
				return MapMetricValue{}, false, err
			}
			multiplier := component.Multiplier
			if multiplier == 0 {
				multiplier = 1
			}
			if value != "" || !isPhysicalMapMetricField(component.Field) {
				provinceHasData = true
			}
			score := component.Default
			if component.Presence {
				if value != "" && value != "0" && value != "none" {
					score = 1
				}
			} else if component.Weights != nil {
				if weighted, ok := component.Weights[value]; ok {
					score = weighted
				}
			} else if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				score = parsed
			}
			provinceScore += score * multiplier
		}
		if !provinceHasData {
			continue
		}
		total += provinceScore
		maximum = math.Max(maximum, provinceScore)
		observations++
	}
	if observations == 0 {
		return MapMetricValue{}, false, nil
	}
	if spec.Aggregate == "mean" {
		total /= float64(observations)
	} else if spec.Aggregate == "max" {
		total = maximum
	}
	return MapMetricValue{ID: id, Value: total}, true, nil
}

func (db *DB) mapMetricField(ctx context.Context, pid int, field string, date int) (string, error) {
	switch field {
	case "elevation", "slope", "ruggedness", "ridge_score", "flow_accumulation", "catchment_area", "river_order", "river_pixel_count", "major_river", "relative_depth", "seabed_slope", "seabed_ruggedness", "shelf_score", "trench_score", "coastal_dropoff", "strait_sill_depth", "ocean_basin_id":
		column := map[string]string{
			"elevation": "elevation_mean", "slope": "slope_mean", "ruggedness": "ruggedness_mean", "ridge_score": "ridge_score",
			"flow_accumulation": "catchment_pixels", "catchment_area": "catchment_pixels", "river_order": "river_order",
			"river_pixel_count": "river_pixel_count", "major_river": "major_river", "relative_depth": "relative_depth_mean",
			"seabed_slope": "seabed_slope", "seabed_ruggedness": "seabed_ruggedness", "shelf_score": "shelf_score", "trench_score": "trench_score",
			"coastal_dropoff": "coastal_dropoff", "strait_sill_depth": "strait_sill_depth", "ocean_basin_id": "water_body_id",
		}[field]
		var value sql.NullFloat64
		if err := db.sql.QueryRowContext(ctx, `SELECT `+column+` FROM map_province_physical WHERE province_id=?`, pid).Scan(&value); err != nil {
			if err == sql.ErrNoRows {
				return "", nil
			}
			return "", err
		}
		if !value.Valid {
			return "", nil
		}
		if field == "ocean_basin_id" || field == "river_order" || field == "river_pixel_count" || field == "major_river" {
			return strconv.FormatInt(int64(math.Round(value.Float64)), 10), nil
		}
		return strconv.FormatFloat(value.Float64, 'f', 8, 64), nil
	case "surface_material":
		var material string
		err := db.sql.QueryRowContext(ctx, `SELECT m.material_id FROM map_province_materials p JOIN map_surface_materials m ON m.material_index=p.material_index WHERE p.province_id=? ORDER BY p.material_rank LIMIT 1`, pid).Scan(&material)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return material, err
	case "surface_material_diversity":
		rows, err := db.sql.QueryContext(ctx, `SELECT weight_share FROM map_province_materials WHERE province_id=? ORDER BY material_rank`, pid)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		var shares []float64
		for rows.Next() {
			var share float64
			if err := rows.Scan(&share); err != nil {
				return "", err
			}
			if share > 0 {
				shares = append(shares, share)
			}
		}
		if len(shares) == 0 {
			return "", rows.Err()
		}
		entropy := 0.0
		for _, share := range shares {
			entropy -= share * math.Log(share)
		}
		if len(shares) > 1 {
			entropy /= math.Log(float64(len(shares)))
		}
		return strconv.FormatFloat(entropy, 'f', 6, 64), rows.Err()
	case "terrain", "area", "is_county_capital":
		var terrain string
		var area, capital int
		if err := db.sql.QueryRowContext(ctx, `SELECT COALESCE(terrain,''),area,is_county_capital FROM map_provinces WHERE province_id=?`, pid).Scan(&terrain, &area, &capital); err != nil {
			return "", err
		}
		switch field {
		case "terrain":
			return terrain, nil
		case "area":
			return strconv.Itoa(area), nil
		default:
			return strconv.Itoa(capital), nil
		}
	case "building_count":
		var county string
		_ = db.sql.QueryRowContext(ctx, `SELECT COALESCE(county,'') FROM map_provinces WHERE province_id=?`, pid).Scan(&county)
		value, err := db.resolveProvinceField(ctx, pid, county, "buildings", date, false)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(len(strings.Fields(value))), nil
	case "holding", "culture", "religion", "special_building", "development", "development_level":
		var county string
		_ = db.sql.QueryRowContext(ctx, `SELECT COALESCE(county,'') FROM map_provinces WHERE province_id=?`, pid).Scan(&county)
		lookupField := field
		if field == "development" || field == "development_level" {
			if value, found, err := db.resolveTitleDevelopment(ctx, county, date); err != nil || found {
				return value, err
			}
			lookupField = "development_level"
			value, err := db.resolveProvinceField(ctx, pid, county, lookupField, date, false)
			if err == nil && value == "" {
				lookupField = "development"
			}
			if value != "" || err != nil {
				return value, err
			}
		}
		return db.resolveProvinceField(ctx, pid, county, lookupField, date, field == "culture" || field == "religion")
	default:
		return "", fmt.Errorf("unsupported map metric field %q", field)
	}
}

func mapMetricUsesPhysicalFields(spec MapMetricSpec) bool {
	if isPhysicalMapMetricField(spec.Field) {
		return true
	}
	for _, component := range spec.Components {
		if isPhysicalMapMetricField(component.Field) {
			return true
		}
	}
	return false
}

func isPhysicalMapMetricField(field string) bool {
	switch field {
	case "elevation", "slope", "ruggedness", "ridge_score", "flow_accumulation", "catchment_area", "river_order", "river_pixel_count", "major_river", "relative_depth", "seabed_slope", "seabed_ruggedness", "shelf_score", "trench_score", "coastal_dropoff", "strait_sill_depth", "ocean_basin_id":
		return true
	default:
		return false
	}
}

func (db *DB) resolveTitleDevelopment(ctx context.Context, county string, date int) (string, bool, error) {
	if county == "" {
		return "", false, nil
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT field,value FROM map_title_history
		WHERE title_id=? AND date_key<=? AND field IN ('development_level','change_development_level')
		ORDER BY date_key,field`, county, date)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	value := 0.0
	found := false
	for rows.Next() {
		var field, raw string
		if err := rows.Scan(&field, &raw); err != nil {
			return "", false, err
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			continue
		}
		found = true
		if field == "development_level" {
			value = parsed
		} else {
			value += parsed
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return strconv.FormatFloat(value, 'f', -1, 64), true, nil
}

func (db *DB) transformMapMetric(ctx context.Context, level string, values map[string]MapMetricValue, groups map[string][]int, transform MapGraphTransform) (map[string]MapMetricValue, error) {
	for id, item := range values {
		if transform.Floor != nil && item.Value < *transform.Floor {
			item.Value = *transform.Floor
		}
		if transform.Cap > 0 {
			if item.Value > transform.Cap {
				item.Value = transform.Cap
			}
		}
		values[id] = item
	}
	if level == "province" {
		return db.transformProvinceMetric(ctx, values, transform)
	}
	levelCode := map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[level]
	edges := map[string]map[string]float64{}
	rows, err := db.sql.QueryContext(ctx, `SELECT title_id,neighbor_id,border_len FROM map_title_adjacencies WHERE level=?`, levelCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		var border float64
		if err := rows.Scan(&a, &b, &border); err != nil {
			return nil, err
		}
		if edges[a] == nil {
			edges[a] = map[string]float64{}
		}
		if transform.EdgeWeight == "border_len" {
			edges[a][b] = border
		} else {
			edges[a][b] = 1
		}
	}
	return applyGraphTransform(values, edges, transform, func(id string) float64 {
		if transform.TerrainAbsorption {
			return db.mapEntityTerrainAbsorption(ctx, groups[id])
		}
		return 1
	}), rows.Err()
}

func (db *DB) transformProvinceMetric(ctx context.Context, values map[string]MapMetricValue, transform MapGraphTransform) (map[string]MapMetricValue, error) {
	edges := map[string]map[string]float64{}
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id,neighbor_id,border_len FROM map_adjacencies WHERE blocked=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b int
		var border float64
		if err := rows.Scan(&a, &b, &border); err != nil {
			return nil, err
		}
		sa, sb := strconv.Itoa(a), strconv.Itoa(b)
		if edges[sa] == nil {
			edges[sa] = map[string]float64{}
		}
		if transform.EdgeWeight == "border_len" {
			edges[sa][sb] = border
		} else {
			edges[sa][sb] = 1
		}
	}
	return applyGraphTransform(values, edges, transform, func(string) float64 { return 1 }), rows.Err()
}

func applyGraphTransform(values map[string]MapMetricValue, edges map[string]map[string]float64, transform MapGraphTransform, absorption func(string) float64) map[string]MapMetricValue {
	current := map[string]MapMetricValue{}
	for id, item := range values {
		current[id] = item
	}
	if transform.Operator == "distance_decay" {
		decay := transform.DistanceDecay
		if decay <= 0 {
			decay = 0.5
		}
		distance := map[string]int{}
		queue := []string{}
		for _, seed := range transform.Seeds {
			if _, ok := current[seed]; ok {
				distance[seed] = 0
				queue = append(queue, seed)
			}
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for n := range edges[id] {
				if _, ok := distance[n]; !ok {
					distance[n] = distance[id] + 1
					queue = append(queue, n)
				}
			}
		}
		for id, item := range current {
			if d, ok := distance[id]; ok {
				item.Value *= math.Pow(decay, float64(d))
				current[id] = item
			}
		}
		return current
	}
	rounds := transform.Rounds
	if rounds <= 0 {
		rounds = 1
	}
	for round := 0; round < rounds; round++ {
		rate := transform.Rate
		if round < len(transform.Rates) {
			rate = transform.Rates[round]
		}
		if rate <= 0 {
			rate = 0.2
		}
		next := map[string]MapMetricValue{}
		for id, item := range current {
			next[id] = item
		}
		for id, item := range current {
			sum, weights := 0.0, 0.0
			for n, w := range edges[id] {
				other, ok := current[n]
				if !ok {
					continue
				}
				delta := other.Value - item.Value
				if transform.Operator == "high_to_low" || transform.OnlyHigherToLower {
					if delta <= 0 {
						continue
					}
				} else {
					delta = other.Value - item.Value
				}
				sum += w * delta
				weights += w
			}
			if weights == 0 {
				continue
			}
			item.Value += rate * (sum / weights) * absorption(id)
			if transform.Cap > 0 && item.Value > transform.Cap {
				item.Value = transform.Cap
			}
			next[id] = item
		}
		current = next
	}
	return current
}

func (db *DB) mapEntityTerrainAbsorption(ctx context.Context, pids []int) float64 {
	if len(pids) == 0 {
		return 1
	}
	total := 0.0
	for _, pid := range pids {
		var terrain string
		_ = db.sql.QueryRowContext(ctx, `SELECT COALESCE(terrain,'') FROM map_provinces WHERE province_id=?`, pid).Scan(&terrain)
		switch terrain {
		case "farmlands", "floodplains", "oasis":
			total += 1
		case "plains", "forest", "steppe":
			total += 0.85
		case "hills", "taiga", "jungle":
			total += 0.65
		case "wetlands", "desert":
			total += 0.45
		case "mountains", "desert_mountains":
			total += 0.25
		default:
			total += 0.7
		}
	}
	return total / float64(len(pids))
}

func mapMetricStats(values []MapMetricValue, total int, kind string) (MapMetricStats, []MapCount) {
	stats := MapMetricStats{Count: len(values), Missing: total - len(values)}
	if total > 0 {
		stats.Coverage = float64(len(values)) / float64(total)
	}
	if kind == "category" {
		counts := map[string]int{}
		for _, v := range values {
			counts[v.Category]++
		}
		return stats, topMapCounts(counts, 100)
	}
	numbers := make([]float64, 0, len(values))
	sum := 0.0
	for _, v := range values {
		numbers = append(numbers, v.Value)
		sum += v.Value
	}
	if len(numbers) == 0 {
		return stats, nil
	}
	sort.Float64s(numbers)
	stats.Minimum = numbers[0]
	stats.Maximum = numbers[len(numbers)-1]
	stats.Mean = sum / float64(len(numbers))
	stats.P10 = quantile(numbers, 0.1)
	stats.Median = quantile(numbers, 0.5)
	stats.P90 = quantile(numbers, 0.9)
	return stats, nil
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	p := q * float64(len(sorted)-1)
	lo := int(math.Floor(p))
	hi := int(math.Ceil(p))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(p-float64(lo))
}

func mapMetricOutliers(values []MapMetricValue, kind string, limit int) []MapMetricValue {
	if kind == "category" || len(values) == 0 {
		return nil
	}
	copyValues := append([]MapMetricValue(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i].Value > copyValues[j].Value })
	if limit > 10 {
		limit = 10
	}
	if limit <= 0 {
		limit = 5
	}
	if len(copyValues) > limit {
		copyValues = copyValues[:limit]
	}
	return copyValues
}
