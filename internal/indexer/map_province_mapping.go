package indexer

import (
	"context"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "golang.org/x/image/bmp"
)

const (
	defaultMapMappingMinShare      = 0.05
	defaultMapMappingMaxCandidates = 5
	mapWarpTileSize                = 64
)

// MapProvinceMappingSpec compares two coherent map snapshots from configured
// sources. Paths are deliberately not accepted: MCP callers select source
// names while the server retains control over filesystem access.
type MapProvinceMappingSpec struct {
	Source          string            `json:"source"`
	Target          string            `json:"target"`
	ControlPoints   []MapControlPoint `json:"control_points,omitempty"`
	MinShare        float64           `json:"min_share,omitempty"`
	MaxCandidates   int               `json:"max_candidates,omitempty"`
	AllowCrossWater bool              `json:"allow_cross_water,omitempty"`
}

type MapControlPoint struct {
	SourceX float64 `json:"source_x"`
	SourceY float64 `json:"source_y"`
	TargetX float64 `json:"target_x"`
	TargetY float64 `json:"target_y"`
}

type MapProvinceMappingResult struct {
	Intent              string                     `json:"intent"`
	Source              MapMappingSnapshot         `json:"source"`
	Target              MapMappingSnapshot         `json:"target"`
	ControlPointCount   int                        `json:"control_point_count"`
	TriangleCount       int                        `json:"triangle_count"`
	MinShare            float64                    `json:"min_share"`
	AllowCrossWater     bool                       `json:"allow_cross_water"`
	ComparedPixels      int64                      `json:"compared_pixels"`
	OutsideWarpPixels   int64                      `json:"outside_warp_pixels"`
	OutsideTargetPixels int64                      `json:"outside_target_pixels"`
	TypeMismatchPixels  int64                      `json:"type_mismatch_pixels"`
	Groups              []MapProvinceMappingGroup  `json:"groups,omitempty"`
	Sources             []MapProvinceMappingSource `json:"sources,omitempty"`
	TotalGroups         int                        `json:"total_groups"`
	TotalSourceRows     int                        `json:"total_source_rows"`
	Summary             MapProvinceMappingSummary  `json:"summary"`
	Guidance            []string                   `json:"guidance"`
}

type MapMappingSnapshot struct {
	Name                     string `json:"name"`
	Width                    int    `json:"width"`
	Height                   int    `json:"height"`
	ProvinceCount            int    `json:"province_count"`
	WaterCount               int    `json:"water_count"`
	WaterClassificationKnown bool   `json:"water_classification_known"`
}

type MapProvinceMappingSummary struct {
	OneToOne       int `json:"one_to_one"`
	Renumbered     int `json:"renumbered"`
	Splits         int `json:"splits"`
	Merges         int `json:"merges"`
	Complex        int `json:"complex"`
	UnmappedSource int `json:"unmapped_source"`
	UnmappedTarget int `json:"unmapped_target"`
}

type MapProvinceMappingGroup struct {
	Kind            string `json:"kind"`
	SourceProvinces []int  `json:"source_provinces"`
	TargetProvinces []int  `json:"target_provinces"`
	OverlapPixels   int64  `json:"overlap_pixels"`
}

type MapProvinceMappingSource struct {
	ProvinceID     int                           `json:"province_id"`
	Pixels         int                           `json:"pixels"`
	MappedPixels   int                           `json:"mapped_pixels"`
	Coverage       float64                       `json:"coverage"`
	Classification string                        `json:"classification"`
	Candidates     []MapProvinceMappingCandidate `json:"candidates,omitempty"`
}

type MapProvinceMappingCandidate struct {
	ProvinceID  int     `json:"province_id"`
	Pixels      int     `json:"pixels"`
	SourceShare float64 `json:"source_share"`
	TargetShare float64 `json:"target_share"`
	Confidence  float64 `json:"confidence"`
	Relation    string  `json:"relation"`
}

type provinceMappingRaster struct {
	name          string
	width, height int
	labels        []int32
	areas         map[int]int
	water         map[int]bool
	waterKnown    bool
}

type mapWarpPoint struct {
	sx, sy float64
	tx, ty float64
}

type mapWarpTriangle struct {
	a, b, c    int
	minX, minY int
	maxX, maxY int
}

