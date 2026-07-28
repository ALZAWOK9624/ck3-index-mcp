package indexer

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strings"
)

const (
	// One relief algorithm makes every mountain on a map a sibling. These four
	// are separate morphologies, not settings of the same one: what changes is
	// the shape of the function, not its parameters.
	//
	// TerrainFoldBelt repeats parallel ridges and valleys across the band, the
	// way a compressed sedimentary belt does -- long even ridges, not one spine.
	TerrainFoldBelt = "fold_belt"
	// TerrainMassif is a blocky high-alpine cluster: hard-edged summits with
	// steep walls and little foothill, rather than a graded ridge line.
	TerrainMassif = "massif"
	// TerrainKarst is a tower field. Most of the band stays a flat floor and a
	// minority of it rises as isolated steep-sided towers.
	TerrainKarst = "karst"
	// TerrainDome is a broad rounded batholith uplift with no crest line at all,
	// which is what makes it read differently from hills at large widths.
	TerrainDome = "dome"

	// The following carry the relief signature of a CK3 terrain type, so a
	// heightmap can be built to match the terrain a province is scripted as
	// rather than the two being drawn independently. Vegetation types --
	// forest, jungle, taiga -- are deliberately absent: they change what grows
	// on the ground, not its shape.
	//
	// TerrainDunes is a desert dune field: asymmetric transverse ridges with a
	// long windward ramp and a short steep slip face.
	TerrainDunes = "dunes"
	// TerrainBadlands is drylands: a high surface destroyed by dense, narrow,
	// steep-sided gullies rather than by broad valleys.
	TerrainBadlands = "badlands"
	// TerrainFloodplain is a river-built flat: almost no relief, but with
	// levees along the channel and abandoned meander scars across it.
	TerrainFloodplain = "floodplain"
	// TerrainWetland is a poorly drained near-flat with shallow closed hollows
	// that hold water because nothing carries it away.
	TerrainWetland = "wetland"
	// TerrainSteppe is a vast, very low-frequency swell. It is not flat and it
	// is not hills; the scale is what distinguishes it.
	TerrainSteppe = "steppe"
	// TerrainTerraced is stepped hillside: flat treads separated by risers,
	// matching CK3's terraced_hills.
	TerrainTerraced = "terraced"

	// These four need a path-relative coordinate the distance field cannot
	// supply, so they are the landforms that were previously impossible rather
	// than merely absent.
	//
	// TerrainFaultBlock is a tilted crustal block: a cliff-like scarp on one
	// flank and a long gentle dip slope on the other. Basin-and-Range.
	TerrainFaultBlock = "fault_block"
	// TerrainCuesta is an eroded tilted stratum: a short steep escarpment
	// facing one way and a shallow dip slope running the other. Steepen the dip
	// with roughness and it reads as a hogback.
	TerrainCuesta = "cuesta"
	// TerrainVolcanicChain places discrete cones in sequence along the path
	// rather than a continuous ridge -- an island arc, not a mountain belt.
	TerrainVolcanicChain = "volcanic_chain"
	// TerrainRiverTerrace is a flight of benches beside a valley that steps
	// down as the valley descends, so the terraces stay parallel to the river
	// rather than level with each other.
	TerrainRiverTerrace = "river_terrace"

	TerrainPlain            = "plain"
	TerrainBasin            = "basin"
	TerrainValley           = "valley"
	TerrainContinentalShelf = "continental_shelf"
	TerrainContinentalSlope = "continental_slope"
	TerrainAbyssalPlain     = "abyssal_plain"
	TerrainTrench           = "trench"
	TerrainOceanRidge       = "ocean_ridge"
	TerrainSeamount         = "seamount"
	TerrainIsland           = "island"
)

const (
	TerrainGeometryPoint    = "point"
	TerrainGeometryPolyline = "polyline"
	TerrainGeometryPolygon  = "polygon"
)

const (
	TerrainDomainLand  = "land"
	TerrainDomainOcean = "ocean"
	TerrainDomainLake  = "lake"
	TerrainDomainWater = "water"
	TerrainDomainAny   = "any"
)

const (
	HydrologyStale            = "stale"
	HydrologySmallRiversStale = "small_rivers_stale"
	HydrologySynchronized     = "synchronized"
)

// MapTerrainGeometry is a point, polyline, or simple polygon in heightmap
// pixels. The origin is the image's top-left corner.
type MapTerrainGeometry struct {
	Type        string            `json:"type"`
	Coordinates []MapTerrainPoint `json:"coordinates"`
}

// MapTerrainLayer is one ordered physical-landform edit. Strength and every
// explicit height use normalized 0..1 units.
type MapTerrainLayer struct {
	ID           string             `json:"id"`
	Kind         string             `json:"kind"`
	Geometry     MapTerrainGeometry `json:"geometry"`
	WidthPx      float64            `json:"width_px"`
	FeatherPx    float64            `json:"feather_px,omitempty"`
	Domain       string             `json:"domain,omitempty"`
	Strength     float64            `json:"strength"`
	Roughness    float64            `json:"roughness,omitempty"`
	Detail       float64            `json:"detail,omitempty"`
	TargetHeight *float64           `json:"target_height,omitempty"`
	Seed         int64              `json:"seed,omitempty"`
}

type MapTerrainElevationStats struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
	Mean    float64 `json:"mean"`
}

type MapTerrainLayerStats struct {
	ID             string                   `json:"id"`
	Kind           string                   `json:"kind"`
	ModifiedBounds *MapTerrainRegion        `json:"modified_bounds,omitempty"`
	ChangedPixels  int                      `json:"changed_pixels"`
	RaisedPixels   int                      `json:"raised_pixels"`
	LoweredPixels  int                      `json:"lowered_pixels"`
	ClampedPixels  int                      `json:"clamped_pixels"`
	Before         MapTerrainElevationStats `json:"before"`
	After          MapTerrainElevationStats `json:"after"`
}

type MapTerrainLargeRiverSettings struct {
	MinDrainage int     `json:"min_drainage"`
	Depth       float64 `json:"depth"`
	ValleyWidth int     `json:"valley_width"`
	SeaLevel    float64 `json:"sea_level"`
}

type MapTerrainLargeRiverStats struct {
	ChannelCells int      `json:"channel_cells"`
	SinksFilled  int      `json:"sinks_filled"`
	MaxDrainage  int      `json:"max_drainage"`
	MeanDepth    float64  `json:"mean_depth"`
	Warnings     []string `json:"warnings,omitempty"`
}

type MapTerrainSmallRiverSettings struct {
	MinDrainage   int     `json:"min_drainage"`
	MaxDrainage   int     `json:"max_drainage"`
	BedDepth      float64 `json:"bed_depth"`
	SeaLevel      float64 `json:"sea_level"`
	MaxWidthIndex int     `json:"max_width_index"`
}

type MapTerrainSmallRiverStats struct {
	StreamPixels         int      `json:"stream_pixels"`
	Sources              int      `json:"sources"`
	Confluences          int      `json:"confluences"`
	BedCells             int      `json:"bed_cells"`
	BlockedBoundaryCells int      `json:"blocked_boundary_cells"`
	Warnings             []string `json:"warnings,omitempty"`
}

// minChannelDepthFraction keeps a headwater channel legible. Below roughly a
// third of the trunk depth a channel stops reading as a valley on a CK3-scale
// heightmap and becomes single-pixel noise.
const minChannelDepthFraction = 0.35

func DefaultTerrainLargeRivers() MapTerrainLargeRiverSettings {
	return MapTerrainLargeRiverSettings{
		MinDrainage: 220, Depth: 9.0 / 255.0, ValleyWidth: 3, SeaLevel: 20.0 / 255.0,
	}
}

func DefaultTerrainSmallRivers() MapTerrainSmallRiverSettings {
	return MapTerrainSmallRiverSettings{
		MinDrainage: 60, MaxDrainage: 4000, BedDepth: 2.5 / 255.0,
		SeaLevel: 20.0 / 255.0, MaxWidthIndex: 6,
	}
}

type terrainHeightRaster struct {
	image    image.Image
	bitDepth int
}

func loadTerrainHeightRaster(path string) (*terrainHeightRaster, error) {
	decoded, err := decodeMapImage(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", mapHeightmapRel, err)
	}
	switch decoded.(type) {
	case *image.Gray:
		return &terrainHeightRaster{image: decoded, bitDepth: 8}, nil
	case *image.Gray16:
		return &terrainHeightRaster{image: decoded, bitDepth: 16}, nil
	default:
		return nil, terrainInputErrorf("%s decoded as %T; terrain editing accepts only grayscale Gray8 or Gray16 images", mapHeightmapRel, decoded)
	}
}

func (r *terrainHeightRaster) bounds() image.Rectangle { return r.image.Bounds() }

func (r *terrainHeightRaster) normalizedAt(x, y int) float64 {
	switch img := r.image.(type) {
	case *image.Gray:
		return float64(img.GrayAt(x, y).Y) / 255
	case *image.Gray16:
		return float64(img.Gray16At(x, y).Y) / 65535
	default:
		panic("terrainHeightRaster contains a non-grayscale image")
	}
}

func (r *terrainHeightRaster) clone() *terrainHeightRaster {
	switch img := r.image.(type) {
	case *image.Gray:
		out := image.NewGray(img.Rect)
		out.Stride = img.Stride
		out.Pix = append(out.Pix[:0], img.Pix...)
		return &terrainHeightRaster{image: out, bitDepth: 8}
	case *image.Gray16:
		out := image.NewGray16(img.Rect)
		out.Stride = img.Stride
		out.Pix = append(out.Pix[:0], img.Pix...)
		return &terrainHeightRaster{image: out, bitDepth: 16}
	default:
		panic("terrainHeightRaster contains a non-grayscale image")
	}
}

func (r *terrainHeightRaster) setNormalized(x, y int, value float64) {
	value = clamp01(value)
	switch img := r.image.(type) {
	case *image.Gray:
		img.SetGray(x, y, color.Gray{Y: uint8(math.Round(value * 255))})
	case *image.Gray16:
		img.SetGray16(x, y, color.Gray16{Y: uint16(math.Round(value * 65535))})
	}
}

