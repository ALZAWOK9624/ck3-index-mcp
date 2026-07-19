package indexer

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"math"
	"sort"
	"strconv"
	"strings"
)

const mapGISCacheVersion = "map_gis_v1"

type mapGISAccumulator struct {
	Count                             int
	Min, Max, Sum                     float64
	SlopeSum, SlopeMax, RuggednessSum float64
	CurvatureSum, RidgeSum, ValleySum float64
	AspectSin, AspectCos              float64
	DepthSum, DepthMax                float64
	DepthCount, RiverPixels           int
	Histogram                         [256]int
}

type mapGISProvinceStats struct {
	ProvinceID                                  int
	Count                                       int
	ElevationMin, ElevationMean, ElevationMax   float64
	ElevationP10, ElevationMedian, ElevationP90 float64
	SlopeMean, SlopeMax, RuggednessMean         float64
	AspectDegrees, CurvatureMean                float64
	RidgeScore, ValleyScore                     float64
	RelativeDepthMean, RelativeDepthMax         *float64
	SeabedSlope, SeabedRuggedness               *float64
	ShelfScore, TrenchScore                     *float64
	CoastalDropoff, StraitSillDepth             *float64
	WaterBodyID                                 *int
	RiverPixels                                 int
	MajorRiver, MajorRiverMouth                 bool
	MajorRiverWidthProxy                        *float64
	CatchmentPixels, FlowPercentile             *float64
	RiverOrder                                  *int
	Confidence                                  float64
}

type mapGISWaterBody struct {
	ID, Area, Impassable int
	Kind                 string
	ProvinceIDs          []int
	SurfaceReference     *float64
	SurfaceMethod        string
	Confidence           float64
}

