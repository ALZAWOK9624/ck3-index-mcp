package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"image"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	mapObjectCacheVersion       = "map_objects_v1"
	mapVegetationSourceGridSize = 64
)

type mapObjectInstanceBuild struct {
	Kind       string
	Subtype    string
	ObjectName string
	ProvinceID int
	X, Y       float64
	Rotation   float64
	Scale      float64
	SourceName string
	SourceRank int
	SourcePath string
}

type mapHoldingLocator struct {
	ProvinceID int
	X, Z       float64
	Rotation   float64
	Scale      float64
}

var (
	mapObjectNamePattern = regexp.MustCompile(`(?m)\bname\s*=\s*"([^"]+)"`)
	mapLocatorPattern    = regexp.MustCompile(`(?s)\bid\s*=\s*(\d+).*?\bposition\s*=\s*\{\s*([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s*\}.*?\brotation\s*=\s*\{\s*([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s*\}.*?\bscale\s*=\s*\{\s*([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s*\}`)
)

func rebuildMapObjectCache(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, provinces map[int]*mapProvinceBuild, mapWidth, mapHeight int) error {
	files := mapSymbolObjectFiles(active)
	defFile := active["map_data/definition.csv"]
	provinceFile := active["map_data/provinces.png"]
	if defFile.Path == "" || provinceFile.Path == "" || mapWidth <= 0 || mapHeight <= 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM map_object_instances`)
		return err
	}

	fingerprintPaths := []string{defFile.Path, provinceFile.Path}
	for _, file := range files {
		fingerprintPaths = append(fingerprintPaths, file.Path)
	}
	fingerprint, err := mapGeometryFingerprint(fingerprintPaths...)
	if err != nil {
		return fmt.Errorf("map object fingerprint: %w", err)
	}
	var cachedFingerprint, cachedVersion string
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_object_fingerprint'`).Scan(&cachedFingerprint)
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_object_cache_version'`).Scan(&cachedVersion)
	if cachedFingerprint == fingerprint && cachedVersion == mapObjectCacheVersion {
		return nil
	}

	definitions, err := parseProvinceDefinitions(defFile.Path)
	if err != nil {
		return fmt.Errorf("map object definitions: %w", err)
	}
	provinceImage, err := decodeMapImage(provinceFile.Path)
	if err != nil {
		return fmt.Errorf("map object provinces.png: %w", err)
	}

	holdingFiles, vegetationFiles := splitMapSymbolFiles(files)
	var locators []mapHoldingLocator
	for _, file := range holdingFiles {
		data, readErr := os.ReadFile(file.Path)
		if readErr != nil {
			return readErr
		}
		locators = append(locators, parseHoldingLocators(data)...)
	}
	flipZ := mapObjectsUseFlippedZ(provinceImage, definitions, provinces, locators, mapHeight)

	instances := make([]mapObjectInstanceBuild, 0, len(locators)+4096)
	for _, locator := range locators {
		if provinces[locator.ProvinceID] == nil {
			continue
		}
		y := locator.Z
		if flipZ {
			y = float64(mapHeight-1) - y
		}
		if locator.X < 0 || locator.X >= float64(mapWidth) || y < 0 || y >= float64(mapHeight) {
			continue
		}
		instances = append(instances, mapObjectInstanceBuild{
			Kind: "holding", Subtype: "holding", ObjectName: "buildings", ProvinceID: locator.ProvinceID,
			X: locator.X, Y: y, Rotation: locator.Rotation, Scale: locator.Scale,
			SourceName: holdingFiles[0].Src.Name, SourceRank: holdingFiles[0].Src.Rank, SourcePath: holdingFiles[0].Rel,
		})
	}

	vegetationByCell := map[[2]int]mapObjectInstanceBuild{}
	vegetationScore := map[[2]int]uint64{}
	for _, file := range vegetationFiles {
		data, readErr := os.ReadFile(file.Path)
		if readErr != nil {
			return readErr
		}
		subtype := vegetationSubtype(file.Rel)
		objectName := mapObjectName(data, file.Rel)
		for _, anchor := range parseTerrainAnchors(data, subtype) {
			y := anchor.Z
			if flipZ {
				y = float64(mapHeight-1) - y
			}
			pid := mapObjectProvinceAt(provinceImage, definitions, anchor.X, y)
			province := provinces[pid]
			if province == nil || province.BlockKind == "water" {
				continue
			}
			cell := [2]int{int(anchor.X) / mapVegetationSourceGridSize, int(y) / mapVegetationSourceGridSize}
			score := stableMapObjectScore(file.Rel, subtype, anchor.X, y)
			if previous, exists := vegetationScore[cell]; exists && previous <= score {
				continue
			}
			vegetationScore[cell] = score
			vegetationByCell[cell] = mapObjectInstanceBuild{
				Kind: "vegetation", Subtype: subtype, ObjectName: objectName, ProvinceID: pid,
				X: anchor.X, Y: y, Rotation: anchor.Rotation, Scale: anchor.Scale,
				SourceName: file.Src.Name, SourceRank: file.Src.Rank, SourcePath: file.Rel,
			}
		}
	}
	for _, instance := range vegetationByCell {
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Kind != instances[j].Kind {
			return instances[i].Kind < instances[j].Kind
		}
		if instances[i].ProvinceID != instances[j].ProvinceID {
			return instances[i].ProvinceID < instances[j].ProvinceID
		}
		if instances[i].Y != instances[j].Y {
			return instances[i].Y < instances[j].Y
		}
		return instances[i].X < instances[j].X
	})

	if _, err := tx.ExecContext(ctx, `DELETE FROM map_object_instances`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO map_object_instances(object_kind,subtype,object_name,province_id,x,y,rotation,scale,source_name,source_rank,source_path) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	counts := map[string]int{}
	for _, instance := range instances {
		if _, err := stmt.ExecContext(ctx, instance.Kind, instance.Subtype, instance.ObjectName, instance.ProvinceID, instance.X, instance.Y, instance.Rotation, instance.Scale, instance.SourceName, instance.SourceRank, instance.SourcePath); err != nil {
			return err
		}
		counts[instance.Kind]++
	}
	meta := map[string]string{
		"map_object_fingerprint":      fingerprint,
		"map_object_cache_version":    mapObjectCacheVersion,
		"map_object_count_vegetation": strconv.Itoa(counts["vegetation"]),
		"map_object_count_holding":    strconv.Itoa(counts["holding"]),
		"map_object_z_flipped":        strconv.FormatBool(flipZ),
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_object_build_count','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func mapSymbolObjectFiles(active map[string]activeMapFile) []activeMapFile {
	var out []activeMapFile
	for _, file := range activeFilesWithPrefix(active, "gfx/map/map_object_data/") {
		rel := strings.ToLower(file.Rel)
		if strings.HasSuffix(rel, "/building_locators.txt") || vegetationSubtype(rel) != "" {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

func splitMapSymbolFiles(files []activeMapFile) ([]activeMapFile, []activeMapFile) {
	var holdings, vegetation []activeMapFile
	for _, file := range files {
		if strings.HasSuffix(strings.ToLower(file.Rel), "/building_locators.txt") {
			holdings = append(holdings, file)
		} else {
			vegetation = append(vegetation, file)
		}
	}
	return holdings, vegetation
}

func vegetationSubtype(rel string) string {
	rel = strings.ToLower(filepath.ToSlash(rel))
	if !strings.Contains(rel, "/generated/") {
		return ""
	}
	base := filepath.Base(rel)
	if !strings.Contains(base, "tree") && !strings.Contains(base, "reeds") && !strings.Contains(base, "bush") && !strings.Contains(base, "savanna") {
		return ""
	}
	switch {
	case strings.Contains(base, "reeds"):
		return "reeds"
	case strings.Contains(base, "palm"):
		return "palm"
	case strings.Contains(base, "jungle"), strings.Contains(base, "muckspunge"):
		return "jungle"
	case strings.Contains(base, "dead"):
		return "deadwood"
	case strings.Contains(base, "pine"), strings.Contains(base, "cedar"), strings.Contains(base, "cypress"), strings.Contains(base, "redwood"):
		return "conifer"
	case strings.Contains(base, "bush"), strings.Contains(base, "savanna"):
		return "scrub"
	default:
		return "broadleaf"
	}
}

func parseHoldingLocators(data []byte) []mapHoldingLocator {
	var out []mapHoldingLocator
	for _, match := range mapLocatorPattern.FindAllSubmatch(data, -1) {
		if len(match) != 12 {
			continue
		}
		pid, err := strconv.Atoi(string(match[1]))
		if err != nil {
			continue
		}
		values := make([]float64, 10)
		valid := true
		for i := range values {
			value, parseErr := strconv.ParseFloat(string(match[i+2]), 64)
			if parseErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				valid = false
				break
			}
			values[i] = value
		}
		if !valid {
			continue
		}
		scale := math.Max(0.2, math.Min(8, (math.Abs(values[7])+math.Abs(values[9]))/2))
		out = append(out, mapHoldingLocator{
			ProvinceID: pid, X: values[0], Z: values[2], Rotation: 2 * math.Atan2(values[4], values[6]), Scale: scale,
		})
	}
	return out
}

func mapObjectsUseFlippedZ(img image.Image, definitions map[uint32]int, provinces map[int]*mapProvinceBuild, locators []mapHoldingLocator, height int) bool {
	directMatches, flippedMatches := 0, 0
	directDistance, flippedDistance := 0.0, 0.0
	count := 0
	for _, locator := range locators {
		province := provinces[locator.ProvinceID]
		if province == nil || province.Area <= 0 {
			continue
		}
		if mapObjectProvinceAt(img, definitions, locator.X, locator.Z) == locator.ProvinceID {
			directMatches++
		}
		flippedY := float64(height-1) - locator.Z
		if mapObjectProvinceAt(img, definitions, locator.X, flippedY) == locator.ProvinceID {
			flippedMatches++
		}
		centerY := float64(province.SumY) / float64(province.Area)
		directDistance += math.Min(float64(height), math.Abs(locator.Z-centerY))
		flippedDistance += math.Min(float64(height), math.Abs(flippedY-centerY))
		count++
	}
	if directMatches != flippedMatches {
		return flippedMatches > directMatches
	}
	return count > 0 && flippedDistance < directDistance
}

func mapObjectProvinceAt(img image.Image, definitions map[uint32]int, x, y float64) int {
	bounds := img.Bounds()
	px, py := int(math.Round(x)), int(math.Round(y))
	if px < 0 || py < 0 || px >= bounds.Dx() || py >= bounds.Dy() {
		return 0
	}
	r16, g16, b16, _ := img.At(bounds.Min.X+px, bounds.Min.Y+py).RGBA()
	key := uint32(uint8(r16>>8))<<16 | uint32(uint8(g16>>8))<<8 | uint32(uint8(b16>>8))
	return definitions[key]
}

func mapObjectName(data []byte, rel string) string {
	if match := mapObjectNamePattern.FindSubmatch(data); len(match) == 2 {
		return string(match[1])
	}
	return strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
}

func stableMapObjectScore(rel, subtype string, x, y float64) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%.3f\x00%.3f", rel, subtype, x, y)
	return h.Sum64()
}