type terrainField struct {
	rect   image.Rectangle
	values []float32
	dirty  []bool
}

func newTerrainField(source *terrainHeightRaster, rect image.Rectangle) *terrainField {
	field := &terrainField{
		rect: rect, values: make([]float32, rect.Dx()*rect.Dy()),
		dirty: make([]bool, rect.Dx()*rect.Dy()),
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			field.values[field.index(x, y)] = float32(source.normalizedAt(x, y))
		}
	}
	return field
}

func (f *terrainField) index(x, y int) int {
	return (y-f.rect.Min.Y)*f.rect.Dx() + x - f.rect.Min.X
}

func (f *terrainField) value(x, y int) float64 {
	return float64(f.values[f.index(x, y)])
}

func (f *terrainField) set(x, y int, value float64) bool {
	i := f.index(x, y)
	value = clamp01(value)
	if math.Abs(float64(f.values[i])-value) < 1e-9 {
		return false
	}
	f.values[i] = float32(value)
	f.dirty[i] = true
	return true
}

func (f *terrainField) materialize(source *terrainHeightRaster) *terrainHeightRaster {
	out := source.clone()
	for y := f.rect.Min.Y; y < f.rect.Max.Y; y++ {
		for x := f.rect.Min.X; x < f.rect.Max.X; x++ {
			i := f.index(x, y)
			if f.dirty[i] {
				out.setNormalized(x, y, float64(f.values[i]))
			}
		}
	}
	return out
}

type terrainDomainMask struct {
	provinces image.Image
	colorToID map[uint32]int
	blocked   map[int]mapBlockKind
}

func loadTerrainDomainMask(cfg Config, bounds image.Rectangle) (*terrainDomainMask, error) {
	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return nil, err
	}
	provinces := active["map_data/provinces.png"]
	definition := active["map_data/definition.csv"]
	defaultMap := active["map_data/default.map"]
	if provinces.Path == "" || definition.Path == "" || defaultMap.Path == "" {
		return nil, terrainInputErrorf("domain-aware terrain layers require active map_data/provinces.png, definition.csv, and default.map")
	}
	img, err := decodeMapImage(provinces.Path)
	if err != nil {
		return nil, fmt.Errorf("provinces.png: %w", err)
	}
	if img.Bounds().Dx() != bounds.Dx() || img.Bounds().Dy() != bounds.Dy() {
		return nil, terrainInputErrorf("provinces.png dimensions %dx%d do not match heightmap.png %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), bounds.Dx(), bounds.Dy())
	}
	defs, err := parseProvinceDefinitions(definition.Path)
	if err != nil {
		return nil, fmt.Errorf("definition.csv: %w", err)
	}
	blocked, err := parseDefaultMapBlocked(defaultMap.Path)
	if err != nil {
		return nil, fmt.Errorf("default.map: %w", err)
	}
	return &terrainDomainMask{provinces: img, colorToID: defs, blocked: blocked}, nil
}

func (m *terrainDomainMask) classAt(x, y int) string {
	id, known := provinceIDAt(m.provinces, m.colorToID, x, y)
	if !known {
		return ""
	}
	block, blocked := m.blocked[id]
	if !blocked || block.BlockKind != "water" {
		return TerrainDomainLand
	}
	if block.WaterKind == "lake" {
		return TerrainDomainLake
	}
	return TerrainDomainOcean
}

func (m *terrainDomainMask) allows(domain string, x, y int) bool {
	if domain == TerrainDomainAny {
		return true
	}
	class := m.classAt(x, y)
	switch domain {
	case TerrainDomainLand, TerrainDomainOcean, TerrainDomainLake:
		return class == domain
	case TerrainDomainWater:
		return class == TerrainDomainOcean || class == TerrainDomainLake
	default:
		return false
	}
}

type terrainLayerMask struct {
	rect     image.Rectangle
	coverage []float32
	profile  []float32
	// across is the distance from the path as a fraction of the half-width: 0
	// on the crest line, 1 at the edge of the band. profile is a smoothstepped
	// version of the same thing and cannot be inverted cleanly, but a landform
	// whose structure repeats across the belt -- a fold belt's parallel ridges
	// -- needs the linear coordinate.
	across []float32
	// side is across with a sign: negative on the left of the path's direction
	// of travel, positive on the right. Any asymmetric landform needs it -- a
	// fault block has a scarp on one flank and a dip slope on the other, and
	// without a side there is no way to say which flank is which.
	side []float32
	// along is arc length down the path, 0 at the first point and 1 at the
	// last. It is what lets features be placed in sequence rather than
	// everywhere at once: a chain of separate volcanoes, terraces that step
	// down as the valley descends.
	along []float32
	// hasPath records whether side/along mean anything. Point geometry is
	// radial and has neither.
	hasPath bool
}

func (m terrainLayerMask) index(x, y int) int {
	return (y-m.rect.Min.Y)*m.rect.Dx() + x - m.rect.Min.X
}

type preparedMapTerrainEdit struct {
	spec          MapTerrainEditSpec
	before        *terrainHeightRaster
	after         *terrainHeightRaster
	rivers        *image.Paletted
	result        MapTerrainEditResult
	resultPreview terrainPreviewSet
}

func prepareMapTerrainEdit(cfg Config, spec MapTerrainEditSpec) (*preparedMapTerrainEdit, error) {
	spec = normalizeMapTerrainEditSpec(spec)
	spec.Operation = strings.TrimSpace(spec.Operation)
	switch spec.Operation {
	case MapTerrainOpCompose:
		if spec.LargeRivers != nil || spec.SmallRivers != nil {
			return nil, terrainInputErrorf("compose accepts layers and optional erosion, not river-operation settings")
		}
	case MapTerrainOpLargeRivers:
		if len(spec.Layers) > 0 || spec.Erosion != nil || spec.SmallRivers != nil {
			return nil, terrainInputErrorf("large_rivers accepts only large_rivers settings, region, and an optional base_artifact_id")
		}
	case MapTerrainOpSmallRivers:
		if len(spec.Layers) > 0 || spec.Erosion != nil || spec.LargeRivers != nil {
			return nil, terrainInputErrorf("small_rivers accepts only small_rivers settings, region, and an optional base_artifact_id")
		}
	default:
		return nil, terrainInputErrorf("unknown terrain operation %q; expected %s, %s, or %s",
			spec.Operation, MapTerrainOpCompose, MapTerrainOpLargeRivers, MapTerrainOpSmallRivers)
	}
	input, err := resolveMapTerrainEditInput(cfg, spec.BaseArtifactID)
	if err != nil {
		return nil, err
	}
	before, err := loadTerrainHeightRaster(input.HeightmapPath)
	if err != nil {
		return nil, err
	}
	region, err := resolveTerrainRegion(spec.Region, before.bounds())
	if err != nil {
		return nil, err
	}
	field := newTerrainField(before, region)
	result := MapTerrainEditResult{
		Operation: spec.Operation, ParentArtifactID: input.ParentArtifactID,
		SourceRel: mapHeightmapRel, SourceName: input.SourceName,
		SourceFingerprint: input.SourceFingerprint,
		Width:             before.bounds().Dx(), Height: before.bounds().Dy(), BitDepth: before.bitDepth,
		Region:            MapTerrainRegion{X: region.Min.X, Y: region.Min.Y, Width: region.Dx(), Height: region.Dy()},
		RequiresCK3Repack: true,
		Applied:           false,
		Guidance: []string{
			"The source Mod was not modified. Review the raw heightmap and previews before copying anything.",
			"heightmap.png is raw only; import it into the CK3 map editor and rebuild the packed/indirection heightmap assets before launching the game.",
			"Copy approved files manually, then rescan so later edits use the reviewed map state.",
		},
	}
	prepared := &preparedMapTerrainEdit{spec: spec, before: before, result: result}

	switch spec.Operation {
	case MapTerrainOpCompose:
		// Eroding relief that already exists is a real request -- it is what
		// ties hand-drawn landforms into their surroundings -- and it needs no
		// new landform. Requiring a zero-strength dummy layer to express it just
		// taught callers to write noise into the spec.
		if len(spec.Layers) == 0 && (spec.Erosion == nil || spec.Erosion.Droplets == 0) {
			return nil, terrainInputErrorf("compose needs at least one terrain layer, or erosion with a positive droplet count")
		}
		if len(spec.Layers) > 128 {
			return nil, terrainInputErrorf("compose accepts at most 128 ordered terrain layers")
		}
		mask, err := loadTerrainDomainMask(cfg, before.bounds())
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for index, layer := range spec.Layers {
			if err := validateMapTerrainLayer(index, layer, before.bounds(), seen); err != nil {
				return nil, err
			}
			stats, err := applyMapTerrainLayer(field, mask, layer)
			if err != nil {
				return nil, err
			}
			result.Layers = append(result.Layers, stats)
		}
		if spec.Erosion != nil && spec.Erosion.Droplets > 0 {
			drops, err := erodeNormalizedTerrain(field, mask, *spec.Erosion)
			if err != nil {
				return nil, terrainInputErrorf("%s", err)
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("hydraulic erosion simulated %d deterministic droplets after composing the layers", drops))
		}
		result.HydrologyStatus = HydrologyStale
	case MapTerrainOpLargeRivers:
		settings := DefaultTerrainLargeRivers()
		if spec.LargeRivers != nil {
			settings = *spec.LargeRivers
		}
		// A major river is a water province in its own right, exactly like a
		// lake, so the carve needs the same domain the other passes respect.
		domains, err := loadTerrainDomainMask(cfg, before.bounds())
		if err != nil {
			return nil, err
		}
		stats, err := carveNormalizedLargeRivers(field, domains, settings)
		if err != nil {
			return nil, terrainInputErrorf("%s", err)
		}
		result.LargeRivers = &stats
		result.Warnings = append(result.Warnings, stats.Warnings...)
		result.HydrologyStatus = HydrologySmallRiversStale
	case MapTerrainOpSmallRivers:
		if input.RiversPath == "" {
			return nil, terrainInputErrorf("small_rivers requires an active rivers.png or a parent artifact that contains one")
		}
		baseRivers, err := loadCanonicalRiverOverlay(input.RiversPath, before.bounds())
		if err != nil {
			return nil, err
		}
		domains, err := loadTerrainDomainMask(cfg, before.bounds())
		if err != nil {
			return nil, err
		}
		settings := DefaultTerrainSmallRivers()
		if spec.SmallRivers != nil {
			settings = *spec.SmallRivers
		}
		overlay, stats, err := generateNormalizedSmallRivers(field, baseRivers, domains, settings)
		if err != nil {
			return nil, terrainInputErrorf("%s", err)
		}
		prepared.rivers = overlay
		result.SmallRivers = &stats
		result.Warnings = append(result.Warnings, stats.Warnings...)
		result.HydrologyStatus = HydrologySynchronized
	}
	prepared.after = field.materialize(before)
	result.ModifiedBounds = dirtyBounds(field)
	beforePreview, afterPreview, diffPreview := terrainPreviewImages(before, prepared.after, region)
	prepared.result = result
	prepared.result.PreviewPNG = afterPreview
	prepared.resultPreview = terrainPreviewSet{before: beforePreview, after: afterPreview, diff: diffPreview}
	return prepared, nil
}

