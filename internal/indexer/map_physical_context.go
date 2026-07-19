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

type MapPhysicalContextSpec struct {
	TargetType           string   `json:"target_type,omitempty"`
	Target               string   `json:"target,omitempty"`
	Targets              []string `json:"targets,omitempty"`
	Operation            string   `json:"operation,omitempty"`
	IncludeAdjacentWater bool     `json:"include_adjacent_water,omitempty"`
}

type MapPhysicalFactSource struct {
	Provenance string  `json:"provenance"`
	Source     string  `json:"source"`
	Algorithm  string  `json:"algorithm,omitempty"`
	Unit       string  `json:"unit"`
	Confidence float64 `json:"confidence"`
}

type MapTerrainContext struct {
	ElevationMin    float64 `json:"elevation_min"`
	ElevationMean   float64 `json:"elevation_mean"`
	ElevationMax    float64 `json:"elevation_max"`
	ElevationP10    float64 `json:"elevation_p10"`
	ElevationMedian float64 `json:"elevation_median"`
	ElevationP90    float64 `json:"elevation_p90"`
	SlopeMean       float64 `json:"slope_mean_per_pixel"`
	SlopeMax        float64 `json:"slope_max_per_pixel"`
	RuggednessMean  float64 `json:"ruggedness_mean"`
	AspectDegrees   float64 `json:"main_aspect_degrees"`
	CurvatureMean   float64 `json:"curvature_mean"`
	RidgeScore      float64 `json:"ridge_score"`
	ValleyScore     float64 `json:"valley_score"`
}

type MapHydrologyContext struct {
	RiverPixelCount     int      `json:"ordinary_river_pixels"`
	MajorRiver          bool     `json:"major_river_province"`
	MajorRiverMouth     bool     `json:"major_river_mouth"`
	MajorRiverWidth     *float64 `json:"major_river_width_proxy,omitempty"`
	UpstreamProvinces   []int    `json:"upstream_provinces,omitempty"`
	DownstreamProvinces []int    `json:"downstream_provinces,omitempty"`
	CatchmentPixels     *float64 `json:"catchment_pixels,omitempty"`
	FlowPercentile      *float64 `json:"flow_percentile,omitempty"`
	RiverOrder          *int     `json:"river_order,omitempty"`
	RiverOrderMethod    string   `json:"river_order_method,omitempty"`
	AdvancedAvailable   bool     `json:"advanced_hydrology_available"`
	UnavailableReason   string   `json:"unavailable_reason,omitempty"`
}

type MapOceanographyContext struct {
	WaterBodyID       *int     `json:"water_body_id,omitempty"`
	WaterBodyKind     string   `json:"water_body_kind,omitempty"`
	SurfaceReference  *float64 `json:"relative_surface_reference,omitempty"`
	SurfaceMethod     string   `json:"surface_reference_method,omitempty"`
	RelativeDepthMean *float64 `json:"relative_depth_mean,omitempty"`
	RelativeDepthMax  *float64 `json:"relative_depth_max,omitempty"`
	SeabedSlope       *float64 `json:"seabed_slope_per_pixel,omitempty"`
	SeabedRuggedness  *float64 `json:"seabed_ruggedness,omitempty"`
	ShelfScore        *float64 `json:"continental_shelf_score,omitempty"`
	TrenchScore       *float64 `json:"trench_score,omitempty"`
	CoastalDropoff    *float64 `json:"coastal_dropoff,omitempty"`
	StraitSillDepth   *float64 `json:"strait_sill_relative_depth,omitempty"`
	ImpassableSea     bool     `json:"game_rule_impassable_sea"`
}

type MapBarrierContext struct {
	Blocked               bool     `json:"blocked"`
	BlockKind             string   `json:"block_kind,omitempty"`
	WaterKind             string   `json:"water_kind,omitempty"`
	CoastalDropoff        *float64 `json:"coastal_dropoff,omitempty"`
	StraitSillDepth       *float64 `json:"strait_sill_relative_depth,omitempty"`
	RiverCrossingCount    int      `json:"river_crossing_count"`
	StraitConnectionCount int      `json:"strait_connection_count"`
}

type MapPhysicalProvinceContext struct {
	ProvinceID   int                     `json:"province_id"`
	Terrain      *MapTerrainContext      `json:"terrain,omitempty"`
	Surface      *MapSurfaceContext      `json:"surface,omitempty"`
	Hydrology    *MapHydrologyContext    `json:"hydrology,omitempty"`
	Oceanography *MapOceanographyContext `json:"oceanography,omitempty"`
	Barriers     *MapBarrierContext      `json:"barriers,omitempty"`
	Source       MapPhysicalFactSource   `json:"source"`
}

type MapPhysicalAggregate struct {
	ProvinceCount          int      `json:"province_count"`
	SampleCount            int64    `json:"sample_count"`
	ElevationMean          *float64 `json:"elevation_mean,omitempty"`
	SlopeMean              *float64 `json:"slope_mean_per_pixel,omitempty"`
	RuggednessMean         *float64 `json:"ruggedness_mean,omitempty"`
	RelativeDepthMean      *float64 `json:"relative_depth_mean,omitempty"`
	RelativeDepthMaximum   *float64 `json:"relative_depth_maximum,omitempty"`
	OrdinaryRiverPixels    int64    `json:"ordinary_river_pixels"`
	MajorRiverProvinces    int      `json:"major_river_provinces"`
	MajorRiverMouths       int      `json:"major_river_mouths"`
	OceanProvinces         int      `json:"ocean_provinces"`
	LakeProvinces          int      `json:"lake_provinces"`
	ImpassableSeaProvinces int      `json:"impassable_sea_provinces"`
	WaterBodyCount         int      `json:"water_body_count"`
	WaterBodyIDs           []int    `json:"water_body_ids,omitempty"`
}

type MapPhysicalOceanAggregate struct {
	ProvinceCount        int      `json:"province_count"`
	SampleCount          int64    `json:"sample_count"`
	BoundaryPixels       int64    `json:"boundary_pixels"`
	ImpassableProvinces  int      `json:"impassable_provinces"`
	RelativeDepthMean    *float64 `json:"relative_depth_mean,omitempty"`
	RelativeDepthP10     *float64 `json:"relative_depth_p10,omitempty"`
	RelativeDepthMedian  *float64 `json:"relative_depth_median,omitempty"`
	RelativeDepthP90     *float64 `json:"relative_depth_p90,omitempty"`
	RelativeDepthMaximum *float64 `json:"relative_depth_maximum,omitempty"`
	ShelfScoreMean       *float64 `json:"continental_shelf_score_mean,omitempty"`
	TrenchScoreMean      *float64 `json:"trench_score_mean,omitempty"`
	SeabedSlopeMean      *float64 `json:"seabed_slope_mean,omitempty"`
}

