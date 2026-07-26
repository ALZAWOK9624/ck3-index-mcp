package indexer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	MapRenderMaxWidth         = 8192
	MapRenderMaxHeight        = 4096
	mapRenderMaxPixels        = int64(MapRenderMaxWidth * MapRenderMaxHeight)
	mapRenderMaxWorkingPixels = int64(48 * 1024 * 1024)
	// Above this output size deflate at its default level costs several seconds
	// and dominates the whole request, so large plates default to the fast level.
	mapRenderFastEncodePixels = int64(8 << 20)
)

// mapRenderPNGEncoder picks the deflate level for the finished plate.
// CK3_INDEX_MAP_PNG_COMPRESSION overrides the choice: "default" always uses
// deflate's default level for the smallest file, "fast" always trades roughly
// 15% more bytes for a 4x quicker encode, "best" and "none" are the remaining
// image/png levels. The zero value, "auto", uses "fast" only for large plates.
func mapRenderPNGEncoder(pixels int64) (png.Encoder, string) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CK3_INDEX_MAP_PNG_COMPRESSION")))
	if mode == "" || mode == "auto" {
		if pixels >= mapRenderFastEncodePixels {
			mode = "fast"
		} else {
			mode = "default"
		}
	}
	switch mode {
	case "fast":
		return png.Encoder{CompressionLevel: png.BestSpeed}, "fast"
	case "best":
		return png.Encoder{CompressionLevel: png.BestCompression}, "best"
	case "none":
		return png.Encoder{CompressionLevel: png.NoCompression}, "none"
	default:
		return png.Encoder{CompressionLevel: png.DefaultCompression}, "default"
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

type MapRenderEdge struct {
	From  string  `json:"from"`
	To    string  `json:"to"`
	Value float64 `json:"value,omitempty"`
	Label string  `json:"label,omitempty"`
}

type MapRenderLayer struct {
	Type       string           `json:"type"`
	Level      string           `json:"level,omitempty"`
	Metric     *MapMetricSpec   `json:"metric,omitempty"`
	Values     []MapMetricValue `json:"values,omitempty"`
	SourceNote string           `json:"source_note,omitempty"`
	Palette    string           `json:"palette,omitempty"`
	Minimum    *float64         `json:"minimum,omitempty"`
	Maximum    *float64         `json:"maximum,omitempty"`
	NoData     string           `json:"no_data,omitempty"`
	Color      string           `json:"color,omitempty"`
	LineWidth  int              `json:"line_width,omitempty"`
	Source     string           `json:"source,omitempty"`
	IDs        []string         `json:"ids,omitempty"`
	Edges      []MapRenderEdge  `json:"edges,omitempty"`
	Limit      int              `json:"limit,omitempty"`
	Threshold  float64          `json:"threshold,omitempty"`
	Classes    int              `json:"classes,omitempty"`
	Texture    string           `json:"texture,omitempty"`
}

type MapRenderSpec struct {
	Recipe               string          `json:"recipe,omitempty"`
	Theme                string          `json:"theme,omitempty"`
	Level                string          `json:"level,omitempty"`
	BoundaryLevels       []string        `json:"boundary_levels,omitempty"`
	Title                string          `json:"title,omitempty"`
	Subtitle             string          `json:"subtitle,omitempty"`
	Target               string          `json:"target,omitempty"`
	Year                 int             `json:"year,omitempty"`
	HistoryYear          int             `json:"history_year,omitempty"`
	Width                int             `json:"width,omitempty"`
	Height               int             `json:"height,omitempty"`
	Padding              int             `json:"padding,omitempty"`
	Background           string          `json:"background,omitempty"`
	FontPath             string          `json:"font_path,omitempty"`
	TerrainOverlay       *bool           `json:"terrain_overlay,omitempty"`
	Style                string          `json:"style,omitempty"`
	Layout               string          `json:"layout,omitempty"`
	ReliefStrength       string          `json:"relief_strength,omitempty"`
	LabelLanguage        string          `json:"label_language,omitempty"`
	ColorStrategy        string          `json:"color_strategy,omitempty"`
	Supersample          int             `json:"supersample,omitempty"`
	Route                *MapRouteResult `json:"route,omitempty"`
	RouteProvinceIDs     []int           `json:"route_province_ids,omitempty"`
	AutoContext          bool            `json:"auto_context,omitempty"`
	CorridorRadiusPixels int             `json:"corridor_radius_pixels,omitempty"`
	ContextLevel         string          `json:"context_level,omitempty"`
	Verbose              bool            `json:"verbose,omitempty"`
	// HitMap asks a basemap for a companion lookup plate so a page can resolve
	// the entity under the pointer exactly. It roughly doubles the returned image
	// payload, so it stays opt-in.
	HitMap      bool             `json:"hit_map,omitempty"`
	Layers      []MapRenderLayer `json:"layers"`
	uiScale     float64
	deviceScale float64
}

type MapRenderSourceSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type MapRenderSourceViewport struct {
	MinX int `json:"min_x"`
	MinY int `json:"min_y"`
	MaxX int `json:"max_x"`
	MaxY int `json:"max_y"`
}

type MapRenderCoordinateTransform struct {
	ScaleX  float64 `json:"scale_x"`
	ScaleY  float64 `json:"scale_y"`
	OffsetX float64 `json:"offset_x"`
	OffsetY float64 `json:"offset_y"`
}

type MapRenderRoutePointOutput struct {
	ProvinceID int     `json:"province_id"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Role       string  `json:"role,omitempty"`
	PathIndex  *int    `json:"path_index,omitempty"`
}

type MapRenderTimings struct {
	CorridorMS int64 `json:"corridor_ms"`
	RenderMS   int64 `json:"render_ms"`
	EncodeMS   int64 `json:"encode_ms"`
}

// MapOverlayEntity is one addressable area on a basemap plate, expressed in the
// plate's own pixel coordinates. It exists so a caller compositing the image into
// HTML can place text, tooltips and hit regions where they actually belong
// instead of estimating positions from the picture.
type MapOverlayEntity struct {
	ID        string  `json:"id"`
	LabelZH   string  `json:"label_zh,omitempty"`
	LabelEN   string  `json:"label_en,omitempty"`
	Tint      string  `json:"tint,omitempty"`
	CenterX   float64 `json:"center_x"`
	CenterY   float64 `json:"center_y"`
	MinX      int     `json:"min_x"`
	MinY      int     `json:"min_y"`
	MaxX      int     `json:"max_x"`
	MaxY      int     `json:"max_y"`
	Provinces int     `json:"provinces"`
	HitColor  string  `json:"hit_color,omitempty"`
}

// MapRenderOverlay accompanies a basemap so the plate can be annotated
// externally. Coordinates are final output pixels, already accounting for
// supersampling, so they index the returned PNG directly.
type MapRenderOverlay struct {
	Level      string             `json:"level"`
	CoordSpace string             `json:"coord_space"`
	Entities   []MapOverlayEntity `json:"entities,omitempty"`
	// HitMap describes the companion lookup plate when one was requested.
	HitMap *MapOverlayHitMap `json:"hit_map,omitempty"`
}

// MapOverlayHitMap tells a caller how to read the companion lookup plate.
type MapOverlayHitMap struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Empty  string `json:"empty_color"`
	Note   string `json:"note"`
}

type MapLegendItem struct {
	Label string  `json:"label"`
	Color string  `json:"color"`
	Value float64 `json:"value,omitempty"`
	// Kind selects how the swatch is drawn. A boundary has to be shown as a
	// line and a settlement as its own glyph; painting a colour chip for every
	// entry mislabelled most of the plate.
	Kind   string `json:"kind,omitempty"`
	Weight int    `json:"weight,omitempty"`
	Glyph  string `json:"glyph,omitempty"`
}

type MapRenderResult struct {
	Intent            string                       `json:"intent"`
	Summary           string                       `json:"summary"`
	Title             string                       `json:"title,omitempty"`
	Target            string                       `json:"target"`
	Year              int                          `json:"year"`
	HistoryYear       int                          `json:"history_year,omitempty"`
	Width             int                          `json:"width"`
	Height            int                          `json:"height"`
	ResolutionMode    string                       `json:"resolution_mode"`
	ResolutionReason  string                       `json:"resolution_reason,omitempty"`
	Bytes             int                          `json:"bytes"`
	PNGCompression    string                       `json:"png_compression,omitempty"`
	Coverage          float64                      `json:"coverage"`
	Provenance        []string                     `json:"provenance"`
	Legend            []MapLegendItem              `json:"legend,omitempty"`
	Metrics           []MapMetricResult            `json:"metrics,omitempty"`
	Warnings          []string                     `json:"warnings,omitempty"`
	IntegrityStatus   string                       `json:"integrity_status"`
	IntegrityIssues   []MapIntegrityIssue          `json:"integrity_warnings,omitempty"`
	LayerCounts       map[string]int               `json:"layer_counts"`
	SourceMap         MapRenderSourceSize          `json:"source_map"`
	SourceViewport    MapRenderSourceViewport      `json:"source_viewport"`
	Output            MapRenderSourceSize          `json:"output"`
	Transform         MapRenderCoordinateTransform `json:"transform"`
	ResolvedFrom      *MapResolvedSubject          `json:"resolved_from,omitempty"`
	ResolvedTo        *MapResolvedSubject          `json:"resolved_to,omitempty"`
	RouteLegs         []MapRouteLeg                `json:"route_legs,omitempty"`
	RoutePointsOutput []MapRenderRoutePointOutput  `json:"route_points_output,omitempty"`
	Overlay           *MapRenderOverlay            `json:"overlay,omitempty"`
	Timings           MapRenderTimings             `json:"timings_ms"`
	PNG               []byte                       `json:"-"`
	HitPNG            []byte                       `json:"-"`
}

type renderViewport struct {
	MinX, MinY int
	MaxX, MaxY int
	Scale      float64
	Padding    int
	OffsetX    int
	OffsetY    int
	Width      int
	Height     int
}

type mapTextRenderer struct {
	parsed *opentype.Font
	faces  map[int]font.Face
}

func loadMapTextRenderer(path string) (*mapTextRenderer, []string) {
	if path == "" {
		path = strings.TrimSpace(os.Getenv("CK3_INDEX_MAP_FONT"))
	}
	if path == "" {
		return &mapTextRenderer{}, []string{"no CJK font configured; localized map labels will be hidden"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return &mapTextRenderer{}, []string{"could not read configured map font; localized map labels will be hidden"}
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		collection, collectionErr := opentype.ParseCollection(data)
		if collectionErr != nil || collection.NumFonts() == 0 {
			return &mapTextRenderer{}, []string{"could not parse configured map font; localized map labels will be hidden"}
		}
		parsed, err = collection.Font(0)
	}
	if err != nil {
		return &mapTextRenderer{}, []string{"could not select configured map font; localized map labels will be hidden"}
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 13, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		return &mapTextRenderer{}, []string{"could not initialize configured map font; localized map labels will be hidden"}
	}
	return &mapTextRenderer{parsed: parsed, faces: map[int]font.Face{13: face}}, nil
}

func (r *mapTextRenderer) Close() {
	for _, face := range r.faces {
		if closer, ok := face.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

func (r *mapTextRenderer) SupportsLocalizedText() bool { return r != nil && r.parsed != nil }

func (r *mapTextRenderer) faceFor(size int) font.Face {
	if r == nil || r.parsed == nil {
		return nil
	}
	if size < 7 {
		size = 7
	}
	if face := r.faces[size]; face != nil {
		return face
	}
	face, err := opentype.NewFace(r.parsed, &opentype.FaceOptions{Size: float64(size), DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	r.faces[size] = face
	return face
}

func (r *mapTextRenderer) Draw(canvas *image.RGBA, x, y int, text string, c color.RGBA) {
	r.DrawSize(canvas, x, y, text, c, 13)
}

func (r *mapTextRenderer) DrawSize(canvas *image.RGBA, x, y int, text string, c color.RGBA, size int) {
	face := r.faceFor(size)
	if face == nil {
		drawBitmapInk(canvas, x, y, strings.ToUpper(text), c)
		return
	}
	// Every other drawing helper here takes straight (non-premultiplied) alpha,
	// but image/draw treats color.RGBA as premultiplied. Handing it a translucent
	// straight colour overflows the compositing arithmetic and fringes the
	// lettering with stray hues, so convert at the boundary.
	drawer := font.Drawer{Dst: canvas, Src: image.NewUniform(premultiplyRGBA(c)), Face: face,
		Dot: fixed.P(x, y+face.Metrics().Ascent.Ceil())}
	drawer.DrawString(text)
}

// premultiplyRGBA converts a straight-alpha colour into the premultiplied form
// image/draw expects.
func premultiplyRGBA(c color.RGBA) color.RGBA {
	if c.A == 255 {
		return c
	}
	a := uint32(c.A)
	scale := func(v uint8) uint8 { return uint8((uint32(v) * a) / 255) }
	return color.RGBA{scale(c.R), scale(c.G), scale(c.B), c.A}
}

// DrawOutlined draws text with a halo. The halo is paper-coloured: it lifts ink
// lettering off a busy wash without stamping a dark block onto the sheet.
func (r *mapTextRenderer) DrawOutlined(canvas *image.RGBA, x, y int, text string, c color.RGBA) {
	r.DrawOutlinedSize(canvas, x, y, text, c, 13)
}

func (r *mapTextRenderer) DrawOutlinedSize(canvas *image.RGBA, x, y int, text string, c color.RGBA, size int) {
	halo := color.RGBA{245, 236, 212, 205}
	spread := maxInt(1, size/9)
	for radius := spread; radius >= 1; radius-- {
		for _, offset := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, 1}, {-1, 1}, {1, -1}} {
			r.DrawSize(canvas, x+offset[0]*radius, y+offset[1]*radius, text, halo, size)
		}
	}
	r.DrawSize(canvas, x, y, text, c, size)
}

func (r *mapTextRenderer) Width(text string) int {
	return r.WidthSize(text, 13)
}

func (r *mapTextRenderer) WidthSize(text string, size int) int {
	face := r.faceFor(size)
	if face == nil {
		return len([]rune(text)) * 6
	}
	drawer := font.Drawer{Face: face}
	return drawer.MeasureString(text).Ceil()
}

func (r *mapTextRenderer) Height() int {
	return r.HeightSize(13)
}

func (r *mapTextRenderer) HeightSize(size int) int {
	face := r.faceFor(size)
	if face == nil {
		return 7
	}
	return face.Metrics().Height.Ceil()
}

// Numeric themes shade by ink density; categorical themes use the tint pots.
// Both live in map_style.go so the whole look is defined in one place.
func sequentialPalette(name string) []color.RGBA { return parchmentRamp(name) }

func parseRenderColor(value string, fallback color.RGBA) color.RGBA {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 && len(value) != 8 {
		return fallback
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return fallback
	}
	result := color.RGBA{decoded[0], decoded[1], decoded[2], 255}
	if len(decoded) == 4 {
		result.A = decoded[3]
	}
	return result
}

func interpolateColor(stops []color.RGBA, ratio float64) color.RGBA {
	if len(stops) == 0 {
		return color.RGBA{120, 120, 120, 255}
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	position := ratio * float64(len(stops)-1)
	lo := int(math.Floor(position))
	hi := int(math.Ceil(position))
	blend := position - float64(lo)
	mix := func(a, b uint8) uint8 { return uint8(math.Round(float64(a)*(1-blend) + float64(b)*blend)) }
	return color.RGBA{mix(stops[lo].R, stops[hi].R), mix(stops[lo].G, stops[hi].G), mix(stops[lo].B, stops[hi].B), mix(stops[lo].A, stops[hi].A)}
}

func categoryColor(category string) color.RGBA {
	h := fnv.New32a()
	_, _ = h.Write([]byte(category))
	return parchmentCategoryWashes[int(h.Sum32())%len(parchmentCategoryWashes)]
}

func (db *DB) politicalEntityColors(ctx context.Context, metric MapMetricResult, muted bool) (map[string]color.RGBA, error) {
	strategy := "native"
	if muted {
		strategy = "muted"
	}
	return db.politicalEntityColorsWithStrategy(ctx, metric, strategy)
}

func (db *DB) politicalEntityColorsWithStrategy(ctx context.Context, metric MapMetricResult, strategy string) (map[string]color.RGBA, error) {
	selected := map[string]bool{}
	for _, item := range metric.Values {
		selected[item.ID] = true
	}
	neighbors := map[string]map[string]bool{}
	levelCode := map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[metric.Level]
	if levelCode != "" {
		rows, err := db.sql.QueryContext(ctx, `SELECT title_id,neighbor_id FROM map_title_adjacencies WHERE level=?`, levelCode)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				return nil, err
			}
			if !selected[a] || !selected[b] {
				continue
			}
			if neighbors[a] == nil {
				neighbors[a] = map[string]bool{}
			}
			neighbors[a][b] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := len(neighbors[ids[i]]), len(neighbors[ids[j]])
		if di != dj {
			return di > dj
		}
		return ids[i] < ids[j]
	})
	assigned := map[string]int{}
	colors := map[string]color.RGBA{}
	for _, id := range ids {
		var rgb sql.NullInt64
		if err := db.sql.QueryRowContext(ctx, `SELECT color_rgb FROM map_titles WHERE title_id=?`, id).Scan(&rgb); err == nil && rgb.Valid {
			value := uint32(rgb.Int64)
			// The title keeps its hue; the wash decides how it is painted.
			colors[id] = parchmentWash(color.RGBA{uint8(value >> 16), uint8(value >> 8), uint8(value), 255})
			continue
		}
		// Titles with no colour of their own fall back to graph colouring over
		// the tint pots, so neighbours still differ.
		used := map[int]bool{}
		for neighbor := range neighbors[id] {
			if index, ok := assigned[neighbor]; ok {
				used[index] = true
			}
		}
		index := 0
		for used[index] {
			index++
		}
		index %= len(parchmentCategoryWashes)
		assigned[id] = index
		colors[id] = parchmentCategoryWashes[index]
	}
	if strategy != "native" {
		// Lightness and chroma are already pinned, so hue is the only axis left
		// and it can be spread much further than the old harmoniser allowed.
		colors = spreadWashHues(colors, neighbors)
	}
	return colors, nil
}

func (db *DB) LLMMapRender(ctx context.Context, spec MapRenderSpec, opts LLMOptions) (MapRenderResult, error) {
	if err := db.RequireMapDatabase(ctx); err != nil {
		return MapRenderResult{}, err
	}
	explicitSize := spec.Width > 0 || spec.Height > 0
	explicitSupersample := spec.Supersample > 0
	deprecatedHistoryYear := spec.Year <= 0 && spec.HistoryYear > 0
	if spec.Year > 0 && spec.HistoryYear > 0 && spec.Year != spec.HistoryYear {
		return MapRenderResult{}, fmt.Errorf("map_render year conflicts with deprecated history_year")
	}
	if deprecatedHistoryYear {
		spec.Year = spec.HistoryYear
	}
	var err error
	spec, err = resolveMapRenderSpec(spec)
	if err != nil {
		return MapRenderResult{}, err
	}
	if len(spec.Layers) == 0 {
		return MapRenderResult{}, fmt.Errorf("map_render requires at least one layer")
	}
	if spec.Year <= 0 {
		spec.Year = 1
	}
	if spec.HistoryYear <= 0 {
		spec.HistoryYear = spec.Year
	}
	routeIDs := mapRenderRouteProvinceIDs(spec)
	originalTarget := spec.Target
	corridorStarted := time.Now()
	if len(routeIDs) > 0 {
		if spec.CorridorRadiusPixels <= 0 {
			spec.CorridorRadiusPixels = 120
		}
		if spec.CorridorRadiusPixels > 2048 {
			return MapRenderResult{}, fmt.Errorf("corridor_radius_pixels must not exceed 2048")
		}
		if spec.ContextLevel == "" {
			spec.ContextLevel = "duchy"
		}
		contextIDs, err := db.mapRenderRouteContext(ctx, routeIDs, spec.CorridorRadiusPixels, spec.ContextLevel, spec.AutoContext)
		if err != nil {
			return MapRenderResult{}, err
		}
		if spec.AutoContext || spec.Target == "" {
			spec.Target = joinMapProvinceIDs(contextIDs)
		}
	}
	corridorMS := time.Since(corridorStarted).Milliseconds()
	if spec.Target == "" {
		spec.Target = "all"
	}
	if spec.Padding <= 0 {
		spec.Padding = 24
		if spec.Layout == "full_atlas" {
			spec.Padding = 72
		}
	}
	if spec.Layout == "full_atlas" && spec.Target != "all" {
		localized := db.mapRenderLocalizedLabel(ctx, spec.Target)
		if spec.Title == "" {
			suffix := map[string]string{"political": "政治地图", "culture": "文化地图", "faith": "信仰地图", "development": "发展度地图", "terrain": "地形地图"}[spec.Theme]
			spec.Title = strings.TrimSpace(localized.Chinese + suffix)
		}
		if spec.Subtitle == "" {
			suffix := map[string]string{"political": " POLITICAL ATLAS", "culture": " CULTURE ATLAS", "faith": " FAITH ATLAS", "development": " DEVELOPMENT ATLAS", "terrain": " TERRAIN ATLAS"}[spec.Theme]
			spec.Subtitle = strings.TrimSpace(localized.English + suffix)
		}
	}
	resultTarget := spec.Target
	if len(routeIDs) > 0 && originalTarget == "" {
		resultTarget = "route"
	}
	result := MapRenderResult{Intent: "map_render", Title: spec.Title, Target: resultTarget, Year: spec.Year, HistoryYear: spec.HistoryYear, LayerCounts: map[string]int{}, Timings: MapRenderTimings{CorridorMS: corridorMS}}
	if spec.Route != nil {
		from, to := spec.Route.ResolvedFrom, spec.Route.ResolvedTo
		result.ResolvedFrom, result.ResolvedTo = &from, &to
		result.RouteLegs = append([]MapRouteLeg(nil), spec.Route.Legs...)
	}
	if deprecatedHistoryYear {
		result.Warnings = append(result.Warnings, "history_year is deprecated; use year")
	}
	metricByLayer := map[int]MapMetricResult{}
	provinceSet := map[int]bool{}
	provenance := map[string]bool{}
	for index, layer := range spec.Layers {
		result.LayerCounts[layer.Type]++
		if layer.Type == "markers" && (layer.Source == "vegetation" || layer.Source == "holdings" || layer.Source == "lakes" || layer.Source == "strategic_portals") {
			provenance["indexed"] = true
		}
		if layer.Type == "flows" && layer.Source == "strategic_passages" {
			provenance["indexed"] = true
		}
		if layer.Type != "fill" {
			continue
		}
		if layer.Metric == nil {
			layer.Metric = &MapMetricSpec{Target: spec.Target, Level: layer.Level, Year: spec.HistoryYear, Values: layer.Values, SourceNote: layer.SourceNote, Kind: "numeric"}
		}
		if layer.Metric.Target == "" {
			layer.Metric.Target = spec.Target
		}
		if layer.Metric.Year <= 0 {
			layer.Metric.Year = spec.HistoryYear
		}
		if layer.Metric.Level == "" {
			layer.Metric.Level = layer.Level
		}
		if len(layer.Values) > 0 {
			layer.Metric.Values = layer.Values
			layer.Metric.SourceNote = layer.SourceNote
		}
		metric, err := db.LLMMapBuildMetric(ctx, *layer.Metric, opts)
		if err != nil {
			return result, fmt.Errorf("fill layer %d: %w", index, err)
		}
		metricByLayer[index] = metric
		result.Metrics = append(result.Metrics, metric)
		provenance[metric.Provenance] = true
		_, metricGroups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level)
		if err != nil {
			return result, err
		}
		for _, value := range metric.Values {
			for _, pid := range metricGroups[value.ID] {
				provinceSet[pid] = true
			}
		}
	}
	targetPIDs, err := db.mapRenderTargetProvinces(ctx, spec.Target)
	if err != nil {
		return result, err
	}
	for _, pid := range targetPIDs {
		provinceSet[pid] = true
	}
	for _, pid := range routeIDs {
		provinceSet[pid] = true
	}
	integrityIssues, err := db.mapIntegrityIssues(ctx, "", provinceSet)
	if err != nil {
		return result, err
	}
	result.IntegrityStatus = "ok"
	result.IntegrityIssues = integrityIssues
	if len(integrityIssues) > 0 {
		result.IntegrityStatus = "warning"
		result.Warnings = append(result.Warnings, integrityMessages(integrityIssues)...)
	}
	if len(provinceSet) == 0 {
		return result, fmt.Errorf("map_render target selected no provinces")
	}
	if !explicitSize {
		spec.Width, result.ResolutionReason = mapRenderAutoWidth(spec, len(provinceSet))
		result.ResolutionMode = "auto"
		if !explicitSupersample {
			spec.Supersample = 2
			if spec.Width > 4096 {
				spec.Supersample = 1
			}
		}
	} else {
		result.ResolutionMode = "explicit"
		if spec.Width <= 0 {
			spec.Width = 1600
		}
	}
	if spec.Width > MapRenderMaxWidth {
		spec.Width = MapRenderMaxWidth
	}
	if spec.Height > MapRenderMaxHeight {
		spec.Height = MapRenderMaxHeight
	}
	spec.uiScale = mapRenderOutputUIScale(spec.Width)
	spec.deviceScale = spec.uiScale * float64(spec.Supersample)
	for i := range spec.Layers {
		if spec.Layers[i].LineWidth > 0 {
			spec.Layers[i].LineWidth = mapRenderUIPixels(spec, spec.Layers[i].LineWidth)
		}
	}
	viewport, err := db.mapRenderViewport(ctx, provinceSet, spec.Width, spec.Height, spec.Padding)
	if err != nil {
		return result, err
	}
	finalWidth, finalHeight := viewport.Width, viewport.Height
	finalViewport := viewport
	sourceWidth, sourceHeight := db.mapRenderSourceDimensions(ctx)
	result.SourceMap = MapRenderSourceSize{Width: sourceWidth, Height: sourceHeight}
	result.SourceViewport = MapRenderSourceViewport{MinX: finalViewport.MinX, MinY: finalViewport.MinY, MaxX: finalViewport.MaxX, MaxY: finalViewport.MaxY}
	result.Output = MapRenderSourceSize{Width: finalWidth, Height: finalHeight}
	result.Transform = MapRenderCoordinateTransform{
		ScaleX: finalViewport.Scale, ScaleY: finalViewport.Scale,
		OffsetX: float64(finalViewport.OffsetX) - float64(finalViewport.MinX)*finalViewport.Scale,
		OffsetY: float64(finalViewport.OffsetY) - float64(finalViewport.MinY)*finalViewport.Scale,
	}
	result.RoutePointsOutput = db.mapRenderRouteOutputPoints(ctx, spec, routeIDs, finalViewport, finalWidth, finalHeight)
	if len(routeIDs) > 0 && len(result.RoutePointsOutput) != len(routeIDs) {
		return result, fmt.Errorf("map_render could transform %d of %d route points into the final output; verify route province ids and viewport geometry", len(result.RoutePointsOutput), len(routeIDs))
	}
	finalPixels := int64(finalWidth) * int64(finalHeight)
	if finalPixels > mapRenderMaxPixels {
		return result, fmt.Errorf("map output %dx%d exceeds the %dx%d pixel budget", finalWidth, finalHeight, MapRenderMaxWidth, MapRenderMaxHeight)
	}
	workingPixels := finalPixels * int64(spec.Supersample*spec.Supersample)
	if workingPixels > mapRenderMaxWorkingPixels && !explicitSupersample {
		// Automatic sizing picks a width before the viewport's height is known, so
		// the supersampled working area can only be checked here. When the caller
		// did not ask for supersampling by name, drop it rather than refusing to
		// draw: a plate the renderer chose the size of must always be renderable.
		// One sample per pixel always fits, because finalPixels is already capped
		// below the working budget.
		spec.Supersample = 1
		spec.deviceScale = spec.uiScale
		workingPixels = finalPixels
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("supersampling disabled: %dx%d at supersample=2 exceeds the %d working-pixel budget", finalWidth, finalHeight, mapRenderMaxWorkingPixels))
		// Recorded whether or not the caller chose the size: an automatic downgrade
		// has to be visible in the result either way.
		const shed = "supersampling disabled to fit the working-pixel budget"
		if result.ResolutionReason == "" {
			result.ResolutionReason = shed
		} else {
			result.ResolutionReason += "; " + shed
		}
	}
	if workingPixels > mapRenderMaxWorkingPixels {
		return result, fmt.Errorf("map output %dx%d with supersample=%d requires %d working pixels; reduce supersample or output size", finalWidth, finalHeight, spec.Supersample, workingPixels)
	}
	if spec.Supersample > 1 {
		viewport.Scale *= float64(spec.Supersample)
		viewport.Padding *= spec.Supersample
		viewport.OffsetX *= spec.Supersample
		viewport.OffsetY *= spec.Supersample
		viewport.Width *= spec.Supersample
		viewport.Height *= spec.Supersample
	}
	canvas := image.NewRGBA(image.Rect(0, 0, viewport.Width, viewport.Height))
	renderStarted := time.Now()
	// Every layer pass shares one scratch so province geometry, province
	// metadata and the border mask are loaded and allocated once per render
	// rather than once per layer.
	scratch := newMapRenderScratch()
	palette := newParchmentPalette()
	metrics := newStyleMetrics(spec)
	// The sheet comes first and covers every pixel; spec.Background can still
	// override the paper tone for callers that need a specific ground.
	drawParchmentPaper(canvas, palette, metrics, 0x50415243)
	if override := strings.TrimSpace(spec.Background); override != "" {
		tone := parseRenderColor(override, palette.Paper)
		for i := 0; i+3 < len(canvas.Pix); i += 4 {
			blended := mixRGBA(color.RGBA{canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2], 255}, tone, 0.85)
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2] = blended.R, blended.G, blended.B
		}
	}
	textRenderer, fontWarnings := loadMapTextRenderer(spec.FontPath)
	defer textRenderer.Close()
	result.Warnings = append(result.Warnings, fontWarnings...)
	ground, physicalCount, err := db.renderParchmentGround(ctx, scratch, canvas, viewport, palette, metrics)
	if err != nil {
		return result, err
	}
	result.LayerCounts["physical_features"] = physicalCount
	legend := []MapLegendItem{}
	// The first fill layer defines what a basemap overlay describes.
	var overlayTints map[string]color.RGBA
	var overlayMetric MapMetricResult
	for index, layer := range spec.Layers {
		if layer.Type != "fill" {
			continue
		}
		tints, items, warnings, err := db.renderFillLayer(ctx, scratch, canvas, viewport, metricByLayer[index], layer, metrics)
		if err != nil {
			return result, err
		}
		legend = append(legend, items...)
		result.Warnings = append(result.Warnings, warnings...)
		if overlayTints == nil {
			overlayTints, overlayMetric = tints, metricByLayer[index]
		}
	}
	reliefStrength := spec.ReliefStrength
	if spec.TerrainOverlay != nil && !*spec.TerrainOverlay {
		reliefStrength = "none"
	}
	reliefCount, reliefWarnings, err := db.renderParchmentRelief(ctx, scratch, canvas, viewport, ground, palette, metrics, reliefStrength)
	if err != nil {
		return result, err
	}
	result.LayerCounts["relief_hachures"] = reliefCount
	result.Warnings = append(result.Warnings, reliefWarnings...)
	for _, layer := range spec.Layers {
		switch layer.Type {
		case "fill":
			continue
		case "borders":
			if err := db.renderBorderLayer(ctx, scratch, canvas, viewport, provinceSet, layer); err != nil {
				return result, err
			}
		case "markers":
			count, warnings, err := db.renderMarkerLayer(ctx, scratch, canvas, viewport, provinceSet, spec.HistoryYear, layer, palette, metrics)
			if err != nil {
				return result, err
			}
			result.LayerCounts["marker_items"] += count
			if layer.Source == "vegetation" {
				result.LayerCounts["vegetation_symbols"] += count
			} else if layer.Source == "holdings" {
				result.LayerCounts["holding_symbols"] += count
			} else if layer.Source == "lakes" {
				result.LayerCounts["lake_symbols"] += count
			} else if layer.Source == "strategic_portals" {
				result.LayerCounts["strategic_portals"] += count
			}
			result.Warnings = append(result.Warnings, warnings...)
		case "flows":
			count, err := db.renderFlowLayer(ctx, canvas, viewport, provinceSet, metricByLayer, layer)
			if err != nil {
				return result, err
			}
			result.LayerCounts["flow_edges"] += count
		case "labels":
			count, warnings, err := db.renderLabelLayer(ctx, scratch, canvas, viewport, metricByLayer, layer, textRenderer, spec, palette)
			if err != nil {
				return result, err
			}
			result.LayerCounts["label_items"] += count
			result.Warnings = append(result.Warnings, warnings...)
		default:
			return result, fmt.Errorf("unsupported map layer type %q", layer.Type)
		}
	}
	if len(integrityIssues) > 0 {
		count, err := db.renderIntegrityOverlay(ctx, scratch, canvas, viewport, integrityIssues)
		if err != nil {
			return result, err
		}
		result.LayerCounts["integrity_conflicts"] = count
		legend = append(legend, MapLegendItem{Label: "归属冲突 / INTEGRITY CONFLICT", Color: "#a8324a", Kind: LegendKindArea})
	}
	if spec.Layout == "basemap" {
		overlay, err := db.buildMapRenderOverlay(ctx, scratch, overlayMetric, overlayTints, finalViewport, finalWidth, finalHeight)
		if err != nil {
			return result, err
		}
		result.Overlay = overlay
		if spec.HitMap && overlay != nil {
			// Drawn at final resolution and never downscaled, so each pixel keeps
			// an exact entity colour.
			hit, byEntity, err := db.renderEntityHitMap(ctx, scratch, overlayMetric, finalViewport, finalWidth, finalHeight)
			if err != nil {
				return result, err
			}
			if hit != nil {
				var encoded bytes.Buffer
				// Flat index colours compress trivially, so always take the fast path.
				hitEncoder := png.Encoder{CompressionLevel: png.BestSpeed}
				if err := hitEncoder.Encode(&encoded, hit); err != nil {
					return result, err
				}
				result.HitPNG = encoded.Bytes()
				overlay.HitMap = &MapOverlayHitMap{
					Width: finalWidth, Height: finalHeight, Empty: rgbaHex(color.RGBA{A: 255}),
					Note: "read the pixel under the pointer and match hit_color; empty_color means no entity",
				}
				for index := range overlay.Entities {
					overlay.Entities[index].HitColor = byEntity[overlay.Entities[index].ID]
				}
			}
		}
	}
	provenanceList := make([]string, 0, len(provenance))
	for item := range provenance {
		provenanceList = append(provenanceList, item)
	}
	sort.Strings(provenanceList)
	result.Provenance = provenanceList
	result.Legend = buildParchmentLegend(spec, legend)
	// Furniture is placed last so it always sits over the map, and in an order
	// that lets each piece reserve space before the next one is positioned.
	layout := newAtlasLayout(canvas, palette, metrics, textRenderer)
	// A basemap carries no furniture at all: the caller is going to draw its own
	// frame, key and lettering in HTML, where real text and interaction are
	// available. Anything drawn here would only have to be worked around.
	if spec.Layout != "map_only" && spec.Layout != "basemap" {
		layout.drawFrame()
	}
	if spec.Layout == "full_atlas" {
		layout.drawCartouche(spec.Title, spec.Subtitle, spec.Year)
		layout.drawCompass()
		layout.drawLegend(result.Legend)
		layout.drawScaleBar(1 / math.Max(1e-9, viewport.Scale))
		layout.drawProvenance(provenanceList)
	} else if spec.Layout == "light_frame" {
		layout.drawScaleBar(1 / math.Max(1e-9, viewport.Scale))
		layout.drawProvenance(provenanceList)
	}
	if spec.Supersample > 1 {
		finalCanvas := image.NewRGBA(image.Rect(0, 0, finalWidth, finalHeight))
		xdraw.CatmullRom.Scale(finalCanvas, finalCanvas.Bounds(), canvas, canvas.Bounds(), draw.Over, nil)
		canvas = finalCanvas
	}
	result.Timings.RenderMS = time.Since(renderStarted).Milliseconds()
	var encoded bytes.Buffer
	encodeStarted := time.Now()
	encoder, compressionMode := mapRenderPNGEncoder(finalPixels)
	encoded.Grow(int(minInt64(finalPixels*2, 64<<20)))
	if err := encoder.Encode(&encoded, canvas); err != nil {
		return result, err
	}
	result.PNGCompression = compressionMode
	result.PNG = encoded.Bytes()
	result.Timings.EncodeMS = time.Since(encodeStarted).Milliseconds()
	result.Bytes = len(result.PNG)
	result.Width = finalWidth
	result.Height = finalHeight
	var total int
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_provinces WHERE area>0 AND blocked=0`).Scan(&total)
	if total > 0 {
		result.Coverage = float64(len(provinceSet)) / float64(total)
	}
	result.Summary = fmt.Sprintf("Rendered %d layer(s) over %d province(s) as %dx%d PNG (%d bytes).", len(spec.Layers), len(provinceSet), result.Width, result.Height, result.Bytes)
	if len(routeIDs) > 0 && spec.LabelLanguage == "bilingual" && result.LayerCounts["label_items"] == 0 {
		result.Warnings = append(result.Warnings, "Bilingual route context was requested, but no label items were rendered.")
	}
	if len(routeIDs) > 0 && !spec.Verbose {
		for index := range result.Metrics {
			result.Metrics[index].Target = "route_context"
			result.Metrics[index].Values = nil
			result.Metrics[index].Categories = nil
			result.Metrics[index].Outliers = nil
			if result.Metrics[index].RecipeSpec != nil {
				result.Metrics[index].RecipeSpec.Target = "route_context"
				result.Metrics[index].RecipeSpec.Values = nil
				result.Metrics[index].RecipeSpec.Transform.Seeds = nil
			}
		}
	}
	return result, nil
}