type terrainPreviewSet struct {
	before []byte
	after  []byte
	diff   []byte
}

// resultPreview is split from the public result so raw PNG bytes never enter a
// manifest or structured MCP payload.
func (p *preparedMapTerrainEdit) previews() terrainPreviewSet { return p.resultPreview }

func validateMapTerrainLayer(index int, layer MapTerrainLayer, bounds image.Rectangle, seen map[string]bool) error {
	layer.ID = strings.TrimSpace(layer.ID)
	if layer.ID == "" {
		return terrainInputErrorf("layers[%d].id is required", index)
	}
	if seen[layer.ID] {
		return terrainInputErrorf("layers[%d].id %q is not unique", index, layer.ID)
	}
	seen[layer.ID] = true
	switch layer.Kind {
	case TerrainPlain, TerrainHills, TerrainRange, TerrainPlateau, TerrainBasin,
		TerrainValley, TerrainCanyon, TerrainVolcano, TerrainContinentalShelf,
		TerrainContinentalSlope, TerrainAbyssalPlain, TerrainTrench,
		TerrainOceanRidge, TerrainSeamount, TerrainIsland,
		TerrainFoldBelt, TerrainMassif, TerrainKarst, TerrainDome,
		TerrainDunes, TerrainBadlands, TerrainFloodplain, TerrainWetland,
		TerrainSteppe, TerrainTerraced,
		TerrainFaultBlock, TerrainCuesta, TerrainVolcanicChain, TerrainRiverTerrace:
	default:
		return terrainInputErrorf("layers[%d] has unknown kind %q", index, layer.Kind)
	}
	if err := validateTerrainGeometry(index, layer.Geometry, bounds); err != nil {
		return err
	}
	for name, value := range map[string]float64{
		"width_px": layer.WidthPx, "feather_px": layer.FeatherPx, "strength": layer.Strength,
		"roughness": layer.Roughness, "detail": layer.Detail,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return terrainInputErrorf("layers[%d].%s must be finite", index, name)
		}
	}
	if layer.WidthPx <= 0 {
		return terrainInputErrorf("layers[%d].width_px must be positive", index)
	}
	if layer.WidthPx > 8192 {
		return terrainInputErrorf("layers[%d].width_px must not exceed 8192", index)
	}
	if layer.FeatherPx < 0 {
		return terrainInputErrorf("layers[%d].feather_px must not be negative", index)
	}
	if layer.FeatherPx > 4096 {
		return terrainInputErrorf("layers[%d].feather_px must not exceed 4096", index)
	}
	if layer.Strength < 0 || layer.Strength > 1 || layer.Roughness < 0 || layer.Roughness > 1 {
		return terrainInputErrorf("layers[%d] strength and roughness must be within 0..1", index)
	}
	if layer.Detail < 0 || layer.Detail > 16 {
		return terrainInputErrorf("layers[%d].detail must be within 0..16", index)
	}
	if layer.TargetHeight != nil {
		if math.IsNaN(*layer.TargetHeight) || math.IsInf(*layer.TargetHeight, 0) || *layer.TargetHeight < 0 || *layer.TargetHeight > 1 {
			return terrainInputErrorf("layers[%d].target_height must be within 0..1", index)
		}
	}
	switch layer.Kind {
	case TerrainPlain, TerrainContinentalShelf, TerrainContinentalSlope, TerrainAbyssalPlain,
		TerrainFloodplain, TerrainWetland:
		if layer.TargetHeight == nil {
			return terrainInputErrorf("layers[%d] kind %q requires target_height", index, layer.Kind)
		}
	}
	switch layer.Kind {
	case TerrainFaultBlock, TerrainCuesta, TerrainVolcanicChain, TerrainRiverTerrace:
		// These are defined relative to the path: which flank, or how far
		// along. A point has no direction of travel and a polygon has no single
		// centre line, so neither can express them and silently degrading to a
		// symmetric blob would be worse than refusing.
		if layer.Geometry.Type != TerrainGeometryPolyline {
			return terrainInputErrorf("layers[%d] kind %q needs polyline geometry; it is defined by which side of the path a pixel is on and how far along it sits", index, layer.Kind)
		}
	}
	domain := normalizedTerrainDomain(layer)
	switch domain {
	case TerrainDomainLand, TerrainDomainOcean, TerrainDomainLake, TerrainDomainWater, TerrainDomainAny:
	default:
		return terrainInputErrorf("layers[%d].domain %q is invalid", index, layer.Domain)
	}
	if layer.Kind == TerrainIsland && domain != TerrainDomainLand {
		return terrainInputErrorf("layers[%d] island must use domain=land; it cannot create passable land in ocean provinces", index)
	}
	return nil
}

func validateTerrainGeometry(index int, geometry MapTerrainGeometry, bounds image.Rectangle) error {
	if len(geometry.Coordinates) > 2048 {
		return terrainInputErrorf("layers[%d] geometry accepts at most 2048 coordinates", index)
	}
	need := 0
	switch geometry.Type {
	case TerrainGeometryPoint:
		need = 1
		if len(geometry.Coordinates) != 1 {
			return terrainInputErrorf("layers[%d] point geometry requires exactly one coordinate", index)
		}
	case TerrainGeometryPolyline:
		need = 2
		if len(geometry.Coordinates) < need {
			return terrainInputErrorf("layers[%d] polyline geometry requires at least two coordinates", index)
		}
	case TerrainGeometryPolygon:
		need = 3
		if len(geometry.Coordinates) < need {
			return terrainInputErrorf("layers[%d] polygon geometry requires at least three coordinates", index)
		}
		areaTwice := int64(0)
		for pointIndex, point := range geometry.Coordinates {
			next := geometry.Coordinates[(pointIndex+1)%len(geometry.Coordinates)]
			if point == next {
				return terrainInputErrorf("layers[%d] polygon has a duplicate consecutive coordinate", index)
			}
			areaTwice += int64(point.X)*int64(next.Y) - int64(next.X)*int64(point.Y)
		}
		if areaTwice == 0 {
			return terrainInputErrorf("layers[%d] polygon has zero area", index)
		}
		if polygonSelfIntersects(geometry.Coordinates) {
			return terrainInputErrorf("layers[%d] polygon is self-intersecting", index)
		}
	default:
		return terrainInputErrorf("layers[%d].geometry.type %q is invalid", index, geometry.Type)
	}
	for pointIndex, point := range geometry.Coordinates {
		if pointIndex > 0 && point == geometry.Coordinates[pointIndex-1] {
			return terrainInputErrorf("layers[%d].geometry has duplicate consecutive coordinates", index)
		}
		if !image.Pt(point.X, point.Y).In(bounds) {
			return terrainInputErrorf("layers[%d].geometry.coordinates[%d] (%d,%d) lies outside the heightmap",
				index, pointIndex, point.X, point.Y)
		}
	}
	return nil
}

func normalizedTerrainDomain(layer MapTerrainLayer) string {
	if domain := strings.ToLower(strings.TrimSpace(layer.Domain)); domain != "" {
		return domain
	}
	switch layer.Kind {
	case TerrainContinentalShelf, TerrainContinentalSlope, TerrainAbyssalPlain,
		TerrainTrench, TerrainOceanRidge, TerrainSeamount:
		return TerrainDomainOcean
	default:
		return TerrainDomainLand
	}
}