type MapPhysicalDepthClassification struct {
	Verdict          string   `json:"verdict"`
	Coverage         float64  `json:"coverage"`
	Confidence       float64  `json:"confidence"`
	ShallowThreshold *float64 `json:"shallow_threshold,omitempty"`
	DeepThreshold    *float64 `json:"deep_threshold,omitempty"`
	ShallowShare     float64  `json:"shallow_share"`
	TransitionShare  float64  `json:"transition_share"`
	DeepShare        float64  `json:"deep_share"`
	Method           string   `json:"method"`
	Unit             string   `json:"unit"`
}

type MapPhysicalWaterRepresentative struct {
	ProvinceID       int      `json:"province_id"`
	WaterKind        string   `json:"water_kind"`
	BoundaryPixels   int64    `json:"boundary_pixels"`
	RelativeDepth    *float64 `json:"relative_depth_mean,omitempty"`
	RelativeDepthMax *float64 `json:"relative_depth_max,omitempty"`
	ShelfScore       *float64 `json:"continental_shelf_score,omitempty"`
	TrenchScore      *float64 `json:"trench_score,omitempty"`
	WaterBodyID      *int     `json:"water_body_id,omitempty"`
}

type MapAdjacentWaterContext struct {
	SelectionMode       string                           `json:"selection_mode"`
	CoastalLandCount    int                              `json:"coastal_land_provinces"`
	BoundaryPixels      int64                            `json:"boundary_pixels"`
	WaterProvinceCount  int                              `json:"water_province_count"`
	Ocean               MapPhysicalOceanAggregate        `json:"ocean"`
	LakeProvinceCount   int                              `json:"lake_province_count"`
	LakeProvinceIDs     []int                            `json:"lake_province_ids,omitempty"`
	MajorRiverCount     int                              `json:"major_river_province_count"`
	MajorRiverIDs       []int                            `json:"major_river_province_ids,omitempty"`
	CoastalDropoffMean  *float64                         `json:"coastal_dropoff_mean,omitempty"`
	DepthClassification MapPhysicalDepthClassification   `json:"depth_classification"`
	Representatives     []MapPhysicalWaterRepresentative `json:"representative_ocean_provinces,omitempty"`
	Source              MapPhysicalFactSource            `json:"source"`
}

type MapPhysicalContextResult struct {
	Intent           string                       `json:"intent"`
	Status           string                       `json:"status"`
	Operation        string                       `json:"operation"`
	TargetType       string                       `json:"target_type"`
	Targets          []string                     `json:"targets"`
	Summary          string                       `json:"summary"`
	Aggregate        *MapPhysicalAggregate        `json:"aggregate,omitempty"`
	Surface          *MapSurfaceContext           `json:"surface,omitempty"`
	AdjacentWater    *MapAdjacentWaterContext     `json:"adjacent_water,omitempty"`
	Provinces        []MapPhysicalProvinceContext `json:"provinces,omitempty"`
	Sidecar          *GISSidecarStatus            `json:"sidecar,omitempty"`
	Sources          []MapPhysicalFactSource      `json:"sources"`
	Unavailable      []string                     `json:"unavailable_fields,omitempty"`
	Guidance         []string                     `json:"guidance,omitempty"`
	CacheFingerprint string                       `json:"cache_fingerprint,omitempty"`
}

