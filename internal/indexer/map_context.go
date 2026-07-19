package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

type activeMapFile struct {
	Path string
	Rel  string
	Src  Source
}

// mapInputFingerprintVersion is deliberately separate from indexRuleVersion:
// it covers only the direct inputs and cache semantics of rebuildMapCache.
// Bump it when that pipeline starts consuming a new input or changes output
// semantics that cannot be inferred from the input bytes alone.
const mapInputFingerprintVersion = "map_input_v1"

type mapProvinceBuild struct {
	ID              int
	ColorRGB        uint32
	Area            int
	Perimeter       int
	SumX, SumY      int64
	MinX, MinY      int
	MaxX, MaxY      int
	BlockKind       string
	WaterKind       string
	Terrain         string
	Barony, County  string
	Duchy, Kingdom  string
	Empire          string
	IsCountyCapital bool
	FillRLE         []byte
	BoundaryRLE     []byte
}

type mapTitleBuild struct {
	ID           string
	Type         string
	ColorRGB     uint32
	Parent       string
	CapitalTitle string
	ProvinceID   int
	Children     []string
	Source       string
	SourceRank   int
	Path         string
	Rel          string
	Line         int
}

type MapIntegrityIssue struct {
	Code       string `json:"code"`
	TitleID    string `json:"title_id,omitempty"`
	ProvinceID int    `json:"province_id,omitempty"`
	Message    string `json:"message"`
	Source     string `json:"source,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
}

type mapTitleAgg struct {
	Count      int
	SumX, SumY float64
	MinX, MinY int
	MaxX, MaxY int
}

type histEntry struct {
	Date  int
	Field string
	Value string
}

type mapBlockKind struct {
	BlockKind string
	WaterKind string
}

func rebuildMapCache(ctx context.Context, tx *sql.Tx, cfg Config) error {
	for _, table := range []string{
		"map_titles", "map_title_adjacencies", "map_province_history",
		"map_title_provinces", "map_integrity_issues", "map_title_history", "map_characters", "map_character_history",
		"map_holy_sites", "map_holy_site_faiths", "map_province_regions",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}

	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return err
	}
	inputFingerprint, _, err := mapInputFingerprintForActive(cfg, active)
	if err != nil {
		return err
	}
	if err := rebuildMapPhysicalCache(ctx, tx, active); err != nil {
		return err
	}
	defFile := active["map_data/definition.csv"]
	pngFile := active["map_data/provinces.png"]
	defaultFile := active["map_data/default.map"]
	if defFile.Path == "" || pngFile.Path == "" {
		// Not every indexed workspace is a full map project. Keep scan usable.
		for _, table := range []string{
			"map_provinces", "map_province_geometry", "map_adjacencies", "map_object_instances",
			"map_strategic_adjacencies", "map_water_body_shores", "map_water_body_provinces", "map_water_bodies",
			"map_surface_rasters", "map_province_materials", "map_surface_materials",
			"map_major_river_edges", "map_physical_water_body_provinces", "map_physical_water_bodies", "map_province_physical",
		} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key LIKE 'map_surface_material_%'`); err != nil {
			return err
		}
		return storeMapInputFingerprint(ctx, tx, inputFingerprint)
	}

	fingerprint, err := mapGeometryFingerprint(defFile.Path, pngFile.Path, defaultFile.Path)
	if err != nil {
		return err
	}
	var cachedFingerprint string
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_geometry_fingerprint'`).Scan(&cachedFingerprint)
	var geometryRows int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_province_geometry`).Scan(&geometryRows)
	geometryChanged := cachedFingerprint != fingerprint || geometryRows == 0

	var provinces map[int]*mapProvinceBuild
	var adj map[[2]int]int
	mapWidth, mapHeight := 0, 0
	if geometryChanged {
		definitions, err := parseProvinceDefinitions(defFile.Path)
		if err != nil {
			return fmt.Errorf("map definition: %w", err)
		}
		blocked := map[int]mapBlockKind{}
		if defaultFile.Path != "" {
			blocked, err = parseDefaultMapBlocked(defaultFile.Path)
			if err != nil {
				return fmt.Errorf("default.map: %w", err)
			}
		}
		provinces, adj, mapWidth, mapHeight, err = scanProvinceImage(pngFile.Path, definitions, blocked)
		if err != nil {
			return fmt.Errorf("provinces.png: %w", err)
		}
		for _, table := range []string{"map_provinces", "map_province_geometry", "map_adjacencies"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return err
			}
		}
	} else {
		provinces, adj, err = loadCachedMapGeometry(ctx, tx)
		if err != nil {
			return err
		}
		_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_width'`).Scan(&mapWidth)
		_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_height'`).Scan(&mapHeight)
	}
	terrains, terrainDefaults, err := parseProvinceTerrains(active)
	if err != nil {
		return err
	}
	applyProvinceTerrain(provinces, terrains, terrainDefaults)

	titles, provinceTitles, titleProvinces, countyCapitals, integrityIssues, err := parseActiveLandedTitles(active)
	if err != nil {
		return err
	}
	for _, issue := range integrityIssues {
		if _, err := tx.ExecContext(ctx, `INSERT INTO map_integrity_issues(code,title_id,province_id,message,source_name,path,line) VALUES(?,?,?,?,?,?,?)`,
			issue.Code, issue.TitleID, issue.ProvinceID, issue.Message, issue.Source, issue.Path, issue.Line); err != nil {
			return err
		}
	}
	for pid, chain := range provinceTitles {
		p := provinces[pid]
		if p == nil {
			p = &mapProvinceBuild{ID: pid, MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1}
			provinces[pid] = p
		}
		p.Barony = chain["b"]
		p.County = chain["c"]
		p.Duchy = chain["d"]
		p.Kingdom = chain["k"]
		p.Empire = chain["e"]
		if p.Empire == "" {
			p.Empire = chain["h"]
		}
	}
	for pid := range countyCapitals {
		p := provinces[pid]
		if p == nil {
			p = &mapProvinceBuild{ID: pid, MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1}
			provinces[pid] = p
		}
		p.IsCountyCapital = true
	}

	if err := insertMapStatic(ctx, tx, provinces, adj, titles, titleProvinces, geometryChanged); err != nil {
		return err
	}
	if err := rebuildMapStrategicCache(ctx, tx, active, provinces, mapWidth, mapHeight, fingerprint); err != nil {
		return err
	}
	if err := rebuildMapObjectCache(ctx, tx, active, provinces, mapWidth, mapHeight); err != nil {
		return err
	}
	if err := rebuildMapWaterBodies(ctx, tx, active, provinces, adj, mapHeight, fingerprint); err != nil {
		return err
	}
	if err := rebuildMapGISCache(ctx, tx, cfg, active, provinces, adj, fingerprint); err != nil {
		return err
	}
	if err := rebuildMapSurfaceMaterialCache(ctx, tx, active, fingerprint); err != nil {
		return err
	}
	if err := insertMapTitleAdjacencies(ctx, tx, provinces, adj, titles, titleProvinces); err != nil {
		return err
	}
	for key, value := range map[string]string{
		"map_geometry_fingerprint": fingerprint,
		"map_geometry_format":      "i32le_y_x0_x1_v1",
		"map_width":                strconv.Itoa(mapWidth),
		"map_height":               strconv.Itoa(mapHeight),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	if geometryChanged {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_geometry_build_count','1')
			ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`); err != nil {
			return err
		}
	}
	if err := insertHolySites(ctx, tx, active, titles, titleProvinces); err != nil {
		return err
	}
	if err := insertMapRegions(ctx, tx, active, titleProvinces); err != nil {
		return err
	}
	if err := insertProvinceHistory(ctx, tx, active); err != nil {
		return err
	}
	if err := insertTitleHistory(ctx, tx, active); err != nil {
		return err
	}
	if err := insertCharacterHistory(ctx, tx, active); err != nil {
		return err
	}
	if err := refreshMapTitleHolders(ctx, tx); err != nil {
		return err
	}
	return storeMapInputFingerprint(ctx, tx, inputFingerprint)
}

func mapGeometryFingerprint(paths ...string) (string, error) {
	h := sha256.New()
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := io.WriteString(h, filepath.Base(path)+"\x00"); err != nil {
			return "", err
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func loadCachedMapGeometry(ctx context.Context, tx *sql.Tx) (map[int]*mapProvinceBuild, map[[2]int]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT province_id,COALESCE(color_rgb,0),center_x,center_y,min_x,min_y,max_x,max_y,area,perimeter,blocked,COALESCE(block_kind,''),COALESCE(water_kind,'') FROM map_provinces`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	provinces := map[int]*mapProvinceBuild{}
	for rows.Next() {
		p := &mapProvinceBuild{}
		var cx, cy float64
		var blocked int
		if err := rows.Scan(&p.ID, &p.ColorRGB, &cx, &cy, &p.MinX, &p.MinY, &p.MaxX, &p.MaxY, &p.Area, &p.Perimeter, &blocked, &p.BlockKind, &p.WaterKind); err != nil {
			return nil, nil, err
		}
		p.SumX = int64(math.Round(cx * float64(p.Area)))
		p.SumY = int64(math.Round(cy * float64(p.Area)))
		provinces[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	adj := map[[2]int]int{}
	adjRows, err := tx.QueryContext(ctx, `SELECT province_id,neighbor_id,border_len FROM map_adjacencies WHERE province_id<neighbor_id`)
	if err != nil {
		return nil, nil, err
	}
	defer adjRows.Close()
	for adjRows.Next() {
		var a, b, border int
		if err := adjRows.Scan(&a, &b, &border); err != nil {
			return nil, nil, err
		}
		adj[[2]int{a, b}] = border
	}
	return provinces, adj, adjRows.Err()
}

func collectActiveMapFiles(cfg Config) (map[string]activeMapFile, error) {
	out := map[string]activeMapFile{}
	replacePaths, err := collectSourceReplacePaths(cfg.Sources)
	if err != nil {
		return nil, err
	}
	for _, src := range cfg.Sources {
		if src.Path == "" {
			continue
		}
		err := filepath.WalkDir(src.Path, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			lower := strings.ToLower(rel)
			if !isMapContextRel(lower) || relReplacedByHigherSource(lower, src.Rank, replacePaths) {
				return nil
			}
			prev, ok := out[lower]
			if !ok || src.Rank < prev.Src.Rank {
				out[lower] = activeMapFile{Path: path, Rel: lower, Src: src}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mapInputFingerprint returns a content-addressed description of every active
// source file that rebuildMapCache can read. Ordinary script scan jobs do not
// include CSV and .map files, so this deliberately walks the active map input
// set instead of relying on the semantic file-index change list.
//
// A configured, trusted GIS sidecar is treated as an independently mutable
// runtime dependency. Its availability/version is established by the map
// rebuild itself, so map-cache reuse stays conservative in that configuration.
func mapInputFingerprint(cfg Config) (string, bool, map[string]activeMapFile, error) {
	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		return "", false, nil, err
	}
	fingerprint, reusable, err := mapInputFingerprintForActive(cfg, active)
	if err != nil {
		return "", false, nil, err
	}
	return fingerprint, reusable, active, nil
}

func mapInputFingerprintForActive(cfg Config, active map[string]activeMapFile) (string, bool, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, mapInputFingerprintVersion+"\x00")
	_, _ = io.WriteString(h, fmt.Sprintf("gis=%t\x00analysis=%s\x00sidecar=%s\x00cache_root=%s\x00cache_limit=%d\x00timeout=%d\x00",
		cfg.GISEnabled,
		strings.TrimSpace(cfg.GISAnalysis),
		strings.TrimSpace(cfg.GISSidecarSHA256),
		filepath.Clean(cfg.GISCacheRoot),
		cfg.GISCacheMaxGiB,
		cfg.GISTimeoutSeconds,
	))

	keys := make([]string, 0, len(active))
	for rel := range active {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		file := active[rel]
		_, _ = io.WriteString(h, fmt.Sprintf("file\x00%s\x00%s\x00%d\x00%s\x00",
			rel,
			file.Src.Name,
			file.Src.Rank,
			filepath.Clean(file.Path),
		))
		f, err := os.Open(file.Path)
		if err != nil {
			return "", false, fmt.Errorf("open map input %q: %w", rel, err)
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", false, fmt.Errorf("hash map input %q: %w", rel, copyErr)
		}
		if closeErr != nil {
			return "", false, fmt.Errorf("close map input %q: %w", rel, closeErr)
		}
		_, _ = io.WriteString(h, "\x00")
	}

	// InspectGISSidecar hashes and executes the external binary. If a trusted
	// sidecar is configured, preserve the old conservative behavior: rebuild
	// map-derived GIS data every full scan so runtime availability changes are
	// never silently retained.
	reusable := !cfg.GISEnabled || strings.TrimSpace(cfg.GISSidecarSHA256) == ""
	return fmt.Sprintf("%x", h.Sum(nil)), reusable, nil
}

func mapCacheMatchesInput(ctx context.Context, tx *sql.Tx, fingerprint string, reusable bool, active map[string]activeMapFile) (bool, error) {
	if !reusable {
		return false, nil
	}
	var cached string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_input_fingerprint'`).Scan(&cached)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cached != fingerprint {
		return false, nil
	}
	// A complete map geometry cache must contain at least one geometry row when
	// the active inputs contain both defining assets. This prevents a manually
	// cleared cache from being accepted solely because its old meta value remains.
	if active["map_data/definition.csv"].Path == "" || active["map_data/provinces.png"].Path == "" {
		return true, nil
	}
	var geometryRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_province_geometry`).Scan(&geometryRows); err != nil {
		return false, err
	}
	return geometryRows > 0, nil
}

func storeMapInputFingerprint(ctx context.Context, tx *sql.Tx, fingerprint string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_input_fingerprint',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fingerprint)
	return err
}

func isMapContextRel(rel string) bool {
	return rel == "map_data/definition.csv" ||
		rel == "map_data/provinces.png" ||
		rel == "map_data/adjacencies.csv" ||
		rel == "map_data/heightmap.png" ||
		rel == "map_data/rivers.png" ||
		rel == "map_data/default.map" ||
		isGeographicalRegionDefinitionsPath(rel) ||
		(strings.HasPrefix(rel, "common/province_terrain/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/landed_titles/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/religion/holy_sites/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/religion/religions/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "gfx/map/map_object_data/") && strings.HasSuffix(rel, ".txt")) ||
		rel == "gfx/map/terrain/materials.settings" ||
		rel == "gfx/map/terrain/detail_index.tga" ||
		rel == "gfx/map/terrain/detail_intensity.tga" ||
		(strings.HasPrefix(rel, "history/provinces/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "history/titles/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "history/characters/") && strings.HasSuffix(rel, ".txt"))
}

// isGeographicalRegionDefinitionsPath is shared by the map aggregation and
// semantic object index. Keeping one path predicate prevents a region file
// from being visible to map queries while remaining invisible to search,
// inspect, and dependency queries.
func isGeographicalRegionDefinitionsPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(strings.TrimSpace(rel)))
	return rel == "map_data/geographical_regions.txt" ||
		rel == "map_data/geographical_region.txt" ||
		(strings.HasPrefix(rel, "map_data/geographical_regions/") && strings.HasSuffix(rel, ".txt")) ||
		rel == "map_data/island_region.txt" ||
		(strings.HasPrefix(rel, "common/geographical_region/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/geographical_regions/") && strings.HasSuffix(rel, ".txt"))
}

func activeFilesWithPrefix(active map[string]activeMapFile, prefix string) []activeMapFile {
	var files []activeMapFile
	for rel, f := range active {
		if strings.HasPrefix(rel, prefix) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Src.Rank != files[j].Src.Rank {
			// Parse low-priority sources first so INSERT OR REPLACE preserves the
			// same winner as CK3's mod load order for duplicate history keys.
			return files[i].Src.Rank > files[j].Src.Rank
		}
		return files[i].Rel < files[j].Rel
	})
	return files
}

func parseProvinceDefinitions(path string) (map[uint32]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	r.FieldsPerRecord = -1
	out := map[uint32]int{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 4 {
			continue
		}
		id, err1 := strconv.Atoi(strings.TrimSpace(rec[0]))
		rr, err2 := strconv.Atoi(strings.TrimSpace(rec[1]))
		gg, err3 := strconv.Atoi(strings.TrimSpace(rec[2]))
		bb, err4 := strconv.Atoi(strings.TrimSpace(rec[3]))
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		out[uint32(rr)<<16|uint32(gg)<<8|uint32(bb)] = id
	}
	return out, nil
}

func parseDefaultMapBlocked(path string) (map[int]mapBlockKind, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	waterKeys := map[string]string{
		"sea_zones": "sea", "sea_zone": "sea",
		"coastal_seas": "coastal_sea", "coastal_sea": "coastal_sea",
		"lakes": "lake", "lake": "lake",
		"river_provinces": "river", "river_province": "river",
		"impassable_seas": "impassable_sea", "impassable_sea": "impassable_sea",
	}
	mountainKeys := map[string]bool{"impassable_mountains": true, "impassable_mountain": true}
	out := map[int]mapBlockKind{}
	matches := regexp.MustCompile(`(?is)\b(\w+)\s*=\s*(?:LIST\s*)?\{([^}]*)\}`).FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		key := strings.ToLower(m[1])
		block := mapBlockKind{}
		if water := waterKeys[key]; water != "" {
			block = mapBlockKind{BlockKind: "water", WaterKind: water}
		} else if mountainKeys[key] {
			block = mapBlockKind{BlockKind: "impassable_mountain"}
		}
		if block.BlockKind == "" {
			continue
		}
		for _, token := range strings.Fields(m[2]) {
			if id, err := strconv.Atoi(token); err == nil {
				out[id] = block
			}
		}
	}
	return out, nil
}

type terrainDefaults struct {
	Land       string
	Sea        string
	CoastalSea string
}

func parseProvinceTerrains(active map[string]activeMapFile) (map[int]string, terrainDefaults, error) {
	out := map[int]string{}
	defaults := terrainDefaults{Land: "plains", Sea: "sea", CoastalSea: "coastal_sea"}
	for _, f := range activeFilesWithPrefix(active, "common/province_terrain/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, defaults, err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			if n.Kind != "atom" || n.Value == "" {
				continue
			}
			switch n.Key {
			case "default_land":
				defaults.Land = n.Value
			case "default_sea":
				defaults.Sea = n.Value
			case "default_coastal_sea":
				defaults.CoastalSea = n.Value
			default:
				if id, err := strconv.Atoi(n.Key); err == nil {
					out[id] = n.Value
				}
			}
		}
	}
	return out, defaults, nil
}

func applyProvinceTerrain(provinces map[int]*mapProvinceBuild, terrains map[int]string, defaults terrainDefaults) {
	for id, p := range provinces {
		if terrain := terrains[id]; terrain != "" {
			p.Terrain = terrain
			continue
		}
		if p.BlockKind == "water" {
			p.Terrain = defaults.Sea
		} else {
			p.Terrain = defaults.Land
		}
	}
}

func scanProvinceImage(path string, defs map[uint32]int, blocked map[int]mapBlockKind) (map[int]*mapProvinceBuild, map[[2]int]int, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	labels := make([]int, w*h)
	provinces := map[int]*mapProvinceBuild{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r16, g16, b16, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			key := uint32(uint8(r16>>8))<<16 | uint32(uint8(g16>>8))<<8 | uint32(uint8(b16>>8))
			id := defs[key]
			labels[y*w+x] = id
			if id <= 0 {
				continue
			}
			p := provinces[id]
			if p == nil {
				p = &mapProvinceBuild{ID: id, ColorRGB: key, MinX: x, MinY: y, MaxX: x, MaxY: y}
				if kind := blocked[id]; kind.BlockKind != "" {
					p.BlockKind = kind.BlockKind
					p.WaterKind = kind.WaterKind
				}
				provinces[id] = p
			}
			p.Area++
			p.SumX += int64(x)
			p.SumY += int64(y)
			if x < p.MinX {
				p.MinX = x
			}
			if y < p.MinY {
				p.MinY = y
			}
			if x > p.MaxX {
				p.MaxX = x
			}
			if y > p.MaxY {
				p.MaxY = y
			}
		}
	}
	adj := map[[2]int]int{}
	add := func(a, c int) {
		if a <= 0 || c <= 0 || a == c {
			return
		}
		if a > c {
			a, c = c, a
		}
		adj[[2]int{a, c}]++
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			id := labels[y*w+x]
			if id <= 0 {
				continue
			}
			if x == 0 {
				provinces[id].Perimeter++
			}
			if y == 0 {
				provinces[id].Perimeter++
			}
			if x+1 < w {
				other := labels[y*w+x+1]
				add(id, other)
				if other != id {
					provinces[id].Perimeter++
					if other > 0 {
						provinces[other].Perimeter++
					}
				}
			} else {
				provinces[id].Perimeter++
			}
			if y+1 < h {
				other := labels[(y+1)*w+x]
				add(id, other)
				if other != id {
					provinces[id].Perimeter++
					if other > 0 {
						provinces[other].Perimeter++
					}
				}
			} else {
				provinces[id].Perimeter++
			}
		}
	}
	fills := encodeProvinceRuns(labels, w, h, false)
	boundaries := encodeProvinceRuns(labels, w, h, true)
	for id, p := range provinces {
		p.FillRLE = fills[id]
		p.BoundaryRLE = boundaries[id]
	}
	return provinces, adj, w, h, nil
}

// MapRun is one inclusive horizontal span in source-map pixel coordinates.
type MapRun struct {
	Y  int32 `json:"y"`
	X0 int32 `json:"x0"`
	X1 int32 `json:"x1"`
}

func appendMapRun(buffers map[int]*bytes.Buffer, id, y, x0, x1 int) {
	if id <= 0 || x1 < x0 {
		return
	}
	b := buffers[id]
	if b == nil {
		b = &bytes.Buffer{}
		buffers[id] = b
	}
	for _, value := range []int32{int32(y), int32(x0), int32(x1)} {
		_ = binary.Write(b, binary.LittleEndian, value)
	}
}

func encodeProvinceRuns(labels []int, width, height int, boundaryOnly bool) map[int][]byte {
	buffers := map[int]*bytes.Buffer{}
	isBoundary := func(x, y, id int) bool {
		if x == 0 || y == 0 || x+1 == width || y+1 == height {
			return true
		}
		return labels[y*width+x-1] != id || labels[y*width+x+1] != id ||
			labels[(y-1)*width+x] != id || labels[(y+1)*width+x] != id
	}
	for y := 0; y < height; y++ {
		runID, runStart := 0, -1
		flush := func(x int) {
			if runID > 0 {
				appendMapRun(buffers, runID, y, runStart, x-1)
			}
			runID, runStart = 0, -1
		}
		for x := 0; x < width; x++ {
			id := labels[y*width+x]
			include := id > 0 && (!boundaryOnly || isBoundary(x, y, id))
			if !include {
				flush(x)
				continue
			}
			if runID != id {
				flush(x)
				runID, runStart = id, x
			}
		}
		flush(width)
	}
	out := map[int][]byte{}
	for id, buffer := range buffers {
		out[id] = buffer.Bytes()
	}
	return out
}

// DecodeMapRuns decodes map_province_geometry RLE blobs.
func DecodeMapRuns(data []byte) ([]MapRun, error) {
	if len(data)%12 != 0 {
		return nil, fmt.Errorf("invalid map RLE length %d", len(data))
	}
	runs := make([]MapRun, 0, len(data)/12)
	r := bytes.NewReader(data)
	for r.Len() > 0 {
		var run MapRun
		if err := binary.Read(r, binary.LittleEndian, &run.Y); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &run.X0); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &run.X1); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func parseActiveLandedTitles(active map[string]activeMapFile) (map[string]*mapTitleBuild, map[int]map[string]string, map[string]map[int]bool, map[int]bool, []MapIntegrityIssue, error) {
	titles := map[string]*mapTitleBuild{}
	var issues []MapIntegrityIssue
	for _, f := range activeFilesWithPrefix(active, "common/landed_titles/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			parseTitleNode(n, "", f, titles, &issues)
		}
	}
	provinceChains := map[int]map[string]string{}
	titleProvinces := map[string]map[int]bool{}
	addTitleProvince := func(title string, provinceID int) {
		if title == "" || provinceID <= 0 {
			return
		}
		set := titleProvinces[title]
		if set == nil {
			set = map[int]bool{}
			titleProvinces[title] = set
		}
		set[provinceID] = true
	}
	orderedTitles := make([]*mapTitleBuild, 0, len(titles))
	for _, t := range titles {
		orderedTitles = append(orderedTitles, t)
	}
	sort.SliceStable(orderedTitles, func(i, j int) bool { return titleBuildPreferred(orderedTitles[i], orderedTitles[j]) })
	provinceOwner := map[int]*mapTitleBuild{}
	for _, t := range orderedTitles {
		if t.Type != "b" || t.ProvinceID <= 0 {
			continue
		}
		if owner := provinceOwner[t.ProvinceID]; owner != nil && owner.ID != t.ID {
			message := fmt.Sprintf("province %d is assigned to both %s and %s; map cache selected %s", t.ProvinceID, owner.ID, t.ID, owner.ID)
			issues = append(issues,
				MapIntegrityIssue{Code: "duplicate_barony_province", TitleID: owner.ID, ProvinceID: t.ProvinceID, Message: message, Source: owner.Source, Path: owner.Rel, Line: owner.Line},
				MapIntegrityIssue{Code: "duplicate_barony_province", TitleID: t.ID, ProvinceID: t.ProvinceID, Message: message, Source: t.Source, Path: t.Rel, Line: t.Line},
			)
			continue
		}
		provinceOwner[t.ProvinceID] = t
		chain := map[string]string{"b": t.ID}
		addTitleProvince(t.ID, t.ProvinceID)
		for cur := t; cur != nil && cur.Parent != ""; {
			parent := titles[cur.Parent]
			if parent == nil {
				break
			}
			chain[parent.Type] = parent.ID
			addTitleProvince(parent.ID, t.ProvinceID)
			cur = parent
		}
		provinceChains[t.ProvinceID] = chain
	}
	countyCapitals := map[int]bool{}
	for _, t := range orderedTitles {
		if t.Type != "c" {
			continue
		}
		capital := t.CapitalTitle
		if capital == "" {
			for _, child := range t.Children {
				if childTitle := titles[child]; childTitle != nil && childTitle.Type == "b" {
					capital = child
					break
				}
			}
		}
		if capitalTitle := titles[capital]; capitalTitle != nil && capitalTitle.ProvinceID > 0 {
			countyCapitals[capitalTitle.ProvinceID] = true
		}
	}
	return titles, provinceChains, titleProvinces, countyCapitals, issues, nil
}

func titleBuildPreferred(a, b *mapTitleBuild) bool {
	if a.SourceRank != b.SourceRank {
		return a.SourceRank < b.SourceRank
	}
	if a.Rel != b.Rel {
		return a.Rel < b.Rel
	}
	return a.Line < b.Line
}

func parseTitleNode(n *script.Node, parent string, file activeMapFile, titles map[string]*mapTitleBuild, issues *[]MapIntegrityIssue) {
	if n.Kind != "block" || !isTitleID(n.Key) {
		return
	}
	t := &mapTitleBuild{ID: n.Key, Type: n.Key[:1], Parent: parent, Source: file.Src.Name, SourceRank: file.Src.Rank, Path: file.Path, Rel: file.Rel, Line: n.Line}
	for _, c := range n.Children {
		switch {
		case c.Kind == "block" && isTitleID(c.Key):
			t.Children = append(t.Children, c.Key)
			parseTitleNode(c, n.Key, file, titles, issues)
		case c.Key == "province":
			t.ProvinceID, _ = strconv.Atoi(c.Value)
		case c.Key == "capital":
			t.CapitalTitle = c.Value
		case c.Key == "color" && c.Kind == "block":
			t.ColorRGB = parseTitleColor(c)
		}
	}
	if previous := titles[t.ID]; previous != nil {
		winner, loser := previous, t
		if titleBuildPreferred(t, previous) {
			winner, loser = t, previous
			titles[t.ID] = t
		}
		message := fmt.Sprintf("title %s has multiple active definitions; map cache selected %s:%d over %s:%d", t.ID, winner.Rel, winner.Line, loser.Rel, loser.Line)
		*issues = append(*issues,
			MapIntegrityIssue{Code: "duplicate_title_id", TitleID: winner.ID, ProvinceID: winner.ProvinceID, Message: message, Source: winner.Source, Path: winner.Rel, Line: winner.Line},
			MapIntegrityIssue{Code: "duplicate_title_id", TitleID: loser.ID, ProvinceID: loser.ProvinceID, Message: message, Source: loser.Source, Path: loser.Rel, Line: loser.Line},
		)
		return
	}
	titles[t.ID] = t
}

func parseTitleColor(n *script.Node) uint32 {
	values := make([]int, 0, 3)
	for _, child := range n.Children {
		value := child.Value
		if value == "" {
			value = child.Key
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 255 {
			continue
		}
		values = append(values, parsed)
		if len(values) == 3 {
			return uint32(values[0])<<16 | uint32(values[1])<<8 | uint32(values[2])
		}
	}
	return 0
}

func isTitleID(s string) bool {
	if len(s) < 3 || s[1] != '_' {
		return false
	}
	switch s[0] {
	case 'e', 'h', 'k', 'd', 'c', 'b':
		return true
	default:
		return false
	}
}

func insertMapStatic(ctx context.Context, tx *sql.Tx, provinces map[int]*mapProvinceBuild, adj map[[2]int]int, titles map[string]*mapTitleBuild, titleProvinces map[string]map[int]bool, geometryChanged bool) error {
	provinceSQL := `UPDATE map_provinces SET terrain=?,barony=?,county=?,duchy=?,kingdom=?,empire=?,is_county_capital=? WHERE province_id=?`
	if geometryChanged {
		provinceSQL = `INSERT INTO map_provinces(province_id,color_rgb,center_x,center_y,min_x,min_y,max_x,max_y,area,perimeter,blocked,block_kind,water_kind,terrain,barony,county,duchy,kingdom,empire,is_county_capital) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	}
	provStmt, err := tx.PrepareContext(ctx, provinceSQL)
	if err != nil {
		return err
	}
	defer provStmt.Close()
	var geometryStmt *sql.Stmt
	if geometryChanged {
		geometryStmt, err = tx.PrepareContext(ctx, `INSERT INTO map_province_geometry(province_id,fill_rle,boundary_rle) VALUES(?,?,?)`)
		if err != nil {
			return err
		}
		defer geometryStmt.Close()
	}
	for _, p := range provinces {
		cx, cy := 0.0, 0.0
		if p.Area > 0 {
			cx = float64(p.SumX) / float64(p.Area)
			cy = float64(p.SumY) / float64(p.Area)
		}
		blocked := 0
		if p.BlockKind != "" {
			blocked = 1
		}
		capital := 0
		if p.IsCountyCapital {
			capital = 1
		}
		if geometryChanged {
			if _, err := provStmt.ExecContext(ctx, p.ID, nullColor(p.ColorRGB), cx, cy, p.MinX, p.MinY, p.MaxX, p.MaxY, p.Area, p.Perimeter, blocked, p.BlockKind, p.WaterKind, p.Terrain, p.Barony, p.County, p.Duchy, p.Kingdom, p.Empire, capital); err != nil {
				return err
			}
			if p.Area > 0 {
				if _, err := geometryStmt.ExecContext(ctx, p.ID, p.FillRLE, p.BoundaryRLE); err != nil {
					return err
				}
			}
		} else if _, err := provStmt.ExecContext(ctx, p.Terrain, p.Barony, p.County, p.Duchy, p.Kingdom, p.Empire, capital, p.ID); err != nil {
			return err
		}
	}
	titleProvStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_title_provinces(title_id,province_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer titleProvStmt.Close()
	titleAgg := map[string]*mapTitleAgg{}
	for title, pids := range titleProvinces {
		for pid := range pids {
			if _, err := titleProvStmt.ExecContext(ctx, title, pid); err != nil {
				return err
			}
			p := provinces[pid]
			if p == nil {
				continue
			}
			cx, cy := 0.0, 0.0
			if p.Area > 0 {
				cx = float64(p.SumX) / float64(p.Area)
				cy = float64(p.SumY) / float64(p.Area)
			}
			a := titleAgg[title]
			if a == nil {
				a = &mapTitleAgg{MinX: p.MinX, MinY: p.MinY, MaxX: p.MaxX, MaxY: p.MaxY}
				titleAgg[title] = a
			}
			a.Count++
			a.SumX += cx
			a.SumY += cy
			if p.MinX < a.MinX {
				a.MinX = p.MinX
			}
			if p.MinY < a.MinY {
				a.MinY = p.MinY
			}
			if p.MaxX > a.MaxX {
				a.MaxX = p.MaxX
			}
			if p.MaxY > a.MaxY {
				a.MaxY = p.MaxY
			}
		}
	}
	if geometryChanged {
		adjStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_adjacencies(province_id,neighbor_id,border_len,blocked) VALUES(?,?,?,?)`)
		if err != nil {
			return err
		}
		defer adjStmt.Close()
		for pair, n := range adj {
			a, b := pair[0], pair[1]
			blocked := 0
			if provinces[a] != nil && provinces[a].BlockKind != "" || provinces[b] != nil && provinces[b].BlockKind != "" {
				blocked = 1
			}
			if _, err := adjStmt.ExecContext(ctx, a, b, n, blocked); err != nil {
				return err
			}
			if _, err := adjStmt.ExecContext(ctx, b, a, n, blocked); err != nil {
				return err
			}
		}
	}
	titleStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_titles(title_id,title_type,color_rgb,parent_id,capital_title,province_id,province_count,center_x,center_y,min_x,min_y,max_x,max_y) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer titleStmt.Close()
	keys := make([]string, 0, len(titles))
	for k := range titles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := titles[k]
		var cx, cy any
		var minX, minY, maxX, maxY any
		count := 0
		if a := titleAgg[k]; a != nil && a.Count > 0 {
			count = a.Count
			cx = a.SumX / float64(a.Count)
			cy = a.SumY / float64(a.Count)
			minX, minY, maxX, maxY = a.MinX, a.MinY, a.MaxX, a.MaxY
		}
		if _, err := titleStmt.ExecContext(ctx, t.ID, t.Type, nullColor(t.ColorRGB), t.Parent, t.CapitalTitle, nullInt(t.ProvinceID), count, cx, cy, minX, minY, maxX, maxY); err != nil {
			return err
		}
	}
	return nil
}

func nullColor(v uint32) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

type mapTitleAdjacencyAgg struct {
	BorderLen  int
	BlockedLen int
	WaterLen   int
}

func insertMapTitleAdjacencies(ctx context.Context, tx *sql.Tx, provinces map[int]*mapProvinceBuild, adj map[[2]int]int, titles map[string]*mapTitleBuild, titleProvinces map[string]map[int]bool) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM map_title_adjacencies`); err != nil {
		return err
	}
	memberships := map[string]map[int][]string{"b": {}, "c": {}, "d": {}, "k": {}, "e": {}}
	for title, pids := range titleProvinces {
		definition := titles[title]
		if definition == nil || memberships[definition.Type] == nil {
			continue
		}
		for pid := range pids {
			memberships[definition.Type][pid] = append(memberships[definition.Type][pid], title)
		}
	}
	type key struct{ Level, A, B string }
	aggregated := map[key]*mapTitleAdjacencyAgg{}
	for pair, border := range adj {
		pa, pb := provinces[pair[0]], provinces[pair[1]]
		for _, level := range []string{"b", "c", "d", "k", "e"} {
			for _, left := range memberships[level][pair[0]] {
				for _, right := range memberships[level][pair[1]] {
					a, b := left, right
					if a == b {
						continue
					}
					if a > b {
						a, b = b, a
					}
					k := key{level, a, b}
					item := aggregated[k]
					if item == nil {
						item = &mapTitleAdjacencyAgg{}
						aggregated[k] = item
					}
					item.BorderLen += border
					if pa.BlockKind != "" || pb.BlockKind != "" {
						item.BlockedLen += border
					}
					if pa.BlockKind == "water" || pb.BlockKind == "water" {
						item.WaterLen += border
					}
				}
			}
		}
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO map_title_adjacencies(level,title_id,neighbor_id,border_len,blocked_border_len,water_border_len) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, item := range aggregated {
		if _, err := stmt.ExecContext(ctx, k.Level, k.A, k.B, item.BorderLen, item.BlockedLen, item.WaterLen); err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, k.Level, k.B, k.A, item.BorderLen, item.BlockedLen, item.WaterLen); err != nil {
			return err
		}
	}
	return nil
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func insertProvinceHistory(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_province_history(province_id,date_key,field,value) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range activeFilesWithPrefix(active, "history/provinces/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			pid, err := strconv.Atoi(n.Key)
			if err != nil || n.Kind != "block" {
				continue
			}
			for _, e := range extractTimelineFields(n, []string{"culture", "religion", "holding", "buildings", "special_building", "special_building_slot", "add_special_building", "add_special_building_slot", "duchy_building", "development", "development_level"}) {
				if _, err := stmt.ExecContext(ctx, pid, e.Date, e.Field, e.Value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func insertTitleHistory(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_title_history(title_id,date_key,field,value) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range activeFilesWithPrefix(active, "history/titles/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			if !isTitleID(n.Key) || n.Kind != "block" {
				continue
			}
			for _, e := range extractTimelineFields(n, []string{"holder", "liege", "development_level", "change_development_level"}) {
				if _, err := stmt.ExecContext(ctx, n.Key, e.Date, e.Field, e.Value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func insertCharacterHistory(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile) error {
	charStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_characters(character_id,name,culture,religion) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer charStmt.Close()
	histStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_character_history(character_id,date_key,field,value) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer histStmt.Close()
	for _, f := range activeFilesWithPrefix(active, "history/characters/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			if n.Key == "" || n.Kind != "block" {
				continue
			}
			name, culture, religion := "", "", ""
			for _, e := range extractTimelineFields(n, []string{"name", "culture", "religion"}) {
				if e.Date == 0 {
					switch e.Field {
					case "name":
						name = e.Value
					case "culture":
						culture = e.Value
					case "religion":
						religion = e.Value
					}
				}
				if _, err := histStmt.ExecContext(ctx, n.Key, e.Date, e.Field, e.Value); err != nil {
					return err
				}
			}
			if _, err := charStmt.ExecContext(ctx, n.Key, name, culture, religion); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertHolySites(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, titles map[string]*mapTitleBuild, titleProvinces map[string]map[int]bool) error {
	siteStmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO map_holy_sites(holy_site_id,county,barony,province_id) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer siteStmt.Close()
	for _, f := range activeFilesWithPrefix(active, "common/religion/holy_sites/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			if n.Kind != "block" || n.Key == "" || strings.HasPrefix(n.Key, "_") {
				continue
			}
			county, barony := "", ""
			for _, c := range n.Children {
				if c.Kind != "atom" {
					continue
				}
				switch c.Key {
				case "county":
					county = c.Value
				case "barony":
					barony = c.Value
				}
			}
			pid := holySiteProvinceID(county, barony, titles, titleProvinces)
			if _, err := siteStmt.ExecContext(ctx, n.Key, county, barony, nullInt(pid)); err != nil {
				return err
			}
		}
	}
	faithStmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO map_holy_site_faiths(holy_site_id,faith_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer faithStmt.Close()
	for _, f := range activeFilesWithPrefix(active, "common/religion/religions/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		refs := collectFaithHolySiteRefs(file.Nodes)
		for site, faiths := range refs {
			for faith := range faiths {
				if _, err := faithStmt.ExecContext(ctx, site, faith); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func holySiteProvinceID(county, barony string, titles map[string]*mapTitleBuild, titleProvinces map[string]map[int]bool) int {
	if barony != "" {
		if t := titles[barony]; t != nil && t.ProvinceID > 0 {
			return t.ProvinceID
		}
	}
	if county == "" {
		return 0
	}
	if t := titles[county]; t != nil {
		if t.CapitalTitle != "" {
			if capTitle := titles[t.CapitalTitle]; capTitle != nil && capTitle.ProvinceID > 0 {
				return capTitle.ProvinceID
			}
		}
		for _, child := range t.Children {
			if childTitle := titles[child]; childTitle != nil && childTitle.Type == "b" && childTitle.ProvinceID > 0 {
				return childTitle.ProvinceID
			}
		}
	}
	var pids []int
	for pid := range titleProvinces[county] {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}

func collectFaithHolySiteRefs(nodes []*script.Node) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	var walk func([]*script.Node)
	walk = func(nodes []*script.Node) {
		for _, n := range nodes {
			if n.Kind == "block" && n.Key == "faiths" {
				for _, faith := range n.Children {
					if faith.Kind != "block" || faith.Key == "" {
						continue
					}
					for _, c := range faith.Children {
						if c.Kind == "atom" && c.Key == "holy_site" && c.Value != "" {
							set := out[c.Value]
							if set == nil {
								set = map[string]bool{}
								out[c.Value] = set
							}
							set[faith.Key] = true
						}
					}
				}
			}
			if len(n.Children) > 0 {
				walk(n.Children)
			}
		}
	}
	walk(nodes)
	return out
}

func insertMapRegions(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, titleProvinces map[string]map[int]bool) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO map_province_regions(province_id,region_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	direct := map[string]map[int]bool{}
	children := map[string][]string{}
	for _, f := range activeRegionFiles(active) {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			if n.Kind != "block" || n.Key == "" {
				continue
			}
			collectRegionNode(n, direct, children, titleProvinces)
		}
	}
	expanded := map[string]map[int]bool{}
	var expand func(string, map[string]bool) map[int]bool
	expand = func(region string, seen map[string]bool) map[int]bool {
		if pids := expanded[region]; pids != nil {
			return pids
		}
		if seen[region] {
			return map[int]bool{}
		}
		seen[region] = true
		out := map[int]bool{}
		for pid := range direct[region] {
			out[pid] = true
		}
		for _, child := range children[region] {
			for pid := range expand(child, seen) {
				out[pid] = true
			}
		}
		expanded[region] = out
		return out
	}
	for region := range direct {
		for pid := range expand(region, map[string]bool{}) {
			if _, err := stmt.ExecContext(ctx, pid, region); err != nil {
				return err
			}
		}
	}
	for region := range children {
		for pid := range expand(region, map[string]bool{}) {
			if _, err := stmt.ExecContext(ctx, pid, region); err != nil {
				return err
			}
		}
	}
	return nil
}

func activeRegionFiles(active map[string]activeMapFile) []activeMapFile {
	var files []activeMapFile
	for rel, f := range active {
		if isGeographicalRegionDefinitionsPath(rel) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files
}

func collectRegionNode(n *script.Node, direct map[string]map[int]bool, children map[string][]string, titleProvinces map[string]map[int]bool) {
	region := n.Key
	for _, c := range n.Children {
		if c.Kind != "block" {
			continue
		}
		values := listBlockValues(c)
		switch strings.ToLower(c.Key) {
		case "province", "provinces":
			for _, v := range values {
				pid, err := strconv.Atoi(v)
				if err != nil || pid <= 0 {
					continue
				}
				addRegionProvince(direct, region, pid)
			}
		case "barony", "baronies", "county", "counties", "duchy", "duchies", "kingdom", "kingdoms", "empire", "empires":
			for _, title := range values {
				for pid := range titleProvinces[title] {
					addRegionProvince(direct, region, pid)
				}
			}
		case "region", "regions":
			for _, child := range values {
				if child != "" {
					children[region] = append(children[region], child)
				}
			}
		}
	}
}

func addRegionProvince(direct map[string]map[int]bool, region string, pid int) {
	set := direct[region]
	if set == nil {
		set = map[int]bool{}
		direct[region] = set
	}
	set[pid] = true
}

func extractTimelineFields(root *script.Node, fields []string) []histEntry {
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	var out []histEntry
	addFields := func(nodes []*script.Node, date int) {
		for _, c := range nodes {
			field := normalizeProvinceHistoryField(c.Key)
			if !want[field] {
				continue
			}
			switch {
			case c.Kind == "atom" && c.Value != "":
				out = append(out, histEntry{Date: date, Field: field, Value: c.Value})
			case c.Kind == "block":
				values := listBlockValues(c)
				if len(values) > 0 {
					out = append(out, histEntry{Date: date, Field: field, Value: strings.Join(values, " ")})
				}
			}
		}
	}
	addFields(root.Children, 0)
	for _, c := range root.Children {
		if c.Kind != "block" {
			continue
		}
		if date, ok := parseDateKey(c.Key); ok {
			addFields(c.Children, date)
		}
	}
	return out
}

func normalizeProvinceHistoryField(field string) string {
	switch field {
	case "add_special_building":
		return "special_building"
	case "add_special_building_slot":
		return "special_building_slot"
	default:
		return field
	}
}

func listBlockValues(n *script.Node) []string {
	if n == nil {
		return nil
	}
	var out []string
	for _, c := range n.Children {
		switch {
		case c.Kind == "bare" && c.Key != "":
			out = append(out, c.Key)
		case c.Kind == "atom" && c.Value != "":
			out = append(out, c.Value)
		case c.Kind == "atom" && c.Key != "":
			out = append(out, c.Key)
		}
	}
	return out
}

func parseDateKey(s string) (int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, false
	}
	y, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	d, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || y < 0 || m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, false
	}
	return y*10000 + m*100 + d, true
}

func yearDateKey(year int) int {
	if year <= 0 {
		year = 1
	}
	return year*10000 + 101
}

func refreshMapTitleHolders(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT title_id FROM map_titles`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		holder, _ := resolveTitleFieldTx(ctx, tx, id, "holder", yearDateKey(6253))
		if holder == "" {
			holder, _ = resolveEffectiveTitleHolderTx(ctx, tx, id, yearDateKey(6253))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE map_titles SET holder=? WHERE title_id=?`, holder, id); err != nil {
			return err
		}
	}
	return nil
}

func resolveTitleFieldTx(ctx context.Context, tx *sql.Tx, title, field string, date int) (string, error) {
	var v string
	err := tx.QueryRowContext(ctx, `SELECT value FROM map_title_history WHERE title_id=? AND field=? AND date_key<=? ORDER BY date_key DESC LIMIT 1`, title, field, date).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func resolveEffectiveTitleHolderTx(ctx context.Context, tx *sql.Tx, title string, date int) (string, error) {
	seen := map[string]bool{}
	for title != "" && !seen[title] {
		seen[title] = true
		holder, err := resolveTitleFieldTx(ctx, tx, title, "holder", date)
		if err != nil {
			return "", err
		}
		if isValidMapHolder(holder) {
			return holder, nil
		}
		var parent sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT parent_id FROM map_titles WHERE title_id=?`, title).Scan(&parent)
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

func isValidMapHolder(holder string) bool {
	holder = strings.TrimSpace(holder)
	return holder != "" && holder != "0" && holder != "wasteland"
}