func rebuildMapGISCache(ctx context.Context, tx *sql.Tx, cfg Config, active map[string]activeMapFile, provinces map[int]*mapProvinceBuild, adj map[[2]int]int, geometryFingerprint string) error {
	status := InspectGISSidecar(ctx, cfg)
	writeGISStatusMeta(ctx, tx, status)
	if !cfg.GISEnabled {
		return clearMapGISTables(ctx, tx)
	}
	heightFile := active["map_data/heightmap.png"]
	if heightFile.Path == "" {
		if err := clearMapGISTables(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_gis_unavailable_reason','heightmap.png is not available in the active map') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
		return err
	}
	paths := []string{heightFile.Path}
	for _, rel := range []string{"map_data/rivers.png", "map_data/default.map", "map_data/adjacencies.csv", "map_data/provinces.png", "map_data/definition.csv"} {
		if file := active[rel]; file.Path != "" {
			paths = append(paths, file.Path)
		}
	}
	assetFingerprint, err := mapGeometryFingerprint(paths...)
	if err != nil {
		return err
	}
	fingerprint := strings.Join([]string{mapGISCacheVersion, geometryFingerprint, assetFingerprint, cfg.GISAnalysis, status.SHA256}, ":")
	var cached string
	var cachedAdvanced string
	var rows int
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_gis_fingerprint'`).Scan(&cached)
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_gis_advanced_status'`).Scan(&cachedAdvanced)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_province_physical`).Scan(&rows)
	advancedComplete := cfg.GISAnalysis != "full" || !status.Available || cachedAdvanced == "ready"
	if cached == fingerprint && rows > 0 && advancedComplete {
		return nil
	}

	heightmap, err := decodeMapImage(heightFile.Path)
	if err != nil {
		return fmt.Errorf("GIS heightmap: %w", err)
	}
	fillRuns, boundaryRuns, err := loadGISProvinceRuns(ctx, tx)
	if err != nil {
		return err
	}
	bodies, provinceBody := buildGISWaterBodies(provinces, adj)
	estimateWaterSurfaceReferences(heightmap, provinces, boundaryRuns, bodies, provinceBody)
	var riverRaster *mapPhysicalRaster
	if active["map_data/rivers.png"].Path != "" {
		riverRaster, _ = loadPhysicalRasterTx(ctx, tx, "rivers")
	}
	stats := make(map[int]*mapGISProvinceStats, len(provinces))
	for id, province := range provinces {
		if province == nil || province.Area <= 0 {
			continue
		}
		var surface *float64
		if bodyID, ok := provinceBody[id]; ok {
			surface = bodies[bodyID].SurfaceReference
		}
		stats[id] = calculateGISProvinceStats(heightmap, riverRaster, fillRuns[id], id, surface)
		if bodyID, ok := provinceBody[id]; ok {
			stats[id].WaterBodyID = intPtr(bodyID)
		}
		if province.WaterKind == "river" {
			stats[id].MajorRiver = true
			width := 2 * float64(province.Area) / math.Max(1, float64(province.Perimeter))
			stats[id].MajorRiverWidthProxy = &width
		}
	}
	applyGISWaterBodyClassifications(stats, bodies)
	applyGISCoastalMetrics(stats, provinces, adj)
	riverEdges := buildGISMajorRiverEdges(stats, provinces, adj)
	advancedStatus := "sidecar_unavailable"
	advancedReason := status.Reason
	if status.Available {
		if cfg.GISAnalysis == "full" {
			if err := applyWhiteboxHydrology(ctx, cfg, assetFingerprint, heightmap, fillRuns, provinces, stats); err != nil {
				advancedStatus = "failed"
				advancedReason = sanitizeGISReason(err.Error())
			} else {
				advancedStatus = "ready"
				advancedReason = ""
			}
		} else {
			advancedStatus = "terrain_only"
			advancedReason = "Full hydrology is disabled by gis_analysis=terrain."
		}
	}

	if err := clearMapGISTables(ctx, tx); err != nil {
		return err
	}
	if err := insertGISWaterBodies(ctx, tx, bodies, stats, fingerprint); err != nil {
		return err
	}
	if err := insertGISProvinceStats(ctx, tx, stats, fingerprint); err != nil {
		return err
	}
	if err := insertGISMajorRiverEdges(ctx, tx, riverEdges); err != nil {
		return err
	}
	meta := map[string]string{
		"map_gis_fingerprint":            fingerprint,
		"map_gis_algorithm":              "ck3-index-heightmap-zonal-v1",
		"map_gis_units":                  "normalized_height_and_pixels",
		"map_gis_province_count":         strconv.Itoa(len(stats)),
		"map_gis_water_body_count":       strconv.Itoa(len(bodies)),
		"map_gis_major_river_edge_count": strconv.Itoa(len(riverEdges)),
	}
	meta["map_gis_advanced_status"] = advancedStatus
	meta["map_gis_advanced_reason"] = advancedReason
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_gis_build_count','1') ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func sanitizeGISReason(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	parts := strings.Fields(value)
	for i, part := range parts {
		if strings.Contains(part, ":/") || strings.HasPrefix(part, "/") {
			parts[i] = "<redacted-path>"
		}
	}
	value = strings.Join(parts, " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func writeGISStatusMeta(ctx context.Context, tx *sql.Tx, status GISSidecarStatus) {
	values := map[string]string{
		"map_gis_sidecar_enabled":   strconv.FormatBool(status.Enabled),
		"map_gis_sidecar_available": strconv.FormatBool(status.Available),
		"map_gis_sidecar_platform":  status.Platform,
		"map_gis_sidecar_version":   status.Version,
		"map_gis_sidecar_sha256":    status.SHA256,
		"map_gis_sidecar_reason":    status.Reason,
		"map_gis_analysis":          status.Analysis,
		"map_gis_allowed_tools":     strings.Join(status.AllowedTools, ","),
	}
	for key, value := range values {
		_, _ = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	}
}

func clearMapGISTables(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"map_major_river_edges", "map_physical_water_body_provinces", "map_physical_water_bodies", "map_province_physical"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	return nil
}

func loadGISProvinceRuns(ctx context.Context, tx *sql.Tx) (map[int][]MapRun, map[int][]MapRun, error) {
	fill, boundary := map[int][]MapRun{}, map[int][]MapRun{}
	rows, err := tx.QueryContext(ctx, `SELECT province_id,fill_rle,boundary_rle FROM map_province_geometry`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var fillBlob, boundaryBlob []byte
		if err := rows.Scan(&id, &fillBlob, &boundaryBlob); err != nil {
			return nil, nil, err
		}
		fill[id], err = DecodeMapRuns(fillBlob)
		if err != nil {
			return nil, nil, err
		}
		boundary[id], err = DecodeMapRuns(boundaryBlob)
		if err != nil {
			return nil, nil, err
		}
	}
	return fill, boundary, rows.Err()
}

func loadPhysicalRasterTx(ctx context.Context, tx *sql.Tx, key string) (*mapPhysicalRaster, error) {
	var data []byte
	var width, height int
	if err := tx.QueryRowContext(ctx, `SELECT width,height,data FROM map_physical_rasters WHERE layer_key=?`, key).Scan(&width, &height, &data); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	gray := image.NewGray(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			gray.Set(x, y, img.At(x, y))
		}
	}
	return &mapPhysicalRaster{Width: width, Height: height, Image: gray}, nil
}

func buildGISWaterBodies(provinces map[int]*mapProvinceBuild, adj map[[2]int]int) (map[int]*mapGISWaterBody, map[int]int) {
	kind := func(p *mapProvinceBuild) string {
		if p == nil {
			return ""
		}
		switch p.WaterKind {
		case "sea", "coastal_sea", "impassable_sea":
			return "ocean"
		case "lake":
			return "lake"
		default:
			return ""
		}
	}
	neighbors := map[int][]int{}
	for pair := range adj {
		if kind(provinces[pair[0]]) != "" && kind(provinces[pair[0]]) == kind(provinces[pair[1]]) {
			neighbors[pair[0]] = append(neighbors[pair[0]], pair[1])
			neighbors[pair[1]] = append(neighbors[pair[1]], pair[0])
		}
	}
	ids := make([]int, 0, len(provinces))
	for id, p := range provinces {
		if kind(p) != "" && p.Area > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	bodies := map[int]*mapGISWaterBody{}
	provinceBody, visited := map[int]int{}, map[int]bool{}
	for _, seed := range ids {
		if visited[seed] {
			continue
		}
		body := &mapGISWaterBody{ID: seed, Kind: kind(provinces[seed]), SurfaceMethod: "coastal_boundary_median", Confidence: 0.72}
		queue := []int{seed}
		visited[seed] = true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			provinceBody[id] = body.ID
			body.ProvinceIDs = append(body.ProvinceIDs, id)
			body.Area += provinces[id].Area
			if provinces[id].WaterKind == "impassable_sea" {
				body.Impassable++
			}
			for _, next := range neighbors[id] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		if body.Kind == "lake" {
			body.SurfaceMethod = "local_lake_boundary_median"
		}
		sort.Ints(body.ProvinceIDs)
		bodies[body.ID] = body
	}
	return bodies, provinceBody
}

func estimateWaterSurfaceReferences(heightmap image.Image, provinces map[int]*mapProvinceBuild, boundaries map[int][]MapRun, bodies map[int]*mapGISWaterBody, provinceBody map[int]int) {
	histograms := map[int]*[256]int{}
	for id, bodyID := range provinceBody {
		province := provinces[id]
		if province == nil {
			continue
		}
		hist := histograms[bodyID]
		if hist == nil {
			hist = &[256]int{}
			histograms[bodyID] = hist
		}
		for _, run := range boundaries[id] {
			for x := int(run.X0); x <= int(run.X1); x++ {
				value := heightSample(heightmap, x, int(run.Y))
				hist[clampByte(value)]++
			}
		}
	}
	for id, body := range bodies {
		hist := histograms[id]
		if hist == nil || histogramCount(hist) == 0 {
			body.Confidence = 0.25
			body.SurfaceMethod = "unavailable"
			continue
		}
		value := histogramQuantile(hist, 0.5)
		body.SurfaceReference = &value
	}
}

func calculateGISProvinceStats(heightmap image.Image, rivers *mapPhysicalRaster, runs []MapRun, id int, surface *float64) *mapGISProvinceStats {
	acc := mapGISAccumulator{Min: 1, Max: 0}
	b := heightmap.Bounds()
	for _, run := range runs {
		y := int(run.Y)
		for x := int(run.X0); x <= int(run.X1); x++ {
			h := heightSample(heightmap, x, y)
			dx := (heightSample(heightmap, x+1, y) - heightSample(heightmap, x-1, y)) / 2
			dy := (heightSample(heightmap, x, y+1) - heightSample(heightmap, x, y-1)) / 2
			slope := math.Hypot(dx, dy)
			localMin, localMax, neighborSum := h, h, 0.0
			for _, delta := range [][2]int{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}} {
				v := heightSample(heightmap, x+delta[0], y+delta[1])
				neighborSum += v
				localMin, localMax = math.Min(localMin, v), math.Max(localMax, v)
			}
			curvature := h - neighborSum/8
			acc.Count++
			acc.Min, acc.Max, acc.Sum = math.Min(acc.Min, h), math.Max(acc.Max, h), acc.Sum+h
			acc.SlopeSum += slope
			acc.SlopeMax = math.Max(acc.SlopeMax, slope)
			acc.RuggednessSum += localMax - localMin
			acc.CurvatureSum += curvature
			acc.RidgeSum += math.Max(0, curvature)
			acc.ValleySum += math.Max(0, -curvature)
			aspect := math.Atan2(-dx, dy)
			acc.AspectSin += math.Sin(aspect) * slope
			acc.AspectCos += math.Cos(aspect) * slope
			acc.Histogram[clampByte(h)]++
			if surface != nil {
				depth := math.Max(0, *surface-h)
				acc.DepthSum += depth
				acc.DepthMax = math.Max(acc.DepthMax, depth)
				acc.DepthCount++
			}
			if rivers != nil && b.Dx() > 0 && b.Dy() > 0 {
				rx := int(float64(x-b.Min.X) * float64(rivers.Width) / float64(b.Dx()))
				ry := int(float64(y-b.Min.Y) * float64(rivers.Height) / float64(b.Dy()))
				if rx >= 0 && rx < rivers.Width && ry >= 0 && ry < rivers.Height && rivers.Image.GrayAt(rx, ry).Y > 0 {
					acc.RiverPixels++
				}
			}
		}
	}
	stats := &mapGISProvinceStats{ProvinceID: id, Count: acc.Count, RiverPixels: acc.RiverPixels, Confidence: 0.88}
	if acc.Count == 0 {
		stats.Confidence = 0
		return stats
	}
	denom := float64(acc.Count)
	stats.ElevationMin, stats.ElevationMean, stats.ElevationMax = acc.Min, acc.Sum/denom, acc.Max
	stats.ElevationP10 = histogramQuantile(&acc.Histogram, 0.10)
	stats.ElevationMedian = histogramQuantile(&acc.Histogram, 0.50)
	stats.ElevationP90 = histogramQuantile(&acc.Histogram, 0.90)
	stats.SlopeMean, stats.SlopeMax = acc.SlopeSum/denom, acc.SlopeMax
	stats.RuggednessMean, stats.CurvatureMean = acc.RuggednessSum/denom, acc.CurvatureSum/denom
	stats.RidgeScore, stats.ValleyScore = acc.RidgeSum/denom, acc.ValleySum/denom
	stats.AspectDegrees = math.Mod(math.Atan2(acc.AspectSin, acc.AspectCos)*180/math.Pi+360, 360)
	if acc.DepthCount > 0 {
		mean, maxDepth := acc.DepthSum/float64(acc.DepthCount), acc.DepthMax
		stats.RelativeDepthMean, stats.RelativeDepthMax = &mean, &maxDepth
		stats.SeabedSlope, stats.SeabedRuggedness = floatPtr(stats.SlopeMean), floatPtr(stats.RuggednessMean)
	}
	return stats
}

func applyGISWaterBodyClassifications(stats map[int]*mapGISProvinceStats, bodies map[int]*mapGISWaterBody) {
	for _, body := range bodies {
		maxDepth, depthSum, slopeSum, count := 0.0, 0.0, 0.0, 0
		for _, id := range body.ProvinceIDs {
			stat := stats[id]
			if stat == nil || stat.RelativeDepthMean == nil {
				continue
			}
			maxDepth = math.Max(maxDepth, *stat.RelativeDepthMax)
			depthSum += *stat.RelativeDepthMean
			slopeSum += stat.SlopeMean
			count++
		}
		meanDepth := 0.0
		if count > 0 {
			meanDepth = depthSum / float64(count)
		}
		for _, id := range body.ProvinceIDs {
			stat := stats[id]
			if stat == nil || stat.RelativeDepthMean == nil {
				continue
			}
			depthRatio := 0.0
			if maxDepth > 0 {
				depthRatio = *stat.RelativeDepthMean / maxDepth
			}
			slopeRatio := stat.SlopeMean / math.Max(0.000001, slopeSum/math.Max(1, float64(count)))
			shelf := clamp01((1-depthRatio)*0.78 + (1-clamp01(slopeRatio/3))*0.22)
			trench := clamp01(depthRatio*0.82 + clamp01(stat.RuggednessMean*40)*0.18)
			stat.ShelfScore, stat.TrenchScore = &shelf, &trench
			if body.Kind == "lake" {
				stat.Confidence = math.Min(stat.Confidence, body.Confidence)
			}
		}
		_ = meanDepth
	}
}

func applyGISCoastalMetrics(stats map[int]*mapGISProvinceStats, provinces map[int]*mapProvinceBuild, adj map[[2]int]int) {
	landWater := map[int][]int{}
	for pair := range adj {
		a, b := provinces[pair[0]], provinces[pair[1]]
		if a == nil || b == nil {
			continue
		}
		if a.BlockKind != "water" && b.BlockKind == "water" && b.WaterKind != "river" {
			landWater[a.ID] = append(landWater[a.ID], b.ID)
		} else if b.BlockKind != "water" && a.BlockKind == "water" && a.WaterKind != "river" {
			landWater[b.ID] = append(landWater[b.ID], a.ID)
		}
	}
	for landID, waterIDs := range landWater {
		land := stats[landID]
		if land == nil {
			continue
		}
		maxDrop := 0.0
		for _, waterID := range waterIDs {
			water := stats[waterID]
			if water != nil {
				maxDrop = math.Max(maxDrop, math.Abs(land.ElevationMean-water.ElevationMean))
			}
		}
		land.CoastalDropoff = &maxDrop
	}
	for id, p := range provinces {
		if p == nil || (p.WaterKind != "sea" && p.WaterKind != "coastal_sea" && p.WaterKind != "impassable_sea") {
			continue
		}
		landNeighbors := 0
		for pair := range adj {
			other := 0
			if pair[0] == id {
				other = pair[1]
			} else if pair[1] == id {
				other = pair[0]
			}
			if other != 0 && provinces[other] != nil && provinces[other].BlockKind != "water" {
				landNeighbors++
			}
		}
		if landNeighbors >= 2 && stats[id] != nil && stats[id].RelativeDepthMean != nil {
			value := *stats[id].RelativeDepthMean
			stats[id].StraitSillDepth = &value
		}
	}
}

type mapGISRiverEdge struct {
	From, To, Border int
	Relation         string
	Confidence       float64
}

func buildGISMajorRiverEdges(stats map[int]*mapGISProvinceStats, provinces map[int]*mapProvinceBuild, adj map[[2]int]int) []mapGISRiverEdge {
	var edges []mapGISRiverEdge
	for pair, border := range adj {
		a, b := provinces[pair[0]], provinces[pair[1]]
		if a == nil || b == nil {
			continue
		}
		if a.WaterKind == "river" && b.WaterKind == "river" {
			from, to, confidence := a.ID, b.ID, 0.55
			relation := "connected"
			if stats[from] != nil && stats[to] != nil && math.Abs(stats[from].ElevationMean-stats[to].ElevationMean) > 1.0/255 {
				if stats[from].ElevationMean < stats[to].ElevationMean {
					from, to = to, from
				}
				relation, confidence = "downstream", 0.78
			}
			edges = append(edges, mapGISRiverEdge{From: from, To: to, Border: border, Relation: relation, Confidence: confidence})
		}
		if a.WaterKind == "river" && isOceanWaterKind(b.WaterKind) {
			stats[a.ID].MajorRiverMouth = true
		}
		if b.WaterKind == "river" && isOceanWaterKind(a.WaterKind) {
			stats[b.ID].MajorRiverMouth = true
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

func insertGISProvinceStats(ctx context.Context, tx *sql.Tx, stats map[int]*mapGISProvinceStats, fingerprint string) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO map_province_physical(
		province_id,sample_count,elevation_min,elevation_mean,elevation_max,elevation_p10,elevation_median,elevation_p90,
		slope_mean,slope_max,ruggedness_mean,aspect_degrees,curvature_mean,ridge_score,valley_score,
		relative_depth_mean,relative_depth_max,seabed_slope,seabed_ruggedness,shelf_score,trench_score,coastal_dropoff,strait_sill_depth,
		water_body_id,river_pixel_count,major_river,major_river_width_proxy,major_river_mouth,catchment_pixels,flow_percentile,river_order,provenance,algorithm,confidence,fingerprint)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ids := make([]int, 0, len(stats))
	for id := range stats {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		s := stats[id]
		if _, err := stmt.ExecContext(ctx, s.ProvinceID, s.Count, s.ElevationMin, s.ElevationMean, s.ElevationMax, s.ElevationP10, s.ElevationMedian, s.ElevationP90,
			s.SlopeMean, s.SlopeMax, s.RuggednessMean, s.AspectDegrees, s.CurvatureMean, s.RidgeScore, s.ValleyScore,
			nullFloat(s.RelativeDepthMean), nullFloat(s.RelativeDepthMax), nullFloat(s.SeabedSlope), nullFloat(s.SeabedRuggedness), nullFloat(s.ShelfScore), nullFloat(s.TrenchScore), nullFloat(s.CoastalDropoff), nullFloat(s.StraitSillDepth),
			nullIntPtr(s.WaterBodyID), s.RiverPixels, boolInt(s.MajorRiver), nullFloat(s.MajorRiverWidthProxy), boolInt(s.MajorRiverMouth), nullFloat(s.CatchmentPixels), nullFloat(s.FlowPercentile), nullIntPtr(s.RiverOrder), "derived", "ck3-index-heightmap-zonal-v1", s.Confidence, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

func insertGISWaterBodies(ctx context.Context, tx *sql.Tx, bodies map[int]*mapGISWaterBody, stats map[int]*mapGISProvinceStats, fingerprint string) error {
	bodyStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_physical_water_bodies(water_body_id,kind,province_count,area_pixels,surface_reference,surface_method,mean_relative_depth,max_relative_depth,mean_seabed_slope,impassable_province_count,confidence,fingerprint) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer bodyStmt.Close()
	memberStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_physical_water_body_provinces(water_body_id,province_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer memberStmt.Close()
	ids := make([]int, 0, len(bodies))
	for id := range bodies {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		body := bodies[id]
		depthSum, maxDepth, slopeSum, count := 0.0, 0.0, 0.0, 0
		for _, pid := range body.ProvinceIDs {
			if s := stats[pid]; s != nil && s.RelativeDepthMean != nil {
				depthSum += *s.RelativeDepthMean
				maxDepth = math.Max(maxDepth, *s.RelativeDepthMax)
				slopeSum += s.SlopeMean
				count++
			}
			if _, err := memberStmt.ExecContext(ctx, id, pid); err != nil {
				return err
			}
		}
		meanDepth, meanSlope := 0.0, 0.0
		if count > 0 {
			meanDepth, meanSlope = depthSum/float64(count), slopeSum/float64(count)
		}
		if _, err := bodyStmt.ExecContext(ctx, id, body.Kind, len(body.ProvinceIDs), body.Area, nullFloat(body.SurfaceReference), body.SurfaceMethod, meanDepth, maxDepth, meanSlope, body.Impassable, body.Confidence, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

func insertGISMajorRiverEdges(ctx context.Context, tx *sql.Tx, edges []mapGISRiverEdge) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO map_major_river_edges(from_province,to_province,relation,border_len,confidence) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, edge := range edges {
		if _, err := stmt.ExecContext(ctx, edge.From, edge.To, edge.Relation, edge.Border, edge.Confidence); err != nil {
			return err
		}
	}
	return nil
}

func histogramCount(hist *[256]int) int {
	total := 0
	for _, count := range hist {
		total += count
	}
	return total
}

func histogramQuantile(hist *[256]int, q float64) float64 {
	total := histogramCount(hist)
	if total == 0 {
		return 0
	}
	target := int(math.Round(q * float64(total-1)))
	seen := 0
	for i, count := range hist {
		seen += count
		if seen > target {
			return float64(i) / 255
		}
	}
	return 1
}

func clampByte(value float64) int     { return int(math.Round(clamp01(value) * 255)) }
func clamp01(value float64) float64   { return math.Max(0, math.Min(1, value)) }
func floatPtr(value float64) *float64 { return &value }
func intPtr(value int) *int           { return &value }
func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
func isOceanWaterKind(kind string) bool {
	return kind == "sea" || kind == "coastal_sea" || kind == "impassable_sea"
}