func (db *DB) LLMMapPhysicalContext(ctx context.Context, input MapPhysicalContextSpec, opts LLMOptions) (MapPhysicalContextResult, error) {
	operation := strings.ToLower(strings.TrimSpace(input.Operation))
	if operation == "" {
		operation = "summary"
	}
	validOperation := map[string]bool{"summary": true, "terrain": true, "surface": true, "hydrology": true, "oceanography": true, "barriers": true}
	if !validOperation[operation] {
		return MapPhysicalContextResult{}, fmt.Errorf("unsupported physical map operation %q", operation)
	}
	targetType := strings.ToLower(strings.TrimSpace(input.TargetType))
	targets := append([]string(nil), input.Targets...)
	if strings.TrimSpace(input.Target) != "" {
		if len(targets) != 0 {
			return MapPhysicalContextResult{}, fmt.Errorf("provide target or targets, not both")
		}
		targets = []string{strings.TrimSpace(input.Target)}
	}
	if targetType == "" {
		switch {
		case len(targets) > 1:
			targetType = "targets"
		case len(targets) == 0 || strings.EqualFold(targets[0], "all"):
			targetType = "all"
		case strings.HasPrefix(strings.TrimSpace(targets[0]), "region:"):
			targetType = "region"
		case isNumericMapTarget(targets[0]):
			targetType = "province"
		default:
			targetType = "title"
		}
	}
	if targetType != "province" && targetType != "title" && targetType != "region" && targetType != "targets" && targetType != "all" {
		return MapPhysicalContextResult{}, fmt.Errorf("unsupported target_type %q", targetType)
	}
	if targetType == "all" {
		targets = []string{"all"}
	} else if len(targets) == 0 {
		return MapPhysicalContextResult{}, fmt.Errorf("target_type %q requires a target", targetType)
	}
	if len(targets) > 16 {
		return MapPhysicalContextResult{}, fmt.Errorf("physical context accepts at most 16 targets")
	}
	selectors, err := parseMapTargetSelectors(targets, targetType)
	if err != nil {
		return MapPhysicalContextResult{}, err
	}
	selectionCTE, selectionArgs, err := mapTargetSelectionCTE(selectors)
	if err != nil {
		return MapPhysicalContextResult{}, err
	}
	var ids []int
	if operation == "surface" {
		ids, err = db.surfaceSelectedProvinceIDs(ctx, selectionCTE, selectionArgs)
	} else {
		ids, err = db.physicalSelectedProvinceIDs(ctx, selectionCTE, selectionArgs)
	}
	if err != nil {
		return MapPhysicalContextResult{}, err
	}
	if len(ids) == 0 && operation == "surface" {
		return MapPhysicalContextResult{}, fmt.Errorf("map target selected no provinces with cached surface-material data; run ck3-index scan with active gfx/map/terrain material rasters")
	}
	if len(ids) == 0 {
		return MapPhysicalContextResult{}, fmt.Errorf("map target selected no provinces with cached physical data; run ck3-index scan with an active heightmap")
	}
	if operation == "surface" {
		if input.IncludeAdjacentWater {
			return MapPhysicalContextResult{}, fmt.Errorf("include_adjacent_water is not supported by operation surface; use summary or oceanography")
		}
		return db.llmMapSurfaceContext(ctx, targetType, targets, selectionCTE, selectionArgs, ids, opts)
	}
	aggregate, err := db.physicalAggregateSelection(ctx, selectionCTE, selectionArgs)
	if err != nil {
		return MapPhysicalContextResult{}, err
	}
	sidecar := db.cachedGISSidecarStatus(ctx)
	result := MapPhysicalContextResult{
		Intent: "map_physical_context", Operation: operation, TargetType: targetType, Targets: targets,
		Aggregate: &aggregate, Sidecar: &sidecar, CacheFingerprint: db.metaValueOrEmpty(ctx, "map_gis_fingerprint"),
		Sources: []MapPhysicalFactSource{
			{Provenance: "observed", Source: "heightmap.png, rivers.png, default.map and adjacencies.csv", Unit: "normalized height and pixels", Confidence: 1},
			{Provenance: "derived", Source: "province zonal statistics and water-body topology", Algorithm: "ck3-index-heightmap-zonal-v1", Unit: "relative map units", Confidence: 0.88},
			{Provenance: "inferred", Source: "relative terrain and topology classification", Algorithm: "ck3-index-physical-inference-v1", Unit: "unitless score", Confidence: 0.72},
		},
	}
	if operation == "summary" || operation == "terrain" {
		result.Surface, err = db.mapSurfaceSelectionContext(ctx, selectionCTE, selectionArgs, limitForSurface(opts.normalizedLimit()))
		if err != nil {
			return MapPhysicalContextResult{}, err
		}
		result.Sources = append(result.Sources, result.Surface.Source)
	}
	if aggregate.WaterBodyCount > len(aggregate.WaterBodyIDs) {
		result.Guidance = append(result.Guidance, fmt.Sprintf("Water-body IDs are capped at %d of %d components; counts and aggregate statistics cover all components.", len(aggregate.WaterBodyIDs), aggregate.WaterBodyCount))
	}
	if !sidecar.Available || sidecar.AnalysisStatus != "ready" {
		result.Status = "degraded"
		result.Unavailable = []string{"catchment_pixels", "flow_percentile", "river_order", "WhiteboxTools-confirmed slope, curvature and hydrology"}
		result.Guidance = append(result.Guidance, "Observed CK3 water classes and built-in relative raster aggregates remain available; advanced WhiteboxTools hydrology is unavailable and was not synthesized.")
	} else {
		result.Status = "ready"
	}
	limit := opts.normalizedLimit()
	if limit > 16 {
		limit = 16
	}
	if input.IncludeAdjacentWater {
		adjacent, err := db.physicalAdjacentWaterContext(ctx, selectionCTE, selectionArgs, limit)
		if err != nil {
			return MapPhysicalContextResult{}, err
		}
		result.AdjacentWater = &adjacent
		result.Guidance = append(result.Guidance, "Adjacent ocean depth classification uses map-wide weighted relative-depth tertiles; lakes and river_provinces are reported separately and never enter the verdict.")
	}
	if targetType == "province" || targetType == "targets" && len(ids) <= limit {
		for _, id := range ids {
			item, err := db.physicalProvinceContext(ctx, id, operation, sidecar)
			if err != nil {
				return MapPhysicalContextResult{}, err
			}
			result.Provinces = append(result.Provinces, item)
		}
	} else if operation != "summary" {
		representative, err := db.physicalRepresentativeSelection(ctx, selectionCTE, selectionArgs, operation, limit)
		if err != nil {
			return MapPhysicalContextResult{}, err
		}
		for _, id := range representative {
			item, err := db.physicalProvinceContext(ctx, id, operation, sidecar)
			if err != nil {
				return MapPhysicalContextResult{}, err
			}
			result.Provinces = append(result.Provinces, item)
		}
		if len(ids) > len(representative) {
			result.Guidance = append(result.Guidance, fmt.Sprintf("Returned %d operation-relevant representative province rows from %d selected provinces; aggregate values cover the complete target.", len(representative), len(ids)))
		}
	}
	result.Summary = fmt.Sprintf("Physical context covers %d provinces, %d major-river provinces, %d ocean provinces and %d lake provinces. Heights and depths are normalized relative map values, never metres.", aggregate.ProvinceCount, aggregate.MajorRiverProvinces, aggregate.OceanProvinces, aggregate.LakeProvinces)
	if result.Surface != nil && result.Surface.Available {
		result.Summary += fmt.Sprintf(" Surface-material sampling found %d retained material(s), dominated by %s.", len(result.Surface.Materials), result.Surface.DominantMaterialID)
	}
	if result.AdjacentWater != nil {
		result.Summary += fmt.Sprintf(" Adjacent-water analysis found %d ocean provinces and classified the coast as %s.", result.AdjacentWater.Ocean.ProvinceCount, result.AdjacentWater.DepthClassification.Verdict)
	}
	result.Guidance = append(result.Guidance, "Do not convert relative depth into CK3 navigability; impassable_seas and explicit adjacencies are separate observed gameplay facts.")
	return result, nil
}

func (db *DB) llmMapSurfaceContext(ctx context.Context, targetType string, targets []string, selectionCTE string, selectionArgs []any, ids []int, opts LLMOptions) (MapPhysicalContextResult, error) {
	limit := opts.normalizedLimit()
	if limit > 16 {
		limit = 16
	}
	surface, err := db.mapSurfaceSelectionContext(ctx, selectionCTE, selectionArgs, limitForSurface(limit))
	if err != nil {
		return MapPhysicalContextResult{}, err
	}
	result := MapPhysicalContextResult{
		Intent: "map_physical_context", Operation: "surface", TargetType: targetType, Targets: targets,
		Surface: surface, CacheFingerprint: surface.CacheFingerprint,
		Sources: []MapPhysicalFactSource{surface.Source},
	}
	if surface.Available {
		result.Status = "ready"
	} else {
		result.Status = "degraded"
		result.Unavailable = []string{"surface_materials"}
		result.Guidance = append(result.Guidance, surface.UnavailableReason)
	}
	// A single-province response already exposes the complete material blend
	// at the top level. Repeating the same texture/resource object under
	// provinces roughly doubles the MCP payload without adding evidence.
	representatives := ids
	omitSingleProvinceDetail := targetType == "province" && len(ids) == 1
	if omitSingleProvinceDetail {
		representatives = nil
	} else if targetType != "province" && !(targetType == "targets" && len(ids) <= limit) {
		representatives, err = db.surfaceRepresentativeSelection(ctx, selectionCTE, selectionArgs, limit)
		if err != nil {
			return MapPhysicalContextResult{}, err
		}
	}
	for _, id := range representatives {
		item, err := db.physicalProvinceContext(ctx, id, "surface", GISSidecarStatus{})
		if err != nil {
			return MapPhysicalContextResult{}, err
		}
		result.Provinces = append(result.Provinces, item)
	}
	if !omitSingleProvinceDetail && len(ids) > len(representatives) {
		result.Guidance = append(result.Guidance, fmt.Sprintf("Returned %d material-representative province rows from %d sampled provinces; the top-level blend covers the complete selection.", len(representatives), len(ids)))
	}
	if surface.Available {
		result.Summary = fmt.Sprintf("Observed terrain-material sampling covers %d province(s) and %d valid sample(s); the dominant retained blend is %s. These are gfx/map/terrain paint weights, not scripted province terrain.", len(ids), surface.SampleCount, surface.DominantMaterialID)
	} else {
		result.Summary = fmt.Sprintf("No observed terrain-material samples were available for the %d selected province(s).", len(ids))
	}
	return result, nil
}

