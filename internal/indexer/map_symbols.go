package indexer

import (
	"context"
	"database/sql"
	"image"
	"image/color"
	"math"
	"sort"
	"strings"
)

type mapObjectInstanceRow struct {
	Subtype    string
	ProvinceID int
	X, Y       float64
	Rotation   float64
	Scale      float64
	Capital    bool
}

func (db *DB) renderVegetationMarkerLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, layer MapRenderLayer) (int, []string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT subtype,province_id,x,y,rotation,scale FROM map_object_instances WHERE object_kind='vegetation' ORDER BY y,x,subtype`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var instances []mapObjectInstanceRow
	for rows.Next() {
		var item mapObjectInstanceRow
		if err := rows.Scan(&item.Subtype, &item.ProvinceID, &item.X, &item.Y, &item.Rotation, &item.Scale); err != nil {
			return 0, nil, err
		}
		if pids[item.ProvinceID] {
			instances = append(instances, item)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(instances) == 0 {
		return 0, []string{"no indexed vegetation transforms were available inside the target"}, nil
	}

	size := layer.LineWidth
	if size <= 0 {
		size = 5
	}
	spacing := maxInt(12, size*4)
	limit := layer.Limit
	if limit <= 0 {
		limit = maxInt(80, minInt(7200, v.Width*v.Height/(spacing*spacing)))
	}
	occupied := map[[2]int]bool{}
	drawn := 0
	for _, item := range instances {
		x, y := sourceToRender(v, item.X, item.Y)
		if x < 0 || y < 0 || x >= v.Width || y >= v.Height {
			continue
		}
		cell := [2]int{x / spacing, y / spacing}
		if occupied[cell] {
			continue
		}
		occupied[cell] = true
		drawVegetationSymbol(canvas, x, y, size, item.Subtype, item.Rotation)
		drawn++
		if drawn >= limit {
			break
		}
	}
	return drawn, nil, nil
}

func (db *DB) renderHoldingMarkerLayer(ctx context.Context, canvas *image.RGBA, v renderViewport, pids map[int]bool, year int, layer MapRenderLayer) (int, []string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT o.subtype,o.province_id,o.x,o.y,o.rotation,o.scale,p.is_county_capital
		FROM map_object_instances o JOIN map_provinces p ON p.province_id=o.province_id
		WHERE o.object_kind='holding' ORDER BY p.is_county_capital DESC,o.province_id`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var instances []mapObjectInstanceRow
	for rows.Next() {
		var item mapObjectInstanceRow
		var capital int
		if err := rows.Scan(&item.Subtype, &item.ProvinceID, &item.X, &item.Y, &item.Rotation, &item.Scale, &capital); err != nil {
			return 0, nil, err
		}
		item.Capital = capital != 0
		if pids[item.ProvinceID] {
			instances = append(instances, item)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(instances) == 0 {
		return 0, []string{"no indexed building locators were available inside the target"}, nil
	}

	size := layer.LineWidth
	if size <= 0 {
		size = 7
	}
	spacing := maxInt(12, size*2)
	limit := layer.Limit
	if limit <= 0 {
		limit = maxInt(80, minInt(6400, v.Width*v.Height/(spacing*spacing*2)))
	}
	occupied := map[[2]int]bool{}
	holdingTypes := map[int]string{}
	drawn := 0
	date := yearDateKey(year)
	for _, item := range instances {
		holding, ok := holdingTypes[item.ProvinceID]
		if !ok {
			holding, err = db.resolveProvinceField(ctx, item.ProvinceID, "", "holding", date, false)
			if err != nil && err != sql.ErrNoRows {
				return drawn, nil, err
			}
			holdingTypes[item.ProvinceID] = holding
		}
		kind := holdingSymbolKind(holding)
		if kind == "" {
			continue
		}
		x, y := sourceToRender(v, item.X, item.Y)
		if x < 0 || y < 0 || x >= v.Width || y >= v.Height {
			continue
		}
		cell := [2]int{x / spacing, y / spacing}
		if occupied[cell] {
			continue
		}
		occupied[cell] = true
		drawHoldingSymbol(canvas, x, y, size, kind, item.Capital)
		drawn++
		if drawn >= limit {
			break
		}
	}
	return drawn, nil, nil
}

func holdingSymbolKind(holding string) string {
	holding = strings.ToLower(strings.TrimSpace(holding))
	switch {
	case holding == "", holding == "none", strings.Contains(holding, "wasteland"), strings.Contains(holding, "empty_holding"):
		return ""
	case strings.Contains(holding, "great_city"), strings.Contains(holding, "metro"):
		return "metropolis"
	case strings.Contains(holding, "necropolis"):
		return "necropolis"
	case strings.Contains(holding, "ruin"):
		return "ruins"
	case strings.Contains(holding, "nomad"):
		return "nomad"
	case strings.Contains(holding, "castle"):
		return "castle"
	case strings.Contains(holding, "city"):
		return "city"
	case strings.Contains(holding, "church"), strings.Contains(holding, "temple"):
		return "church"
	case strings.Contains(holding, "tribal"):
		return "tribal"
	default:
		return "settlement"
	}
}

func drawVegetationSymbol(canvas *image.RGBA, x, y, size int, subtype string, rotation float64) {
	ink := color.RGBA{19, 27, 22, 220}
	leaves := color.RGBA{39, 67, 45, 205}
	highlight := color.RGBA{90, 106, 69, 145}
	trunk := color.RGBA{55, 45, 32, 215}
	shadow := color.RGBA{7, 13, 12, 105}
	s := maxInt(3, size)
	drawDisc(canvas, x+1, y+s/2+1, maxInt(1, s/2), shadow)
	switch subtype {
	case "conifer":
		drawLine(canvas, x, y-s, x, y+s, maxInt(1, s/6), trunk)
		for i, width := range []int{s, s * 4 / 5, s * 3 / 5} {
			cy := y + s/2 - i*s/2
			fillTriangle(canvas, image.Pt(x-width, cy+s/2), image.Pt(x+width, cy+s/2), image.Pt(x, cy-s), leaves)
		}
		drawLine(canvas, x, y-s, x, y+s/2, maxInt(1, s/8), ink)
	case "palm":
		dx := int(math.Round(math.Sin(rotation) * float64(s) * 0.35))
		topX, topY := x+dx, y-s
		drawLine(canvas, x, y+s, topX, topY, maxInt(1, s/5), trunk)
		for _, angle := range []float64{-2.8, -2.2, -1.55, -0.9, -0.35} {
			ex := topX + int(math.Cos(angle)*float64(s*2))
			ey := topY + int(math.Sin(angle)*float64(s))
			drawLine(canvas, topX, topY, ex, ey, maxInt(1, s/7), leaves)
		}
		drawDisc(canvas, topX, topY, maxInt(1, s/3), highlight)
	case "reeds":
		for i := -2; i <= 2; i++ {
			top := y - s - absInt(i)*s/5
			drawLine(canvas, x+i*s/3, y+s, x+i*s/4, top, maxInt(1, s/8), leaves)
			drawLine(canvas, x+i*s/4, top+s/3, x+i*s/2, top, maxInt(1, s/10), highlight)
		}
	case "scrub":
		for _, p := range []image.Point{{x - s/2, y}, {x + s/2, y}, {x, y - s/3}} {
			drawDisc(canvas, p.X, p.Y, maxInt(2, s/2), leaves)
		}
		drawLine(canvas, x-s, y+s/2, x+s, y+s/2, maxInt(1, s/7), ink)
	case "deadwood":
		drawLine(canvas, x, y+s, x, y-s, maxInt(1, s/5), trunk)
		drawLine(canvas, x, y-s/3, x-s, y-s, maxInt(1, s/7), ink)
		drawLine(canvas, x, y-s/2, x+s, y-s, maxInt(1, s/7), ink)
		drawLine(canvas, x-s/2, y-s*2/3, x-s, y-s/2, maxInt(1, s/8), ink)
	case "jungle":
		drawLine(canvas, x, y+s, x, y, maxInt(1, s/4), trunk)
		for _, p := range []image.Point{{x - s, y}, {x + s, y}, {x, y - s}, {x - s/2, y - s/2}, {x + s/2, y - s/2}} {
			drawDisc(canvas, p.X, p.Y, maxInt(2, s*2/3), leaves)
		}
		drawDisc(canvas, x-s/3, y-s, maxInt(1, s/3), highlight)
	default:
		drawLine(canvas, x, y+s, x, y, maxInt(1, s/4), trunk)
		for _, p := range []image.Point{{x - s*2/3, y}, {x + s*2/3, y}, {x, y - s*2/3}} {
			drawDisc(canvas, p.X, p.Y, maxInt(2, s*2/3), leaves)
		}
		drawDisc(canvas, x-s/3, y-s/2, maxInt(1, s/3), highlight)
	}
}

func drawHoldingSymbol(canvas *image.RGBA, x, y, size int, kind string, capital bool) {
	s := maxInt(4, size)
	ink := color.RGBA{20, 21, 20, 245}
	paper := color.RGBA{213, 194, 151, 235}
	accent := map[string]color.RGBA{
		"castle": {121, 88, 57, 235}, "city": {134, 75, 62, 230}, "metropolis": {117, 65, 56, 235},
		"church": {150, 123, 67, 235}, "tribal": {77, 96, 65, 230}, "nomad": {88, 103, 79, 230},
		"ruins": {100, 96, 86, 225}, "necropolis": {87, 79, 92, 230}, "settlement": {105, 89, 65, 230},
	}[kind]
	drawDisc(canvas, x+1, y+1, s+2, color.RGBA{6, 10, 10, 135})
	if capital {
		drawCircle(canvas, x, y, s+2, color.RGBA{219, 185, 92, 235}, maxInt(1, s/5))
	}
	switch kind {
	case "castle":
		fillRect(canvas, image.Rect(x-s, y-s/2, x+s+1, y+s+1), accent)
		fillRect(canvas, image.Rect(x-s, y-s, x-s/3, y+s+1), paper)
		fillRect(canvas, image.Rect(x+s/3, y-s, x+s+1, y+s+1), paper)
		for _, bx := range []int{x - s, x - s/3, x + s/3, x + s} {
			fillRect(canvas, image.Rect(bx-s/6, y-s-s/3, bx+s/6+1, y-s/2), accent)
		}
		outlineRect(canvas, image.Rect(x-s, y-s, x+s+1, y+s+1), ink, maxInt(1, s/6))
	case "city", "metropolis":
		count := 3
		if kind == "metropolis" {
			count = 4
		}
		for i := 0; i < count; i++ {
			cx := x + (i-(count-1)/2)*s
			h := s + (i%2)*s/2
			fillRect(canvas, image.Rect(cx-s/2, y-h/2, cx+s/2+1, y+s), paper)
			fillTriangle(canvas, image.Pt(cx-s*2/3, y-h/2), image.Pt(cx+s*2/3, y-h/2), image.Pt(cx, y-h), accent)
			outlineRect(canvas, image.Rect(cx-s/2, y-h/2, cx+s/2+1, y+s), ink, maxInt(1, s/8))
		}
		drawLine(canvas, x-s*2, y+s, x+s*2, y+s, maxInt(1, s/6), ink)
	case "church":
		fillRect(canvas, image.Rect(x-s, y-s/3, x+s+1, y+s+1), paper)
		fillTriangle(canvas, image.Pt(x-s-s/3, y-s/3), image.Pt(x+s+s/3, y-s/3), image.Pt(x, y-s-s/3), accent)
		fillRect(canvas, image.Rect(x-s/4, y-s-s/2, x+s/4+1, y-s/3), accent)
		drawLine(canvas, x, y-s-s, x, y-s-s/2, maxInt(1, s/7), ink)
		drawLine(canvas, x-s/3, y-s-s*4/5, x+s/3, y-s-s*4/5, maxInt(1, s/8), ink)
		outlineRect(canvas, image.Rect(x-s, y-s/3, x+s+1, y+s+1), ink, maxInt(1, s/7))
	case "tribal":
		fillTriangle(canvas, image.Pt(x-s, y+s), image.Pt(x+s, y+s), image.Pt(x, y-s), accent)
		drawLine(canvas, x-s, y+s, x+s/3, y-s-s/2, maxInt(1, s/7), ink)
		drawLine(canvas, x+s, y+s, x-s/3, y-s-s/2, maxInt(1, s/7), ink)
		fillTriangle(canvas, image.Pt(x-s/4, y+s), image.Pt(x+s/4, y+s), image.Pt(x, y+s/4), paper)
	case "nomad":
		fillTriangle(canvas, image.Pt(x-s-s/2, y+s), image.Pt(x, y+s), image.Pt(x-s/2, y-s/2), accent)
		fillTriangle(canvas, image.Pt(x, y+s), image.Pt(x+s+s/2, y+s), image.Pt(x+s/2, y-s/2), paper)
		drawLine(canvas, x-s/2, y-s/2, x+s/2, y-s-s/2, maxInt(1, s/8), ink)
		drawLine(canvas, x+s/2, y-s-s/2, x+s/2, y-s*2, maxInt(1, s/8), ink)
		fillTriangle(canvas, image.Pt(x+s/2, y-s*2), image.Pt(x+s+s/2, y-s*2+s/3), image.Pt(x+s/2, y-s*2+s/2), accent)
		drawLine(canvas, x-s-s/2, y+s, x+s+s/2, y+s, maxInt(1, s/7), ink)
	case "ruins":
		fillRect(canvas, image.Rect(x-s-s/2, y, x-s/2, y+s+1), accent)
		fillRect(canvas, image.Rect(x-s/4, y-s/2, x+s/3, y+s+1), paper)
		fillRect(canvas, image.Rect(x+s/2, y+s/3, x+s+s/2, y+s+1), accent)
		drawLine(canvas, x-s-s/2, y, x-s, y-s/2, maxInt(1, s/7), ink)
		drawLine(canvas, x-s, y-s/2, x-s/2, y, maxInt(1, s/7), ink)
		drawLine(canvas, x-s/4, y-s/2, x, y-s, maxInt(1, s/7), ink)
		drawLine(canvas, x-s-s/2, y+s, x+s+s/2, y+s, maxInt(1, s/6), ink)
	case "necropolis":
		fillRect(canvas, image.Rect(x-s, y-s/4, x+s+1, y+s+1), accent)
		fillTriangle(canvas, image.Pt(x-s-s/3, y-s/4), image.Pt(x+s+s/3, y-s/4), image.Pt(x, y-s-s/2), paper)
		drawCircle(canvas, x, y+s/4, maxInt(2, s/3), ink, maxInt(1, s/8))
		fillRect(canvas, image.Rect(x-s/5, y+s/4, x+s/5+1, y+s+1), ink)
		outlineRect(canvas, image.Rect(x-s, y-s/4, x+s+1, y+s+1), ink, maxInt(1, s/7))
	default:
		fillRect(canvas, image.Rect(x-s, y, x+s+1, y+s+1), paper)
		fillTriangle(canvas, image.Pt(x-s-s/3, y), image.Pt(x+s+s/3, y), image.Pt(x, y-s), accent)
		outlineRect(canvas, image.Rect(x-s, y, x+s+1, y+s+1), ink, maxInt(1, s/7))
	}
}

func fillRect(canvas *image.RGBA, rect image.Rectangle, c color.RGBA) {
	rect = rect.Intersect(canvas.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			blendPixel(canvas, x, y, c)
		}
	}
}

func outlineRect(canvas *image.RGBA, rect image.Rectangle, c color.RGBA, width int) {
	for i := 0; i < width; i++ {
		drawLine(canvas, rect.Min.X+i, rect.Min.Y+i, rect.Max.X-1-i, rect.Min.Y+i, 0, c)
		drawLine(canvas, rect.Min.X+i, rect.Max.Y-1-i, rect.Max.X-1-i, rect.Max.Y-1-i, 0, c)
		drawLine(canvas, rect.Min.X+i, rect.Min.Y+i, rect.Min.X+i, rect.Max.Y-1-i, 0, c)
		drawLine(canvas, rect.Max.X-1-i, rect.Min.Y+i, rect.Max.X-1-i, rect.Max.Y-1-i, 0, c)
	}
}

func fillTriangle(canvas *image.RGBA, a, b, cPoint image.Point, c color.RGBA) {
	points := []image.Point{a, b, cPoint}
	sort.Slice(points, func(i, j int) bool { return points[i].Y < points[j].Y })
	minX, maxX := minInt(a.X, minInt(b.X, cPoint.X)), maxInt(a.X, maxInt(b.X, cPoint.X))
	minY, maxY := points[0].Y, points[2].Y
	area := edgeFunction(a, b, cPoint)
	if area == 0 {
		return
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			p := image.Pt(x, y)
			w0, w1, w2 := edgeFunction(b, cPoint, p), edgeFunction(cPoint, a, p), edgeFunction(a, b, p)
			if (w0 >= 0 && w1 >= 0 && w2 >= 0) || (w0 <= 0 && w1 <= 0 && w2 <= 0) {
				if p.In(canvas.Bounds()) {
					blendPixel(canvas, x, y, c)
				}
			}
		}
	}
}

func edgeFunction(a, b, c image.Point) int {
	return (c.X-a.X)*(b.Y-a.Y) - (c.Y-a.Y)*(b.X-a.X)
}

func drawCircle(canvas *image.RGBA, cx, cy, radius int, c color.RGBA, width int) {
	inner := maxInt(0, radius-width)
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			d2 := (x-cx)*(x-cx) + (y-cy)*(y-cy)
			if d2 <= radius*radius && d2 >= inner*inner && image.Pt(x, y).In(canvas.Bounds()) {
				blendPixel(canvas, x, y, c)
			}
		}
	}
}
