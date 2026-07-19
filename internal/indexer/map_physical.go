package indexer

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const mapPhysicalRasterFormat = "png_gray8_v2"

type mapPhysicalRaster struct {
	Width, Height int
	Image         *image.Gray
}

type cachedMapPhysicalRaster struct {
	Fingerprint string
	Raster      *mapPhysicalRaster
}

func rebuildMapPhysicalCache(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile) error {
	heightFile := active["map_data/heightmap.png"]
	riverFile := active["map_data/rivers.png"]
	objectFiles := physicalMapObjectFiles(active)
	if heightFile.Path == "" && riverFile.Path == "" && len(objectFiles) == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM map_physical_rasters`)
		return err
	}
	fingerprintPaths := []string{heightFile.Path, riverFile.Path}
	for _, file := range objectFiles {
		fingerprintPaths = append(fingerprintPaths, file.Path)
	}
	fingerprint, err := mapGeometryFingerprint(fingerprintPaths...)
	if err != nil {
		return fmt.Errorf("physical map fingerprint: %w", err)
	}
	var cachedFingerprint string
	var rasterRows int
	_ = tx.QueryRowContext(ctx, `SELECT fingerprint FROM map_physical_rasters LIMIT 1`).Scan(&cachedFingerprint)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_physical_rasters`).Scan(&rasterRows)
	expectedRows := 0
	if heightFile.Path != "" {
		expectedRows += 3
		if len(objectFiles) > 0 {
			expectedRows++
		}
	}
	if riverFile.Path != "" {
		expectedRows++
	}
	if cachedFingerprint == fingerprint && rasterRows == expectedRows {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM map_physical_rasters`); err != nil {
		return err
	}
	if heightFile.Path != "" {
		heightmap, err := decodeMapImage(heightFile.Path)
		if err != nil {
			return fmt.Errorf("heightmap.png: %w", err)
		}
		hillshade, detail, elevation := buildMultiScaleRelief(heightmap)
		if err := insertMapPhysicalRaster(ctx, tx, "hillshade", fingerprint, hillshade); err != nil {
			return err
		}
		if err := insertMapPhysicalRaster(ctx, tx, "terrain_detail", fingerprint, detail); err != nil {
			return err
		}
		if err := insertMapPhysicalRaster(ctx, tx, "elevation", fingerprint, elevation); err != nil {
			return err
		}
		if len(objectFiles) > 0 {
			anchors, count, err := buildTerrainAnchorMask(heightmap, objectFiles)
			if err != nil {
				return err
			}
			if err := insertMapPhysicalRaster(ctx, tx, "terrain_anchors", fingerprint, anchors); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_object_anchor_count',?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(count)); err != nil {
				return err
			}
		}
	}
	if riverFile.Path != "" {
		rivers, err := decodeMapImage(riverFile.Path)
		if err != nil {
			return fmt.Errorf("rivers.png: %w", err)
		}
		mask := buildRiverMask(rivers)
		if err := insertMapPhysicalRaster(ctx, tx, "rivers", fingerprint, mask); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_physical_build_count','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func decodeMapImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func insertMapPhysicalRaster(ctx context.Context, tx *sql.Tx, key, fingerprint string, raster *image.Gray) error {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, raster); err != nil {
		return err
	}
	b := raster.Bounds()
	_, err := tx.ExecContext(ctx, `INSERT INTO map_physical_rasters(layer_key,width,height,format,fingerprint,data) VALUES(?,?,?,?,?,?)`,
		key, b.Dx(), b.Dy(), mapPhysicalRasterFormat, fingerprint, encoded.Bytes())
	return err
}

func (db *DB) loadMapPhysicalRaster(ctx context.Context, key string) (*mapPhysicalRaster, error) {
	db.physicalRasterMu.Lock()
	defer db.physicalRasterMu.Unlock()
	var width, height int
	var format, fingerprint string
	if err := db.sql.QueryRowContext(ctx, `SELECT width,height,format,fingerprint FROM map_physical_rasters WHERE layer_key=?`, key).Scan(&width, &height, &format, &fingerprint); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if cached, ok := db.physicalRasterCache[key]; ok && cached.Fingerprint == fingerprint {
		return cached.Raster, nil
	}
	var data []byte
	if err := db.sql.QueryRowContext(ctx, `SELECT data FROM map_physical_rasters WHERE layer_key=?`, key).Scan(&data); err != nil {
		return nil, err
	}
	if format != mapPhysicalRasterFormat {
		return nil, fmt.Errorf("unsupported physical raster format %q", format)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	gray, ok := decoded.(*image.Gray)
	if !ok {
		bounds := decoded.Bounds()
		gray = image.NewGray(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				gray.Set(x, y, decoded.At(x, y))
			}
		}
	}
	raster := &mapPhysicalRaster{Width: width, Height: height, Image: gray}
	if db.physicalRasterCache == nil {
		db.physicalRasterCache = map[string]cachedMapPhysicalRaster{}
	}
	db.physicalRasterCache[key] = cachedMapPhysicalRaster{Fingerprint: fingerprint, Raster: raster}
	return raster, nil
}

func heightSample(img image.Image, x, y int) float64 {
	b := img.Bounds()
	if x < b.Min.X {
		x = b.Min.X
	} else if x >= b.Max.X {
		x = b.Max.X - 1
	}
	if y < b.Min.Y {
		y = b.Min.Y
	} else if y >= b.Max.Y {
		y = b.Max.Y - 1
	}
	if gray, ok := img.(*image.Gray16); ok {
		return float64(gray.Gray16At(x, y).Y) / 65535
	}
	r, g, b16, _ := img.At(x, y).RGBA()
	return (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b16)) / 65535
}

func buildMultiDirectionalHillshade(heightmap image.Image) *image.Gray {
	hillshade, _, _ := buildMultiScaleRelief(heightmap)
	return hillshade
}

func buildMultiScaleRelief(heightmap image.Image) (*image.Gray, *image.Gray, *image.Gray) {
	b := heightmap.Bounds()
	hillshade := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	detail := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	elevation := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			h0 := heightSample(heightmap, x, y)
			dxFine := (heightSample(heightmap, x+1, y) - heightSample(heightmap, x-1, y)) * 9.0
			dyFine := (heightSample(heightmap, x, y+1) - heightSample(heightmap, x, y-1)) * 9.0
			dxBroad := (heightSample(heightmap, x+4, y) - heightSample(heightmap, x-4, y)) * 2.25
			dyBroad := (heightSample(heightmap, x, y+4) - heightSample(heightmap, x, y-4)) * 2.25
			dx := 0.62*dxFine + 0.38*dxBroad
			dy := 0.62*dyFine + 0.38*dyBroad
			nx, ny, nz := -dx, -dy, 1.0
			length := math.Sqrt(nx*nx + ny*ny + nz*nz)
			nx, ny, nz = nx/length, ny/length, nz/length
			light := func(azimuth float64) float64 {
				altitude := 45 * math.Pi / 180
				azimuth *= math.Pi / 180
				lx := math.Cos(altitude) * math.Sin(azimuth)
				ly := -math.Cos(altitude) * math.Cos(azimuth)
				lz := math.Sin(altitude)
				return math.Max(0, nx*lx+ny*ly+nz*lz)
			}
			shade := 0.72*light(315) + 0.28*light(45)
			broadMean := (heightSample(heightmap, x-5, y) + heightSample(heightmap, x+5, y) + heightSample(heightmap, x, y-5) + heightSample(heightmap, x, y+5)) / 4
			fineMean := (heightSample(heightmap, x-2, y) + heightSample(heightmap, x+2, y) + heightSample(heightmap, x, y-2) + heightSample(heightmap, x, y+2)) / 4
			curvature := (h0-fineMean)*42 + (h0-broadMean)*18
			shade = math.Max(0, math.Min(1, shade+math.Max(-0.10, math.Min(0.10, curvature*0.12))))
			value := uint8(math.Round(math.Max(0, math.Min(1, 0.12+shade*0.88)) * 255))
			detailValue := uint8(math.Round(math.Max(0, math.Min(1, 0.5+curvature)) * 255))
			elevationValue := uint8(math.Round(math.Max(0, math.Min(1, h0)) * 255))
			hillshade.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: value})
			detail.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: detailValue})
			elevation.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: elevationValue})
		}
	}
	return hillshade, detail, elevation
}

type terrainAnchor struct {
	X, Z     float64
	Rotation float64
	Scale    float64
	Kind     string
}

var mapObjectTransformPattern = regexp.MustCompile(`(?s)\btransform\s*=\s*"([^"]*)"`)

