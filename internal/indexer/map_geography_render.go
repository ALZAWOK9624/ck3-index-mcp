package indexer

import (
	"context"
	"image"
	"image/color"
	"math"
	"sort"
)

type mapStrategicRenderRow struct {
	ID, From, To                  int
	Kind, ConnectionType, Comment string
	Start, Stop                   MapPoint
	Distance                      float64
}

type mapLakeRenderRow struct {
	ID, Area, LocatorCount int
	Center                 MapPoint
}

func (db *DB) loadStrategicRenderRows(ctx context.Context) ([]mapStrategicRenderRow, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id,from_province,to_province,passage_kind,connection_type,comment,start_x,start_y,stop_x,stop_y,distance_pixels FROM map_strategic_adjacencies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mapStrategicRenderRow
	for rows.Next() {
		var item mapStrategicRenderRow
		if err := rows.Scan(&item.ID, &item.From, &item.To, &item.Kind, &item.ConnectionType, &item.Comment,
			&item.Start.X, &item.Start.Y, &item.Stop.X, &item.Stop.Y, &item.Distance); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) renderStrategicPassageLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, layer MapRenderLayer) (int, error) {
	rows, err := db.loadStrategicRenderRows(ctx)
	if err != nil {
		return 0, err
	}
	allowedKinds := map[string]bool{}
	for _, kind := range layer.IDs {
		allowedKinds[kind] = true
	}
	width := layer.LineWidth
	if width <= 0 {
		width = 2
	}
	limit := layer.Limit
	if limit <= 0 {
		limit = len(rows)
	}
	overview := v.Scale <= 1.15
	drawn := 0
	for _, item := range rows {
		if len(allowedKinds) > 0 && !allowedKinds[item.Kind] {
			continue
		}
		fromInside, toInside := pids[item.From], pids[item.To]
		if !fromInside && !toInside {
			continue
		}
		x0, y0 := sourceToRender(v, item.Start.X, item.Start.Y)
		x1, y1 := sourceToRender(v, item.Stop.X, item.Stop.Y)
		if item.Kind == "underground_gateway" || item.Kind == "offmap_gateway" {
			if fromInside {
				drawPassageStub(canvas, x0, y0, x1, y1, width, passageColor(item.Kind, layer.Color))
			}
			if toInside {
				drawPassageStub(canvas, x1, y1, x0, y0, width, passageColor(item.Kind, layer.Color))
			}
			drawn++
		} else if fromInside && toInside {
			c := passageColor(item.Kind, layer.Color)
			shadow := color.RGBA{12, 17, 18, 175}
			lineWidth := width
			shadowWidth := width + 2
			if overview {
				switch item.Kind {
				case "strait", "river_crossing":
					lineWidth, shadowWidth = maxInt(1, width/2), maxInt(2, width)
					c.A, shadow.A = minUint8(c.A, 105), 55
				case "underground_internal":
					lineWidth, shadowWidth = maxInt(1, width/2), maxInt(2, width)
					c.A, shadow.A = minUint8(c.A, 125), 65
				case "sea_route":
					lineWidth, shadowWidth = maxInt(1, width), width+maxInt(1, width/2)
					c.A, shadow.A = minUint8(c.A, 175), 85
				}
			}
			dashed := item.Kind == "underground_internal"
			drawCurvedPassage(canvas, x0, y0, x1, y1, shadowWidth, shadow, dashed)
			drawCurvedPassage(canvas, x0, y0, x1, y1, lineWidth, c, dashed)
			if item.Kind == "river_crossing" && !overview {
				drawBridgeTick(canvas, x0, y0, x1, y1, lineWidth)
			}
			drawn++
		}
		if drawn >= limit {
			break
		}
	}
	return drawn, nil
}

func minUint8(a, b uint8) uint8 {
	if a < b {
		return a
	}
	return b
}