func (db *DB) physicalRepresentativeSelection(ctx context.Context, cte string, cteArgs []any, operation string, limit int) ([]int, error) {
	if operation == "surface" {
		return db.surfaceRepresentativeSelection(ctx, cte, cteArgs, limit)
	}
	condition, order := "1=1", "g.province_id"
	switch operation {
	case "oceanography":
		condition = "g.relative_depth_mean IS NOT NULL OR g.coastal_dropoff IS NOT NULL OR g.strait_sill_depth IS NOT NULL"
		order = "g.relative_depth_mean IS NULL, g.relative_depth_mean DESC, g.province_id"
	case "hydrology":
		condition = "g.major_river=1 OR g.river_pixel_count>0 OR g.catchment_pixels IS NOT NULL"
		order = "g.major_river DESC, g.catchment_pixels DESC, g.river_pixel_count DESC, g.province_id"
	case "barriers":
		condition = "p.blocked=1 OR g.coastal_dropoff IS NOT NULL OR g.strait_sill_depth IS NOT NULL"
		order = "p.blocked DESC, g.coastal_dropoff DESC, g.province_id"
	}
	args := append([]any(nil), cteArgs...)
	args = append(args, limit)
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT g.province_id
		FROM selected s
		JOIN map_province_physical g ON g.province_id=s.province_id
		JOIN map_provinces p ON p.province_id=g.province_id
		WHERE `+condition+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int, 0, limit)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		fallbackArgs := append([]any(nil), cteArgs...)
		fallbackArgs = append(fallbackArgs, limit)
		fallbackRows, fallbackErr := db.sql.QueryContext(ctx, cte+`SELECT g.province_id
			FROM selected s JOIN map_province_physical g ON g.province_id=s.province_id
			ORDER BY g.province_id LIMIT ?`, fallbackArgs...)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		defer fallbackRows.Close()
		for fallbackRows.Next() {
			var id int
			if err := fallbackRows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		if err := fallbackRows.Err(); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}

func limitForSurface(limit int) int {
	if limit <= 0 || limit > 8 {
		return 4
	}
	return limit
}

func (db *DB) surfaceSelectedProvinceIDs(ctx context.Context, cte string, cteArgs []any) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT DISTINCT p.province_id
		FROM selected s JOIN map_province_materials p ON p.province_id=s.province_id
		ORDER BY p.province_id`, cteArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) surfaceRepresentativeSelection(ctx context.Context, cte string, cteArgs []any, limit int) ([]int, error) {
	args := append([]any(nil), cteArgs...)
	args = append(args, limit)
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT p.province_id
		FROM selected s JOIN map_province_materials p ON p.province_id=s.province_id
		WHERE p.material_rank=1
		ORDER BY p.sample_count DESC,p.weight_share DESC,p.province_id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func isNumericMapTarget(value string) bool {
	_, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil
}

func (db *DB) physicalProvinceIDs(ctx context.Context, selected map[int]bool) ([]int, error) {
	query := `SELECT province_id FROM map_province_physical`
	args := make([]any, 0, len(selected))
	if len(selected) > 0 {
		ids := make([]int, 0, len(selected))
		for id := range selected {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		query += ` WHERE province_id IN (` + sqlPlaceholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += ` ORDER BY province_id`
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) physicalSelectedProvinceIDs(ctx context.Context, cte string, cteArgs []any) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT DISTINCT g.province_id
		FROM selected s JOIN map_province_physical g ON g.province_id=s.province_id
		ORDER BY g.province_id`, cteArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) physicalProvinceContext(ctx context.Context, id int, operation string, sidecar GISSidecarStatus) (MapPhysicalProvinceContext, error) {
	if operation == "surface" {
		surface, err := db.mapSurfaceProvinceContext(ctx, id, 4)
		if err != nil {
			return MapPhysicalProvinceContext{}, err
		}
		return MapPhysicalProvinceContext{ProvinceID: id, Surface: surface, Source: surface.Source}, nil
	}
	var terrain MapTerrainContext
	var hydro MapHydrologyContext
	var ocean MapOceanographyContext
	var barrier MapBarrierContext
	var waterBody, riverOrder sql.NullInt64
	var depthMean, depthMax, seabedSlope, seabedRugged, shelf, trench, dropoff, sill, width, catchment, percentile sql.NullFloat64
	var blocked, major, mouth int
	var waterKind, blockKind, algorithm string
	var confidence float64
	err := db.sql.QueryRowContext(ctx, `SELECT p.blocked,COALESCE(p.block_kind,''),COALESCE(p.water_kind,''),
		g.elevation_min,g.elevation_mean,g.elevation_max,g.elevation_p10,g.elevation_median,g.elevation_p90,g.slope_mean,g.slope_max,g.ruggedness_mean,COALESCE(g.aspect_degrees,0),g.curvature_mean,g.ridge_score,g.valley_score,
		g.relative_depth_mean,g.relative_depth_max,g.seabed_slope,g.seabed_ruggedness,g.shelf_score,g.trench_score,g.coastal_dropoff,g.strait_sill_depth,g.water_body_id,g.river_pixel_count,g.major_river,g.major_river_width_proxy,g.major_river_mouth,g.catchment_pixels,g.flow_percentile,g.river_order,g.algorithm,g.confidence
		FROM map_province_physical g JOIN map_provinces p ON p.province_id=g.province_id WHERE g.province_id=?`, id).Scan(
		&blocked, &blockKind, &waterKind, &terrain.ElevationMin, &terrain.ElevationMean, &terrain.ElevationMax, &terrain.ElevationP10, &terrain.ElevationMedian, &terrain.ElevationP90,
		&terrain.SlopeMean, &terrain.SlopeMax, &terrain.RuggednessMean, &terrain.AspectDegrees, &terrain.CurvatureMean, &terrain.RidgeScore, &terrain.ValleyScore,
		&depthMean, &depthMax, &seabedSlope, &seabedRugged, &shelf, &trench, &dropoff, &sill, &waterBody, &hydro.RiverPixelCount, &major, &width, &mouth, &catchment, &percentile, &riverOrder, &algorithm, &confidence)
	if err != nil {
		return MapPhysicalProvinceContext{}, err
	}
	hydro.MajorRiver, hydro.MajorRiverMouth = major != 0, mouth != 0
	hydro.MajorRiverWidth, hydro.CatchmentPixels, hydro.FlowPercentile = nullableFloatPtr(width), nullableFloatPtr(catchment), nullableFloatPtr(percentile)
	if riverOrder.Valid {
		v := int(riverOrder.Int64)
		hydro.RiverOrder = &v
		hydro.RiverOrderMethod = "province D8 catchment-percentile order proxy (1-5)"
	}
	hydro.AdvancedAvailable = sidecar.Available && catchment.Valid
	if !hydro.AdvancedAvailable {
		hydro.UnavailableReason = "Verified WhiteboxTools hydrology has not been cached for this map fingerprint."
	}
	if major != 0 {
		up, down, err := db.majorRiverNeighbors(ctx, id)
		if err != nil {
			return MapPhysicalProvinceContext{}, err
		}
		hydro.UpstreamProvinces, hydro.DownstreamProvinces = up, down
	}
	ocean.WaterBodyID = nullableIntPtr(waterBody)
	ocean.RelativeDepthMean, ocean.RelativeDepthMax = nullableFloatPtr(depthMean), nullableFloatPtr(depthMax)
	ocean.SeabedSlope, ocean.SeabedRuggedness = nullableFloatPtr(seabedSlope), nullableFloatPtr(seabedRugged)
	ocean.ShelfScore, ocean.TrenchScore = nullableFloatPtr(shelf), nullableFloatPtr(trench)
	ocean.CoastalDropoff, ocean.StraitSillDepth = nullableFloatPtr(dropoff), nullableFloatPtr(sill)
	ocean.ImpassableSea = waterKind == "impassable_sea"
	if waterBody.Valid {
		var surface sql.NullFloat64
		_ = db.sql.QueryRowContext(ctx, `SELECT kind,surface_reference,surface_method FROM map_physical_water_bodies WHERE water_body_id=?`, waterBody.Int64).Scan(&ocean.WaterBodyKind, &surface, &ocean.SurfaceMethod)
		ocean.SurfaceReference = nullableFloatPtr(surface)
	}
	barrier.Blocked, barrier.BlockKind, barrier.WaterKind = blocked != 0, blockKind, waterKind
	barrier.CoastalDropoff, barrier.StraitSillDepth = nullableFloatPtr(dropoff), nullableFloatPtr(sill)
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_strategic_adjacencies WHERE passage_kind='river_crossing' AND (from_province=? OR to_province=?)`, id, id).Scan(&barrier.RiverCrossingCount)
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_strategic_adjacencies WHERE passage_kind='strait' AND (from_province=? OR to_province=?)`, id, id).Scan(&barrier.StraitConnectionCount)
	item := MapPhysicalProvinceContext{ProvinceID: id, Source: MapPhysicalFactSource{Provenance: "derived", Source: "active CK3 map rasters and topology", Algorithm: algorithm, Unit: "normalized height and pixels", Confidence: confidence}}
	if operation == "summary" || operation == "terrain" {
		item.Terrain = &terrain
		item.Surface, err = db.mapSurfaceProvinceContext(ctx, id, 4)
		if err != nil {
			return MapPhysicalProvinceContext{}, err
		}
	}
	if operation == "summary" || operation == "hydrology" {
		item.Hydrology = &hydro
	}
	if operation == "summary" || operation == "oceanography" {
		item.Oceanography = &ocean
	}
	if operation == "summary" || operation == "barriers" {
		item.Barriers = &barrier
	}
	return item, nil
}

func (db *DB) majorRiverNeighbors(ctx context.Context, id int) ([]int, []int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT from_province,to_province,relation FROM map_major_river_edges WHERE from_province=? OR to_province=? ORDER BY from_province,to_province`, id, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var upstream, downstream []int
	for rows.Next() {
		var from, to int
		var relation string
		if err := rows.Scan(&from, &to, &relation); err != nil {
			return nil, nil, err
		}
		if relation == "downstream" {
			if from == id {
				downstream = append(downstream, to)
			} else {
				upstream = append(upstream, from)
			}
		} else {
			other := from
			if other == id {
				other = to
			}
			downstream = append(downstream, other)
		}
	}
	return uniqueSortedInts(upstream), uniqueSortedInts(downstream), rows.Err()
}

func (db *DB) physicalAggregateSelection(ctx context.Context, cte string, cteArgs []any) (MapPhysicalAggregate, error) {
	var out MapPhysicalAggregate
	var elevation, slope, rugged, depthMean, depthMax sql.NullFloat64
	err := db.sql.QueryRowContext(ctx, cte+`SELECT COUNT(*),COALESCE(SUM(g.sample_count),0),AVG(g.elevation_mean),AVG(g.slope_mean),AVG(g.ruggedness_mean),AVG(g.relative_depth_mean),MAX(g.relative_depth_max),COALESCE(SUM(g.river_pixel_count),0),COALESCE(SUM(g.major_river),0),COALESCE(SUM(g.major_river_mouth),0)
		FROM selected s JOIN map_province_physical g ON g.province_id=s.province_id`, cteArgs...).Scan(
		&out.ProvinceCount, &out.SampleCount, &elevation, &slope, &rugged, &depthMean, &depthMax, &out.OrdinaryRiverPixels, &out.MajorRiverProvinces, &out.MajorRiverMouths)
	if err != nil {
		return out, err
	}
	out.ElevationMean, out.SlopeMean, out.RuggednessMean = nullableFloatPtr(elevation), nullableFloatPtr(slope), nullableFloatPtr(rugged)
	out.RelativeDepthMean, out.RelativeDepthMaximum = nullableFloatPtr(depthMean), nullableFloatPtr(depthMax)
	rows, err := db.sql.QueryContext(ctx, cte+`SELECT p.water_kind,COUNT(*)
		FROM selected s JOIN map_provinces p ON p.province_id=s.province_id
		GROUP BY p.water_kind`, cteArgs...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind sql.NullString
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return out, err
		}
		switch kind.String {
		case "sea", "coastal_sea":
			out.OceanProvinces += count
		case "impassable_sea":
			out.OceanProvinces += count
			out.ImpassableSeaProvinces += count
		case "lake":
			out.LakeProvinces += count
		}
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	bodyRows, err := db.sql.QueryContext(ctx, cte+`SELECT DISTINCT wb.water_body_id
		FROM selected s JOIN map_physical_water_body_provinces wb ON wb.province_id=s.province_id
		ORDER BY wb.water_body_id`, cteArgs...)
	if err != nil {
		return out, err
	}
	defer bodyRows.Close()
	for bodyRows.Next() {
		var id int
		if err := bodyRows.Scan(&id); err != nil {
			return out, err
		}
		out.WaterBodyCount++
		if len(out.WaterBodyIDs) < 16 {
			out.WaterBodyIDs = append(out.WaterBodyIDs, id)
		}
	}
	return out, bodyRows.Err()
}

type mapAdjacentPhysicalWaterRow struct {
	ProvinceID       int
	WaterKind        string
	SampleCount      int64
	BoundaryPixels   int64
	RelativeDepth    *float64
	RelativeDepthMax *float64
	SeabedSlope      *float64
	ShelfScore       *float64
	TrenchScore      *float64
	WaterBodyID      *int
	MajorRiver       bool
	Confidence       float64
}

type mapWeightedPhysicalValue struct {
	Value  float64
	Weight int64
}

func (db *DB) physicalAdjacentWaterContext(ctx context.Context, cte string, cteArgs []any, limit int) (MapAdjacentWaterContext, error) {
	result := MapAdjacentWaterContext{
		DepthClassification: MapPhysicalDepthClassification{
			Verdict: "insufficient_data", Method: "map-wide sample-weighted ocean relative-depth tertiles (P33/P67)", Unit: "relative map depth",
		},
		Source: MapPhysicalFactSource{Provenance: "inferred", Source: "cached province bathymetry and pixel-border adjacency", Algorithm: "ck3-index-adjacent-water-v1", Unit: "relative map depth and boundary pixels", Confidence: 0.72},
	}
	landCTE := cte + `, selected_land(province_id) AS (
		SELECT s.province_id FROM selected s
		JOIN map_provinces p ON p.province_id=s.province_id
		JOIN map_province_physical g ON g.province_id=s.province_id
		WHERE COALESCE(p.block_kind,'')<>'water' AND g.major_river=0
	) `
	var selectedLandCount int
	if err := db.sql.QueryRowContext(ctx, landCTE+`SELECT COUNT(*) FROM selected_land`, cteArgs...).Scan(&selectedLandCount); err != nil {
		return result, err
	}
	var rows *sql.Rows
	var err error
	if selectedLandCount > 0 {
		result.SelectionMode = "adjacent_to_selected_land"
		coastCTE := landCTE + `, coastal_edges(land_id,province_id,border_len) AS (
			SELECT sl.province_id,a.neighbor_id,a.border_len
			FROM selected_land sl
			JOIN map_adjacencies a ON a.province_id=sl.province_id
			JOIN map_provinces wp ON wp.province_id=a.neighbor_id
			JOIN map_province_physical wg ON wg.province_id=a.neighbor_id
			WHERE COALESCE(wp.block_kind,'')='water' OR wg.major_river=1
		), adjacent_water(province_id,border_len) AS (
			SELECT province_id,SUM(border_len) FROM coastal_edges GROUP BY province_id
		) `
		if err := db.sql.QueryRowContext(ctx, coastCTE+`SELECT COUNT(DISTINCT land_id),COALESCE(SUM(border_len),0) FROM coastal_edges`, cteArgs...).Scan(&result.CoastalLandCount, &result.BoundaryPixels); err != nil {
			return result, err
		}
		var dropoff sql.NullFloat64
		if err := db.sql.QueryRowContext(ctx, coastCTE+`SELECT SUM(g.coastal_dropoff*e.border_len)/NULLIF(SUM(CASE WHEN g.coastal_dropoff IS NOT NULL THEN e.border_len ELSE 0 END),0)
			FROM (SELECT land_id,SUM(border_len) border_len FROM coastal_edges GROUP BY land_id) e
			JOIN map_province_physical g ON g.province_id=e.land_id`, cteArgs...).Scan(&dropoff); err != nil {
			return result, err
		}
		result.CoastalDropoffMean = nullableFloatPtr(dropoff)
		rows, err = db.sql.QueryContext(ctx, coastCTE+`SELECT g.province_id,COALESCE(p.water_kind,''),g.sample_count,aw.border_len,
			g.relative_depth_mean,g.relative_depth_max,g.seabed_slope,g.shelf_score,g.trench_score,g.water_body_id,g.major_river,g.confidence
			FROM adjacent_water aw
			JOIN map_province_physical g ON g.province_id=aw.province_id
			JOIN map_provinces p ON p.province_id=aw.province_id
			ORDER BY g.province_id`, cteArgs...)
	} else {
		result.SelectionMode = "selected_water"
		rows, err = db.sql.QueryContext(ctx, cte+`SELECT g.province_id,COALESCE(p.water_kind,''),g.sample_count,0,
			g.relative_depth_mean,g.relative_depth_max,g.seabed_slope,g.shelf_score,g.trench_score,g.water_body_id,g.major_river,g.confidence
			FROM selected s
			JOIN map_province_physical g ON g.province_id=s.province_id
			JOIN map_provinces p ON p.province_id=s.province_id
			WHERE COALESCE(p.block_kind,'')='water' OR g.major_river=1
			ORDER BY g.province_id`, cteArgs...)
	}
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var waterRows []mapAdjacentPhysicalWaterRow
	for rows.Next() {
		var row mapAdjacentPhysicalWaterRow
		var depth, depthMax, slope, shelf, trench sql.NullFloat64
		var waterBody sql.NullInt64
		var major int
		if err := rows.Scan(&row.ProvinceID, &row.WaterKind, &row.SampleCount, &row.BoundaryPixels,
			&depth, &depthMax, &slope, &shelf, &trench, &waterBody, &major, &row.Confidence); err != nil {
			return result, err
		}
		row.RelativeDepth, row.RelativeDepthMax = nullableFloatPtr(depth), nullableFloatPtr(depthMax)
		row.SeabedSlope, row.ShelfScore, row.TrenchScore = nullableFloatPtr(slope), nullableFloatPtr(shelf), nullableFloatPtr(trench)
		row.WaterBodyID, row.MajorRiver = nullableIntPtr(waterBody), major != 0
		waterRows = append(waterRows, row)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.WaterProvinceCount = len(waterRows)
	for _, row := range waterRows {
		if row.MajorRiver || row.WaterKind == "river" {
			result.MajorRiverCount++
			if len(result.MajorRiverIDs) < limit {
				result.MajorRiverIDs = append(result.MajorRiverIDs, row.ProvinceID)
			}
			continue
		}
		if row.WaterKind == "lake" {
			result.LakeProvinceCount++
			if len(result.LakeProvinceIDs) < limit {
				result.LakeProvinceIDs = append(result.LakeProvinceIDs, row.ProvinceID)
			}
			continue
		}
		if isOceanPhysicalWaterKind(row.WaterKind) {
			result.Ocean.ProvinceCount++
			result.Ocean.SampleCount += row.SampleCount
			result.Ocean.BoundaryPixels += row.BoundaryPixels
			if row.WaterKind == "impassable_sea" {
				result.Ocean.ImpassableProvinces++
			}
		}
	}
	if err := db.populatePhysicalOceanSummary(ctx, &result, waterRows); err != nil {
		return result, err
	}
	result.Representatives = physicalWaterRepresentatives(waterRows, limit)
	result.Source.Confidence = result.DepthClassification.Confidence
	return result, nil
}

func (db *DB) populatePhysicalOceanSummary(ctx context.Context, result *MapAdjacentWaterContext, rows []mapAdjacentPhysicalWaterRow) error {
	var depths, shelf, trench, slope, confidences []mapWeightedPhysicalValue
	var validSamples int64
	var maximum *float64
	for _, row := range rows {
		if !isOceanPhysicalWaterKind(row.WaterKind) || row.MajorRiver {
			continue
		}
		if row.RelativeDepth != nil {
			depths = append(depths, mapWeightedPhysicalValue{Value: *row.RelativeDepth, Weight: row.SampleCount})
			confidences = append(confidences, mapWeightedPhysicalValue{Value: row.Confidence, Weight: row.SampleCount})
			validSamples += row.SampleCount
		}
		if row.RelativeDepthMax != nil && (maximum == nil || *row.RelativeDepthMax > *maximum) {
			value := *row.RelativeDepthMax
			maximum = &value
		}
		if row.ShelfScore != nil {
			shelf = append(shelf, mapWeightedPhysicalValue{Value: *row.ShelfScore, Weight: row.SampleCount})
		}
		if row.TrenchScore != nil {
			trench = append(trench, mapWeightedPhysicalValue{Value: *row.TrenchScore, Weight: row.SampleCount})
		}
		if row.SeabedSlope != nil {
			slope = append(slope, mapWeightedPhysicalValue{Value: *row.SeabedSlope, Weight: row.SampleCount})
		}
	}
	result.Ocean.RelativeDepthMean = weightedPhysicalMean(depths)
	result.Ocean.RelativeDepthP10 = weightedPhysicalQuantile(depths, 0.10)
	result.Ocean.RelativeDepthMedian = weightedPhysicalQuantile(depths, 0.50)
	result.Ocean.RelativeDepthP90 = weightedPhysicalQuantile(depths, 0.90)
	result.Ocean.RelativeDepthMaximum = maximum
	result.Ocean.ShelfScoreMean = weightedPhysicalMean(shelf)
	result.Ocean.TrenchScoreMean = weightedPhysicalMean(trench)
	result.Ocean.SeabedSlopeMean = weightedPhysicalMean(slope)
	if result.Ocean.SampleCount > 0 {
		result.DepthClassification.Coverage = float64(validSamples) / float64(result.Ocean.SampleCount)
	}
	confidence := weightedPhysicalMean(confidences)
	if confidence != nil {
		result.DepthClassification.Confidence = *confidence * result.DepthClassification.Coverage
	}
	globalDepths, err := db.globalOceanRelativeDepths(ctx)
	if err != nil {
		return err
	}
	shallowThreshold := weightedPhysicalQuantile(globalDepths, 0.33)
	deepThreshold := weightedPhysicalQuantile(globalDepths, 0.67)
	result.DepthClassification.ShallowThreshold = shallowThreshold
	result.DepthClassification.DeepThreshold = deepThreshold
	if result.DepthClassification.Coverage < 0.80 || shallowThreshold == nil || deepThreshold == nil || validSamples == 0 {
		return nil
	}
	var shallowWeight, transitionWeight, deepWeight int64
	for _, value := range depths {
		switch {
		case value.Value <= *shallowThreshold:
			shallowWeight += value.Weight
		case value.Value >= *deepThreshold:
			deepWeight += value.Weight
		default:
			transitionWeight += value.Weight
		}
	}
	denominator := float64(shallowWeight + transitionWeight + deepWeight)
	if denominator == 0 {
		return nil
	}
	result.DepthClassification.ShallowShare = float64(shallowWeight) / denominator
	result.DepthClassification.TransitionShare = float64(transitionWeight) / denominator
	result.DepthClassification.DeepShare = float64(deepWeight) / denominator
	switch {
	case result.DepthClassification.ShallowShare >= 0.60:
		result.DepthClassification.Verdict = "predominantly_shallow"
	case result.DepthClassification.DeepShare >= 0.60:
		result.DepthClassification.Verdict = "predominantly_deep"
	default:
		result.DepthClassification.Verdict = "mixed"
	}
	return nil
}

func (db *DB) globalOceanRelativeDepths(ctx context.Context) ([]mapWeightedPhysicalValue, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT g.relative_depth_mean,g.sample_count
		FROM map_province_physical g JOIN map_provinces p ON p.province_id=g.province_id
		WHERE p.water_kind IN ('sea','coastal_sea','impassable_sea') AND g.relative_depth_mean IS NOT NULL
		ORDER BY g.relative_depth_mean,g.province_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []mapWeightedPhysicalValue
	for rows.Next() {
		var value mapWeightedPhysicalValue
		if err := rows.Scan(&value.Value, &value.Weight); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func weightedPhysicalMean(values []mapWeightedPhysicalValue) *float64 {
	var weighted float64
	var total int64
	for _, value := range values {
		if value.Weight <= 0 {
			continue
		}
		weighted += value.Value * float64(value.Weight)
		total += value.Weight
	}
	if total == 0 {
		return nil
	}
	mean := weighted / float64(total)
	return &mean
}

func weightedPhysicalQuantile(values []mapWeightedPhysicalValue, q float64) *float64 {
	filtered := append([]mapWeightedPhysicalValue(nil), values...)
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Value < filtered[j].Value })
	var total int64
	for _, value := range filtered {
		if value.Weight > 0 {
			total += value.Weight
		}
	}
	if total == 0 {
		return nil
	}
	target := int64(math.Round(q * float64(total-1)))
	var seen int64
	for _, value := range filtered {
		if value.Weight <= 0 {
			continue
		}
		seen += value.Weight
		if seen > target {
			result := value.Value
			return &result
		}
	}
	result := filtered[len(filtered)-1].Value
	return &result
}

func physicalWaterRepresentatives(rows []mapAdjacentPhysicalWaterRow, limit int) []MapPhysicalWaterRepresentative {
	if limit <= 0 {
		return nil
	}
	var ocean []mapAdjacentPhysicalWaterRow
	for _, row := range rows {
		if isOceanPhysicalWaterKind(row.WaterKind) && !row.MajorRiver {
			ocean = append(ocean, row)
		}
	}
	selected := make([]mapAdjacentPhysicalWaterRow, 0, minInt(limit, len(ocean)))
	seen := map[int]bool{}
	addFirst := func(sorted []mapAdjacentPhysicalWaterRow) {
		for _, row := range sorted {
			if !seen[row.ProvinceID] && len(selected) < limit {
				seen[row.ProvinceID] = true
				selected = append(selected, row)
				return
			}
		}
	}
	byBoundary := append([]mapAdjacentPhysicalWaterRow(nil), ocean...)
	sort.Slice(byBoundary, func(i, j int) bool {
		if byBoundary[i].BoundaryPixels != byBoundary[j].BoundaryPixels {
			return byBoundary[i].BoundaryPixels > byBoundary[j].BoundaryPixels
		}
		return byBoundary[i].ProvinceID < byBoundary[j].ProvinceID
	})
	byShallow := append([]mapAdjacentPhysicalWaterRow(nil), ocean...)
	sort.Slice(byShallow, func(i, j int) bool {
		return physicalOptionalLess(byShallow[i].RelativeDepth, byShallow[j].RelativeDepth, byShallow[i].ProvinceID, byShallow[j].ProvinceID)
	})
	byDeep := append([]mapAdjacentPhysicalWaterRow(nil), ocean...)
	sort.Slice(byDeep, func(i, j int) bool {
		return physicalOptionalGreater(byDeep[i].RelativeDepth, byDeep[j].RelativeDepth, byDeep[i].ProvinceID, byDeep[j].ProvinceID)
	})
	byShelf := append([]mapAdjacentPhysicalWaterRow(nil), ocean...)
	sort.Slice(byShelf, func(i, j int) bool {
		return physicalOptionalGreater(byShelf[i].ShelfScore, byShelf[j].ShelfScore, byShelf[i].ProvinceID, byShelf[j].ProvinceID)
	})
	addFirst(byBoundary)
	addFirst(byShallow)
	addFirst(byDeep)
	addFirst(byShelf)
	for len(selected) < limit {
		before := len(selected)
		addFirst(byBoundary)
		if len(selected) == before {
			break
		}
	}
	out := make([]MapPhysicalWaterRepresentative, 0, len(selected))
	for _, row := range selected {
		out = append(out, MapPhysicalWaterRepresentative{
			ProvinceID: row.ProvinceID, WaterKind: row.WaterKind, BoundaryPixels: row.BoundaryPixels,
			RelativeDepth: row.RelativeDepth, RelativeDepthMax: row.RelativeDepthMax, ShelfScore: row.ShelfScore,
			TrenchScore: row.TrenchScore, WaterBodyID: row.WaterBodyID,
		})
	}
	return out
}

func physicalOptionalLess(a, b *float64, aid, bid int) bool {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return aid < bid
		}
		return a != nil
	}
	if *a != *b {
		return *a < *b
	}
	return aid < bid
}