func physicalMapObjectFiles(active map[string]activeMapFile) []activeMapFile {
	files := activeFilesWithPrefix(active, "gfx/map/map_object_data/")
	out := files[:0]
	for _, file := range files {
		rel := strings.ToLower(file.Rel)
		if strings.Contains(rel, "mountain") || strings.Contains(rel, "cliff") {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

func parseTerrainAnchors(data []byte, kind string) []terrainAnchor {
	var anchors []terrainAnchor
	for _, match := range mapObjectTransformPattern.FindAllSubmatch(data, -1) {
		fields := strings.Fields(string(match[1]))
		for i := 0; i+9 < len(fields); i += 10 {
			values := make([]float64, 10)
			valid := true
			for j := range values {
				value, err := strconv.ParseFloat(fields[i+j], 64)
				if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
					valid = false
					break
				}
				values[j] = value
			}
			if !valid {
				continue
			}
			scale := math.Max(0.2, math.Min(8, (math.Abs(values[7])+math.Abs(values[9]))/2))
			anchors = append(anchors, terrainAnchor{
				X: values[0], Z: values[2], Rotation: 2 * math.Atan2(values[4], values[6]), Scale: scale, Kind: kind,
			})
		}
	}
	return anchors
}

func buildTerrainAnchorMask(heightmap image.Image, files []activeMapFile) (*image.Gray, int, error) {
	b := heightmap.Bounds()
	mask := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	var anchors []terrainAnchor
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, 0, err
		}
		kind := "cliff"
		if strings.Contains(strings.ToLower(file.Rel), "mountain") {
			kind = "mountain"
		}
		anchors = append(anchors, parseTerrainAnchors(data, kind)...)
	}
	flipZ := terrainAnchorsUseFlippedZ(heightmap, anchors)
	for _, anchor := range anchors {
		y := anchor.Z
		if flipZ {
			y = float64(b.Dy()-1) - y
		}
		drawTerrainAnchor(mask, anchor.X-float64(b.Min.X), y-float64(b.Min.Y), anchor)
	}
	return mask, len(anchors), nil
}