func applyMapTerrainLayer(field *terrainField, domains *terrainDomainMask, layer MapTerrainLayer) (MapTerrainLayerStats, error) {
	mask := rasterizeTerrainLayerMask(layer, field.rect)
	stats := MapTerrainLayerStats{ID: layer.ID, Kind: layer.Kind}
	if mask.rect.Empty() {
		return stats, nil
	}
	detail := layer.Detail
	if detail == 0 {
		detail = 1
	}
	frequency := detail / math.Max(layer.WidthPx, 1)
	ridged := layer.Kind == TerrainRange || layer.Kind == TerrainOceanRidge ||
		layer.Kind == TerrainMassif || layer.Kind == TerrainFoldBelt
	noise := DefaultMountainNoise(layer.Seed, frequency)
	noise.Ridged = ridged
	noise.Multifractal = ridged
	if !ridged {
		noise.Warp = 0.45 / frequency
	}
	// Each new morphology needs its own noise character, not just its own
	// profile curve; sharing one field is what made every landform a sibling.
	switch layer.Kind {
	case TerrainMassif:
		// High lacunarity and low gain give sparse, hard-edged summit blocks
		// instead of a smoothly graded crest.
		noise.Frequency = frequency * 1.9
		noise.Lacunarity = 2.61
		noise.Gain = 0.42
		noise.Octaves = 6
		noise.Warp = 0.30 / frequency
	case TerrainKarst:
		// Few octaves and no ridging keep the towers separate blobs rather than
		// a connected crest network.
		noise.Frequency = frequency * 3.4
		noise.Octaves = 2
		noise.Ridged = false
		noise.Multifractal = false
		noise.Warp = 0.7 / frequency
	case TerrainDome:
		noise.Frequency = frequency * 0.55
		noise.Octaves = 4
		noise.Gain = 0.42
		noise.Warp = 0.9 / frequency
	case TerrainFoldBelt:
		// The across-band periodicity carries the structure here, so the noise
		// only has to bend the fold axes rather than build them.
		noise.Frequency = frequency * 0.8
		noise.Octaves = 4
		noise.Warp = 1.1 / frequency
	case TerrainDunes:
		// Smooth and low-octave: sand has no fine structure, and the dune shape
		// itself is periodic rather than fractal.
		noise.Frequency = frequency * 0.9
		noise.Octaves = 2
		noise.Gain = 0.4
		noise.Warp = 1.4 / frequency
	case TerrainBadlands:
		// Dense high-frequency ridged noise is exactly a gully network once it
		// is used to cut rather than to raise.
		noise.Frequency = frequency * 2.8
		noise.Octaves = 5
		noise.Ridged = true
		noise.Multifractal = false
		noise.Lacunarity = 2.2
		noise.Warp = 0.35 / frequency
	case TerrainSteppe:
		// An order of magnitude longer wavelength than hills; that gap is the
		// whole difference between the two.
		noise.Frequency = frequency * 0.18
		noise.Octaves = 3
		noise.Gain = 0.48
		noise.Warp = 2.0 / frequency
	case TerrainWetland, TerrainFloodplain:
		noise.Frequency = frequency * 1.3
		noise.Octaves = 3
		noise.Warp = 1.0 / frequency
	case TerrainTerraced:
		noise.Frequency = frequency * 0.75
		noise.Octaves = 4
		noise.Warp = 0.8 / frequency
	}
	envelopeFrequency := math.Max(frequency*0.35, 1.0/65536)
	envelopeNoise := TerrainNoise{
		Frequency:  envelopeFrequency,
		Octaves:    3,
		Lacunarity: 2.07,
		Gain:       0.55,
		Warp:       0.22 / envelopeFrequency,
		Seed:       layer.Seed + 104729,
	}

	beforeMin, beforeMax, beforeSum := 1.0, 0.0, 0.0
	afterMin, afterMax, afterSum := 1.0, 0.0, 0.0
	sampled := 0
	modified := image.Rectangle{}
	for y := mask.rect.Min.Y; y < mask.rect.Max.Y; y++ {
		for x := mask.rect.Min.X; x < mask.rect.Max.X; x++ {
			i := mask.index(x, y)
			coverage := float64(mask.coverage[i])
			if coverage <= 0 || !domains.allows(normalizedTerrainDomain(layer), x, y) {
				continue
			}
			before := field.value(x, y)
			profile := float64(mask.profile[i])
			n := noise.Sample(float64(x), float64(y))
			envelope := 1.0
			if ridged {
				envelope = envelopeNoise.Sample(float64(x), float64(y))
			}
			after := terrainLayerValue(layer, before, profile, coverage, n, envelope,
				float64(mask.across[i]), float64(mask.side[i]), float64(mask.along[i]), x, y, mask.rect)
			unclamped := after
			after = clamp01(after)
			if after != unclamped {
				stats.ClampedPixels++
			}
			beforeMin = math.Min(beforeMin, before)
			beforeMax = math.Max(beforeMax, before)
			beforeSum += before
			afterMin = math.Min(afterMin, after)
			afterMax = math.Max(afterMax, after)
			afterSum += after
			sampled++
			if !field.set(x, y, after) {
				continue
			}
			stats.ChangedPixels++
			if after > before {
				stats.RaisedPixels++
			} else {
				stats.LoweredPixels++
			}
			modified = includePoint(modified, x, y)
		}
	}
	if sampled > 0 {
		stats.Before = MapTerrainElevationStats{Minimum: beforeMin, Maximum: beforeMax, Mean: beforeSum / float64(sampled)}
		stats.After = MapTerrainElevationStats{Minimum: afterMin, Maximum: afterMax, Mean: afterSum / float64(sampled)}
	}
	if !modified.Empty() {
		region := regionFromRect(modified)
		stats.ModifiedBounds = &region
	}
	return stats, nil
}