type mapWarpMesh struct {
	points    []mapWarpPoint
	triangles []mapWarpTriangle
	tiles     map[[2]int][]int
}

// MapProvinceMapping builds an evidence-first province overlap graph. It does
// not write a converter mapping or modify either map snapshot.
func MapProvinceMapping(ctx context.Context, cfg Config, input MapProvinceMappingSpec) (MapProvinceMappingResult, error) {
	spec, err := normalizeMapProvinceMappingSpec(input)
	if err != nil {
		return MapProvinceMappingResult{}, err
	}
	source, err := loadConfiguredProvinceMappingRaster(cfg, spec.Source)
	if err != nil {
		return MapProvinceMappingResult{}, fmt.Errorf("source map %q: %w", spec.Source, err)
	}
	target, err := loadConfiguredProvinceMappingRaster(cfg, spec.Target)
	if err != nil {
		return MapProvinceMappingResult{}, fmt.Errorf("target map %q: %w", spec.Target, err)
	}
	points := makeMapWarpPoints(spec.ControlPoints, source.width, source.height, target.width, target.height)
	mesh, err := buildMapWarpMesh(points, source.width, source.height)
	if err != nil {
		return MapProvinceMappingResult{}, err
	}

	overlaps := map[int]map[int]int{}
	mappedBySource := map[int]int{}
	result := MapProvinceMappingResult{
		Intent: "map_province_mapping", Source: mappingSnapshot(source), Target: mappingSnapshot(target),
		ControlPointCount: len(points), TriangleCount: len(mesh.triangles), MinShare: spec.MinShare,
		AllowCrossWater: spec.AllowCrossWater,
		Guidance: []string{
			"Review split, merge, and complex groups before migrating titles or history; this operation never writes map files.",
			"source_share measures how much of the old province lands in the candidate; target_share measures how much of the new province came from the source.",
			"Add control_points when coastlines or projections moved; points outside the control-point convex hull are reported as outside_warp_pixels.",
		},
	}
	if !source.waterKnown || !target.waterKnown {
		result.Guidance = append(result.Guidance, "Water/land isolation is disabled because at least one selected source has no map_data/default.map classification.")
	}
	directScale := len(spec.ControlPoints) == 0
	for y := 0; y < source.height; y++ {
		if y&255 == 0 {
			select {
			case <-ctx.Done():
				return MapProvinceMappingResult{}, ctx.Err()
			default:
			}
		}
		row := y * source.width
		for x := 0; x < source.width; x++ {
			sourceID := int(source.labels[row+x])
			if sourceID <= 0 {
				continue
			}
			tx, ty, ok := 0.0, 0.0, true
			if directScale {
				if source.width == target.width && source.height == target.height {
					tx, ty = float64(x), float64(y)
				} else {
					tx = float64(x) * float64(target.width-1) / float64(source.width-1)
					ty = float64(y) * float64(target.height-1) / float64(source.height-1)
				}
			} else {
				tx, ty, ok = mesh.transform(float64(x), float64(y))
			}
			if !ok {
				result.OutsideWarpPixels++
				continue
			}
			targetX, targetY := int(math.Round(tx)), int(math.Round(ty))
			if targetX < 0 || targetX >= target.width || targetY < 0 || targetY >= target.height {
				result.OutsideTargetPixels++
				continue
			}
			targetID := int(target.labels[targetY*target.width+targetX])
			if targetID <= 0 {
				continue
			}
			if !spec.AllowCrossWater && source.waterKnown && target.waterKnown && source.water[sourceID] != target.water[targetID] {
				result.TypeMismatchPixels++
				continue
			}
			if overlaps[sourceID] == nil {
				overlaps[sourceID] = map[int]int{}
			}
			overlaps[sourceID][targetID]++
			mappedBySource[sourceID]++
			result.ComparedPixels++
		}
	}

	targetAreaScale := float64(source.width*source.height) / float64(target.width*target.height)
	edges := significantMapMappingEdges(overlaps, source.areas, target.areas, targetAreaScale, spec.MinShare)
	groups, sourceDegree, targetDegree, mappedTargets := buildMapMappingGroups(edges)
	result.Groups = groups
	result.Sources = buildMapMappingSources(source, mappedBySource, edges, sourceDegree, targetDegree, spec.MaxCandidates)
	result.TotalGroups = len(result.Groups)
	result.TotalSourceRows = len(result.Sources)
	result.Summary = summarizeMapMapping(groups, len(source.areas), len(target.areas), sourceDegree, mappedTargets)
	return result, nil
}