func mapRenderRouteProvinceIDs(spec MapRenderSpec) []int {
	seen := map[int]bool{}
	out := []int{}
	appendID := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if spec.Route != nil {
		appendID(spec.Route.ResolvedFrom.ProvinceID)
		for _, point := range spec.Route.Path {
			appendID(point.ProvinceID)
		}
		appendID(spec.Route.ResolvedTo.ProvinceID)
	}
	for _, id := range spec.RouteProvinceIDs {
		appendID(id)
	}
	return out
}

func (db *DB) mapRenderRouteContext(ctx context.Context, routeIDs []int, radius int, level string, auto bool) ([]int, error) {
	set := map[int]bool{}
	for _, id := range routeIDs {
		set[id] = true
	}
	if !auto {
		return sortedMapRouteIDs(set), nil
	}
	graph, err := db.loadMapRouteGraph(ctx)
	if err != nil {
		return nil, err
	}
	corridor := graph.corridorTargets(routeIDs, radius)
	for _, id := range corridor.ProvinceIDs {
		set[id] = true
	}
	countySet, duchySet := map[string]bool{}, map[string]bool{}
	for _, id := range corridor.ProvinceIDs {
		node := graph.Nodes[id]
		if node.County != "" {
			countySet[node.County] = true
		}
		if node.Duchy != "" {
			duchySet[node.Duchy] = true
		}
	}
	for id, node := range graph.Nodes {
		if level == "county" && countySet[node.County] || level == "duchy" && duchySet[node.Duchy] {
			set[id] = true
		}
	}
	return sortedMapRouteIDs(set), nil
}