func terrainLayerValue(layer MapTerrainLayer, before, profile, coverage, noise, envelope, across, side, along float64, x, y int, bounds image.Rectangle) float64 {
	rough := (1-layer.Roughness)*0.5 + layer.Roughness*noise
	weight := coverage
	switch layer.Kind {
	case TerrainFaultBlock:
		// One flank is a fault scarp, the other is the tilted top of the block.
		// The asymmetry is the whole landform: a symmetric version of this is
		// just a ridge.
		var relief float64
		if side < 0 {
			// Scarp: full height reached within a fraction of the half-width,
			// so the flank is a wall rather than a slope.
			scarp := clamp01((1 + side) / 0.22)
			relief = smoothstep(scarp)
		} else {
			// Dip slope: a long, near-linear ramp shedding height outward.
			relief = math.Pow(1-side, 1.25)
		}
		// The crest is faceted rather than smooth; a fault block is bedrock.
		facet := 0.82 + 0.18*rough
		return before + layer.Strength*relief*facet*weight
	case TerrainCuesta:
		// Same asymmetry as a fault block but shallower and stratified: the dip
		// slope carries the bedding, and roughness steepens it toward a hogback.
		dip := 1.0 + 2.2*layer.Roughness
		var relief float64
		if side < 0 {
			relief = smoothstep(clamp01((1 + side) / 0.30))
		} else {
			relief = math.Pow(1-side, dip)
		}
		// Faint bedding steps along the dip slope.
		bedding := 1.0
		if side > 0 {
			bedding = 0.93 + 0.07*(0.5+0.5*math.Cos(side*7*math.Pi))
		}
		return before + layer.Strength*relief*bedding*weight
	case TerrainVolcanicChain:
		// Cones spaced along the path, each with its own crater. Spacing comes
		// from detail; the noise jitters both position and height so the chain
		// is not a row of identical stamps -- which is exactly the failure mode
		// a hand-drawn chain falls into.
		cones := math.Max(2, math.Round(2+5*math.Max(layer.Detail, 0.3)))
		phase := along*cones + (noise-0.5)*0.22
		nearest := math.Abs(phase-math.Round(phase)) * 2
		if nearest >= 1 {
			return before
		}
		// Radial distance from this cone's centre, combining along- and
		// across-path offsets.
		radial := math.Min(1, math.Hypot(nearest, across))
		cone := math.Pow(1-radial, 1.6)
		// Crater: the summit dips, so the rim is the high point.
		if radial < 0.18 {
			cone -= 0.55 * math.Pow(1-radial/0.18, 2)
		}
		// Per-cone height variation keyed to which cone this is, so no two
		// cones in the chain are the same size.
		scale := 0.55 + 0.45*latticeValue(int(math.Round(phase)), 0, layer.Seed+31337)
		return before + layer.Strength*math.Max(cone, 0)*scale*weight
	case TerrainRiverTerrace:
		// Benches beside a valley. They step away from the channel across the
		// band and descend with the valley along it, so a terrace stays
		// parallel to the river instead of level with itself.
		benches := math.Max(2, math.Round(2+3*math.Max(layer.Detail, 0.3)))
		lift := math.Abs(side)*benches + (noise-0.5)*0.35
		step := math.Floor(lift)
		riser := smoothstep(clamp01((lift - step) * 3.0))
		bench := (step + riser) / benches
		// Downstream descent: the whole flight loses height along the path.
		descent := 1 - 0.55*along
		return before + layer.Strength*bench*descent*weight
	case TerrainFoldBelt:
		// Contours of `across` are offset curves of the path, so a plain cosine
		// of it draws a machined racetrack that wraps around the belt ends. Two
		// things break that up: a large phase displacement so spacing varies and
		// axes wander, and a plunge term so individual folds die out laterally
		// instead of every fold running the belt's whole length.
		folds := 1.0 + 1.8*math.Max(layer.Detail, 0.4)
		axis := across*folds + (noise-0.5)*4.5 + (envelope-0.5)*2.6
		ridge := 0.5 + 0.5*math.Cos(axis*2*math.Pi)
		ridge = math.Pow(ridge, 1.6)
		plunge := 0.15 + 0.85*smoothstep(envelope)
		taper := smoothstep(1 - across)
		return before + layer.Strength*ridge*plunge*taper*weight
	case TerrainMassif:
		// Bare rock between summits rather than a graded surface. Rounding to
		// hard steps drew flat horizontal shelves, so the quantisation is only
		// blended in: cliff bands read as bands while the rock inside each one
		// still varies.
		block := clamp01((noise - 0.30) / 0.70)
		block = math.Pow(block, 0.55)
		band := math.Floor(block*4) / 4
		block = band*0.65 + block*0.35
		shoulder := math.Pow(smoothstep(profile), 0.5)
		return before + layer.Strength*(0.15+0.85*block)*shoulder*weight
	case TerrainKarst:
		// Only the top of the noise field becomes a tower; everything below the
		// cutoff stays floor. That minority-coverage is what makes a tower field
		// look nothing like a range.
		const cutoff = 0.62
		if noise <= cutoff {
			return before
		}
		tower := (noise - cutoff) / (1 - cutoff)
		// A low exponent gives near-vertical sides and a blunt top.
		tower = math.Pow(tower, 0.38)
		return before + layer.Strength*tower*smoothstep(profile)*weight
	case TerrainDome:
		// No crest line: a broad rounded swell with only fine surface texture.
		// The 0.7 is calibration, not decoration -- a dome delivers its strength
		// over the whole footprint while a range concentrates it on a spine, so
		// without it the same strength value builds a far bigger mountain here
		// than it does for every other kind.
		swell := math.Pow(smoothstep(profile), 0.8)
		texture := 0.9 + 0.1*(noise-0.5)*2*layer.Roughness
		return before + layer.Strength*0.7*swell*texture*weight
	case TerrainPlain:
		target := *layer.TargetHeight
		// A barely visible grade prevents an exact dead-flat plane with no
		// drainage direction. Low-frequency noise keeps it natural.
		grade := (float64(x-bounds.Min.X)/math.Max(float64(bounds.Dx()-1), 1) - 0.5) * 0.006
		micro := (noise - 0.5) * 0.012 * layer.Roughness
		return before + (target+grade+micro-before)*layer.Strength*weight
	case TerrainAbyssalPlain:
		target := *layer.TargetHeight
		grade := (float64(y-bounds.Min.Y)/math.Max(float64(bounds.Dy()-1), 1) - 0.5) * 0.008
		micro := (noise - 0.5) * 0.006 * layer.Roughness
		return before + (target+grade+micro-before)*layer.Strength*weight
	case TerrainHills:
		// Rolling hills are rounded summits separated by graded hollows. Raw
		// noise times the profile gives neither -- it gives bumpy sandpaper --
		// so the field is pushed through an S-curve that flattens both the tops
		// and the bottoms and steepens only the slopes between them.
		swell := smoothstep(clamp01(noise*1.15 - 0.07))
		relief := (1-layer.Roughness)*0.55 + layer.Roughness*swell
		return before + layer.Strength*relief*math.Pow(profile, 0.8)*weight
	case TerrainSteppe:
		// Centred on zero so a steppe undulates about the existing ground
		// instead of lifting all of it, which is what keeps it reading as open
		// country rather than as a plateau.
		return before + layer.Strength*(noise-0.5)*1.6*math.Pow(profile, 0.6)*weight
	case TerrainDunes:
		// Transverse dunes: a long windward ramp and a short steep slip face.
		// The crest direction comes from the seed so neighbouring fields do not
		// all march the same way, and the noise term makes crests sinuous and
		// occasionally break, which is what stops it looking like corrugation.
		angle := float64(layer.Seed%360) * math.Pi / 180
		wind := (float64(x)*math.Cos(angle) + float64(y)*math.Sin(angle))
		spacing := math.Max(layer.WidthPx, 8) / (2 + 3*math.Max(layer.Detail, 0.3))
		phase := wind/spacing + (noise-0.5)*1.9
		t := phase - math.Floor(phase)
		const windward = 0.72
		var dune float64
		if t < windward {
			dune = smoothstep(t / windward)
		} else {
			dune = 1 - smoothstep((t-windward)/(1-windward))
		}
		// Interdune flats: the field is not dunes everywhere.
		field := smoothstep(clamp01((noise - 0.25) / 0.75))
		return before + layer.Strength*dune*(0.35+0.65*field)*smoothstep(profile)*weight
	case TerrainBadlands:
		// A surface destroyed by incision, so it is built by subtracting narrow
		// gullies from a raised bench rather than by adding relief.
		bench := math.Pow(smoothstep(profile), 0.7)
		// Ridged noise inverted is a dense channel network; the high exponent
		// makes the channels narrow and the divides broad, which is the
		// characteristic badlands texture.
		gully := math.Pow(clamp01(1-noise), 2.6)
		return before + layer.Strength*bench*(0.30-0.95*gully)*weight
	case TerrainFloodplain:
		// A river-built flat: relax hard toward the target, then put back the
		// two features that keep it from being a dead plane -- levees close to
		// the channel and abandoned meander scars across the rest.
		target := *layer.TargetHeight
		flattened := before + (target-before)*clamp01(layer.Strength)*weight
		levee := math.Pow(profile, 3.5) * 0.010
		scar := 0.0
		if noise > 0.70 {
			scar = -((noise - 0.70) / 0.30) * 0.008
		}
		return flattened + (levee+scar)*weight
	case TerrainWetland:
		// Near-flat, but with shallow closed hollows: water sits here because
		// nothing drains it, and closed depressions are what express that.
		target := *layer.TargetHeight
		flattened := before + (target-before)*clamp01(layer.Strength)*weight
		hollow := 0.0
		if noise < 0.42 {
			hollow = -math.Pow((0.42-noise)/0.42, 0.7) * 0.012
		}
		hummock := 0.0
		if noise > 0.88 {
			hummock = ((noise - 0.88) / 0.12) * 0.004
		}
		return flattened + (hollow+hummock)*weight
	case TerrainTerraced:
		// Flat treads separated by risers. The tread count scales with detail,
		// and the riser is smoothstepped so it is a slope rather than a cliff
		// of one pixel.
		treads := math.Max(2, math.Round(2+4*math.Max(layer.Detail, 0.3)))
		raw := math.Pow(smoothstep(profile), 0.85) * (0.85 + 0.15*rough)
		scaled := raw * treads
		step := math.Floor(scaled)
		riser := smoothstep(clamp01((scaled - step) * 2.6))
		return before + layer.Strength*((step+riser)/treads)*weight
	case TerrainRange:
		return before + layer.Strength*terrainRangeRelief(profile, noise, envelope)*weight
	case TerrainPlateau:
		top := smoothstep(math.Min(profile*1.7, 1))
		return before + layer.Strength*top*(0.9+0.1*rough)*weight
	case TerrainBasin:
		return before - layer.Strength*(0.3+0.7*profile)*(0.9+0.1*rough)*weight
	case TerrainValley:
		return before - layer.Strength*math.Pow(profile, 1.7)*(0.8+0.2*rough)*weight
	case TerrainCanyon:
		return before - layer.Strength*math.Pow(profile, 3.2)*(0.85+0.15*rough)*weight
	case TerrainVolcano:
		cone := profile * (0.85 + 0.15*rough)
		crater := math.Max((profile-0.78)/0.22, 0)
		return before + layer.Strength*(cone-0.55*crater*crater)*weight
	case TerrainContinentalShelf:
		target := *layer.TargetHeight
		progress := float64(x-bounds.Min.X) / math.Max(float64(bounds.Dx()-1), 1)
		gradedTarget := target + (0.5-progress)*layer.Strength*0.2
		return before + (gradedTarget-before)*layer.Strength*weight
	case TerrainContinentalSlope:
		target := *layer.TargetHeight
		progress := float64(x-bounds.Min.X) / math.Max(float64(bounds.Dx()-1), 1)
		gradedTarget := target - layer.Strength*0.25*progress
		return before + (gradedTarget-before)*layer.Strength*weight
	case TerrainTrench:
		return before - layer.Strength*math.Pow(profile, 3.5)*(0.9+0.1*rough)*weight
	case TerrainOceanRidge:
		return before + layer.Strength*terrainRangeRelief(profile, noise, envelope)*weight
	case TerrainSeamount:
		return before + layer.Strength*math.Pow(profile, 1.5)*(0.85+0.15*rough)*weight
	case TerrainIsland:
		return before + layer.Strength*math.Pow(profile, 1.25)*(0.85+0.15*rough)*weight
	default:
		return before
	}
}

func terrainRangeRelief(profile, noise, envelope float64) float64 {
	// A range needs a continuous geographic belt without becoming a continuous
	// wall. Low-frequency envelope noise creates peak groups and saddles along
	// the path, while ridged multifractal noise supplies the individual crests.
	// Both terms retain a small floor so a saddle connects neighboring massifs,
	// but their product makes that floor visually negligible instead of forcing
	// every path pixel to receive at least a quarter of the requested height.
	envelope = clamp01(envelope)
	profileExponent := 1.15 + 0.65*(1-envelope)
	spine := math.Pow(clamp01(profile), profileExponent)
	peaks := math.Pow(clamp01((noise-0.08)/0.92), 1.35)
	continuity := 0.04 + 0.96*math.Pow(smoothstep(envelope), 1.35)
	return spine * continuity * (0.02 + 0.98*peaks)
}

func rasterizeTerrainLayerMask(layer MapTerrainLayer, clip image.Rectangle) terrainLayerMask {
	points := layer.Geometry.Coordinates
	minX, maxX, minY, maxY := points[0].X, points[0].X, points[0].Y, points[0].Y
	for _, point := range points[1:] {
		minX, maxX = min(minX, point.X), max(maxX, point.X)
		minY, maxY = min(minY, point.Y), max(maxY, point.Y)
	}
	influence := layer.WidthPx/2 + layer.FeatherPx
	if layer.Geometry.Type == TerrainGeometryPolygon {
		influence = layer.FeatherPx
	}
	radius := int(math.Ceil(influence + 1))
	rect := image.Rect(minX-radius, minY-radius, maxX+radius+1, maxY+radius+1).Intersect(clip)
	mask := terrainLayerMask{rect: rect}
	if rect.Empty() {
		return mask
	}
	mask.coverage = make([]float32, rect.Dx()*rect.Dy())
	mask.profile = make([]float32, rect.Dx()*rect.Dy())
	mask.across = make([]float32, rect.Dx()*rect.Dy())
	mask.side = make([]float32, rect.Dx()*rect.Dy())
	mask.along = make([]float32, rect.Dx()*rect.Dy())
	seeds := make([]bool, rect.Dx()*rect.Dy())
	inside := make([]bool, rect.Dx()*rect.Dy())
	rasterizeTerrainGeometrySeeds(layer.Geometry, rect, seeds)
	if layer.Geometry.Type == TerrainGeometryPolygon {
		scanlinePolygonFill(points, rect, inside)
	}
	distance := chamferDistance(seeds, rect.Dx(), rect.Dy())
	halfWidth := math.Max(layer.WidthPx/2, 0.5)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			i := mask.index(x, y)
			if layer.Geometry.Type == TerrainGeometryPolygon {
				if inside[i] {
					mask.coverage[i] = 1
					mask.profile[i] = 1
				} else if layer.FeatherPx > 0 && distance[i] <= layer.FeatherPx {
					mask.coverage[i] = float32(smoothstep(1 - distance[i]/layer.FeatherPx))
					mask.profile[i] = 1
				}
				continue
			}
			if distance[i] <= halfWidth {
				mask.coverage[i] = 1
				mask.profile[i] = float32(smoothstep(1 - distance[i]/halfWidth))
				mask.across[i] = float32(distance[i] / halfWidth)
			} else if layer.FeatherPx > 0 && distance[i] <= halfWidth+layer.FeatherPx {
				mask.coverage[i] = float32(smoothstep(1 - (distance[i]-halfWidth)/layer.FeatherPx))
				mask.across[i] = 1
			}
		}
	}
	if layer.Geometry.Type == TerrainGeometryPolyline && len(points) >= 2 {
		fillTerrainPathCoordinates(&mask, points, halfWidth)
	}
	return mask
}

