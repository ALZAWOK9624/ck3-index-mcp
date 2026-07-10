package indexer

import (
	"context"
	"database/sql"
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

type mapProvinceBuild struct {
	ID              int
	Area            int
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
}

type mapTitleBuild struct {
	ID           string
	Type         string
	Parent       string
	CapitalTitle string
	ProvinceID   int
	Children     []string
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
		"map_provinces", "map_adjacencies", "map_titles", "map_province_history",
		"map_title_provinces", "map_title_history", "map_characters", "map_character_history",
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
	defFile := active["map_data/definition.csv"]
	pngFile := active["map_data/provinces.png"]
	defaultFile := active["map_data/default.map"]
	if defFile.Path == "" || pngFile.Path == "" {
		// Not every indexed workspace is a full map project. Keep scan usable.
		return nil
	}

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
	terrains, terrainDefaults, err := parseProvinceTerrains(active)
	if err != nil {
		return err
	}
	provinces, adj, err := scanProvinceImage(pngFile.Path, definitions, blocked)
	if err != nil {
		return fmt.Errorf("provinces.png: %w", err)
	}
	applyProvinceTerrain(provinces, terrains, terrainDefaults)

	titles, provinceTitles, titleProvinces, countyCapitals, err := parseActiveLandedTitles(active)
	if err != nil {
		return err
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

	if err := insertMapStatic(ctx, tx, provinces, adj, titles, titleProvinces); err != nil {
		return err
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
	return nil
}

func collectActiveMapFiles(cfg Config) (map[string]activeMapFile, error) {
	out := map[string]activeMapFile{}
	for _, src := range cfg.Sources {
		if src.Path == "" {
			continue
		}
		err := filepath.WalkDir(src.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(src.Path, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			lower := strings.ToLower(rel)
			if !isMapContextRel(lower) {
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

func isMapContextRel(rel string) bool {
	return rel == "map_data/definition.csv" ||
		rel == "map_data/provinces.png" ||
		rel == "map_data/default.map" ||
		rel == "map_data/geographical_regions.txt" ||
		rel == "map_data/geographical_region.txt" ||
		rel == "map_data/island_region.txt" ||
		(strings.HasPrefix(rel, "common/province_terrain/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/geographical_region/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/geographical_regions/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/landed_titles/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/religion/holy_sites/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "common/religion/religions/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "history/provinces/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "history/titles/") && strings.HasSuffix(rel, ".txt")) ||
		(strings.HasPrefix(rel, "history/characters/") && strings.HasSuffix(rel, ".txt"))
}

func activeFilesWithPrefix(active map[string]activeMapFile, prefix string) []activeMapFile {
	var files []activeMapFile
	for rel, f := range active {
		if strings.HasPrefix(rel, prefix) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
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

func scanProvinceImage(path string, defs map[uint32]int, blocked map[int]mapBlockKind) (map[int]*mapProvinceBuild, map[[2]int]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, nil, err
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
				p = &mapProvinceBuild{ID: id, MinX: x, MinY: y, MaxX: x, MaxY: y}
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
			if x+1 < w {
				add(id, labels[y*w+x+1])
			}
			if y+1 < h {
				add(id, labels[(y+1)*w+x])
			}
		}
	}
	return provinces, adj, nil
}

func parseActiveLandedTitles(active map[string]activeMapFile) (map[string]*mapTitleBuild, map[int]map[string]string, map[string]map[int]bool, map[int]bool, error) {
	titles := map[string]*mapTitleBuild{}
	for _, f := range activeFilesWithPrefix(active, "common/landed_titles/") {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		file := script.Parse(string(data))
		for _, n := range file.Nodes {
			parseTitleNode(n, "", titles)
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
	for _, t := range titles {
		if t.Type != "b" || t.ProvinceID <= 0 {
			continue
		}
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
	for _, t := range titles {
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
	return titles, provinceChains, titleProvinces, countyCapitals, nil
}

func parseTitleNode(n *script.Node, parent string, titles map[string]*mapTitleBuild) {
	if n.Kind != "block" || !isTitleID(n.Key) {
		return
	}
	t := &mapTitleBuild{ID: n.Key, Type: n.Key[:1], Parent: parent}
	for _, c := range n.Children {
		switch {
		case c.Kind == "block" && isTitleID(c.Key):
			t.Children = append(t.Children, c.Key)
			parseTitleNode(c, n.Key, titles)
		case c.Key == "province":
			t.ProvinceID, _ = strconv.Atoi(c.Value)
		case c.Key == "capital":
			t.CapitalTitle = c.Value
		}
	}
	titles[t.ID] = t
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

func insertMapStatic(ctx context.Context, tx *sql.Tx, provinces map[int]*mapProvinceBuild, adj map[[2]int]int, titles map[string]*mapTitleBuild, titleProvinces map[string]map[int]bool) error {
	provStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_provinces(province_id,center_x,center_y,min_x,min_y,max_x,max_y,area,blocked,block_kind,water_kind,terrain,barony,county,duchy,kingdom,empire,is_county_capital) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer provStmt.Close()
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
		if _, err := provStmt.ExecContext(ctx, p.ID, cx, cy, p.MinX, p.MinY, p.MaxX, p.MaxY, p.Area, blocked, p.BlockKind, p.WaterKind, p.Terrain, p.Barony, p.County, p.Duchy, p.Kingdom, p.Empire, capital); err != nil {
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
	titleStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_titles(title_id,title_type,parent_id,capital_title,province_id,province_count,center_x,center_y,min_x,min_y,max_x,max_y) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
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
		if _, err := titleStmt.ExecContext(ctx, t.ID, t.Type, t.Parent, t.CapitalTitle, nullInt(t.ProvinceID), count, cx, cy, minX, minY, maxX, maxY); err != nil {
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
			for _, e := range extractTimelineFields(n, []string{"culture", "religion", "holding", "buildings", "special_building", "special_building_slot", "add_special_building", "add_special_building_slot", "duchy_building"}) {
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
			for _, e := range extractTimelineFields(n, []string{"holder", "liege"}) {
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
		if rel == "map_data/geographical_regions.txt" ||
			rel == "map_data/geographical_region.txt" ||
			rel == "map_data/island_region.txt" ||
			strings.HasPrefix(rel, "common/geographical_region/") ||
			strings.HasPrefix(rel, "common/geographical_regions/") {
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