func passageColor(kind, override string) color.RGBA {
	if override != "" {
		return parseRenderColor(override, color.RGBA{59, 91, 103, 220})
	}
	switch kind {
	case "river_crossing":
		return color.RGBA{54, 104, 118, 225}
	case "mountain_pass":
		return color.RGBA{111, 91, 61, 225}
	case "underground_internal", "underground_gateway":
		return color.RGBA{91, 70, 101, 225}
	case "offmap_gateway":
		return color.RGBA{64, 60, 65, 230}
	case "sea_route":
		return color.RGBA{45, 79, 94, 205}
	default:
		return color.RGBA{63, 100, 112, 220}
	}
}

func drawCurvedPassage(canvas *image.RGBA, x0, y0, x1, y1, width int, c color.RGBA, dashed bool) {
	dx, dy := float64(x1-x0), float64(y1-y0)
	distance := math.Hypot(dx, dy)
	if distance < 1 {
		return
	}
	offset := math.Min(28, distance*0.08)
	cx := (float64(x0+x1) / 2) - dy/distance*offset
	cy := (float64(y0+y1) / 2) + dx/distance*offset
	steps := maxInt(8, int(distance/4))
	previousX, previousY := x0, y0
	for step := 1; step <= steps; step++ {
		t := float64(step) / float64(steps)
		oneMinus := 1 - t
		x := int(math.Round(oneMinus*oneMinus*float64(x0) + 2*oneMinus*t*cx + t*t*float64(x1)))
		y := int(math.Round(oneMinus*oneMinus*float64(y0) + 2*oneMinus*t*cy + t*t*float64(y1)))
		if !dashed || (step/3)%2 == 0 {
			drawLine(canvas, previousX, previousY, x, y, width, c)
		}
		previousX, previousY = x, y
	}
}

func drawPassageStub(canvas *image.RGBA, x0, y0, towardX, towardY, width int, c color.RGBA) {
	dx, dy := float64(towardX-x0), float64(towardY-y0)
	distance := math.Hypot(dx, dy)
	if distance < 1 {
		return
	}
	length := math.Min(28, distance*0.18)
	x1 := x0 + int(math.Round(dx/distance*length))
	y1 := y0 + int(math.Round(dy/distance*length))
	drawCurvedPassage(canvas, x0, y0, x1, y1, width+2, color.RGBA{10, 13, 14, 170}, true)
	drawCurvedPassage(canvas, x0, y0, x1, y1, width, c, true)
}

func drawBridgeTick(canvas *image.RGBA, x0, y0, x1, y1, width int) {
	mx, my := (x0+x1)/2, (y0+y1)/2
	dx, dy := float64(x1-x0), float64(y1-y0)
	distance := math.Hypot(dx, dy)
	if distance < 1 {
		return
	}
	px, py := -dy/distance, dx/distance
	half := float64(maxInt(4, width*3))
	drawLine(canvas, mx+int(px*half), my+int(py*half), mx-int(px*half), my-int(py*half), maxInt(1, width), color.RGBA{191, 172, 126, 225})
}

func (db *DB) renderStrategicPortalLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, layer MapRenderLayer) (int, []string, error) {
	rows, err := db.loadStrategicRenderRows(ctx)
	if err != nil {
		return 0, nil, err
	}
	size := layer.LineWidth
	if size <= 0 {
		size = 6
	}
	seen := map[[3]int]bool{}
	drawn := 0
	for _, item := range rows {
		if item.Kind != "underground_gateway" && item.Kind != "offmap_gateway" {
			continue
		}
		points := []struct {
			PID   int
			Point MapPoint
		}{{item.From, item.Start}, {item.To, item.Stop}}
		for _, point := range points {
			if !pids[point.PID] {
				continue
			}
			x, y := sourceToRender(v, point.Point.X, point.Point.Y)
			key := [3]int{point.PID, x / maxInt(1, size), y / maxInt(1, size)}
			if seen[key] {
				continue
			}
			seen[key] = true
			drawPortalSymbol(canvas, x, y, size, passageColor(item.Kind, layer.Color))
			drawn++
			if layer.Limit > 0 && drawn >= layer.Limit {
				return drawn, nil, nil
			}
		}
	}
	if drawn == 0 {
		return 0, []string{"no underground or off-map gateways were present inside the target"}, nil
	}
	return drawn, nil, nil
}