// fillTerrainPathCoordinates solves each covered pixel against the polyline
// itself rather than against the rasterized distance field, because the two
// coordinates that the distance field cannot carry -- which side of the path a
// pixel is on, and how far along it sits -- are exactly what asymmetric and
// sequenced landforms are made of.
func fillTerrainPathCoordinates(mask *terrainLayerMask, points []MapTerrainPoint, halfWidth float64) {
	segments := len(points) - 1
	starts := make([]float64, segments+1)
	total := 0.0
	for i := 0; i < segments; i++ {
		starts[i] = total
		total += math.Hypot(float64(points[i+1].X-points[i].X), float64(points[i+1].Y-points[i].Y))
	}
	starts[segments] = total
	if total <= 0 {
		return
	}
	mask.hasPath = true
	for y := mask.rect.Min.Y; y < mask.rect.Max.Y; y++ {
		for x := mask.rect.Min.X; x < mask.rect.Max.X; x++ {
			i := mask.index(x, y)
			if mask.coverage[i] <= 0 {
				continue
			}
			px, py := float64(x), float64(y)
			best, bestCross, bestArc := math.Inf(1), 0.0, 0.0
			for s := 0; s < segments; s++ {
				ax, ay := float64(points[s].X), float64(points[s].Y)
				bx, by := float64(points[s+1].X), float64(points[s+1].Y)
				dx, dy := bx-ax, by-ay
				lengthSquared := dx*dx + dy*dy
				t := 0.0
				if lengthSquared > 0 {
					t = ((px-ax)*dx + (py-ay)*dy) / lengthSquared
					t = math.Max(0, math.Min(1, t))
				}
				cx, cy := ax+t*dx, ay+t*dy
				d := math.Hypot(px-cx, py-cy)
				if d >= best {
					continue
				}
				best = d
				// z of the cross product of the segment direction with the
				// offset to the pixel: negative to the left of travel, positive
				// to the right.
				bestCross = dx*(py-ay) - dy*(px-ax)
				bestArc = starts[s] + t*math.Sqrt(lengthSquared)
			}
			signed := math.Min(best/halfWidth, 1)
			if bestCross < 0 {
				signed = -signed
			}
			mask.side[i] = float32(signed)
			mask.along[i] = float32(bestArc / total)
		}
	}
}

func rasterizeTerrainGeometrySeeds(geometry MapTerrainGeometry, rect image.Rectangle, seeds []bool) {
	set := func(x, y int) {
		if image.Pt(x, y).In(rect) {
			seeds[(y-rect.Min.Y)*rect.Dx()+x-rect.Min.X] = true
		}
	}
	line := func(a, b MapTerrainPoint) {
		x0, y0, x1, y1 := a.X, a.Y, b.X, b.Y
		dx, dy := terrainAbsInt(x1-x0), -terrainAbsInt(y1-y0)
		stepX, stepY := -1, -1
		if x0 < x1 {
			stepX = 1
		}
		if y0 < y1 {
			stepY = 1
		}
		err := dx + dy
		for {
			set(x0, y0)
			if x0 == x1 && y0 == y1 {
				break
			}
			twice := 2 * err
			if twice >= dy {
				err += dy
				x0 += stepX
			}
			if twice <= dx {
				err += dx
				y0 += stepY
			}
		}
	}
	points := geometry.Coordinates
	if geometry.Type == TerrainGeometryPoint {
		set(points[0].X, points[0].Y)
		return
	}
	for index := 0; index+1 < len(points); index++ {
		line(points[index], points[index+1])
	}
	if geometry.Type == TerrainGeometryPolygon {
		line(points[len(points)-1], points[0])
	}
}

func scanlinePolygonFill(points []MapTerrainPoint, rect image.Rectangle, inside []bool) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		sampleY := float64(y) + 0.5
		intersections := make([]float64, 0, len(points))
		for index, a := range points {
			b := points[(index+1)%len(points)]
			ay, by := float64(a.Y), float64(b.Y)
			if (ay > sampleY) == (by > sampleY) {
				continue
			}
			x := float64(a.X) + (sampleY-ay)*(float64(b.X-a.X))/(by-ay)
			intersections = append(intersections, x)
		}
		sort.Float64s(intersections)
		for pair := 0; pair+1 < len(intersections); pair += 2 {
			start := max(rect.Min.X, int(math.Ceil(intersections[pair]-0.5)))
			end := min(rect.Max.X-1, int(math.Floor(intersections[pair+1]-0.5)))
			for x := start; x <= end; x++ {
				inside[(y-rect.Min.Y)*rect.Dx()+x-rect.Min.X] = true
			}
		}
	}
}

func chamferDistance(seeds []bool, width, height int) []float64 {
	distance := make([]float64, len(seeds))
	for index := range distance {
		if seeds[index] {
			distance[index] = 0
		} else {
			distance[index] = math.Inf(1)
		}
	}
	update := func(index, neighbour int, cost float64) {
		if neighbour >= 0 && neighbour < len(distance) && distance[neighbour]+cost < distance[index] {
			distance[index] = distance[neighbour] + cost
		}
	}
	diagonal := math.Sqrt2
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := y*width + x
			if x > 0 {
				update(i, i-1, 1)
			}
			if y > 0 {
				update(i, i-width, 1)
				if x > 0 {
					update(i, i-width-1, diagonal)
				}
				if x+1 < width {
					update(i, i-width+1, diagonal)
				}
			}
		}
	}
	for y := height - 1; y >= 0; y-- {
		for x := width - 1; x >= 0; x-- {
			i := y*width + x
			if x+1 < width {
				update(i, i+1, 1)
			}
			if y+1 < height {
				update(i, i+width, 1)
				if x > 0 {
					update(i, i+width-1, diagonal)
				}
				if x+1 < width {
					update(i, i+width+1, diagonal)
				}
			}
		}
	}
	return distance
}

func terrainAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func polygonSelfIntersects(points []MapTerrainPoint) bool {
	n := len(points)
	for i := 0; i < n; i++ {
		a, b := points[i], points[(i+1)%n]
		for j := i + 1; j < n; j++ {
			if j == i || (j+1)%n == i || (i+1)%n == j {
				continue
			}
			c, d := points[j], points[(j+1)%n]
			if segmentsIntersect(a, b, c, d) {
				return true
			}
		}
	}
	return false
}

func segmentsIntersect(a, b, c, d MapTerrainPoint) bool {
	orient := func(p, q, r MapTerrainPoint) int64 {
		return int64(q.X-p.X)*int64(r.Y-p.Y) - int64(q.Y-p.Y)*int64(r.X-p.X)
	}
	o1, o2, o3, o4 := orient(a, b, c), orient(a, b, d), orient(c, d, a), orient(c, d, b)
	if (o1 < 0) != (o2 < 0) && (o3 < 0) != (o4 < 0) {
		return true
	}
	onSegment := func(p, q, r MapTerrainPoint) bool {
		return q.X >= min(p.X, r.X) && q.X <= max(p.X, r.X) &&
			q.Y >= min(p.Y, r.Y) && q.Y <= max(p.Y, r.Y)
	}
	return (o1 == 0 && onSegment(a, c, b)) ||
		(o2 == 0 && onSegment(a, d, b)) ||
		(o3 == 0 && onSegment(c, a, d)) ||
		(o4 == 0 && onSegment(c, b, d))
}

func includePoint(rect image.Rectangle, x, y int) image.Rectangle {
	point := image.Rect(x, y, x+1, y+1)
	if rect.Empty() {
		return point
	}
	return rect.Union(point)
}

func dirtyBounds(field *terrainField) *MapTerrainRegion {
	rect := image.Rectangle{}
	for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
		for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
			if field.dirty[field.index(x, y)] {
				rect = includePoint(rect, x, y)
			}
		}
	}
	if rect.Empty() {
		return nil
	}
	region := regionFromRect(rect)
	return &region
}

func regionFromRect(rect image.Rectangle) MapTerrainRegion {
	return MapTerrainRegion{X: rect.Min.X, Y: rect.Min.Y, Width: rect.Dx(), Height: rect.Dy()}
}

