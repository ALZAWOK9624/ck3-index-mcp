package indexer

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	mapStrategicCacheVersion = "map_strategic_v1"
	mapWaterBodyCacheVersion = "map_water_bodies_v1"
)

type mapStrategicBuild struct {
	From, To, Through       int
	ConnectionType, Comment string
	StartX, StartY          float64
	StopX, StopY            float64
	PassageKind             string
	Distance                float64
	FromSubterranean        bool
	ToSubterranean          bool
}

type mapWaterBodyBuild struct {
	ID, ProvinceCount, Area, Shoreline int
	SumX, SumY                         float64
	MinX, MinY, MaxX, MaxY             int
	LocatorCount                       int
	Provinces                          []int
	Shores                             map[int]int
}

func rebuildMapStrategicCache(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, provinces map[int]*mapProvinceBuild, mapWidth, mapHeight int, geometryFingerprint string) error {
	file := active["map_data/adjacencies.csv"]
	if file.Path == "" {
		_, err := tx.ExecContext(ctx, `DELETE FROM map_strategic_adjacencies`)
		return err
	}
	fileFingerprint, err := mapGeometryFingerprint(file.Path)
	if err != nil {
		return err
	}
	fingerprint := mapStrategicCacheVersion + ":" + geometryFingerprint + ":" + fileFingerprint
	var cached string
	var rows int
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_strategic_fingerprint'`).Scan(&cached)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_strategic_adjacencies`).Scan(&rows)
	if cached == fingerprint && rows > 0 {
		return nil
	}

	items, err := parseStrategicAdjacencies(file.Path, provinces, mapWidth, mapHeight)
	if err != nil {
		return fmt.Errorf("adjacencies.csv: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM map_strategic_adjacencies`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO map_strategic_adjacencies(
		from_province,to_province,connection_type,through_province,start_x,start_y,stop_x,stop_y,comment,passage_kind,distance_pixels,
		from_subterranean,to_subterranean,source_name,source_rank,source_path) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	counts := map[string]int{}
	for _, item := range items {
		if _, err := stmt.ExecContext(ctx, item.From, item.To, item.ConnectionType, item.Through, item.StartX, item.StartY, item.StopX, item.StopY,
			item.Comment, item.PassageKind, item.Distance, boolInt(item.FromSubterranean), boolInt(item.ToSubterranean), file.Src.Name, file.Src.Rank, file.Rel); err != nil {
			return err
		}
		counts[item.PassageKind]++
	}
	meta := map[string]string{
		"map_strategic_fingerprint": fingerprint,
		"map_strategic_count":       strconv.Itoa(len(items)),
	}
	for kind, count := range counts {
		meta["map_strategic_count_"+kind] = strconv.Itoa(count)
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_strategic_build_count','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func parseStrategicAdjacencies(path string, provinces map[int]*mapProvinceBuild, mapWidth, mapHeight int) ([]mapStrategicBuild, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	r.Comment = '#'
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	var header []string
	for {
		header, err = r.Read()
		if err != nil {
			return nil, err
		}
		if len(header) > 1 && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(header[0], "\ufeff")), "from") {
			break
		}
		if len(header) == 1 && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(header[0], "\ufeff")), "#") {
			continue
		}
	}
	columns := map[string]int{}
	for i, value := range header {
		columns[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "\ufeff")))] = i
	}
	field := func(record []string, name string) string {
		i, ok := columns[name]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	parseInt := func(record []string, name string, fallback int) int {
		value, err := strconv.Atoi(field(record, name))
		if err != nil {
			return fallback
		}
		return value
	}
	parseFloat := func(record []string, name string, fallback float64) float64 {
		value, err := strconv.ParseFloat(field(record, name), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fallback
		}
		return value
	}
	diagonal := math.Hypot(float64(mapWidth), float64(mapHeight))
	longThreshold := math.Max(250, diagonal*0.05)
	var out []mapStrategicBuild
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		from := parseInt(record, "from", -1)
		to := parseInt(record, "to", -1)
		if from < 0 || to < 0 || provinces[from] == nil || provinces[to] == nil {
			continue
		}
		fromPoint := mapProvinceBuildCenter(provinces[from])
		toPoint := mapProvinceBuildCenter(provinces[to])
		startX, startY := parseFloat(record, "start_x", -1), parseFloat(record, "start_y", -1)
		stopX, stopY := parseFloat(record, "stop_x", -1), parseFloat(record, "stop_y", -1)
		if startX < 0 || startY < 0 {
			startX, startY = fromPoint.X, fromPoint.Y
		}
		if stopX < 0 || stopY < 0 {
			stopX, stopY = toPoint.X, toPoint.Y
		}
		through := parseInt(record, "through", -1)
		fromSub := isSubterraneanProvince(provinces[from])
		toSub := isSubterraneanProvince(provinces[to])
		throughSub := isSubterraneanProvince(provinces[through])
		distance := math.Hypot(toPoint.X-fromPoint.X, toPoint.Y-fromPoint.Y)
		connectionType := strings.ToLower(field(record, "type"))
		kind := classifyStrategicPassage(connectionType, distance, longThreshold, fromSub, toSub, throughSub)
		out = append(out, mapStrategicBuild{
			From: from, To: to, Through: through, ConnectionType: connectionType, Comment: field(record, "comment"),
			StartX: startX, StartY: startY, StopX: stopX, StopY: stopY, PassageKind: kind, Distance: distance,
			FromSubterranean: fromSub, ToSubterranean: toSub,
		})
	}
	return out, nil
}