func joinMapProvinceIDs(ids []int) string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, strconv.Itoa(id))
	}
	return strings.Join(values, ",")
}

func (db *DB) mapRenderSourceDimensions(ctx context.Context) (int, int) {
	var width, height int
	_ = db.sql.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='map_width'`).Scan(&width)
	_ = db.sql.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='map_height'`).Scan(&height)
	if width > 0 && height > 0 {
		return width, height
	}
	_ = db.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(max_x)+1,0),COALESCE(MAX(max_y)+1,0) FROM map_provinces`).Scan(&width, &height)
	return width, height
}

func (db *DB) mapRenderRouteOutputPoints(ctx context.Context, spec MapRenderSpec, routeIDs []int, viewport renderViewport, width, height int) []MapRenderRoutePointOutput {
	if len(routeIDs) == 0 {
		return nil
	}
	pathIndex := map[int]int{}
	origin, destination := 0, 0
	if spec.Route != nil {
		origin, destination = spec.Route.ResolvedFrom.ProvinceID, spec.Route.ResolvedTo.ProvinceID
		for index, point := range spec.Route.Path {
			pathIndex[point.ProvinceID] = index
		}
	} else {
		origin, destination = routeIDs[0], routeIDs[len(routeIDs)-1]
		for index, id := range routeIDs {
			pathIndex[id] = index
		}
	}
	out := make([]MapRenderRoutePointOutput, 0, len(routeIDs))
	for _, id := range routeIDs {
		var x, y float64
		if err := db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_provinces WHERE province_id=?`, id).Scan(&x, &y); err != nil {
			continue
		}
		rx := float64(viewport.OffsetX) + (x-float64(viewport.MinX))*viewport.Scale
		ry := float64(viewport.OffsetY) + (y-float64(viewport.MinY))*viewport.Scale
		if rx < 0 || ry < 0 || rx > float64(width-1) || ry > float64(height-1) {
			continue
		}
		point := MapRenderRoutePointOutput{ProvinceID: id, X: math.Round(rx*100) / 100, Y: math.Round(ry*100) / 100}
		if index, ok := pathIndex[id]; ok {
			copy := index
			point.PathIndex = &copy
		}
		if id == origin {
			point.Role = "origin"
		} else if id == destination {
			point.Role = "destination"
		} else {
			point.Role = "route"
		}
		out = append(out, point)
	}
	return out
}