// erodeNormalizedTerrain runs the droplet simulation over the field, leaving
// every water-domain pixel exactly as it found it.
//
// The domain has to be honoured here for the same reason the layers honour it:
// provinces.png decides what is sea, and the heightmap only describes it. A
// droplet that dumps its load in a sea province raises that pixel above the
// water line, and CK3 then has land geometry inside a province it treats as
// ocean. Deltas are real, but growing one is an authoring decision made by
// editing provinces.png -- not something an erosion pass may do by side effect.
func erodeNormalizedTerrain(field *terrainField, domains *terrainDomainMask, settings MapErosionSettings) (int, error) {
	if settings.Droplets < 0 || settings.Droplets > 5_000_000 {
		return 0, fmt.Errorf("droplets must be within 0..5000000")
	}
	if settings.MaxSteps < 0 || settings.MaxSteps > 4096 {
		return 0, fmt.Errorf("max_steps must be within 0..4096")
	}
	if settings.Radius < 0 || settings.Radius > 16 {
		return 0, fmt.Errorf("radius must be within 0..16")
	}
	for name, value := range map[string]float64{
		"inertia": settings.Inertia, "capacity": settings.Capacity, "erode": settings.Erode,
		"deposit": settings.Deposit, "evaporate": settings.Evaporate,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("erosion %s is not finite", name)
		}
	}
	if settings.Inertia < 0 || settings.Inertia > 1 ||
		settings.Capacity < 0 ||
		settings.Erode < 0 || settings.Erode > 1 ||
		settings.Deposit < 0 || settings.Deposit > 1 ||
		settings.Evaporate < 0 || settings.Evaporate > 1 {
		return 0, fmt.Errorf("erosion values fall outside their documented normalized ranges")
	}
	seaLevel := DefaultErosionSeaLevel
	if settings.SeaLevel != nil {
		seaLevel = *settings.SeaLevel
		if math.IsNaN(seaLevel) || math.IsInf(seaLevel, 0) || seaLevel < 0 || seaLevel > 1 {
			return 0, fmt.Errorf("erosion sea_level must be a finite normalized value within 0..1")
		}
	}
	w, h := field.rect.Dx(), field.rect.Dy()
	if w < 4 || h < 4 || settings.Droplets == 0 {
		return 0, nil
	}
	height := make([]float64, len(field.values))
	for i, value := range field.values {
		height[i] = float64(value) * 255
	}
	// Precomputed because classAt decodes a provinces.png pixel every call, and
	// a droplet run touches the same cells repeatedly.
	waterCell := make([]bool, len(field.values))
	if domains != nil {
		for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
			for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
				switch domains.classAt(x, y) {
				case TerrainDomainOcean, TerrainDomainLake:
					waterCell[field.index(x, y)] = true
				}
			}
		}
	}
	maxSteps := settings.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 64
	}
	radius := max(settings.Radius, 1)
	random := splitMix64(uint64(settings.Seed) + 0x9E3779B97F4A7C15)
	for drop := 0; drop < settings.Droplets; drop++ {
		px := float64(random()%uint64(w-2)) + 1
		py := float64(random()%uint64(h-2)) + 1
		// Rain falls on land. Spawning in the sea would spend the budget
		// simulating currents the heightmap does not model.
		if waterCell[int(py)*w+int(px)] {
			continue
		}
		dirX, dirY, water, sediment, speed := 0.0, 0.0, 1.0, 0.0, 1.0
		for step := 0; step < maxSteps; step++ {
			cellX, cellY := int(px), int(py)
			if cellX < 1 || cellY < 1 || cellX >= w-1 || cellY >= h-1 {
				break
			}
			// The droplet has reached its outlet. Ending the run here is both
			// the physical answer and what keeps the sea from acting as an
			// infinite sediment sink that scours the coast feeding it.
			if waterCell[cellY*w+cellX] {
				break
			}
			fx, fy := px-float64(cellX), py-float64(cellY)
			here, gradX, gradY := bilinearHeightGradient(height, w, cellX, cellY, fx, fy)
			dirX = dirX*settings.Inertia - gradX*(1-settings.Inertia)
			dirY = dirY*settings.Inertia - gradY*(1-settings.Inertia)
			length := math.Hypot(dirX, dirY)
			if length < 1e-9 {
				break
			}
			dirX, dirY = dirX/length, dirY/length
			px, py = px+dirX, py+dirY
			if px < 1 || py < 1 || px >= float64(w-1) || py >= float64(h-1) {
				break
			}
			next, _, _ := bilinearHeightGradient(height, w, int(px), int(py), px-float64(int(px)), py-float64(int(py)))
			delta := next - here
			capacity := math.Max(-delta, 0.01) * speed * water * settings.Capacity
			if sediment > capacity || delta > 0 {
				amount := settings.Deposit * (sediment - capacity)
				if delta > 0 {
					amount = math.Min(delta, sediment)
				}
				sediment -= amount
				depositAt(height, w, h, cellX, cellY, fx, fy, amount)
			} else {
				amount := math.Min(settings.Erode*(capacity-sediment), -delta)
				sediment += amount
				erodeAround(height, w, h, cellX, cellY, radius, amount)
			}
			speed = math.Sqrt(math.Max(speed*speed-delta, 0.0001))
			water *= 1 - settings.Evaporate
			if water < 0.01 {
				break
			}
		}
	}
	// The radius-weighted deposit and erode helpers spread over neighbours and
	// can reach a water cell from the land beside it, so the guarantee is made
	// here at write-back rather than trusted to every call site above.
	for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
		for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
			i := field.index(x, y)
			if waterCell[i] {
				continue
			}
			// Ground already under the water line keeps its own height as the
			// floor: the point is to stop erosion drowning a coast, not to
			// dredge up terrain the author drew below sea level.
			floor := seaLevel
			if original := float64(field.values[i]); original < floor {
				floor = original
			}
			field.set(x, y, math.Max(height[i]/255, floor))
		}
	}
	return settings.Droplets, nil
}

// carveNormalizedLargeRivers cuts major-river valleys into land, and only into
// land. Ocean, lake and river provinces are all one thing here -- water the
// province files already declare -- and a heightmap pass may not redraw where
// they sit. The carve also only ever lowers ground: raising a pixel is not
// something carving a valley can plausibly mean.
func carveNormalizedLargeRivers(field *terrainField, domains *terrainDomainMask, settings MapTerrainLargeRiverSettings) (MapTerrainLargeRiverStats, error) {
	stats := MapTerrainLargeRiverStats{}
	if settings.MinDrainage < 1 {
		return stats, fmt.Errorf("min_drainage must be at least 1")
	}
	if settings.ValleyWidth < 0 || settings.ValleyWidth > 64 {
		return stats, fmt.Errorf("valley_width must be within 0..64")
	}
	if settings.Depth < 0 || settings.Depth > 1 || settings.SeaLevel < 0 || settings.SeaLevel > 1 ||
		math.IsNaN(settings.Depth) || math.IsNaN(settings.SeaLevel) ||
		math.IsInf(settings.Depth, 0) || math.IsInf(settings.SeaLevel, 0) {
		return stats, fmt.Errorf("large river depth and sea_level must be finite normalized values within 0..1")
	}
	w, h := field.rect.Dx(), field.rect.Dy()
	if w < 8 || h < 8 {
		return stats, nil
	}
	height := make([]float64, len(field.values))
	for i, value := range field.values {
		height[i] = float64(value)
	}
	filled, sinks := fillDepressions(height, w, h, settings.SeaLevel)
	stats.SinksFilled = sinks
	drainage := accumulateDrainage(filled, w, h)
	for _, d := range drainage {
		stats.MaxDrainage = max(stats.MaxDrainage, d)
	}
	if stats.MaxDrainage < settings.MinDrainage {
		stats.Warnings = append(stats.Warnings, "no catchment reached min_drainage")
		return stats, nil
	}
	// Channel depth follows discharge, but the reference discharge has to be a
	// large channel rather than the single largest pixel on the map. Dividing by
	// the global maximum let one trunk river own the whole depth budget and left
	// everything else cutting a fraction of a grey level -- a drainage network
	// that reads as scratches. The 90th percentile of the channels actually
	// found is a large river in this network, so `depth` means what a caller
	// would expect it to mean.
	channelDrainage := make([]int, 0, 1024)
	for i, drainageCells := range drainage {
		if drainageCells < settings.MinDrainage || height[i] <= settings.SeaLevel || filled[i]-height[i] > pondedDepth/255 {
			continue
		}
		channelDrainage = append(channelDrainage, drainageCells)
	}
	if len(channelDrainage) == 0 {
		return stats, nil
	}
	sort.Ints(channelDrainage)
	reference := float64(channelDrainage[(len(channelDrainage)*9)/10])
	if reference < float64(settings.MinDrainage) {
		reference = float64(settings.MinDrainage)
	}

	cut := make([]float64, len(height))
	depthSum := 0.0
	for i, drainageCells := range drainage {
		if drainageCells < settings.MinDrainage || height[i] <= settings.SeaLevel || filled[i]-height[i] > pondedDepth/255 {
			continue
		}
		// Headwaters stay shallower than the trunk they feed, but not so shallow
		// that they vanish; anything at or above the reference gets full depth.
		ratio := math.Sqrt(float64(drainageCells) / reference)
		ratio = math.Max(minChannelDepthFraction, math.Min(ratio, 1))
		depth := settings.Depth * ratio
		cut[i] = depth
		depthSum += depth
		stats.ChannelCells++
	}
	if stats.ChannelCells == 0 {
		return stats, nil
	}
	stats.MeanDepth = depthSum / float64(stats.ChannelCells)
	if settings.ValleyWidth > 0 {
		cut = spreadValley(cut, w, h, settings.ValleyWidth)
	}
	for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
		for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
			i := field.index(x, y)
			if cut[i] <= 0 {
				continue
			}
			// spreadValley widens the channel past the cells that passed the
			// sea-level test above, so the valley shoulder can land on a lake or
			// on the sea. Those are water provinces and keep their bed.
			if domains != nil {
				switch domains.classAt(x, y) {
				case TerrainDomainOcean, TerrainDomainLake:
					continue
				}
			}
			// Clamping up to sea level was what turned a lake at grey 11 into a
			// ridge at grey 18. A carve floors at sea level only where that is
			// still a descent; ground already lower simply keeps its height.
			carved := height[i] - cut[i]
			if carved < settings.SeaLevel {
				carved = settings.SeaLevel
			}
			if carved > height[i] {
				carved = height[i]
			}
			field.set(x, y, carved)
		}
	}
	return stats, nil
}

func loadCanonicalRiverOverlay(path string, bounds image.Rectangle) (*image.Paletted, error) {
	decoded, err := decodeMapImage(path)
	if err != nil {
		return nil, fmt.Errorf("rivers.png: %w", err)
	}
	paletted, ok := decoded.(*image.Paletted)
	if !ok {
		return nil, terrainInputErrorf("rivers.png decoded as %T; CK3 river editing requires an indexed palette PNG", decoded)
	}
	if paletted.Bounds().Dx() != bounds.Dx() || paletted.Bounds().Dy() != bounds.Dy() {
		return nil, terrainInputErrorf("rivers.png dimensions do not match heightmap.png")
	}
	if !canonicalRiverPaletteMatches(paletted.Palette) {
		return nil, terrainInputErrorf("rivers.png does not use the canonical 256-entry CK3 river palette")
	}
	out := image.NewPaletted(paletted.Rect, ck3RiverPalette())
	out.Stride = paletted.Stride
	out.Pix = append(out.Pix[:0], paletted.Pix...)
	return out, nil
}