func normalizeMapProvinceMappingSpec(spec MapProvinceMappingSpec) (MapProvinceMappingSpec, error) {
	spec.Source = strings.TrimSpace(spec.Source)
	spec.Target = strings.TrimSpace(spec.Target)
	if spec.Source == "" || spec.Target == "" {
		return spec, fmt.Errorf("source and target configured source names are required")
	}
	if spec.MinShare == 0 {
		spec.MinShare = defaultMapMappingMinShare
	}
	if spec.MinShare <= 0 || spec.MinShare > 1 || math.IsNaN(spec.MinShare) || math.IsInf(spec.MinShare, 0) {
		return spec, fmt.Errorf("min_share must be greater than 0 and at most 1")
	}
	if spec.MaxCandidates == 0 {
		spec.MaxCandidates = defaultMapMappingMaxCandidates
	}
	if spec.MaxCandidates < 1 || spec.MaxCandidates > 50 {
		return spec, fmt.Errorf("max_candidates must be between 1 and 50")
	}
	if n := len(spec.ControlPoints); n > 0 && n < 3 {
		return spec, fmt.Errorf("control_points must be empty or contain at least 3 point pairs")
	}
	for i, point := range spec.ControlPoints {
		for _, value := range []float64{point.SourceX, point.SourceY, point.TargetX, point.TargetY} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return spec, fmt.Errorf("control_points[%d] contains a non-finite coordinate", i)
			}
		}
	}
	return spec, nil
}