func physicalOptionalGreater(a, b *float64, aid, bid int) bool {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return aid < bid
		}
		return a != nil
	}
	if *a != *b {
		return *a > *b
	}
	return aid < bid
}

func isOceanPhysicalWaterKind(kind string) bool {
	return kind == "sea" || kind == "coastal_sea" || kind == "impassable_sea"
}

func (db *DB) physicalAggregate(ctx context.Context, ids []int) (MapPhysicalAggregate, error) {
	placeholders := sqlPlaceholders(len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var out MapPhysicalAggregate
	var elevation, slope, rugged, depthMean, depthMax sql.NullFloat64
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(sample_count),0),AVG(elevation_mean),AVG(slope_mean),AVG(ruggedness_mean),AVG(relative_depth_mean),MAX(relative_depth_max),COALESCE(SUM(river_pixel_count),0),COALESCE(SUM(major_river),0),COALESCE(SUM(major_river_mouth),0) FROM map_province_physical WHERE province_id IN (`+placeholders+`)`, args...).Scan(
		&out.ProvinceCount, &out.SampleCount, &elevation, &slope, &rugged, &depthMean, &depthMax, &out.OrdinaryRiverPixels, &out.MajorRiverProvinces, &out.MajorRiverMouths)
	if err != nil {
		return out, err
	}
	out.ElevationMean, out.SlopeMean, out.RuggednessMean = nullableFloatPtr(elevation), nullableFloatPtr(slope), nullableFloatPtr(rugged)
	out.RelativeDepthMean, out.RelativeDepthMaximum = nullableFloatPtr(depthMean), nullableFloatPtr(depthMax)
	rows, err := db.sql.QueryContext(ctx, `SELECT p.water_kind,COUNT(*) FROM map_provinces p WHERE p.province_id IN (`+placeholders+`) GROUP BY p.water_kind`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind sql.NullString
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return out, err
		}
		switch kind.String {
		case "sea", "coastal_sea":
			out.OceanProvinces += count
		case "impassable_sea":
			out.OceanProvinces += count
			out.ImpassableSeaProvinces += count
		case "lake":
			out.LakeProvinces += count
		}
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	bodyRows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT water_body_id FROM map_physical_water_body_provinces WHERE province_id IN (`+placeholders+`) ORDER BY water_body_id`, args...)
	if err != nil {
		return out, err
	}
	defer bodyRows.Close()
	for bodyRows.Next() {
		var id int
		if err := bodyRows.Scan(&id); err != nil {
			return out, err
		}
		out.WaterBodyCount++
		if len(out.WaterBodyIDs) < 16 {
			out.WaterBodyIDs = append(out.WaterBodyIDs, id)
		}
	}
	return out, bodyRows.Err()
}