func canonicalRiverPaletteMatches(palette color.Palette) bool {
	want := ck3RiverPalette()
	if len(palette) != len(want) {
		return false
	}
	for i := range want {
		ar, ag, ab, aa := palette[i].RGBA()
		br, bg, bb, ba := want[i].RGBA()
		if ar != br || ag != bg || ab != bb || aa != ba {
			return false
		}
	}
	return true
}

func generateNormalizedSmallRivers(field *terrainField, overlay *image.Paletted, domains *terrainDomainMask, settings MapTerrainSmallRiverSettings) (*image.Paletted, MapTerrainSmallRiverStats, error) {
	stats := MapTerrainSmallRiverStats{}
	if settings.MinDrainage < 1 {
		return nil, stats, fmt.Errorf("min_drainage must be at least 1")
	}
	if settings.MaxDrainage > 0 && settings.MaxDrainage <= settings.MinDrainage {
		return nil, stats, fmt.Errorf("max_drainage must exceed min_drainage")
	}
	if settings.MaxDrainage < 0 {
		return nil, stats, fmt.Errorf("max_drainage must not be negative")
	}
	if settings.MaxWidthIndex != 0 && (settings.MaxWidthIndex < riverBodyMin || settings.MaxWidthIndex > riverBodyMax) {
		return nil, stats, fmt.Errorf("max_width_index must be within %d..%d", riverBodyMin, riverBodyMax)
	}
	if settings.BedDepth < 0 || settings.BedDepth > 1 || settings.SeaLevel < 0 || settings.SeaLevel > 1 ||
		math.IsNaN(settings.BedDepth) || math.IsNaN(settings.SeaLevel) ||
		math.IsInf(settings.BedDepth, 0) || math.IsInf(settings.SeaLevel, 0) {
		return nil, stats, fmt.Errorf("small river bed_depth and sea_level must be finite normalized values within 0..1")
	}
	w, h := field.rect.Dx(), field.rect.Dy()
	if w < 8 || h < 8 {
		return nil, stats, fmt.Errorf("region is too small to route drainage")
	}
	maxWidth := min(max(settings.MaxWidthIndex, riverBodyMin), riverBodyMax)
	height := make([]float64, len(field.values))
	land := make([]bool, len(field.values))
	for i, value := range field.values {
		height[i] = float64(value)
	}
	for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
		for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
			land[field.index(x, y)] = domains.classAt(x, y) == TerrainDomainLand
		}
	}
	filled, _ := fillDepressions(height, w, h, settings.SeaLevel)
	drainage := accumulateDrainage(filled, w, h)
	legacy := MapSmallRiverSettings{
		MinDrainage: settings.MinDrainage, MaxDrainage: settings.MaxDrainage,
		SeaLevel: int(math.Round(settings.SeaLevel * 255)),
	}
	inStream := make([]bool, len(height))
	arrivals := make([]int, len(height))
	var painted []int
	for i, d := range drainage {
		if d < settings.MinDrainage || (settings.MaxDrainage > 0 && d > settings.MaxDrainage) ||
			!land[i] || height[i] <= settings.SeaLevel || filled[i]-height[i] > pondedDepth/255 {
			continue
		}
		if hasUpstreamStream(filled, drainage, w, h, i, legacy) {
			continue
		}
		cell := i
		for step := 0; step < w*h; step++ {
			if !inStream[cell] {
				inStream[cell] = true
				painted = append(painted, cell)
			}
			next, ok := steepestDownhill(filled, w, h, cell)
			if !ok || !land[next] || height[next] <= settings.SeaLevel ||
				(settings.MaxDrainage > 0 && drainage[next] > settings.MaxDrainage) {
				break
			}
			cx, cy, nx, ny := cell%w, cell/w, next%w, next/w
			if cx != nx && cy != ny {
				bridge := cy*w + nx
				if other := ny*w + cx; drainage[other] < drainage[bridge] {
					bridge = other
				}
				if settings.MaxDrainage > 0 && drainage[bridge] > settings.MaxDrainage {
					break
				}
				if !inStream[bridge] {
					inStream[bridge] = true
					painted = append(painted, bridge)
				}
				arrivals[bridge]++
			}
			arrivals[next]++
			cell = next
		}
	}
	painted = dropIsolatedStreamCells(painted, inStream, w, h)
	painted, stats.BlockedBoundaryCells = blockUnconnectedBoundaryStreams(painted, inStream, field.rect, overlay)
	if stats.BlockedBoundaryCells > 0 {
		stats.Warnings = append(stats.Warnings, fmt.Sprintf(
			"%d stream pixel(s) in boundary segment(s) were blocked because their component did not join an existing channel or sea orthogonally",
			stats.BlockedBoundaryCells))
	}

	// Replace only the requested window. Outside Pix bytes and palette indices
	// are inherited exactly from the parent overlay.
	for y := field.rect.Min.Y; y < field.rect.Max.Y; y++ {
		for x := field.rect.Min.X; x < field.rect.Max.X; x++ {
			index := uint8(riverLand)
			if domains.classAt(x, y) != TerrainDomainLand {
				index = riverSea
			}
			overlay.SetColorIndex(x, y, index)
		}
	}
	if len(painted) == 0 {
		stats.Warnings = append(stats.Warnings, "no stream met the drainage window")
		return overlay, stats, nil
	}
	maxDrainage := 1
	for _, i := range painted {
		maxDrainage = max(maxDrainage, drainage[i])
	}
	for _, i := range painted {
		x, y := field.rect.Min.X+i%w, field.rect.Min.Y+i/w
		share := math.Sqrt(float64(drainage[i])) / math.Sqrt(float64(maxDrainage))
		index := riverBodyMin + int(math.Round(share*float64(maxWidth-riverBodyMin)))
		overlay.SetColorIndex(x, y, uint8(min(max(index, riverBodyMin), maxWidth)))
		stats.StreamPixels++
		if settings.BedDepth > 0 {
			field.set(x, y, math.Max(settings.SeaLevel, field.value(x, y)-settings.BedDepth*(0.4+0.6*share)))
			stats.BedCells++
		}
	}
	// A confluence is a junction, so it is counted from the neighbours that
	// actually drain into a cell. Counting how many head-to-mouth walks stepped
	// through it instead marked the whole trunk below any two headwaters as a
	// merge, which painted thousands of junction markers down a single river.
	for _, i := range painted {
		x, y := field.rect.Min.X+i%w, field.rect.Min.Y+i/w
		inflows := 0
		cx, cy := i%w, i/w
		for _, step := range [8][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
			nx, ny := cx+step[0], cy+step[1]
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				continue
			}
			neighbour := ny*w + nx
			if !inStream[neighbour] {
				continue
			}
			if down, ok := steepestDownhill(filled, w, h, neighbour); ok && down == i {
				inflows++
			}
		}
		switch {
		case inflows == 0:
			overlay.SetColorIndex(x, y, riverSource)
			stats.Sources++
		case inflows > 1:
			overlay.SetColorIndex(x, y, riverConfluence)
			stats.Confluences++
		}
	}
	return overlay, stats, nil
}

func blockUnconnectedBoundaryStreams(painted []int, inStream []bool, rect image.Rectangle, base *image.Paletted) ([]int, int) {
	w, h := rect.Dx(), rect.Dy()
	blocked := 0
	visited := make([]bool, len(inStream))
	for _, start := range painted {
		if !inStream[start] || visited[start] {
			continue
		}
		queue := []int{start}
		visited[start] = true
		component := make([]int, 0, 32)
		touchesBoundary := false
		joinsInheritedWater := false
		for len(queue) > 0 {
			i := queue[0]
			queue = queue[1:]
			component = append(component, i)
			x, y := i%w, i/w
			onBoundary := x == 0 || y == 0 || x == w-1 || y == h-1
			if onBoundary {
				touchesBoundary = true
			}
			for _, step := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+step[0], y+step[1]
				if nx >= 0 && ny >= 0 && nx < w && ny < h {
					next := ny*w + nx
					if inStream[next] && !visited[next] {
						visited[next] = true
						queue = append(queue, next)
					}
					continue
				}
				if !onBoundary {
					continue
				}
				globalX, globalY := rect.Min.X+nx, rect.Min.Y+ny
				if !image.Pt(globalX, globalY).In(base.Bounds()) {
					continue
				}
				index := base.ColorIndexAt(globalX, globalY)
				if index == riverSea || index == riverSource || index == riverConfluence ||
					(index >= riverBodyMin && index <= riverBodyMax) {
					joinsInheritedWater = true
				}
			}
		}
		if !touchesBoundary || joinsInheritedWater {
			continue
		}
		for _, i := range component {
			if !inStream[i] {
				continue
			}
			inStream[i] = false
			blocked++
		}
	}
	kept := painted[:0]
	for _, i := range painted {
		if inStream[i] {
			kept = append(kept, i)
		}
	}
	return dropIsolatedStreamCells(kept, inStream, w, h), blocked
}

func (p *preparedMapTerrainEdit) write(outputDir string) (MapTerrainEditResult, error) {
	result := p.result
	if p.rivers != nil {
		output, err := writeRiverOverlayOutput(outputDir, p.rivers)
		if err != nil {
			return result, err
		}
		result.Outputs = append(result.Outputs, output)
	}
	height, err := writeHeightmapOutput(outputDir, p.after.image)
	if err != nil {
		return result, err
	}
	result.Outputs = append(result.Outputs, height)
	for _, preview := range []struct {
		rel  string
		kind string
		data []byte
	}{
		{"previews/hillshade_before.png", "hillshade_before", p.resultPreview.before},
		{"previews/hillshade_after.png", "hillshade_after", p.resultPreview.after},
		{"previews/height_diff.png", "height_diff", p.resultPreview.diff},
	} {
		imageValue, err := decodePNGBytes(preview.data)
		if err != nil {
			return result, err
		}
		output, err := writeMapRaster(outputDir, preview.rel, preview.kind, imageValue)
		if err != nil {
			return result, err
		}
		result.Outputs = append(result.Outputs, output)
	}
	return result, nil
}