func resolveMapRenderSpec(spec MapRenderSpec) (MapRenderSpec, error) {
	// MapRenderSpec is taken by value, but its layers (and each layer's metric)
	// are shared with the caller. Resolution rewrites palettes and scales line
	// widths to device pixels, so without this copy rendering the same spec twice
	// scaled the line widths twice and returned a different image each call.
	spec.Layers = append([]MapRenderLayer(nil), spec.Layers...)
	for i := range spec.Layers {
		if spec.Layers[i].Metric != nil {
			metric := *spec.Layers[i].Metric
			spec.Layers[i].Metric = &metric
		}
	}
	validRecipes := map[string]bool{"": true, "duchy_political_atlas": true, "political_atlas": true, "thematic_atlas": true, "strategic_waterways_atlas": true}
	if !validRecipes[spec.Recipe] {
		return spec, fmt.Errorf("unknown map render recipe %q", spec.Recipe)
	}
	if spec.Recipe == "duchy_political_atlas" {
		spec.Recipe = "political_atlas"
		spec.Theme = "political"
		if spec.Level == "" {
			spec.Level = "duchy"
		}
	}
	strategicRecipe := spec.Recipe == "strategic_waterways_atlas"
	if strategicRecipe {
		spec.Recipe = "political_atlas"
		spec.Theme = "political"
		// The world-scale waterways plate needs a small number of readable
		// political masses. Lower ranks turn the overview into a patchwork and
		// obscure the passages, so this dedicated recipe always uses empires.
		spec.Level = "empire"
		if spec.Target == "" {
			spec.Target = "all"
		}
		if spec.ReliefStrength == "" {
			spec.ReliefStrength = "strong"
		}
	}
	if spec.Recipe == "political_atlas" {
		spec.Theme = "political"
	}
	if spec.Recipe == "thematic_atlas" && spec.Theme == "" {
		spec.Theme = "culture"
	}
	if spec.Recipe == "political_atlas" || spec.Recipe == "thematic_atlas" {
		if spec.Year <= 0 {
			spec.Year = 6254
		}
		if spec.HistoryYear <= 0 {
			spec.HistoryYear = spec.Year
		}
		if spec.Style == "" {
			spec.Style = "parchment"
		}
		if spec.Layout == "" {
			spec.Layout = "full_atlas"
		}
		if spec.ReliefStrength == "" {
			spec.ReliefStrength = "subtle"
		}
		if spec.LabelLanguage == "" {
			spec.LabelLanguage = "bilingual"
		}
		if spec.ColorStrategy == "" {
			spec.ColorStrategy = "coordinated"
		}
		if spec.Supersample == 0 {
			spec.Supersample = 2
			if spec.Width > MapRenderMaxWidth/2 {
				spec.Supersample = 1
			}
		}
		if spec.Level == "" {
			spec.Level = "county"
		}
		if len(spec.Layers) == 0 {
			layers, err := buildAdaptiveAtlasLayers(spec)
			if err != nil {
				return spec, err
			}
			if strategicRecipe && len(layers) > 0 {
				cleaned := layers[:0]
				for _, layer := range layers {
					if layer.Type == "markers" && (layer.Source == "vegetation" || layer.Source == "holdings") {
						continue
					}
					if layer.Type == "labels" {
						layer.Level = "empire"
						layer.Limit = 80
					}
					cleaned = append(cleaned, layer)
				}
				layers = cleaned
				insertAt := len(layers) - 1
				strategicLayers := []MapRenderLayer{
					{Type: "flows", Source: "strategic_passages", LineWidth: 2, Limit: 1200},
					{Type: "markers", Source: "strategic_portals", LineWidth: 6, Limit: 200},
				}
				layers = append(layers[:insertAt], append(strategicLayers, layers[insertAt:]...)...)
			}
			if spec.Layout == "basemap" {
				// Lettering is the caller's job on a basemap, so the recipe's label
				// layer is dropped. Explicitly supplied layers are always honoured.
				kept := layers[:0]
				for _, layer := range layers {
					if layer.Type != "labels" {
						kept = append(kept, layer)
					}
				}
				layers = kept
			}
			spec.Layers = layers
		}
	}
	if spec.Style == "" {
		spec.Style = "parchment"
	}
	if spec.Layout == "" {
		spec.Layout = "map_only"
	}
	if spec.ReliefStrength == "" {
		spec.ReliefStrength = "subtle"
	}
	if spec.LabelLanguage == "" {
		spec.LabelLanguage = "chinese"
	}
	if spec.ColorStrategy == "" {
		spec.ColorStrategy = "coordinated"
	}
	if spec.Supersample == 0 {
		spec.Supersample = 1
	}
	valid := func(value string, choices ...string) bool {
		for _, choice := range choices {
			if value == choice {
				return true
			}
		}
		return false
	}
	// The renderer draws a single parchment style. "standard" and
	// "historical_atlas" named two looks that no longer exist and are accepted as
	// aliases so existing specs keep rendering.
	switch spec.Style {
	case "standard", "historical_atlas":
		spec.Style = "parchment"
	}
	if !valid(spec.Style, "parchment") {
		return spec, fmt.Errorf("unsupported map style %q", spec.Style)
	}
	if !valid(spec.Layout, "map_only", "light_frame", "full_atlas", "basemap") {
		return spec, fmt.Errorf("unsupported map layout %q", spec.Layout)
	}
	if !valid(spec.ReliefStrength, "none", "subtle", "strong") {
		return spec, fmt.Errorf("unsupported relief_strength %q", spec.ReliefStrength)
	}
	if !valid(spec.LabelLanguage, "chinese", "english", "bilingual") {
		return spec, fmt.Errorf("unsupported label_language %q", spec.LabelLanguage)
	}
	if !valid(spec.ColorStrategy, "native", "muted", "coordinated") {
		return spec, fmt.Errorf("unsupported color_strategy %q", spec.ColorStrategy)
	}
	if spec.Theme != "" && !valid(spec.Theme, "political", "culture", "faith", "development", "terrain", "custom") {
		return spec, fmt.Errorf("unsupported map theme %q", spec.Theme)
	}
	if spec.Level != "" {
		if _, err := normalizeMapLevel(spec.Level); err != nil {
			return spec, err
		}
	}
	if spec.Supersample != 1 && spec.Supersample != 2 {
		return spec, fmt.Errorf("supersample must be 1 or 2")
	}
	palette := map[string]string{"native": "political", "muted": "political_muted", "coordinated": "political_coordinated"}[spec.ColorStrategy]
	for i := range spec.Layers {
		if spec.Layers[i].Type == "fill" && strings.HasPrefix(spec.Layers[i].Palette, "political") {
			spec.Layers[i].Palette = palette
		}
		if spec.Layers[i].Type == "borders" && spec.Layers[i].Source == "title_color" {
			spec.Layers[i].Palette = palette
		}
	}
	return spec, nil
}

