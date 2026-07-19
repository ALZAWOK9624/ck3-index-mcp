package indexer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	pathpkg "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	mapSurfaceMaterialCacheVersion = "map_surface_material_v2"
	mapSurfaceRasterFormat         = "png_gray8_material_v1"
	mapSurfaceSampleStride         = 2
)

type mapSurfaceMaterial struct {
	Index                             int
	ID, Name                          string
	Diffuse, Normal, Properties, Mask string
}

type MapSurfaceResourceContext struct {
	ConfiguredPath string `json:"configured_path,omitempty"`
	ResolvedPath   string `json:"resolved_path,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Source         string `json:"source,omitempty"`
	Resolved       bool   `json:"resolved"`
	Resolution     string `json:"resolution,omitempty"`
}

type MapSurfaceMaterialContext struct {
	MaterialIndex       int                       `json:"material_index"`
	MaterialID          string                    `json:"material_id"`
	Name                string                    `json:"name,omitempty"`
	WeightShare         float64                   `json:"weight_share"`
	WeightedSampleScore float64                   `json:"weighted_sample_score"`
	ProvinceCount       int                       `json:"province_count"`
	Rank                int                       `json:"rank"`
	Diffuse             MapSurfaceResourceContext `json:"diffuse"`
	Normal              MapSurfaceResourceContext `json:"normal"`
	Properties          MapSurfaceResourceContext `json:"properties"`
	Mask                MapSurfaceResourceContext `json:"mask"`
}

type MapSurfaceContext struct {
	Available                bool                        `json:"available"`
	DominantMaterialID       string                      `json:"dominant_material_id,omitempty"`
	SampleCount              int                         `json:"sample_count"`
	SampleStridePixels       int                         `json:"sample_stride_pixels"`
	RetainedBlendWeightShare float64                     `json:"retained_blend_weight_share"`
	EffectiveMaterialCount   float64                     `json:"effective_material_count,omitempty"`
	Materials                []MapSurfaceMaterialContext `json:"materials,omitempty"`
	Source                   MapPhysicalFactSource       `json:"source"`
	CacheFingerprint         string                      `json:"cache_fingerprint,omitempty"`
	UnavailableReason        string                      `json:"unavailable_reason,omitempty"`
	Guidance                 []string                    `json:"guidance,omitempty"`
}

type mapMaterialProvinceRun struct {
	ProvinceID int
	X0, X1     int
}

type tgaReader struct {
	File                 *os.File
	Width, Height, Depth int
	TopOrigin            bool
	DataOffset           int64
}

func rebuildMapSurfaceMaterialCache(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, geometryFingerprint string) error {
	settings := active["gfx/map/terrain/materials.settings"]
	indexFile := active["gfx/map/terrain/detail_index.tga"]
	intensityFile := active["gfx/map/terrain/detail_intensity.tga"]
	if settings.Path == "" || indexFile.Path == "" || intensityFile.Path == "" {
		return clearMapSurfaceMaterialCache(ctx, tx)
	}
	filesFingerprint, err := mapGeometryFingerprint(settings.Path, indexFile.Path, intensityFile.Path)
	if err != nil {
		return err
	}
	fingerprint := mapSurfaceMaterialCacheVersion + ":" + geometryFingerprint + ":" + filesFingerprint
	var cached string
	var materialRows, provinceRows, rasterRows int
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_surface_material_fingerprint'`).Scan(&cached)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_surface_materials`).Scan(&materialRows)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_province_materials`).Scan(&provinceRows)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_surface_rasters WHERE fingerprint=?`, fingerprint).Scan(&rasterRows)
	if cached == fingerprint && materialRows > 0 && provinceRows > 0 && rasterRows == 2 {
		return nil
	}

	materials, err := parseSurfaceMaterials(settings.Path)
	if err != nil {
		return err
	}
	if len(materials) == 0 || len(materials) > 255 {
		return fmt.Errorf("surface material catalog has unsupported size %d", len(materials))
	}
	indexTGA, err := openTGA(indexFile.Path)
	if err != nil {
		return fmt.Errorf("detail_index.tga: %w", err)
	}
	defer indexTGA.Close()
	intensityTGA, err := openTGA(intensityFile.Path)
	if err != nil {
		return fmt.Errorf("detail_intensity.tga: %w", err)
	}
	defer intensityTGA.Close()
	if indexTGA.Width != intensityTGA.Width || indexTGA.Height != intensityTGA.Height || indexTGA.Depth != 32 || intensityTGA.Depth != 32 {
		return fmt.Errorf("surface material TGAs must be matching uncompressed 32-bit images")
	}

	runsByY, err := loadMaterialProvinceRuns(ctx, tx, indexTGA.Height)
	if err != nil {
		return err
	}
	indexRaster := image.NewGray(image.Rect(0, 0, indexTGA.Width, indexTGA.Height))
	strengthRaster := image.NewGray(indexRaster.Bounds())
	weights := map[int]map[int]uint64{}
	samples := map[int]int{}
	indexRow := make([]byte, indexTGA.Width*4)
	intensityRow := make([]byte, intensityTGA.Width*4)
	for y := 0; y < indexTGA.Height; y++ {
		if err := indexTGA.ReadRow(y, indexRow); err != nil {
			return err
		}
		if err := intensityTGA.ReadRow(y, intensityRow); err != nil {
			return err
		}
		for x := 0; x < indexTGA.Width; x++ {
			offset := x * 4
			bestChannel := 0
			for channel := 1; channel < 4; channel++ {
				if intensityRow[offset+channel] > intensityRow[offset+bestChannel] {
					bestChannel = channel
				}
			}
			indexRaster.SetGray(x, y, color.Gray{Y: indexRow[offset+bestChannel]})
			strengthRaster.SetGray(x, y, color.Gray{Y: intensityRow[offset+bestChannel]})
		}
		addSample := func(provinceID, x int) bool {
			offset := x * 4
			if weights[provinceID] == nil {
				weights[provinceID] = map[int]uint64{}
			}
			hasWeight := false
			for channel := 0; channel < 4; channel++ {
				materialIndex := int(indexRow[offset+channel])
				weight := uint64(intensityRow[offset+channel])
				if materialIndex >= len(materials) || weight == 0 {
					continue
				}
				weights[provinceID][materialIndex] += weight
				hasWeight = true
			}
			if hasWeight {
				samples[provinceID]++
			}
			return hasWeight
		}
		for _, run := range runsByY[y] {
			fallbackX := -1
			// Absolute sampling grids can entirely miss tiny baronies. Always
			// retain one deterministic observed pixel per land province.
			if samples[run.ProvinceID] == 0 {
				fallbackX = run.X0
				addSample(run.ProvinceID, fallbackX)
			}
			if y%mapSurfaceSampleStride != 0 {
				continue
			}
			start := run.X0
			if remainder := start % mapSurfaceSampleStride; remainder != 0 {
				start += mapSurfaceSampleStride - remainder
			}
			for x := start; x <= run.X1; x += mapSurfaceSampleStride {
				if x == fallbackX {
					continue
				}
				addSample(run.ProvinceID, x)
			}
		}
	}

	for _, table := range []string{"map_surface_rasters", "map_province_materials", "map_surface_materials"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	materialStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_surface_materials(material_index,material_id,name,diffuse_path,normal_path,properties_path,mask_path,source_name,source_rank,source_path) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer materialStmt.Close()
	for _, material := range materials {
		if _, err := materialStmt.ExecContext(ctx, material.Index, material.ID, material.Name, material.Diffuse, material.Normal, material.Properties, material.Mask, settings.Src.Name, settings.Src.Rank, settings.Rel); err != nil {
			return err
		}
	}
	if err := insertMapSurfaceRaster(ctx, tx, "material_index", fingerprint, indexRaster); err != nil {
		return err
	}
	if err := insertMapSurfaceRaster(ctx, tx, "material_strength", fingerprint, strengthRaster); err != nil {
		return err
	}
	provinceStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_province_materials(province_id,material_index,weight_share,sample_count,material_rank) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer provinceStmt.Close()
	insertedProvinceRows := 0
	for pid, materialWeights := range weights {
		type weightedMaterial struct {
			Index  int
			Weight uint64
		}
		items := make([]weightedMaterial, 0, len(materialWeights))
		var total uint64
		for materialIndex, weight := range materialWeights {
			items = append(items, weightedMaterial{Index: materialIndex, Weight: weight})
			total += weight
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Weight != items[j].Weight {
				return items[i].Weight > items[j].Weight
			}
			return items[i].Index < items[j].Index
		})
		if len(items) > 4 {
			items = items[:4]
		}
		for rank, item := range items {
			share := 0.0
			if total > 0 {
				share = float64(item.Weight) / float64(total)
			}
			if _, err := provinceStmt.ExecContext(ctx, pid, item.Index, share, samples[pid], rank+1); err != nil {
				return err
			}
			insertedProvinceRows++
		}
	}
	meta := map[string]string{
		"map_surface_material_fingerprint":     fingerprint,
		"map_surface_material_count":           strconv.Itoa(len(materials)),
		"map_surface_material_province_rows":   strconv.Itoa(insertedProvinceRows),
		"map_surface_material_sample_stride":   strconv.Itoa(mapSurfaceSampleStride),
		"map_surface_material_index_semantics": "materials.settings zero-based order; TGA BGRA channels paired with intensity BGRA",
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_surface_material_build_count','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func clearMapSurfaceMaterialCache(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"map_surface_rasters", "map_province_materials", "map_surface_materials"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key LIKE 'map_surface_material_%'`)
	return err
}