func classifyStrategicPassage(connectionType string, distance, longThreshold float64, fromSub, toSub, throughSub bool) string {
	if distance >= longThreshold {
		if fromSub || toSub || throughSub {
			return "underground_gateway"
		}
		return "offmap_gateway"
	}
	if fromSub || toSub || throughSub {
		if fromSub && toSub {
			return "underground_internal"
		}
		return "underground_gateway"
	}
	switch connectionType {
	case "river", "river_large":
		return "river_crossing"
	case "mountain":
		return "mountain_pass"
	case "sea":
		if distance <= 80 {
			return "strait"
		}
		return "sea_route"
	case "land":
		return "land_passage"
	default:
		return "explicit_passage"
	}
}

func isSubterraneanProvince(p *mapProvinceBuild) bool {
	if p == nil {
		return false
	}
	terrain := strings.ToLower(p.Terrain)
	return strings.HasPrefix(terrain, "mayik_") || strings.HasPrefix(terrain, "underworld_") ||
		strings.Contains(terrain, "cavern") || strings.Contains(terrain, "dungeon") ||
		strings.Contains(terrain, "subterranean") || strings.Contains(terrain, "underground")
}

func mapProvinceBuildCenter(p *mapProvinceBuild) MapPoint {
	if p == nil || p.Area <= 0 {
		return MapPoint{}
	}
	return MapPoint{X: float64(p.SumX) / float64(p.Area), Y: float64(p.SumY) / float64(p.Area)}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rebuildMapWaterBodies(ctx context.Context, tx *sql.Tx, active map[string]activeMapFile, provinces map[int]*mapProvinceBuild, adj map[[2]int]int, mapHeight int, geometryFingerprint string) error {
	lakeFile := active["gfx/map/map_object_data/lakes.txt"]
	lakeFingerprint := ""
	if lakeFile.Path != "" {
		var err error
		lakeFingerprint, err = mapGeometryFingerprint(lakeFile.Path)
		if err != nil {
			return err
		}
	}
	fingerprint := mapWaterBodyCacheVersion + ":" + geometryFingerprint + ":" + lakeFingerprint
	var cached string
	var bodyRows int
	_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='map_water_body_fingerprint'`).Scan(&cached)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM map_water_bodies`).Scan(&bodyRows)
	if cached == fingerprint && bodyRows > 0 {
		return nil
	}

	lakeIDs := map[int]bool{}
	for id, province := range provinces {
		if province != nil && province.WaterKind == "lake" && province.Area > 0 {
			lakeIDs[id] = true
		}
	}
	neighbors := map[int][]int{}
	for pair := range adj {
		if lakeIDs[pair[0]] && lakeIDs[pair[1]] {
			neighbors[pair[0]] = append(neighbors[pair[0]], pair[1])
			neighbors[pair[1]] = append(neighbors[pair[1]], pair[0])
		}
	}
	visited := map[int]bool{}
	bodies := map[int]*mapWaterBodyBuild{}
	provinceBody := map[int]int{}
	ids := make([]int, 0, len(lakeIDs))
	for id := range lakeIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, start := range ids {
		if visited[start] {
			continue
		}
		queue := []int{start}
		visited[start] = true
		component := []int{}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			component = append(component, pid)
			for _, next := range neighbors[pid] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		sort.Ints(component)
		body := &mapWaterBodyBuild{ID: component[0], MinX: math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1, Shores: map[int]int{}, Provinces: component}
		for _, pid := range component {
			provinceBody[pid] = body.ID
			p := provinces[pid]
			body.ProvinceCount++
			body.Area += p.Area
			body.SumX += float64(p.SumX)
			body.SumY += float64(p.SumY)
			body.MinX = minInt(body.MinX, p.MinX)
			body.MinY = minInt(body.MinY, p.MinY)
			body.MaxX = maxInt(body.MaxX, p.MaxX)
			body.MaxY = maxInt(body.MaxY, p.MaxY)
		}
		bodies[body.ID] = body
	}
	for pair, border := range adj {
		for _, side := range [][2]int{{pair[0], pair[1]}, {pair[1], pair[0]}} {
			bodyID := provinceBody[side[0]]
			if bodyID == 0 || provinceBody[side[1]] == bodyID {
				continue
			}
			other := provinces[side[1]]
			if other == nil || other.BlockKind == "water" {
				continue
			}
			bodies[bodyID].Shoreline += border
			bodies[bodyID].Shores[side[1]] += border
		}
	}
	if lakeFile.Path != "" && len(bodies) > 0 {
		if err := correlateLakeLocators(ctx, tx, lakeFile.Path, bodies, provinceBody, mapHeight); err != nil {
			return err
		}
	}

	for _, table := range []string{"map_water_body_shores", "map_water_body_provinces", "map_water_bodies"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
	}
	bodyStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_water_bodies(water_body_id,kind,province_count,area_pixels,shoreline_pixels,center_x,center_y,min_x,min_y,max_x,max_y,locator_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer bodyStmt.Close()
	provinceStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_water_body_provinces(water_body_id,province_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer provinceStmt.Close()
	shoreStmt, err := tx.PrepareContext(ctx, `INSERT INTO map_water_body_shores(water_body_id,province_id,shoreline_pixels) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer shoreStmt.Close()
	bodyIDs := make([]int, 0, len(bodies))
	for id := range bodies {
		bodyIDs = append(bodyIDs, id)
	}
	sort.Ints(bodyIDs)
	locatorCount := 0
	for _, id := range bodyIDs {
		body := bodies[id]
		centerX, centerY := 0.0, 0.0
		if body.Area > 0 {
			centerX, centerY = body.SumX/float64(body.Area), body.SumY/float64(body.Area)
		}
		if _, err := bodyStmt.ExecContext(ctx, body.ID, "lake", body.ProvinceCount, body.Area, body.Shoreline, centerX, centerY, body.MinX, body.MinY, body.MaxX, body.MaxY, body.LocatorCount); err != nil {
			return err
		}
		locatorCount += body.LocatorCount
		for _, pid := range body.Provinces {
			if _, err := provinceStmt.ExecContext(ctx, body.ID, pid); err != nil {
				return err
			}
		}
		shoreIDs := make([]int, 0, len(body.Shores))
		for pid := range body.Shores {
			shoreIDs = append(shoreIDs, pid)
		}
		sort.Ints(shoreIDs)
		for _, pid := range shoreIDs {
			if _, err := shoreStmt.ExecContext(ctx, body.ID, pid, body.Shores[pid]); err != nil {
				return err
			}
		}
	}
	meta := map[string]string{
		"map_water_body_fingerprint":     fingerprint,
		"map_water_body_count_lake":      strconv.Itoa(len(bodies)),
		"map_water_body_province_count":  strconv.Itoa(len(lakeIDs)),
		"map_water_body_locator_matches": strconv.Itoa(locatorCount),
	}
	for key, value := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('map_water_body_build_count','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(meta.value AS INTEGER)+1`)
	return err
}

func correlateLakeLocators(ctx context.Context, tx *sql.Tx, path string, bodies map[int]*mapWaterBodyBuild, provinceBody map[int]int, mapHeight int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	anchors := parseTerrainAnchors(data, "lake")
	if len(anchors) == 0 {
		return nil
	}
	runCache := map[int][]MapRun{}
	contains := func(x, y float64) int {
		px, py := int(math.Round(x)), int(math.Round(y))
		for pid, bodyID := range provinceBody {
			runs, ok := runCache[pid]
			if !ok {
				var blob []byte
				if tx.QueryRowContext(ctx, `SELECT fill_rle FROM map_province_geometry WHERE province_id=?`, pid).Scan(&blob) != nil {
					runCache[pid] = nil
					continue
				}
				runs, _ = DecodeMapRuns(blob)
				runCache[pid] = runs
			}
			for _, run := range runs {
				if int(run.Y) == py && px >= int(run.X0) && px <= int(run.X1) {
					return bodyID
				}
			}
		}
		return 0
	}
	directHits, flippedHits := 0, 0
	for _, anchor := range anchors {
		if contains(anchor.X, anchor.Z) != 0 {
			directHits++
		}
		if contains(anchor.X, float64(mapHeight-1)-anchor.Z) != 0 {
			flippedHits++
		}
	}
	flip := flippedHits > directHits
	for _, anchor := range anchors {
		y := anchor.Z
		if flip {
			y = float64(mapHeight-1) - y
		}
		bodyID := contains(anchor.X, y)
		if bodyID == 0 {
			bestDistance := math.MaxFloat64
			for id, body := range bodies {
				cx, cy := body.SumX/float64(body.Area), body.SumY/float64(body.Area)
				distance := math.Hypot(cx-anchor.X, cy-y)
				if distance < bestDistance {
					bestDistance, bodyID = distance, id
				}
			}
			if bestDistance > 160 {
				bodyID = 0
			}
		}
		if bodyID != 0 {
			bodies[bodyID].LocatorCount++
		}
	}
	return nil
}