func buildAdaptiveAtlasLayers(spec MapRenderSpec) ([]MapRenderLayer, error) {
	level, err := normalizeMapLevel(spec.Level)
	if err != nil {
		return nil, err
	}
	theme := spec.Theme
	metricYear := spec.HistoryYear
	if metricYear <= 0 {
		metricYear = spec.Year
	}
	metric := MapMetricSpec{Target: spec.Target, Level: level, Year: metricYear, Provenance: "indexed"}
	fill := MapRenderLayer{Type: "fill", Level: level, Texture: "political_material"}
	label := MapRenderLayer{Type: "labels"}
	switch theme {
	case "political":
		metric.Kind, metric.Field, metric.Aggregate = "category", "entity_id", "majority"
		metric.SourceNote = "Indexed de jure title membership and landed-title colors."
		fill.Palette = "political_coordinated"
		label.Source = "entities"
	case "culture":
		metric.Kind, metric.Field, metric.Aggregate = "category", "culture", "majority"
		metric.SourceNote = "Indexed province culture at the selected year."
		fill.Palette = "categorical20"
		label.Source, label.Limit = "categories", 30
	case "faith":
		metric.Kind, metric.Field, metric.Aggregate = "category", "religion", "majority"
		metric.SourceNote = "Indexed province faith at the selected year."
		fill.Palette = "categorical20"
		label.Source, label.Limit = "categories", 30
	case "development":
		metric.Kind, metric.Field, metric.Aggregate = "numeric", "development", "max"
		if level == "duchy" || level == "kingdom" || level == "empire" {
			metric.Aggregate = "mean"
		}
		metric.SourceNote = "Indexed in-game development_level at the selected year."
		fill.Palette, fill.Classes = "development", 7
		label.Source, label.Limit = "top_metric", 24
	case "terrain":
		metric.Kind, metric.Field, metric.Aggregate = "category", "terrain", "majority"
		metric.SourceNote = "Indexed province terrain."
		fill.Palette = "categorical20"
		label.Source, label.Limit = "categories", 24
	case "custom":
		return nil, fmt.Errorf("theme=custom requires explicit layers and a source-noted metric")
	default:
		return nil, fmt.Errorf("adaptive atlas requires theme political, culture, faith, development, terrain, or custom")
	}
	fill.Metric = &metric
	layers := []MapRenderLayer{
		fill,
		{Type: "markers", Source: "vegetation", LineWidth: 4},
		{Type: "borders", Level: level, Color: "#4a3c2c", LineWidth: 1},
	}
	boundaries := append([]string(nil), spec.BoundaryLevels...)
	if len(boundaries) == 0 {
		order := []string{"barony", "county", "duchy", "kingdom", "empire"}
		start := 0
		if level == "province" {
			start = 1
		} else {
			for i, item := range order {
				if item == level {
					start = i + 1
					break
				}
			}
		}
		boundaries = order[start:]
	}
	// Pen weights, not casings. A drawn map separates ranks by line weight over a
	// narrow range; the old stack cased every boundary with a second wider dark
	// line, which on a light sheet turns into a black rope.
	lineWidths := map[string]int{"barony": 1, "county": 1, "duchy": 2, "kingdom": 2, "empire": 3}
	seen := map[string]bool{level: true}
	targetLevel := map[byte]string{'b': "barony", 'c': "county", 'd': "duchy", 'k': "kingdom", 'e': "empire"}
	targetRank := ""
	if len(spec.Target) > 2 && spec.Target[1] == '_' {
		targetRank = targetLevel[spec.Target[0]]
	}
	for _, boundary := range boundaries {
		boundary, err = normalizeMapLevel(boundary)
		if err != nil || boundary == "province" || boundary == targetRank || seen[boundary] {
			if err != nil {
				return nil, err
			}
			continue
		}
		seen[boundary] = true
		line := MapRenderLayer{Type: "borders", Level: boundary, Color: "#3e2f21", LineWidth: lineWidths[boundary]}
		if theme == "political" {
			// Higher ranks take a trace of their own heraldic hue, pulled into
			// the ink range so they stay pen work.
			line.Source, line.Palette = "title_color", "political_coordinated"
		}
		layers = append(layers, line)
	}
	layers = append(layers,
		MapRenderLayer{Type: "borders", Source: "outer", Color: "#2b2015", LineWidth: 2},
		MapRenderLayer{Type: "markers", Source: "holdings", LineWidth: 7},
		label,
	)
	return layers, nil
}