func terrainAnchorsUseFlippedZ(heightmap image.Image, anchors []terrainAnchor) bool {
	b := heightmap.Bounds()
	direct, flipped, count := 0.0, 0.0, 0
	for _, anchor := range anchors {
		if anchor.Kind != "mountain" {
			continue
		}
		x, z := int(math.Round(anchor.X)), int(math.Round(anchor.Z))
		if x < b.Min.X || x >= b.Max.X || z < 0 || z >= b.Dy() {
			continue
		}
		direct += terrainAnchorHeightScore(heightmap, x, b.Min.Y+z)
		flipped += terrainAnchorHeightScore(heightmap, x, b.Max.Y-1-z)
		count++
	}
	return count > 0 && flipped > direct
}

func terrainAnchorHeightScore(heightmap image.Image, x, y int) float64 {
	h := heightSample(heightmap, x, y)
	ruggedness := math.Abs(heightSample(heightmap, x+4, y)-heightSample(heightmap, x-4, y)) + math.Abs(heightSample(heightmap, x, y+4)-heightSample(heightmap, x, y-4))
	return h + ruggedness*3
}

func drawTerrainAnchor(mask *image.Gray, cx, cy float64, anchor terrainAnchor) {
	major := (9.0 + anchor.Scale*7.0)
	minor := 1.8 + anchor.Scale*1.6
	if anchor.Kind == "cliff" {
		major, minor = 6.0+anchor.Scale*5.0, 1.4+anchor.Scale
	}
	radius := int(math.Ceil(major * 1.8))
	cosA, sinA := math.Cos(anchor.Rotation), math.Sin(anchor.Rotation)
	for y := int(math.Floor(cy)) - radius; y <= int(math.Ceil(cy))+radius; y++ {
		if y < 0 || y >= mask.Bounds().Dy() {
			continue
		}
		for x := int(math.Floor(cx)) - radius; x <= int(math.Ceil(cx))+radius; x++ {
			if x < 0 || x >= mask.Bounds().Dx() {
				continue
			}
			dx, dy := float64(x)-cx, float64(y)-cy
			along := cosA*dx + sinA*dy
			across := -sinA*dx + cosA*dy
			weight := math.Exp(-1.8 * (along*along/(major*major) + across*across/(minor*minor)))
			value := uint8(math.Round(math.Min(255, weight*220)))
			if value > mask.GrayAt(x, y).Y {
				mask.SetGray(x, y, color.Gray{Y: value})
			}
		}
	}
}

func isRiverPixel(c color.Color) bool {
	r16, g16, b16, _ := c.RGBA()
	r, g, b := uint8(r16>>8), uint8(g16>>8), uint8(b16>>8)
	// CK3 rivers are blue-to-cyan. White/magenta backgrounds and red/green/yellow
	// control markers intentionally fail these bounds.
	return r <= 12 && b >= 90 && b > g+20
}

func buildRiverMask(rivers image.Image) *image.Gray {
	b := rivers.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if isRiverPixel(rivers.At(x, y)) {
				out.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: 255})
			}
		}
	}
	return out
}