var mapSurfaceMaterialBlockPattern = regexp.MustCompile(`(?s)\{([^{}]*)\}`)

func parseSurfaceMaterials(path string) ([]mapSurfaceMaterial, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	field := func(block, name string) string {
		pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]*)"`)
		match := pattern.FindStringSubmatch(block)
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
		return ""
	}
	var out []mapSurfaceMaterial
	seen := map[string]bool{}
	for _, match := range mapSurfaceMaterialBlockPattern.FindAllStringSubmatch(string(data), -1) {
		block := match[1]
		id := field(block, "id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, mapSurfaceMaterial{
			Index: len(out), ID: id, Name: field(block, "name"), Diffuse: field(block, "diffuse"),
			Normal: field(block, "normal"), Properties: field(block, "material"), Mask: field(block, "mask"),
		})
	}
	return out, nil
}

type mapSurfaceMaterialAggregate struct {
	MaterialIndex                     int
	MaterialID, Name                  string
	Diffuse, Normal, Properties, Mask string
	WeightedSamples                   float64
	Provinces                         map[int]bool
}

func (db *DB) mapSurfaceProvinceContext(ctx context.Context, provinceID, limit int) (*MapSurfaceContext, error) {
	return db.mapSurfaceSelectionContext(ctx, `WITH selected(province_id) AS (VALUES (?)) `, []any{provinceID}, limit)
}