func drawPortalSymbol(canvas *image.RGBA, x, y, size int, c color.RGBA) {
	size = maxInt(4, size)
	drawDisc(canvas, x, y, size+3, color.RGBA{10, 12, 13, 190})
	drawDisc(canvas, x, y, size+1, c)
	drawDisc(canvas, x, y, maxInt(2, size-2), color.RGBA{22, 24, 24, 245})
	drawLine(canvas, x-size-3, y, x-size+1, y, 1, c)
	drawLine(canvas, x+size-1, y, x+size+3, y, 1, c)
	drawLine(canvas, x, y-size-3, x, y-size+1, 1, c)
}

func (db *DB) renderLakeMarkerLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, layer MapRenderLayer) (int, []string, error) {
	selectedBodies := map[int]bool{}
	rows, err := db.sql.QueryContext(ctx, `SELECT water_body_id,province_id FROM map_water_body_provinces`)
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var bodyID, pid int
		if err := rows.Scan(&bodyID, &pid); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if pids[pid] {
			selectedBodies[bodyID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	shoreRows, err := db.sql.QueryContext(ctx, `SELECT water_body_id,province_id FROM map_water_body_shores`)
	if err != nil {
		return 0, nil, err
	}
	for shoreRows.Next() {
		var bodyID, pid int
		if err := shoreRows.Scan(&bodyID, &pid); err != nil {
			shoreRows.Close()
			return 0, nil, err
		}
		if pids[pid] {
			selectedBodies[bodyID] = true
		}
	}
	if err := shoreRows.Close(); err != nil {
		return 0, nil, err
	}
	bodyRows, err := db.sql.QueryContext(ctx, `SELECT water_body_id,area_pixels,locator_count,center_x,center_y FROM map_water_bodies WHERE kind='lake'`)
	if err != nil {
		return 0, nil, err
	}
	var lakes []mapLakeRenderRow
	for bodyRows.Next() {
		var lake mapLakeRenderRow
		if err := bodyRows.Scan(&lake.ID, &lake.Area, &lake.LocatorCount, &lake.Center.X, &lake.Center.Y); err != nil {
			bodyRows.Close()
			return 0, nil, err
		}
		if selectedBodies[lake.ID] {
			lakes = append(lakes, lake)
		}
	}
	if err := bodyRows.Close(); err != nil {
		return 0, nil, err
	}
	sort.Slice(lakes, func(i, j int) bool {
		if lakes[i].Area != lakes[j].Area {
			return lakes[i].Area > lakes[j].Area
		}
		return lakes[i].ID < lakes[j].ID
	})
	limit := layer.Limit
	if limit <= 0 || limit > len(lakes) {
		limit = len(lakes)
	}
	size := layer.LineWidth
	if size <= 0 {
		size = 5
	}
	c := parseRenderColor(layer.Color, color.RGBA{104, 153, 158, 220})
	for _, lake := range lakes[:limit] {
		x, y := sourceToRender(v, lake.Center.X, lake.Center.Y)
		drawLakeSymbol(canvas, x, y, size, c)
	}
	if limit == 0 {
		return 0, []string{"no indexed lake body was present inside the target"}, nil
	}
	return limit, nil, nil
}

func drawLakeSymbol(canvas *image.RGBA, x, y, size int, c color.RGBA) {
	size = maxInt(3, size)
	shadow := color.RGBA{9, 18, 21, 165}
	for row := 0; row < 2; row++ {
		yy := y + row*maxInt(3, size/2)
		drawLine(canvas, x-size*2, yy+1, x-size, yy-1, 2, shadow)
		drawLine(canvas, x-size, yy-1, x, yy+1, 2, shadow)
		drawLine(canvas, x, yy+1, x+size, yy-1, 2, shadow)
		drawLine(canvas, x+size, yy-1, x+size*2, yy+1, 2, shadow)
		drawLine(canvas, x-size*2, yy, x-size, yy-2, 1, c)
		drawLine(canvas, x-size, yy-2, x, yy, 1, c)
		drawLine(canvas, x, yy, x+size, yy-2, 1, c)
		drawLine(canvas, x+size, yy-2, x+size*2, yy, 1, c)
	}
}