func loadConfiguredProvinceMappingRaster(cfg Config, name string) (*provinceMappingRaster, error) {
	var provincesPath, definitionsPath, defaultMapPath string
	if strings.EqualFold(name, "active") {
		active, err := collectActiveMapFiles(cfg)
		if err != nil {
			return nil, err
		}
		provincesPath = active["map_data/provinces.png"].Path
		definitionsPath = active["map_data/definition.csv"].Path
		defaultMapPath = active["map_data/default.map"].Path
	} else {
		var root string
		for _, source := range cfg.Sources {
			if strings.EqualFold(strings.TrimSpace(source.Name), name) {
				root = source.Path
				name = source.Name
				break
			}
		}
		if root == "" {
			return nil, fmt.Errorf("configured source was not found")
		}
		definitionsPath = filepath.Join(root, "map_data", "definition.csv")
		provincesPath = filepath.Join(root, "map_data", "provinces.png")
		if _, err := os.Stat(provincesPath); os.IsNotExist(err) {
			provincesPath = filepath.Join(root, "map_data", "provinces.bmp")
		}
		defaultMapPath = filepath.Join(root, "map_data", "default.map")
	}
	if provincesPath == "" || definitionsPath == "" {
		return nil, fmt.Errorf("map_data/provinces.png and map_data/definition.csv are required")
	}
	defs, err := parseProvinceDefinitions(definitionsPath)
	if err != nil {
		return nil, fmt.Errorf("definition.csv: %w", err)
	}
	water := map[int]bool{}
	waterKnown := false
	if defaultMapPath != "" {
		if blocked, err := parseDefaultMapBlocked(defaultMapPath); err == nil {
			waterKnown = true
			for id, kind := range blocked {
				water[id] = kind.BlockKind == "water"
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("default.map: %w", err)
		}
	}
	f, err := os.Open(provincesPath)
	if err != nil {
		return nil, fmt.Errorf("provinces image: %w", err)
	}
	img, _, decodeErr := image.Decode(f)
	closeErr := f.Close()
	if decodeErr != nil {
		return nil, fmt.Errorf("provinces image: %w", decodeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 2 || height < 2 {
		return nil, fmt.Errorf("provinces image must be at least 2x2 pixels")
	}
	labels := make([]int32, width*height)
	areas := map[int]int{}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			key := uint32(uint8(r>>8))<<16 | uint32(uint8(g>>8))<<8 | uint32(uint8(b>>8))
			id := defs[key]
			if id > math.MaxInt32 {
				return nil, fmt.Errorf("province id %d exceeds supported 32-bit label range", id)
			}
			labels[y*width+x] = int32(id)
			if id > 0 {
				areas[id]++
			}
		}
	}
	if len(areas) == 0 {
		return nil, fmt.Errorf("provinces image contains no colors defined by definition.csv")
	}
	return &provinceMappingRaster{name: name, width: width, height: height, labels: labels, areas: areas, water: water, waterKnown: waterKnown}, nil
}

func mappingSnapshot(raster *provinceMappingRaster) MapMappingSnapshot {
	waterCount := 0
	for id := range raster.areas {
		if raster.water[id] {
			waterCount++
		}
	}
	return MapMappingSnapshot{Name: raster.name, Width: raster.width, Height: raster.height, ProvinceCount: len(raster.areas), WaterCount: waterCount, WaterClassificationKnown: raster.waterKnown}
}

func makeMapWarpPoints(input []MapControlPoint, sourceWidth, sourceHeight, targetWidth, targetHeight int) []mapWarpPoint {
	if len(input) == 0 {
		return []mapWarpPoint{
			{sx: 0, sy: 0, tx: 0, ty: 0},
			{sx: float64(sourceWidth - 1), sy: 0, tx: float64(targetWidth - 1), ty: 0},
			{sx: float64(sourceWidth - 1), sy: float64(sourceHeight - 1), tx: float64(targetWidth - 1), ty: float64(targetHeight - 1)},
			{sx: 0, sy: float64(sourceHeight - 1), tx: 0, ty: float64(targetHeight - 1)},
		}
	}
	out := make([]mapWarpPoint, len(input))
	for i, point := range input {
		out[i] = mapWarpPoint{sx: point.SourceX, sy: point.SourceY, tx: point.TargetX, ty: point.TargetY}
	}
	return out
}

func buildMapWarpMesh(points []mapWarpPoint, width, height int) (*mapWarpMesh, error) {
	triangles, err := delaunayMapWarp(points)
	if err != nil {
		return nil, err
	}
	mesh := &mapWarpMesh{points: points, tiles: map[[2]int][]int{}}
	for _, triangle := range triangles {
		p1, p2, p3 := points[triangle[0]], points[triangle[1]], points[triangle[2]]
		minX := maxInt(0, int(math.Floor(math.Min(p1.sx, math.Min(p2.sx, p3.sx)))))
		minY := maxInt(0, int(math.Floor(math.Min(p1.sy, math.Min(p2.sy, p3.sy)))))
		maxX := minInt(width-1, int(math.Ceil(math.Max(p1.sx, math.Max(p2.sx, p3.sx)))))
		maxY := minInt(height-1, int(math.Ceil(math.Max(p1.sy, math.Max(p2.sy, p3.sy)))))
		if maxX < minX || maxY < minY {
			continue
		}
		index := len(mesh.triangles)
		mesh.triangles = append(mesh.triangles, mapWarpTriangle{a: triangle[0], b: triangle[1], c: triangle[2], minX: minX, minY: minY, maxX: maxX, maxY: maxY})
		for tileY := minY / mapWarpTileSize; tileY <= maxY/mapWarpTileSize; tileY++ {
			for tileX := minX / mapWarpTileSize; tileX <= maxX/mapWarpTileSize; tileX++ {
				key := [2]int{tileX, tileY}
				mesh.tiles[key] = append(mesh.tiles[key], index)
			}
		}
	}
	if len(mesh.triangles) == 0 {
		return nil, fmt.Errorf("control points do not form a usable source-map triangulation")
	}
	return mesh, nil
}

func (mesh *mapWarpMesh) transform(x, y float64) (float64, float64, bool) {
	candidates := mesh.tiles[[2]int{int(math.Floor(x)) / mapWarpTileSize, int(math.Floor(y)) / mapWarpTileSize}]
	for _, index := range candidates {
		triangle := mesh.triangles[index]
		if x < float64(triangle.minX) || x > float64(triangle.maxX) || y < float64(triangle.minY) || y > float64(triangle.maxY) {
			continue
		}
		p1, p2, p3 := mesh.points[triangle.a], mesh.points[triangle.b], mesh.points[triangle.c]
		w1, w2, w3, ok := mapBarycentric(x, y, p1.sx, p1.sy, p2.sx, p2.sy, p3.sx, p3.sy)
		if !ok || w1 < -1e-7 || w2 < -1e-7 || w3 < -1e-7 {
			continue
		}
		return w1*p1.tx + w2*p2.tx + w3*p3.tx, w1*p1.ty + w2*p2.ty + w3*p3.ty, true
	}
	return 0, 0, false
}

func mapBarycentric(x, y, x1, y1, x2, y2, x3, y3 float64) (float64, float64, float64, bool) {
	determinant := (y2-y3)*(x1-x3) + (x3-x2)*(y1-y3)
	if math.Abs(determinant) < 1e-12 {
		return 0, 0, 0, false
	}
	w1 := ((y2-y3)*(x-x3) + (x3-x2)*(y-y3)) / determinant
	w2 := ((y3-y1)*(x-x3) + (x1-x3)*(y-y3)) / determinant
	return w1, w2, 1 - w1 - w2, true
}

func delaunayMapWarp(points []mapWarpPoint) ([][3]int, error) {
	if len(points) < 3 {
		return nil, fmt.Errorf("at least 3 control points are required")
	}
	for i := range points {
		for j := 0; j < i; j++ {
			if math.Hypot(points[i].sx-points[j].sx, points[i].sy-points[j].sy) < 1e-9 {
				return nil, fmt.Errorf("control points %d and %d have duplicate source coordinates", j, i)
			}
		}
	}
	work := append([]mapWarpPoint(nil), points...)
	minX, maxX, minY, maxY := points[0].sx, points[0].sx, points[0].sy, points[0].sy
	for _, point := range points[1:] {
		minX, maxX = math.Min(minX, point.sx), math.Max(maxX, point.sx)
		minY, maxY = math.Min(minY, point.sy), math.Max(maxY, point.sy)
	}
	delta := math.Max(maxX-minX, maxY-minY)
	if delta < 1e-9 {
		return nil, fmt.Errorf("control points are degenerate")
	}
	midX, midY := (minX+maxX)/2, (minY+maxY)/2
	superStart := len(work)
	work = append(work,
		mapWarpPoint{sx: midX - 32*delta, sy: midY - delta},
		mapWarpPoint{sx: midX, sy: midY + 32*delta},
		mapWarpPoint{sx: midX + 32*delta, sy: midY - delta},
	)
	triangles := [][3]int{{superStart, superStart + 1, superStart + 2}}
	type edge struct{ a, b int }
	canonicalEdge := func(a, b int) edge {
		if a > b {
			a, b = b, a
		}
		return edge{a: a, b: b}
	}
	for pointIndex := range points {
		bad := make([]bool, len(triangles))
		edgeCounts := map[edge]int{}
		for i, triangle := range triangles {
			if !mapCircumcircleContains(work[triangle[0]], work[triangle[1]], work[triangle[2]], work[pointIndex]) {
				continue
			}
			bad[i] = true
			edgeCounts[canonicalEdge(triangle[0], triangle[1])]++
			edgeCounts[canonicalEdge(triangle[1], triangle[2])]++
			edgeCounts[canonicalEdge(triangle[2], triangle[0])]++
		}
		kept := triangles[:0]
		for i, triangle := range triangles {
			if !bad[i] {
				kept = append(kept, triangle)
			}
		}
		triangles = kept
		boundary := make([]edge, 0, len(edgeCounts))
		for candidate, count := range edgeCounts {
			if count == 1 {
				boundary = append(boundary, candidate)
			}
		}
		sort.Slice(boundary, func(i, j int) bool {
			if boundary[i].a != boundary[j].a {
				return boundary[i].a < boundary[j].a
			}
			return boundary[i].b < boundary[j].b
		})
		for _, candidate := range boundary {
			triangles = append(triangles, [3]int{candidate.a, candidate.b, pointIndex})
		}
	}
	out := make([][3]int, 0, len(triangles))
	for _, triangle := range triangles {
		if triangle[0] >= superStart || triangle[1] >= superStart || triangle[2] >= superStart {
			continue
		}
		p1, p2, p3 := work[triangle[0]], work[triangle[1]], work[triangle[2]]
		if math.Abs((p2.sx-p1.sx)*(p3.sy-p1.sy)-(p2.sy-p1.sy)*(p3.sx-p1.sx)) < 1e-9 {
			continue
		}
		out = append(out, triangle)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("control points are collinear")
	}
	sort.Slice(out, func(i, j int) bool {
		for k := 0; k < 3; k++ {
			if out[i][k] != out[j][k] {
				return out[i][k] < out[j][k]
			}
		}
		return false
	})
	return out, nil
}

func mapCircumcircleContains(a, b, c, point mapWarpPoint) bool {
	ax, ay := a.sx-point.sx, a.sy-point.sy
	bx, by := b.sx-point.sx, b.sy-point.sy
	cx, cy := c.sx-point.sx, c.sy-point.sy
	determinant := (ax*ax+ay*ay)*(bx*cy-cx*by) - (bx*bx+by*by)*(ax*cy-cx*ay) + (cx*cx+cy*cy)*(ax*by-bx*ay)
	orientation := (b.sx-a.sx)*(c.sy-a.sy) - (b.sy-a.sy)*(c.sx-a.sx)
	if orientation > 0 {
		return determinant > 1e-9
	}
	return determinant < -1e-9
}

type mapMappingEdge struct {
	source, target int
	pixels         int
	sourceShare    float64
	targetShare    float64
}

func significantMapMappingEdges(overlaps map[int]map[int]int, sourceAreas, targetAreas map[int]int, targetAreaScale, minShare float64) []mapMappingEdge {
	var edges []mapMappingEdge
	for sourceID, targets := range overlaps {
		for targetID, pixels := range targets {
			sourceShare := float64(pixels) / float64(sourceAreas[sourceID])
			targetAreaInSourcePixels := float64(targetAreas[targetID]) * targetAreaScale
			targetShare := float64(pixels) / targetAreaInSourcePixels
			if targetShare > 1 {
				targetShare = 1
			}
			if sourceShare+1e-12 < minShare && targetShare+1e-12 < minShare {
				continue
			}
			edges = append(edges, mapMappingEdge{source: sourceID, target: targetID, pixels: pixels, sourceShare: sourceShare, targetShare: targetShare})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].source != edges[j].source {
			return edges[i].source < edges[j].source
		}
		if edges[i].pixels != edges[j].pixels {
			return edges[i].pixels > edges[j].pixels
		}
		return edges[i].target < edges[j].target
	})
	return edges
}