func (db *DB) mapSurfaceSelectionContext(ctx context.Context, selectionCTE string, selectionArgs []any, limit int) (*MapSurfaceContext, error) {
	if limit <= 0 || limit > 8 {
		limit = 4
	}
	source := MapPhysicalFactSource{
		Provenance: "observed",
		Source:     "gfx/map/terrain/materials.settings, detail_index.tga and detail_intensity.tga",
		Algorithm:  "ck3-index-surface-material-zonal-v1",
		Unit:       "normalized blend-weight share",
		Confidence: 1,
	}
	result := &MapSurfaceContext{
		SampleStridePixels: mapSurfaceSampleStride,
		Source:             source,
		CacheFingerprint:   db.metaValueOrEmpty(ctx, "map_surface_material_fingerprint"),
		Guidance: []string{
			"Material placement comes from detail_index.tga channels weighted by detail_intensity.tga and aggregated over province pixels.",
			"Mask, diffuse, normal, and properties paths are observed materials.settings references; resource resolution does not infer visual meaning from texture pixels.",
		},
	}
	rows, err := db.sql.QueryContext(ctx, selectionCTE+`SELECT p.province_id,p.weight_share,p.sample_count,
		m.material_index,m.material_id,m.name,m.diffuse_path,m.normal_path,m.properties_path,m.mask_path
		FROM selected s
		JOIN map_province_materials p ON p.province_id=s.province_id
		JOIN map_surface_materials m ON m.material_index=p.material_index
		ORDER BY p.province_id,p.material_rank,m.material_index`, selectionArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aggregates := map[int]*mapSurfaceMaterialAggregate{}
	provinceSamples := map[int]int{}
	for rows.Next() {
		var provinceID, sampleCount, materialIndex int
		var share float64
		var materialID, name, diffuse, normal, properties, mask string
		if err := rows.Scan(&provinceID, &share, &sampleCount, &materialIndex, &materialID, &name, &diffuse, &normal, &properties, &mask); err != nil {
			return nil, err
		}
		if sampleCount > provinceSamples[provinceID] {
			provinceSamples[provinceID] = sampleCount
		}
		item := aggregates[materialIndex]
		if item == nil {
			item = &mapSurfaceMaterialAggregate{
				MaterialIndex: materialIndex, MaterialID: materialID, Name: name,
				Diffuse: diffuse, Normal: normal, Properties: properties, Mask: mask,
				Provinces: map[int]bool{},
			}
			aggregates[materialIndex] = item
		}
		item.WeightedSamples += share * float64(sampleCount)
		item.Provinces[provinceID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, count := range provinceSamples {
		result.SampleCount += count
	}
	if result.SampleCount == 0 || len(aggregates) == 0 {
		result.UnavailableReason = "No cached surface-material samples cover the selected provinces; run ck3-index scan with active terrain material rasters."
		return result, nil
	}
	switch {
	case result.SampleCount < 4:
		result.Source.Confidence = 0.55
		result.Guidance = append(result.Guidance, fmt.Sprintf("Only %d valid material sample(s) covered this target; treat the dominant blend as sparse evidence.", result.SampleCount))
	case result.SampleCount < 16:
		result.Source.Confidence = 0.75
		result.Guidance = append(result.Guidance, fmt.Sprintf("Only %d valid material samples covered this target; minority blends may be underrepresented.", result.SampleCount))
	case result.SampleCount < 64:
		result.Source.Confidence = 0.9
	}
	items := make([]*mapSurfaceMaterialAggregate, 0, len(aggregates))
	for _, item := range aggregates {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].WeightedSamples != items[j].WeightedSamples {
			return items[i].WeightedSamples > items[j].WeightedSamples
		}
		return items[i].MaterialIndex < items[j].MaterialIndex
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result.Available = true
	result.DominantMaterialID = items[0].MaterialID
	for rank, item := range items {
		share := item.WeightedSamples / float64(result.SampleCount)
		result.RetainedBlendWeightShare += share
		result.Materials = append(result.Materials, MapSurfaceMaterialContext{
			MaterialIndex:       item.MaterialIndex,
			MaterialID:          item.MaterialID,
			Name:                item.Name,
			WeightShare:         share,
			WeightedSampleScore: item.WeightedSamples,
			ProvinceCount:       len(item.Provinces),
			Rank:                rank + 1,
			Diffuse:             db.resolveMapSurfaceResource(ctx, item.Diffuse, false),
			Normal:              db.resolveMapSurfaceResource(ctx, item.Normal, false),
			Properties:          db.resolveMapSurfaceResource(ctx, item.Properties, false),
			Mask:                db.resolveMapSurfaceResource(ctx, item.Mask, true),
		})
	}
	if result.RetainedBlendWeightShare > 0 {
		entropy := 0.0
		for _, item := range result.Materials {
			p := item.WeightShare / result.RetainedBlendWeightShare
			if p > 0 {
				entropy -= p * math.Log(p)
			}
		}
		result.EffectiveMaterialCount = math.Exp(entropy)
	}
	return result, nil
}

func (db *DB) resolveMapSurfaceResource(ctx context.Context, configured string, allowPNGVariant bool) MapSurfaceResourceContext {
	configured = mapSurfaceResourcePath(configured)
	if configured == "" {
		return MapSurfaceResourceContext{Resolved: false, Resolution: "not_configured"}
	}
	result := MapSurfaceResourceContext{ConfiguredPath: configured, Resolution: "missing"}
	candidates := []string{configured}
	if allowPNGVariant && strings.EqualFold(pathpkg.Ext(configured), ".bmp") {
		candidates = append(candidates, strings.TrimSuffix(configured, pathpkg.Ext(configured))+".png")
	}
	for index, candidate := range candidates {
		var resolvedPath, kind, source string
		err := db.sql.QueryRowContext(ctx, `SELECT r.resource_path,r.kind,r.source_name
			FROM resources r JOIN files f ON f.id=r.file_id
			WHERE r.resource_path=? AND f.overridden=0
			ORDER BY r.source_rank,r.path LIMIT 1`, candidate).Scan(&resolvedPath, &kind, &source)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return result
		}
		result.Resolved = true
		result.ResolvedPath = resolvedPath
		result.Kind = kind
		result.Source = source
		result.Resolution = "exact"
		if index > 0 {
			result.Resolution = "extension_variant"
		}
		return result
	}
	return result
}

func mapSurfaceResourcePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(pathpkg.Clean("/"+value), "/")
	if strings.HasPrefix(strings.ToLower(value), "gfx/") {
		return value
	}
	return pathpkg.Join("gfx/map/terrain", value)
}

func openTGA(path string) (*tgaReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 18)
	if _, err := io.ReadFull(f, header); err != nil {
		f.Close()
		return nil, err
	}
	if header[1] != 0 || header[2] != 2 {
		f.Close()
		return nil, fmt.Errorf("only uncompressed true-color TGA is supported")
	}
	width := int(binary.LittleEndian.Uint16(header[12:14]))
	height := int(binary.LittleEndian.Uint16(header[14:16]))
	depth := int(header[16])
	if width <= 0 || height <= 0 || depth != 32 {
		f.Close()
		return nil, fmt.Errorf("invalid TGA dimensions or depth %dx%dx%d", width, height, depth)
	}
	return &tgaReader{File: f, Width: width, Height: height, Depth: depth, TopOrigin: header[17]&0x20 != 0, DataOffset: int64(18 + int(header[0]))}, nil
}