func (db *DB) mapRenderEntityProvinces(ctx context.Context, level, id string) ([]int, error) {
	if level == "province" {
		pid, err := strconv.Atoi(id)
		if err != nil {
			return nil, err
		}
		return []int{pid}, nil
	}
	if level == "region" {
		if regionID, ok := mapRegionTargetID(id); ok {
			id = regionID
		}
		rows, err := db.sql.QueryContext(ctx, `SELECT province_id FROM map_province_regions WHERE region_id=? ORDER BY province_id`, id)
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
	rows, err := db.sql.QueryContext(ctx, `SELECT province_id FROM map_title_provinces WHERE title_id=? ORDER BY province_id`, id)
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

func (db *DB) mapRenderTargetProvinces(ctx context.Context, target string) ([]int, error) {
	if targets := splitMapTargets(target); len(targets) > 1 {
		seen := map[int]bool{}
		for _, item := range targets {
			pids, err := db.mapRenderTargetProvinces(ctx, item)
			if err != nil {
				return nil, err
			}
			for _, pid := range pids {
				seen[pid] = true
			}
		}
		result := make([]int, 0, len(seen))
		for pid := range seen {
			result = append(result, pid)
		}
		sort.Ints(result)
		return result, nil
	}
	query := `SELECT province_id FROM map_provinces WHERE area>0 AND blocked=0`
	args := []any{}
	if target != "" && target != "all" {
		if pid, err := strconv.Atoi(target); err == nil {
			query = `SELECT province_id FROM map_provinces WHERE province_id=?`
			args = []any{pid}
		} else if region, ok := mapRegionTargetID(target); ok {
			query = `SELECT province_id FROM map_province_regions WHERE region_id=? ORDER BY province_id`
			args = []any{region}
		} else {
			query = `SELECT province_id FROM map_title_provinces WHERE title_id=?`
			args = []any{target}
		}
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
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

func (db *DB) mapRenderViewport(ctx context.Context, pids map[int]bool, maxWidth, requestedHeight, padding int) (renderViewport, error) {
	v := renderViewport{MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1, Padding: padding}
	for pid := range pids {
		var minx, miny, maxx, maxy int
		if err := db.sql.QueryRowContext(ctx, `SELECT min_x,min_y,max_x,max_y FROM map_provinces WHERE province_id=?`, pid).Scan(&minx, &miny, &maxx, &maxy); err != nil {
			continue
		}
		if minx < v.MinX {
			v.MinX = minx
		}
		if miny < v.MinY {
			v.MinY = miny
		}
		if maxx > v.MaxX {
			v.MaxX = maxx
		}
		if maxy > v.MaxY {
			v.MaxY = maxy
		}
	}
	if v.MaxX < v.MinX {
		return v, fmt.Errorf("no renderable province geometry")
	}
	sourceW, sourceH := v.MaxX-v.MinX+1, v.MaxY-v.MinY+1
	availableW := maxWidth - 2*padding
	if availableW < 64 {
		availableW = 64
	}
	scale := float64(availableW) / float64(sourceW)
	if requestedHeight > 0 {
		availableH := requestedHeight - 2*padding
		if availableH < 64 {
			availableH = 64
		}
		if s := float64(availableH) / float64(sourceH); s < scale {
			scale = s
		}
		contentW := int(math.Ceil(float64(sourceW) * scale))
		contentH := int(math.Ceil(float64(sourceH) * scale))
		v.Scale = scale
		v.Width = maxWidth
		v.Height = requestedHeight
		v.OffsetX = padding + maxInt(0, (availableW-contentW)/2)
		v.OffsetY = padding + maxInt(0, (availableH-contentH)/2)
		return v, nil
	}
	availableH := MapRenderMaxHeight - 2*padding
	if availableH >= 64 {
		if s := float64(availableH) / float64(sourceH); s < scale {
			scale = s
		}
	}
	v.Scale = scale
	v.Width = int(math.Ceil(float64(sourceW)*scale)) + 2*padding
	v.Height = int(math.Ceil(float64(sourceH)*scale)) + 2*padding
	v.OffsetX = padding
	v.OffsetY = padding
	return v, nil
}

func sourceToRender(v renderViewport, x, y float64) (int, int) {
	return v.OffsetX + int(math.Round((x-float64(v.MinX))*v.Scale)), v.OffsetY + int(math.Round((y-float64(v.MinY))*v.Scale))
}

func mapRenderOutputUIScale(width int) float64 {
	if width <= 0 {
		return 1
	}
	return math.Max(0.5, math.Min(1, float64(width)/1600))
}

func mapRenderAutoWidth(spec MapRenderSpec, provinceCount int) (int, string) {
	features := []string{fmt.Sprintf("%d provinces", provinceCount)}
	detailSignals := 0
	hasLabels := false
	fineLabels := false
	seenMarker := map[string]bool{}
	for _, layer := range spec.Layers {
		switch layer.Type {
		case "labels":
			hasLabels = true
			level := strings.ToLower(strings.TrimSpace(layer.Level))
			if level == "province" || level == "barony" || level == "county" {
				fineLabels = true
			}
		case "markers":
			if !seenMarker[layer.Source] {
				seenMarker[layer.Source] = true
				switch layer.Source {
				case "holdings", "vegetation", "special_buildings", "holy_sites", "strategic_portals":
					detailSignals++
					features = append(features, layer.Source)
				}
			}
		case "flows":
			detailSignals++
			features = append(features, "flows")
		}
	}
	level := strings.ToLower(strings.TrimSpace(spec.Level))
	if level == "province" || level == "barony" || level == "county" {
		fineLabels = fineLabels || hasLabels
	}
	if hasLabels {
		detailSignals++
		features = append(features, "labels")
	}
	if fineLabels {
		detailSignals += 2
		features = append(features, "fine-grained labels")
	}
	if spec.Layout == "full_atlas" {
		detailSignals++
		features = append(features, "full atlas layout")
	}
	if spec.ReliefStrength == "strong" {
		detailSignals++
		features = append(features, "strong relief")
	}

	width := 2560
	tier := "2K-class"
	if provinceCount >= 6000 || provinceCount >= 3500 && detailSignals >= 6 {
		width, tier = 8192, "8K-class"
	} else if provinceCount >= 1200 || provinceCount >= 400 && detailSignals >= 4 || detailSignals >= 6 {
		width, tier = 3840, "4K-class"
	}
	return width, fmt.Sprintf("auto-selected %s long edge from %s", tier, strings.Join(features, ", "))
}

func mapRenderUIPixels(spec MapRenderSpec, pixels int) int {
	if pixels <= 0 {
		return 0
	}
	scale := spec.deviceScale
	if scale <= 0 {
		scale = mapRenderOutputUIScale(spec.Width) * float64(maxInt(1, spec.Supersample))
	}
	return maxInt(1, int(math.Round(float64(pixels)*scale)))
}

func (db *DB) mapProvinceRuns(ctx context.Context, scratch *mapRenderScratch, pid int, boundary bool) ([]MapRun, error) {
	return scratch.runs(ctx, db, pid, boundary)
}

// drawRuns fills the render-space rectangle of every span. The uniform source
// is hoisted out of the loop because a world atlas walks hundreds of thousands
// of spans per layer.
func drawRuns(canvas *image.RGBA, v renderViewport, runs []MapRun, c color.RGBA) {
	if len(runs) == 0 {
		return
	}
	uniform := image.NewUniform(c)
	for _, run := range runs {
		if int(run.Y) < v.MinY || int(run.Y) > v.MaxY || int(run.X1) < v.MinX || int(run.X0) > v.MaxX {
			continue
		}
		x0, y0 := sourceToRender(v, float64(run.X0), float64(run.Y))
		x1, y1 := sourceToRender(v, float64(run.X1+1), float64(run.Y+1))
		if x1 <= x0 {
			x1 = x0 + 1
		}
		if y1 <= y0 {
			y1 = y0 + 1
		}
		draw.Draw(canvas, image.Rect(x0, y0, x1, y1), uniform, image.Point{}, draw.Src)
	}
}

// blendPixel composites one pixel. It indexes Pix directly: the raster passes
// call it tens of millions of times per render, and image.RGBA's accessors
// recompute the same bounds check and pixel offset on every call.
func blendPixel(canvas *image.RGBA, x, y int, c color.RGBA) {
	if c.A == 0 {
		return
	}
	b := canvas.Rect
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	i := (y-b.Min.Y)*canvas.Stride + (x-b.Min.X)*4
	pix := canvas.Pix[i : i+4 : i+4]
	if c.A == 255 {
		pix[0], pix[1], pix[2], pix[3] = c.R, c.G, c.B, c.A
		return
	}
	a := uint32(c.A)
	inv := uint32(255 - c.A)
	pix[0] = uint8((uint32(c.R)*a + uint32(pix[0])*inv + 127) / 255)
	pix[1] = uint8((uint32(c.G)*a + uint32(pix[1])*inv + 127) / 255)
	pix[2] = uint8((uint32(c.B)*a + uint32(pix[2])*inv + 127) / 255)
	pix[3] = 255
}

// maskBounds is the tight render-space rectangle covering every set pixel of a
// mask, plus the row extents inside it. The relief passes use it so they walk
// only the painted land instead of the whole canvas.
type maskBounds struct {
	MinX, MinY int
	MaxX, MaxY int
	RowMinX    []int
	RowMaxX    []int
}

func newMaskBounds(height int) *maskBounds {
	b := &maskBounds{MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1,
		RowMinX: make([]int, height), RowMaxX: make([]int, height)}
	for y := 0; y < height; y++ {
		b.RowMinX[y] = math.MaxInt
		b.RowMaxX[y] = -1
	}
	return b
}

func (b *maskBounds) add(x0, x1, y int) {
	if x1 < x0 {
		return
	}
	if x0 < b.RowMinX[y] {
		b.RowMinX[y] = x0
	}
	if x1 > b.RowMaxX[y] {
		b.RowMaxX[y] = x1
	}
	if x0 < b.MinX {
		b.MinX = x0
	}
	if x1 > b.MaxX {
		b.MaxX = x1
	}
	if y < b.MinY {
		b.MinY = y
	}
	if y > b.MaxY {
		b.MaxY = y
	}
}

func (b *maskBounds) empty() bool { return b.MaxY < b.MinY }

// rowSpan returns the inclusive x range worth scanning on one row, already
// clipped to lo..hi-1.
func (b *maskBounds) rowSpan(y, lo, hi int) (int, int) {
	x0, x1 := b.RowMinX[y], b.RowMaxX[y]
	if x1 < x0 {
		return 0, -1
	}
	if x0 < lo {
		x0 = lo
	}
	if x1 > hi-1 {
		x1 = hi - 1
	}
	return x0, x1
}

func samplePhysicalRaster(raster *mapPhysicalRaster, x, y float64) float64 {
	if raster == nil || raster.Width <= 0 || raster.Height <= 0 || x < 0 || y < 0 || x >= float64(raster.Width) || y >= float64(raster.Height) {
		return 0
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := minInt(raster.Width-1, x0+1), minInt(raster.Height-1, y0+1)
	tx, ty := x-float64(x0), y-float64(y0)
	v00 := float64(raster.Image.GrayAt(x0, y0).Y) / 255
	v10 := float64(raster.Image.GrayAt(x1, y0).Y) / 255
	v01 := float64(raster.Image.GrayAt(x0, y1).Y) / 255
	v11 := float64(raster.Image.GrayAt(x1, y1).Y) / 255
	return (v00*(1-tx)+v10*tx)*(1-ty) + (v01*(1-tx)+v11*tx)*ty
}

// clipRunToCanvas maps one span into render space and clips it to the canvas.
// The pattern and material passes evaluate expensive per-pixel noise, so pixels
// that blendPixel would discard must never be visited at all.
func clipRunToCanvas(canvas *image.RGBA, v renderViewport, run MapRun) (x0, x1, y0, y1 int, ok bool) {
	if int(run.Y) < v.MinY || int(run.Y) > v.MaxY || int(run.X1) < v.MinX || int(run.X0) > v.MaxX {
		return 0, 0, 0, 0, false
	}
	x0, y0 = sourceToRender(v, float64(run.X0), float64(run.Y))
	x1, y1 = sourceToRender(v, float64(run.X1+1), float64(run.Y+1))
	x1, y1 = maxInt(x0+1, x1), maxInt(y0+1, y1)
	b := canvas.Rect
	x0, y0 = maxInt(x0, b.Min.X), maxInt(y0, b.Min.Y)
	x1, y1 = minInt(x1, b.Max.X), minInt(y1, b.Max.Y)
	return x0, x1, y0, y1, x1 > x0 && y1 > y0
}

func (db *DB) renderFillLayer(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, metric MapMetricResult, layer MapRenderLayer, metrics styleMetrics) (map[string]color.RGBA, []MapLegendItem, []string, error) {
	entityColor := map[string]color.RGBA{}
	legend := []MapLegendItem{}
	warnings := []string{}
	if metric.Kind == "category" {
		political := strings.HasPrefix(layer.Palette, "political") || layer.Palette == "" && (metric.RecipeSpec != nil && metric.RecipeSpec.Field == "entity_id" || metric.RecipeSpec == nil && metric.Kind == "category" && layer.Metric != nil && layer.Metric.Field == "entity_id")
		politicalByID := map[string]color.RGBA{}
		if political {
			var err error
			strategy := "native"
			if layer.Palette == "political_muted" {
				strategy = "muted"
			} else if layer.Palette == "political_coordinated" {
				strategy = "coordinated"
			}
			politicalByID, err = db.politicalEntityColorsWithStrategy(ctx, metric, strategy)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		categories := map[string]bool{}
		categoryLabels := map[string]string{}
		for _, item := range metric.Values {
			if item.Category != "" {
				categories[item.Category] = true
			}
			if strings.TrimSpace(item.Label) != "" {
				categoryLabels[item.Category] = strings.TrimSpace(item.Label)
			}
			if political {
				entityColor[item.ID] = politicalByID[item.ID]
			}
		}
		keys := make([]string, 0, len(categories))
		for key := range categories {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		categoryPalette := map[string]color.RGBA{}
		if !political {
			for index, key := range keys {
				categoryPalette[key] = parchmentCategoryWashes[index%len(parchmentCategoryWashes)]
			}
			for _, item := range metric.Values {
				entityColor[item.ID] = categoryPalette[item.Category]
			}
		}
		limit := len(keys)
		if limit > 20 && !political {
			limit = 20
			warnings = append(warnings, "categorical legend truncated to 20 items")
		}
		if political {
			limit = 0
		}
		missingLegendLabels := 0
		for _, key := range keys[:limit] {
			c := categoryPalette[key]
			if political {
				c = politicalByID[key]
			}
			label := categoryLabels[key]
			if label == "" {
				localized := db.mapRenderLocalizedLabel(ctx, key)
				if localized.Chinese != "" && localized.English != "" && localized.Chinese != localized.English {
					label = localized.Chinese + " / " + localized.English
				} else if localized.Chinese != "" {
					label = localized.Chinese
				} else {
					label = localized.English
				}
			}
			if label == "" {
				missingLegendLabels++
				continue
			}
			legend = append(legend, MapLegendItem{Label: label, Color: rgbaHex(c)})
		}
		if missingLegendLabels > 0 {
			warnings = append(warnings, fmt.Sprintf("%d categorical legend item(s) hidden because localization was missing", missingLegendLabels))
		}
	} else {
		minimum, maximum := metric.Stats.P10, metric.Stats.P90
		if layer.Minimum != nil {
			minimum = *layer.Minimum
		}
		if layer.Maximum != nil {
			maximum = *layer.Maximum
		}
		if maximum <= minimum {
			minimum, maximum = metric.Stats.Minimum, metric.Stats.Maximum
		}
		palette := sequentialPalette(layer.Palette)
		classes := layer.Classes
		if classes < 2 {
			classes = 0
		}
		if classes > 12 {
			classes = 12
		}
		breaks := []float64{}
		if classes > 0 && layer.Minimum == nil && layer.Maximum == nil {
			numbers := make([]float64, 0, len(metric.Values))
			for _, item := range metric.Values {
				numbers = append(numbers, item.Value)
			}
			sort.Float64s(numbers)
			for i := 1; i <= classes; i++ {
				breaks = append(breaks, quantile(numbers, float64(i)/float64(classes)))
			}
		}
		for _, item := range metric.Values {
			ratio := 0.5
			if len(breaks) > 0 {
				class := sort.SearchFloat64s(breaks, item.Value)
				if class >= classes {
					class = classes - 1
				}
				ratio = float64(class) / float64(classes-1)
			} else if maximum > minimum {
				ratio = (item.Value - minimum) / (maximum - minimum)
			}
			entityColor[item.ID] = interpolateColor(palette, ratio)
		}
		legendCount := 6
		if classes > 0 {
			legendCount = classes
		}
		for i := 0; i < legendCount; i++ {
			ratio := float64(i) / float64(maxInt(1, legendCount-1))
			value := minimum + (maximum-minimum)*ratio
			if len(breaks) > 0 {
				value = breaks[i]
			}
			c := interpolateColor(palette, ratio)
			legend = append(legend, MapLegendItem{Label: fmt.Sprintf("≤ %.1f", value), Color: rgbaHex(c), Value: value})
		}
	}
	noData := parseRenderColor(layer.NoData, color.RGBA{48, 52, 54, 255})
	if metric.Stats.Missing > 0 {
		legend = append(legend, MapLegendItem{Label: "无数据 / No data", Color: rgbaHex(noData)})
	}
	painted := map[int]bool{}
	_, metricGroups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level)
	if err != nil {
		return nil, nil, nil, err
	}
	// Political areas are laid down as translucent washes so the paper grain,
	// the coastline and the relief all stay visible through them. Painting them
	// opaque is what made the old plates look like flat vector maps.
	for _, item := range metric.Values {
		for _, pid := range metricGroups[item.ID] {
			runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
			if err != nil {
				return nil, nil, nil, err
			}
			boundary, err := db.mapProvinceRuns(ctx, scratch, pid, true)
			if err != nil {
				return nil, nil, nil, err
			}
			drawPigmentWash(canvas, v, runs, boundary, entityColor[item.ID],
				entityWashSeed(item.ID), metrics, 0.66)
			painted[pid] = true
		}
	}
	if metric.Target != "all" {
		pids, _ := db.mapRenderTargetProvinces(ctx, metric.Target)
		for _, pid := range pids {
			if !painted[pid] {
				runs, _ := db.mapProvinceRuns(ctx, scratch, pid, false)
				boundary, _ := db.mapProvinceRuns(ctx, scratch, pid, true)
				drawPigmentWash(canvas, v, runs, boundary, noData, 0x6e6f4441, metrics, 0.42)
			}
		}
	}
	return entityColor, legend, warnings, nil
}

func materialHash(seed uint32, x, y int) float64 {
	value := seed ^ uint32(x)*0x9e3779b1 ^ uint32(y)*0x85ebca6b
	value ^= value >> 16
	value *= 0x7feb352d
	value ^= value >> 15
	value *= 0x846ca68b
	value ^= value >> 16
	return float64(value&0xffff) / 65535
}

func materialNoise(seed uint32, x, y, scale int) float64 {
	if scale < 1 {
		scale = 1
	}
	gx, gy := x/scale, y/scale
	fx, fy := float64(x%scale)/float64(scale), float64(y%scale)/float64(scale)
	fx = fx * fx * (3 - 2*fx)
	fy = fy * fy * (3 - 2*fy)
	n00 := materialHash(seed, gx, gy)
	n10 := materialHash(seed, gx+1, gy)
	n01 := materialHash(seed, gx, gy+1)
	n11 := materialHash(seed, gx+1, gy+1)
	top := n00 + (n10-n00)*fx
	bottom := n01 + (n11-n01)*fx
	return top + (bottom-top)*fy
}

func materialByte(value float64) uint8 {
	if value < 0 {
		value = 0
	}
	if value > 255 {
		value = 255
	}
	return uint8(math.Round(value))
}

func rgbaHex(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A) }

func (db *DB) renderBorderLayer(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, pids map[int]bool, layer MapRenderLayer) error {
	level := "province"
	if layer.Source != "outer" {
		var err error
		level, err = normalizeMapLevel(layer.Level)
		if err != nil {
			return err
		}
	}
	lineWidth := layer.LineWidth
	if lineWidth <= 0 {
		lineWidth = 1
	}
	// Every boundary is pen work. Whatever colour the caller asked for is pulled
	// into the ink range so a bright heraldic red cannot cut across the sheet as a
	// saturated line; the hue survives, the brightness does not.
	c := parchmentPen(parseRenderColor(layer.Color, newParchmentPalette().Ink))
	// A political atlas stacks up to nine border layers; each one needs a full
	// canvas of entity indices, so the buffer is borrowed and cleared instead of
	// reallocated per layer.
	mask := scratch.mask(v.Width * v.Height)
	entityIndex := map[string]int32{}
	indexEntity := map[int32]string{}
	indexColors := map[int32]color.RGBA{}
	regionByProvince := map[int]string{}
	if level == "region" {
		regionIDs := make([]string, 0, len(layer.IDs))
		for _, item := range layer.IDs {
			if regionID, ok := mapRegionTargetID(item); ok {
				item = regionID
			}
			if item = strings.TrimSpace(item); item != "" {
				regionIDs = append(regionIDs, item)
			}
		}
		if len(regionIDs) == 0 {
			return fmt.Errorf("region borders require ids with the selected geographical regions")
		}
		args := make([]any, 0, len(regionIDs))
		for _, regionID := range regionIDs {
			args = append(args, regionID)
		}
		rows, err := db.sql.QueryContext(ctx, `SELECT region_id,province_id FROM map_province_regions
			WHERE region_id IN (`+sqlPlaceholders(len(regionIDs))+`) ORDER BY region_id,province_id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var regionID string
			var pid int
			if err := rows.Scan(&regionID, &pid); err != nil {
				rows.Close()
				return err
			}
			if pids[pid] && regionByProvince[pid] == "" {
				regionByProvince[pid] = regionID
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	var meta map[int]mapProvinceMeta
	if layer.Source != "outer" && level != "province" && level != "region" {
		var err error
		if meta, err = scratch.provinceMeta(ctx, db); err != nil {
			return err
		}
	}
	if err := scratch.primeRuns(ctx, db, len(pids), false); err != nil {
		return err
	}
	maskBox := newMaskBounds(v.Height)
	next := int32(1)
	for _, pid := range sortedProvinceIDs(pids) {
		var id string
		if layer.Source == "outer" {
			id = "target"
		} else if level == "province" {
			id = strconv.Itoa(pid)
		} else if level == "region" {
			id = regionByProvince[pid]
		} else {
			id = meta[pid].levelID(level)
		}
		if id == "" {
			continue
		}
		index := entityIndex[id]
		if index == 0 {
			index = next
			next++
			entityIndex[id] = index
			indexEntity[index] = id
			if layer.Source == "title_color" {
				var rgb sql.NullInt64
				if err := db.sql.QueryRowContext(ctx, `SELECT color_rgb FROM map_titles WHERE title_id=?`, id).Scan(&rgb); err == nil && rgb.Valid {
					value := uint32(rgb.Int64)
					indexColors[index] = parchmentPen(color.RGBA{uint8(value >> 16), uint8(value >> 8), uint8(value), c.A})
				}
			}
		}
		runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
		if err != nil {
			return err
		}
		for _, run := range runs {
			x0, y0 := sourceToRender(v, float64(run.X0), float64(run.Y))
			x1, y1 := sourceToRender(v, float64(run.X1+1), float64(run.Y+1))
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			lowX, highX := maxInt(0, x0), minInt(v.Width, x1)
			if highX <= lowX {
				continue
			}
			for y := maxInt(0, y0); y < minInt(v.Height, y1); y++ {
				row := mask[y*v.Width : y*v.Width+v.Width]
				for x := lowX; x < highX; x++ {
					row[x] = index
				}
				maskBox.add(lowX, highX-1, y)
			}
		}
	}
	if layer.Source == "title_color" && layer.Palette == "political_coordinated" {
		metric := MapMetricResult{Level: level}
		entityIDs := make([]string, 0, len(indexEntity))
		for _, id := range indexEntity {
			entityIDs = append(entityIDs, id)
		}
		sort.Strings(entityIDs)
		for _, id := range entityIDs {
			metric.Values = append(metric.Values, MapMetricValue{ID: id, Category: id})
		}
		coordinated, err := db.politicalEntityColorsWithStrategy(ctx, metric, "coordinated")
		if err != nil {
			return err
		}
		for index, id := range indexEntity {
			if dynamic, ok := coordinated[id]; ok {
				dynamic.A = c.A
				indexColors[index] = parchmentPen(dynamic)
			}
		}
	}
	// Only painted mask pixels can start a boundary, so the neighbour scan is
	// limited to the mask's rows and spans rather than the whole canvas.
	// Boundaries are laid with a soft round nib rather than a hard disc, which is
	// what makes them read as drawn lines instead of stepped blocks.
	outer := layer.Source == "outer"
	if outer {
		// The subject outline follows the realm's edge, not the edge of every
		// impassable patch inside it, so enclosed gaps are closed up first.
		fillMaskHoles(mask, v.Width, v.Height, 1)
		maskBox = newMaskBounds(v.Height)
		for y := 0; y < v.Height; y++ {
			row := mask[y*v.Width : y*v.Width+v.Width]
			lo, hi := -1, -1
			for x := 0; x < v.Width; x++ {
				if row[x] != 0 {
					if lo < 0 {
						lo = x
					}
					hi = x
				}
			}
			if lo >= 0 {
				maskBox.add(lo, hi, y)
			}
		}
	}
	radius := math.Max(0.45, float64(lineWidth)/2)
	for y := maxInt(1, maskBox.MinY); y <= minInt(v.Height-2, maskBox.MaxY); y++ {
		lo, hi := maskBox.rowSpan(y, 1, v.Width-1)
		row := mask[y*v.Width : y*v.Width+v.Width]
		above := mask[(y-1)*v.Width : (y-1)*v.Width+v.Width]
		below := mask[(y+1)*v.Width : (y+1)*v.Width+v.Width]
		for x := lo; x <= hi; x++ {
			id := row[x]
			if id == 0 {
				continue
			}
			left, right := row[x-1], row[x+1]
			up, down := above[x], below[x]
			drawBoundary := false
			if outer {
				drawBoundary = left == 0 || right == 0 || up == 0 || down == 0
			} else {
				drawBoundary = left != 0 && left != id || right != 0 && right != id || up != 0 && up != id || down != 0 && down != id
			}
			if drawBoundary {
				borderColor := c
				if dynamic, ok := indexColors[id]; ok {
					borderColor = dynamic
				}
				stampInkDot(canvas, float64(x), float64(y), radius, borderColor)
			}
		}
	}
	return nil
}

func drawDisc(canvas *image.RGBA, cx, cy, radius int, c color.RGBA) {
	if radius < 0 {
		radius = 0
	}
	// Clip once instead of re-deriving the canvas bounds for every candidate
	// pixel: border layers call this for every boundary pixel on the map.
	b := canvas.Rect
	width, height := b.Dx(), b.Dy()
	y0, y1 := maxInt(0, cy-radius), minInt(height-1, cy+radius)
	x0, x1 := maxInt(0, cx-radius), minInt(width-1, cx+radius)
	limit := radius * radius
	for y := y0; y <= y1; y++ {
		dy := (y - cy) * (y - cy)
		for x := x0; x <= x1; x++ {
			if (x-cx)*(x-cx)+dy <= limit {
				blendPixel(canvas, x, y, c)
			}
		}
	}
}

func (db *DB) renderMarkerLayer(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, pids map[int]bool, year int, layer MapRenderLayer, palette parchmentPalette, metrics styleMetrics) (int, []string, error) {
	if layer.Source == "vegetation" {
		return db.renderVegetationMarkerLayer(ctx, scratch, canvas, v, pids, layer, palette, metrics)
	}
	if layer.Source == "holdings" {
		return db.renderHoldingMarkerLayer(ctx, scratch, canvas, v, pids, year, layer, palette, metrics)
	}
	if layer.Source == "lakes" {
		return db.renderLakeMarkerLayer(ctx, canvas, v, pids, layer, palette, metrics)
	}
	if layer.Source == "strategic_portals" {
		return db.renderStrategicPortalLayer(ctx, canvas, v, pids, layer, palette, metrics)
	}
	selected := map[int]bool{}
	for _, id := range layer.IDs {
		if pid, err := strconv.Atoi(id); err == nil {
			selected[pid] = true
		} else {
			rows, err := db.mapRenderEntityProvinces(ctx, "county", id)
			if err == nil {
				for _, p := range rows {
					selected[p] = true
				}
			}
		}
	}
	if layer.Source == "capitals" {
		for pid := range pids {
			var capital int
			_ = db.sql.QueryRowContext(ctx, `SELECT is_county_capital FROM map_provinces WHERE province_id=?`, pid).Scan(&capital)
			if capital != 0 {
				selected[pid] = true
			}
		}
	} else if layer.Source == "holy_sites" {
		rows, err := db.sql.QueryContext(ctx, `SELECT province_id FROM map_holy_sites WHERE province_id IS NOT NULL`)
		if err != nil {
			return 0, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var pid int
			if err := rows.Scan(&pid); err != nil {
				return 0, nil, err
			}
			if pids[pid] {
				selected[pid] = true
			}
		}
	} else if layer.Source == "special_buildings" {
		date := yearDateKey(year)
		for pid := range pids {
			p, err := db.mapProvinceAt(ctx, pid, date)
			if err != nil {
				return 0, nil, err
			}
			if p.Building.HasSpecialBuilding {
				selected[pid] = true
			}
		}
	}
	c := parseRenderColor(layer.Color, color.RGBA{246, 204, 91, 255})
	radius := layer.LineWidth
	if radius <= 0 {
		radius = 5
	}
	for _, pid := range sortedProvinceIDs(selected) {
		var x, y float64
		if err := db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_provinces WHERE province_id=?`, pid).Scan(&x, &y); err != nil {
			continue
		}
		rx, ry := sourceToRender(v, x, y)
		drawDisc(canvas, rx, ry, radius, color.RGBA{20, 20, 20, 220})
		drawDisc(canvas, rx, ry, maxInt(2, radius-2), c)
	}
	return len(selected), nil, nil
}

func (db *DB) renderFlowLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, metrics map[int]MapMetricResult, layer MapRenderLayer) (int, error) {
	if layer.Source == "strategic_passages" {
		return db.renderStrategicPassageLayer(ctx, canvas, v, pids, layer)
	}
	edges := append([]MapRenderEdge(nil), layer.Edges...)
	metricCenters := map[string]MapPoint{}
	if layer.Source == "metric" && len(metrics) > 0 {
		// Lowest layer index wins so a spec with several fill layers always
		// drives this layer from the same metric.
		var metric MapMetricResult
		lowest := math.MaxInt
		for index, item := range metrics {
			if index < lowest {
				lowest, metric = index, item
			}
		}
		values := map[string]float64{}
		for _, item := range metric.Values {
			values[item.ID] = item.Value
		}
		metricCenters, _ = db.mapMetricEntityCenters(ctx, metric)
		if metric.Level != "province" {
			code := map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[metric.Level]
			rows, err := db.sql.QueryContext(ctx, `SELECT title_id,neighbor_id FROM map_title_adjacencies WHERE level=? AND title_id<neighbor_id`, code)
			if err != nil {
				return 0, err
			}
			defer rows.Close()
			for rows.Next() {
				var a, b string
				if err := rows.Scan(&a, &b); err != nil {
					return 0, err
				}
				fromValue, fromOK := values[a]
				toValue, toOK := values[b]
				if !fromOK || !toOK {
					continue
				}
				delta := fromValue - toValue
				if math.Abs(delta) < layer.Threshold {
					continue
				}
				if delta > 0 {
					edges = append(edges, MapRenderEdge{From: a, To: b, Value: delta})
				} else {
					edges = append(edges, MapRenderEdge{From: b, To: a, Value: -delta})
				}
			}
		}
	}
	c := parseRenderColor(layer.Color, color.RGBA{244, 176, 80, 180})
	width := layer.LineWidth
	if width <= 0 {
		width = 2
	}
	limit := layer.Limit
	if limit <= 0 || limit > len(edges) {
		limit = len(edges)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Value > edges[j].Value })
	for _, edge := range edges[:limit] {
		from, ok := metricCenters[edge.From]
		if !ok {
			from, ok = db.mapRenderEntityCenter(ctx, edge.From)
		}
		if !ok {
			continue
		}
		to, ok := metricCenters[edge.To]
		if !ok {
			to, ok = db.mapRenderEntityCenter(ctx, edge.To)
		}
		if !ok {
			continue
		}
		x0, y0 := sourceToRender(v, from.X, from.Y)
		x1, y1 := sourceToRender(v, to.X, to.Y)
		drawLine(canvas, x0, y0, x1, y1, width, c)
		drawArrowHead(canvas, x0, y0, x1, y1, width, c)
	}
	return limit, nil
}

func (db *DB) mapMetricEntityCenters(ctx context.Context, metric MapMetricResult) (map[string]MapPoint, error) {
	_, groups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level)
	if err != nil {
		return nil, err
	}
	centers := map[string]MapPoint{}
	for _, item := range metric.Values {
		pids := groups[item.ID]
		if len(pids) == 0 {
			continue
		}
		var sumX, sumY, totalArea float64
		for _, pid := range pids {
			var x, y, area float64
			if err := db.sql.QueryRowContext(ctx, `SELECT center_x,center_y,area FROM map_provinces WHERE province_id=?`, pid).Scan(&x, &y, &area); err != nil {
				continue
			}
			if area <= 0 {
				area = 1
			}
			sumX += x * area
			sumY += y * area
			totalArea += area
		}
		if totalArea > 0 {
			centers[item.ID] = MapPoint{X: sumX / totalArea, Y: sumY / totalArea}
		}
	}
	return centers, nil
}

func (db *DB) mapRenderEntityCenter(ctx context.Context, id string) (MapPoint, bool) {
	if pid, err := strconv.Atoi(id); err == nil {
		var p MapPoint
		if db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_provinces WHERE province_id=?`, pid).Scan(&p.X, &p.Y) == nil {
			return p, true
		}
		return p, false
	}
	var p MapPoint
	if db.sql.QueryRowContext(ctx, `SELECT center_x,center_y FROM map_titles WHERE title_id=?`, id).Scan(&p.X, &p.Y) == nil {
		return p, true
	}
	return p, false
}

func drawLine(canvas *image.RGBA, x0, y0, x1, y1, width int, c color.RGBA) {
	dx := absInt(x1 - x0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	dy := -absInt(y1 - y0)
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		drawDisc(canvas, x0, y0, width, c)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}
func drawArrowHead(canvas *image.RGBA, x0, y0, x1, y1, width int, c color.RGBA) {
	angle := math.Atan2(float64(y1-y0), float64(x1-x0))
	size := float64(maxInt(7, width*4))
	for _, offset := range []float64{2.55, -2.55} {
		x := x1 + int(math.Cos(angle+offset)*size)
		y := y1 + int(math.Sin(angle+offset)*size)
		drawLine(canvas, x1, y1, x, y, maxInt(1, width-1), c)
	}
}

func (db *DB) renderLabelLayer(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA, v renderViewport, metrics map[int]MapMetricResult, layer MapRenderLayer, textRenderer *mapTextRenderer, spec MapRenderSpec, palette parchmentPalette) (int, []string, error) {
	ids := append([]string(nil), layer.IDs...)
	metricCenters := map[string]MapPoint{}
	explicitLabels := map[string]string{}
	levels := map[string]string{}
	areas := map[string]int{}
	if (layer.Source == "top_metric" || layer.Source == "entities") && len(metrics) > 0 {
		// Lowest layer index wins so a spec with several fill layers always
		// drives this layer from the same metric.
		var metric MapMetricResult
		lowest := math.MaxInt
		for index, item := range metrics {
			if index < lowest {
				lowest, metric = index, item
			}
		}
		values := append([]MapMetricValue(nil), metric.Values...)
		if layer.Source == "top_metric" {
			sort.Slice(values, func(i, j int) bool { return values[i].Value > values[j].Value })
		}
		limit := layer.Limit
		if limit <= 0 {
			limit = 12
			if layer.Source == "entities" {
				limit = mapRenderLabelCapacity(v, spec)
			}
		}
		if limit > len(values) {
			limit = len(values)
		}
		for _, item := range values[:limit] {
			ids = append(ids, item.ID)
			levels[item.ID] = map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[metric.Level]
			if strings.TrimSpace(item.Label) != "" {
				explicitLabels[item.ID] = item.Label
			}
		}
		metricCenters, _ = db.mapMetricEntityCenters(ctx, metric)
		if _, groups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level); err == nil {
			for id, pids := range groups {
				areas[id] = len(pids)
			}
		}
	}
	if layer.Source == "categories" && len(metrics) > 0 {
		// Lowest layer index wins so a spec with several fill layers always
		// drives this layer from the same metric.
		var metric MapMetricResult
		lowest := math.MaxInt
		for index, item := range metrics {
			if index < lowest {
				lowest, metric = index, item
			}
		}
		centers, _ := db.mapMetricEntityCenters(ctx, metric)
		_, groups, _ := db.mapMetricEntities(ctx, metric.Target, metric.Level)
		type categoryCenterAccum struct {
			x, y, weight float64
		}
		accum := map[string]categoryCenterAccum{}
		for _, item := range metric.Values {
			if item.Category == "" {
				continue
			}
			center, ok := centers[item.ID]
			if !ok {
				continue
			}
			weight := float64(maxInt(1, len(groups[item.ID])))
			itemAccum := accum[item.Category]
			itemAccum.x += center.X * weight
			itemAccum.y += center.Y * weight
			itemAccum.weight += weight
			accum[item.Category] = itemAccum
		}
		bestDistance := map[string]float64{}
		for category := range accum {
			bestDistance[category] = math.Inf(1)
		}
		for _, item := range metric.Values {
			itemAccum, ok := accum[item.Category]
			center, centerOK := centers[item.ID]
			if !ok || !centerOK || itemAccum.weight <= 0 {
				continue
			}
			targetX, targetY := itemAccum.x/itemAccum.weight, itemAccum.y/itemAccum.weight
			distance := math.Hypot(center.X-targetX, center.Y-targetY)
			if distance < bestDistance[item.Category] {
				bestDistance[item.Category] = distance
				metricCenters[item.Category] = center
			}
		}
		for category := range metricCenters {
			ids = append(ids, category)
		}
		sort.Slice(ids, func(i, j int) bool {
			if accum[ids[i]].weight != accum[ids[j]].weight {
				return accum[ids[i]].weight > accum[ids[j]].weight
			}
			return ids[i] < ids[j]
		})
		if layer.Limit > 0 && len(ids) > layer.Limit {
			ids = ids[:layer.Limit]
		}
	}
	if layer.Source == "capitals" && len(ids) == 0 {
		return 0, []string{"labels source=capitals requires explicit ids in the first renderer version"}, nil
	}
	if layer.Source == "titles" || layer.Source == "atlas_titles" {
		allowed := map[string]bool{}
		if targetPIDs, err := db.mapRenderTargetProvinces(ctx, spec.Target); err == nil {
			for _, pid := range targetPIDs {
				var duchy, kingdom, empire string
				if err := db.sql.QueryRowContext(ctx, `SELECT COALESCE(duchy,''),COALESCE(kingdom,''),COALESCE(empire,'') FROM map_provinces WHERE province_id=?`, pid).Scan(&duchy, &kingdom, &empire); err == nil {
					allowed[duchy], allowed[kingdom], allowed[empire] = duchy != "", kingdom != "", empire != ""
				}
			}
		}
		query := `SELECT title_id,title_type,COALESCE(province_count,0),center_x,center_y FROM map_titles WHERE center_x IS NOT NULL AND center_y IS NOT NULL`
		args := []any{}
		if layer.Source == "titles" && layer.Level != "" {
			level, err := normalizeMapLevel(layer.Level)
			if err != nil {
				return 0, nil, err
			}
			query += ` AND title_type=?`
			args = append(args, map[string]string{"barony": "b", "county": "c", "duchy": "d", "kingdom": "k", "empire": "e"}[level])
		} else {
			query += ` AND title_type IN ('e','k','d')`
		}
		rows, err := db.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return 0, nil, err
		}
		for rows.Next() {
			var id, level string
			var area int
			var center MapPoint
			if err := rows.Scan(&id, &level, &area, &center.X, &center.Y); err != nil {
				rows.Close()
				return 0, nil, err
			}
			if len(allowed) > 0 && !allowed[id] {
				continue
			}
			x, y := sourceToRender(v, center.X, center.Y)
			if x < v.Padding/2 || y < v.Padding/2 || x >= v.Width-v.Padding/2 || y >= v.Height-v.Padding/2 {
				continue
			}
			ids = append(ids, id)
			levels[id], areas[id], metricCenters[id] = level, area, center
		}
		if err := rows.Close(); err != nil {
			return 0, nil, err
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		rank := map[string]int{"e": 3, "k": 2, "d": 1}
		if rank[levels[ids[i]]] != rank[levels[ids[j]]] {
			return rank[levels[ids[i]]] > rank[levels[ids[j]]]
		}
		if areas[ids[i]] != areas[ids[j]] {
			return areas[ids[i]] > areas[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if layer.Limit > 0 && len(ids) > layer.Limit {
		ids = ids[:layer.Limit]
	}
	c := parseRenderColor(layer.Color, palette.Ink)
	drawn := 0
	missing := 0
	for _, id := range ids {
		center, ok := metricCenters[id]
		if !ok {
			center, ok = db.mapRenderEntityCenter(ctx, id)
		}
		if !ok {
			continue
		}
		x, y := sourceToRender(v, center.X, center.Y)
		localized := db.mapRenderLocalizedLabel(ctx, id)
		mainLabel, subLabel := explicitLabels[id], ""
		if mainLabel == "" {
			switch spec.LabelLanguage {
			case "english":
				mainLabel = localized.English
			case "bilingual":
				mainLabel, subLabel = localized.Chinese, localized.English
				if mainLabel == "" {
					mainLabel, subLabel = subLabel, ""
				}
			default:
				mainLabel = localized.Chinese
				if mainLabel == "" {
					mainLabel = localized.English
				}
			}
		}
		if mainLabel == "" || !textRenderer.SupportsLocalizedText() {
			missing++
			continue
		}
		basePixels := map[string]int{"e": 22, "k": 17, "d": 12}[levels[id]]
		if basePixels == 0 {
			basePixels = 13
		}
		baseSize := mapRenderUIPixels(spec, basePixels)
		subSize := mapRenderUIPixels(spec, maxInt(8, int(math.Round(float64(basePixels)*0.58))))
		lineGap := mapRenderUIPixels(spec, 1)
		width := textRenderer.WidthSize(mainLabel, baseSize)
		height := textRenderer.HeightSize(baseSize)
		if subLabel != "" && subLabel != mainLabel {
			width = maxInt(width, textRenderer.WidthSize(subLabel, subSize))
			height += textRenderer.HeightSize(subSize) + lineGap
		}
		// A name may step off its centroid to find clear paper. Symbols have
		// already claimed their boxes, so this is what stops settlement glyphs
		// from printing over the very places they label.
		pad := mapRenderUIPixels(spec, 3)
		nudge := height + mapRenderUIPixels(spec, 2)
		placed := false
		var labelX, labelY int
		var bounds image.Rectangle
		for _, offset := range []int{0, -nudge, nudge, -2 * nudge, 2 * nudge} {
			labelX, labelY = x-width/2, y-height/2+offset
			bounds = image.Rect(labelX-pad, labelY-pad, labelX+width+pad, labelY+height+pad)
			if !bounds.In(canvas.Bounds()) {
				continue
			}
			if scratch.claim(bounds) {
				placed = true
				break
			}
		}
		if !placed {
			continue
		}
		mainX := x - textRenderer.WidthSize(mainLabel, baseSize)/2
		textRenderer.DrawOutlinedSize(canvas, mainX, labelY, mainLabel, c, baseSize)
		if subLabel != "" && subLabel != mainLabel {
			subX := x - textRenderer.WidthSize(subLabel, subSize)/2
			textRenderer.DrawOutlinedSize(canvas, subX, labelY+textRenderer.HeightSize(baseSize), strings.ToUpper(subLabel), palette.InkSoft, subSize)
		}
		drawn++
	}
	warnings := []string{}
	if missing > 0 {
		warnings = append(warnings, fmt.Sprintf("%d label(s) hidden because localization or a usable font was missing", missing))
	}
	return drawn, warnings, nil
}

func mapRenderLabelCapacity(v renderViewport, spec MapRenderSpec) int {
	supersample := maxInt(1, spec.Supersample)
	finalWidth := maxInt(1, v.Width/supersample)
	finalHeight := maxInt(1, v.Height/supersample)
	return maxInt(120, minInt(3000, finalWidth*finalHeight/6000))
}

type mapLocalizedLabel struct {
	Chinese string
	English string
}

func (db *DB) mapRenderLocalizedLabel(ctx context.Context, id string) mapLocalizedLabel {
	loc, err := db.QueryLocalization(ctx, id)
	if err != nil {
		return mapLocalizedLabel{}
	}
	result := mapLocalizedLabel{}
	for _, item := range loc.Values {
		language := strings.ToLower(item.Language + " " + item.Path)
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		if result.Chinese == "" && (strings.Contains(language, "chinese") || strings.Contains(language, "simp")) {
			result.Chinese = value
		}
		if result.English == "" && strings.Contains(language, "english") {
			result.English = value
		}
	}
	return result
}

func (db *DB) mapRenderLabel(ctx context.Context, id string, localized bool) string {
	if !localized {
		return strings.ToUpper(id)
	}
	label := db.mapRenderLocalizedLabel(ctx, id)
	if label.Chinese != "" {
		return label.Chinese
	}
	if label.English != "" {
		return label.English
	}
	return id
}

func atlasPrimaryLevel(spec MapRenderSpec) string {
	for _, layer := range spec.Layers {
		if layer.Type != "fill" {
			continue
		}
		if layer.Metric != nil && layer.Metric.Level != "" {
			return strings.ToLower(layer.Metric.Level)
		}
		if layer.Level != "" {
			return strings.ToLower(layer.Level)
		}
	}
	return ""
}

var tinyGlyphs = map[rune][7]byte{
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30}, 'C': {14, 17, 16, 16, 16, 17, 14}, 'D': {30, 17, 17, 17, 17, 17, 30}, 'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16}, 'G': {14, 17, 16, 23, 17, 17, 14}, 'H': {17, 17, 17, 31, 17, 17, 17}, 'I': {14, 4, 4, 4, 4, 4, 14}, 'J': {7, 2, 2, 2, 2, 18, 12}, 'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31}, 'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 25, 21, 19, 17, 17, 17}, 'O': {14, 17, 17, 17, 17, 17, 14}, 'P': {30, 17, 17, 30, 16, 16, 16}, 'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17}, 'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4}, 'U': {17, 17, 17, 17, 17, 17, 14}, 'V': {17, 17, 17, 17, 17, 10, 4}, 'W': {17, 17, 17, 21, 21, 21, 10}, 'X': {17, 17, 10, 4, 10, 17, 17}, 'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14}, '2': {14, 17, 1, 2, 4, 8, 31}, '3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 16, 30, 1, 1, 30}, '6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8}, '8': {14, 17, 17, 14, 17, 17, 14}, '9': {14, 17, 17, 15, 1, 1, 14}, '_': {0, 0, 0, 0, 0, 0, 31}, '-': {0, 0, 0, 31, 0, 0, 0}, '.': {0, 0, 0, 0, 0, 12, 12}, '/': {1, 2, 2, 4, 8, 8, 16}, ' ': {0, 0, 0, 0, 0, 0, 0},
}

// sortedProvinceIDs returns province ids in ascending order. Layer passes must
// paint in a fixed order: at output scales below the source map several source
// provinces land on the same render pixel, so painting in Go map order made the
// rendered image differ between runs of the same request.
func sortedProvinceIDs(pids map[int]bool) []int {
	out := make([]int, 0, len(pids))
	for pid := range pids {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