func (db *DB) cachedGISSidecarStatus(ctx context.Context) GISSidecarStatus {
	status := GISSidecarStatus{
		Enabled:        db.metaValueOrEmpty(ctx, "map_gis_sidecar_enabled") == "true",
		Available:      db.metaValueOrEmpty(ctx, "map_gis_sidecar_available") == "true",
		Platform:       db.metaValueOrEmpty(ctx, "map_gis_sidecar_platform"),
		Version:        db.metaValueOrEmpty(ctx, "map_gis_sidecar_version"),
		SHA256:         db.metaValueOrEmpty(ctx, "map_gis_sidecar_sha256"),
		Analysis:       db.metaValueOrEmpty(ctx, "map_gis_analysis"),
		Reason:         db.metaValueOrEmpty(ctx, "map_gis_sidecar_reason"),
		AnalysisStatus: db.metaValueOrEmpty(ctx, "map_gis_advanced_status"),
	}
	if reason := db.metaValueOrEmpty(ctx, "map_gis_advanced_reason"); reason != "" {
		status.Reason = reason
	}
	status.AllowedTools = strings.FieldsFunc(db.metaValueOrEmpty(ctx, "map_gis_allowed_tools"), func(r rune) bool { return r == ',' })
	return status
}

func (db *DB) metaValueOrEmpty(ctx context.Context, key string) string {
	value, _ := db.metaValue(ctx, key)
	return value
}
func nullableFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	v := value.Float64
	return &v
}
func nullableIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}
func uniqueSortedInts(values []int) []int {
	sort.Ints(values)
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}