func (t *tgaReader) Close() error { return t.File.Close() }

func (t *tgaReader) ReadRow(y int, buffer []byte) error {
	if y < 0 || y >= t.Height || len(buffer) < t.Width*4 {
		return fmt.Errorf("invalid TGA row %d", y)
	}
	fileY := y
	if !t.TopOrigin {
		fileY = t.Height - 1 - y
	}
	offset := t.DataOffset + int64(fileY*t.Width*4)
	_, err := t.File.ReadAt(buffer[:t.Width*4], offset)
	return err
}

func loadMaterialProvinceRuns(ctx context.Context, tx *sql.Tx, height int) ([][]mapMaterialProvinceRun, error) {
	rowsByY := make([][]mapMaterialProvinceRun, height)
	rows, err := tx.QueryContext(ctx, `SELECT g.province_id,g.fill_rle FROM map_province_geometry g JOIN map_provinces p ON p.province_id=g.province_id WHERE COALESCE(p.block_kind,'')<>'water'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var data []byte
		if err := rows.Scan(&pid, &data); err != nil {
			return nil, err
		}
		runs, err := DecodeMapRuns(data)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.Y < 0 || int(run.Y) >= height {
				continue
			}
			rowsByY[run.Y] = append(rowsByY[run.Y], mapMaterialProvinceRun{ProvinceID: pid, X0: int(run.X0), X1: int(run.X1)})
		}
	}
	return rowsByY, rows.Err()
}

func insertMapSurfaceRaster(ctx context.Context, tx *sql.Tx, key, fingerprint string, raster *image.Gray) error {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, raster); err != nil {
		return err
	}
	b := raster.Bounds()
	_, err := tx.ExecContext(ctx, `INSERT INTO map_surface_rasters(layer_key,width,height,format,fingerprint,data) VALUES(?,?,?,?,?,?)`,
		key, b.Dx(), b.Dy(), mapSurfaceRasterFormat, fingerprint, encoded.Bytes())
	return err
}

func (db *DB) loadMapSurfaceRaster(ctx context.Context, key string) (*mapPhysicalRaster, error) {
	cacheKey := "surface:" + key
	db.physicalRasterMu.Lock()
	defer db.physicalRasterMu.Unlock()
	var width, height int
	var format, fingerprint string
	var data []byte
	if err := db.sql.QueryRowContext(ctx, `SELECT width,height,format,fingerprint,data FROM map_surface_rasters WHERE layer_key=?`, key).Scan(&width, &height, &format, &fingerprint, &data); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if cached, ok := db.physicalRasterCache[cacheKey]; ok && cached.Fingerprint == fingerprint {
		return cached.Raster, nil
	}
	if format != mapSurfaceRasterFormat {
		return nil, fmt.Errorf("unsupported surface raster format %q", format)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	gray, ok := decoded.(*image.Gray)
	if !ok {
		return nil, fmt.Errorf("surface raster %s is not gray8", key)
	}
	raster := &mapPhysicalRaster{Width: width, Height: height, Image: gray}
	if db.physicalRasterCache == nil {
		db.physicalRasterCache = map[string]cachedMapPhysicalRaster{}
	}
	db.physicalRasterCache[cacheKey] = cachedMapPhysicalRaster{Fingerprint: fingerprint, Raster: raster}
	return raster, nil
}

func (db *DB) renderSurfaceMaterialOverlay(ctx context.Context, canvas *image.RGBA, v renderViewport, landMask []bool, strength string) (int, []string, error) {
	indexRaster, err := db.loadMapSurfaceRaster(ctx, "material_index")
	if err != nil {
		return 0, nil, err
	}
	strengthRaster, err := db.loadMapSurfaceRaster(ctx, "material_strength")
	if err != nil {
		return 0, nil, err
	}
	if indexRaster == nil || strengthRaster == nil {
		return 0, []string{"surface material cache unavailable; material tint omitted"}, nil
	}
	materialIDs := map[uint8]string{}
	rows, err := db.sql.QueryContext(ctx, `SELECT material_index,material_id FROM map_surface_materials`)
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var index int
		var id string
		if err := rows.Scan(&index, &id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if index >= 0 && index <= 255 {
			materialIDs[uint8(index)] = id
		}
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	alphaScale := 0.11
	if strength == "strong" {
		alphaScale = 0.18
	}
	for y := 0; y < v.Height; y++ {
		sy := int(math.Round(float64(v.MinY) + float64(y-v.OffsetY)/v.Scale))
		if sy < 0 || sy >= indexRaster.Height {
			continue
		}
		for x := 0; x < v.Width; x++ {
			if !landMask[y*v.Width+x] {
				continue
			}
			sx := int(math.Round(float64(v.MinX) + float64(x-v.OffsetX)/v.Scale))
			if sx < 0 || sx >= indexRaster.Width {
				continue
			}
			weight := strengthRaster.Image.GrayAt(sx, sy).Y
			if weight < 20 {
				continue
			}
			materialID := materialIDs[indexRaster.Image.GrayAt(sx, sy).Y]
			c := surfaceMaterialTint(materialID)
			c.A = uint8(math.Min(42, float64(weight)*alphaScale))
			blendPixel(canvas, x, y, c)
		}
	}
	return 1, nil, nil
}

func surfaceMaterialTint(id string) color.RGBA {
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "snow"), strings.Contains(id, "ice"), strings.Contains(id, "arctic"):
		return color.RGBA{190, 199, 190, 255}
	case strings.Contains(id, "lava"), strings.Contains(id, "volcan"), strings.Contains(id, "black_sand"):
		return color.RGBA{52, 47, 43, 255}
	case strings.Contains(id, "bone"), strings.Contains(id, "boneyard"):
		return color.RGBA{160, 151, 126, 255}
	case strings.Contains(id, "mayik"), strings.Contains(id, "lich"):
		return color.RGBA{69, 78, 79, 255}
	case strings.Contains(id, "desert"), strings.Contains(id, "sand"), strings.Contains(id, "salt"), strings.Contains(id, "beach"):
		return color.RGBA{151, 125, 78, 255}
	case strings.Contains(id, "farm"), strings.Contains(id, "paddy"):
		return color.RGBA{104, 111, 67, 255}
	case strings.Contains(id, "forest"), strings.Contains(id, "grass"), strings.Contains(id, "clover"), strings.Contains(id, "plains"):
		return color.RGBA{70, 91, 65, 255}
	case strings.Contains(id, "wet"), strings.Contains(id, "mud"), strings.Contains(id, "flood"), strings.Contains(id, "moss"):
		return color.RGBA{61, 80, 70, 255}
	case strings.Contains(id, "mountain"), strings.Contains(id, "rock"), strings.Contains(id, "cliff"), strings.Contains(id, "canyon"), strings.Contains(id, "gravel"):
		return color.RGBA{99, 91, 78, 255}
	default:
		return color.RGBA{109, 98, 75, 255}
	}
}