func buildMapMappingGroups(edges []mapMappingEdge) ([]MapProvinceMappingGroup, map[int]int, map[int]int, map[int]bool) {
	sourceTargets := map[int][]int{}
	targetSources := map[int][]int{}
	overlap := map[[2]int]int{}
	for _, edge := range edges {
		sourceTargets[edge.source] = append(sourceTargets[edge.source], edge.target)
		targetSources[edge.target] = append(targetSources[edge.target], edge.source)
		overlap[[2]int{edge.source, edge.target}] = edge.pixels
	}
	sourceDegree, targetDegree, mappedTargets := map[int]int{}, map[int]int{}, map[int]bool{}
	for id, targets := range sourceTargets {
		sourceDegree[id] = len(targets)
	}
	for id, sources := range targetSources {
		targetDegree[id] = len(sources)
		mappedTargets[id] = true
	}
	visitedSources, visitedTargets := map[int]bool{}, map[int]bool{}
	var groups []MapProvinceMappingGroup
	sourceIDs := make([]int, 0, len(sourceTargets))
	for id := range sourceTargets {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Ints(sourceIDs)
	for _, start := range sourceIDs {
		if visitedSources[start] {
			continue
		}
		sourceQueue := []int{start}
		sourceSet, targetSet := map[int]bool{}, map[int]bool{}
		for len(sourceQueue) > 0 {
			sourceID := sourceQueue[0]
			sourceQueue = sourceQueue[1:]
			if visitedSources[sourceID] {
				continue
			}
			visitedSources[sourceID], sourceSet[sourceID] = true, true
			for _, targetID := range sourceTargets[sourceID] {
				if !visitedTargets[targetID] {
					visitedTargets[targetID], targetSet[targetID] = true, true
					for _, linkedSource := range targetSources[targetID] {
						if !visitedSources[linkedSource] {
							sourceQueue = append(sourceQueue, linkedSource)
						}
					}
				} else {
					targetSet[targetID] = true
				}
			}
		}
		sources, targets := sortedIntSet(sourceSet), sortedIntSet(targetSet)
		pixels := int64(0)
		for _, sourceID := range sources {
			for _, targetID := range targets {
				pixels += int64(overlap[[2]int{sourceID, targetID}])
			}
		}
		kind := "complex"
		switch {
		case len(sources) == 1 && len(targets) == 1:
			if sources[0] == targets[0] {
				kind = "one_to_one"
			} else {
				kind = "renumbered"
			}
		case len(sources) == 1:
			kind = "split"
		case len(targets) == 1:
			kind = "merge"
		}
		groups = append(groups, MapProvinceMappingGroup{Kind: kind, SourceProvinces: sources, TargetProvinces: targets, OverlapPixels: pixels})
	}
	return groups, sourceDegree, targetDegree, mappedTargets
}

func buildMapMappingSources(source *provinceMappingRaster, mapped map[int]int, edges []mapMappingEdge, sourceDegree, targetDegree map[int]int, maxCandidates int) []MapProvinceMappingSource {
	edgesBySource := map[int][]mapMappingEdge{}
	for _, edge := range edges {
		edgesBySource[edge.source] = append(edgesBySource[edge.source], edge)
	}
	ids := make([]int, 0, len(source.areas))
	for id := range source.areas {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]MapProvinceMappingSource, 0, len(ids))
	for _, sourceID := range ids {
		row := MapProvinceMappingSource{ProvinceID: sourceID, Pixels: source.areas[sourceID], MappedPixels: mapped[sourceID], Classification: "unmapped"}
		if row.Pixels > 0 {
			row.Coverage = roundMapMapping(float64(row.MappedPixels) / float64(row.Pixels))
		}
		candidates := edgesBySource[sourceID]
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].pixels != candidates[j].pixels {
				return candidates[i].pixels > candidates[j].pixels
			}
			return candidates[i].target < candidates[j].target
		})
		if len(candidates) > 0 {
			if sourceDegree[sourceID] > 1 {
				row.Classification = "split"
			} else if targetDegree[candidates[0].target] > 1 {
				row.Classification = "merge"
			} else if sourceID == candidates[0].target {
				row.Classification = "one_to_one"
			} else {
				row.Classification = "renumbered"
			}
		}
		for i, edge := range candidates {
			if i >= maxCandidates {
				break
			}
			relation := "one_to_one"
			switch {
			case sourceDegree[sourceID] > 1 && targetDegree[edge.target] > 1:
				relation = "complex"
			case sourceDegree[sourceID] > 1:
				relation = "split"
			case targetDegree[edge.target] > 1:
				relation = "merge"
			case sourceID != edge.target:
				relation = "renumbered"
			}
			confidence := 0.0
			if edge.sourceShare+edge.targetShare > 0 {
				confidence = 2 * edge.sourceShare * edge.targetShare / (edge.sourceShare + edge.targetShare)
			}
			row.Candidates = append(row.Candidates, MapProvinceMappingCandidate{
				ProvinceID: edge.target, Pixels: edge.pixels, SourceShare: roundMapMapping(edge.sourceShare),
				TargetShare: roundMapMapping(edge.targetShare), Confidence: roundMapMapping(confidence), Relation: relation,
			})
		}
		out = append(out, row)
	}
	return out
}

func summarizeMapMapping(groups []MapProvinceMappingGroup, sourceCount, targetCount int, sourceDegree map[int]int, mappedTargets map[int]bool) MapProvinceMappingSummary {
	result := MapProvinceMappingSummary{UnmappedSource: sourceCount - len(sourceDegree), UnmappedTarget: targetCount - len(mappedTargets)}
	for _, group := range groups {
		switch group.Kind {
		case "one_to_one":
			result.OneToOne++
		case "renumbered":
			result.Renumbered++
		case "split":
			result.Splits++
		case "merge":
			result.Merges++
		default:
			result.Complex++
		}
	}
	return result
}

func sortedIntSet(values map[int]bool) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func roundMapMapping(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
