package indexer

import (
	"context"
	"image"
	"image/color"
	"math"
	"sort"
)

// parchmentGround is the physical sheet under the politics: what is land, what
// is water, and the tight bounds of each. Every later pass reuses these masks
// instead of re-deriving coverage from the database.
type parchmentGround struct {
	Land        []bool
	LandBounds  *maskBounds
	Water       []bool
	WaterBounds *maskBounds
	// Distance in pixels from the nearest land pixel, for sea pixels only.
	SeaDistance []int32
}

// renderParchmentGround establishes the sheet: it finds every land province in
// view, washes the open water, engraves the coastlines, and tints lakes and
// impassable rock.
//
// Land outside the requested subject is deliberately left as bare paper with a
// coastline. That is how a period atlas shows neighbouring territory, and it
// removes the grey unlabelled blobs the old renderer left around the subject.
func (db *DB) renderParchmentGround(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA,
	v renderViewport, palette parchmentPalette, metrics styleMetrics) (*parchmentGround, int, error) {
	meta, err := scratch.provinceMeta(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	ground := &parchmentGround{
		Land:        scratch.boolMask(0, v.Width*v.Height),
		Water:       scratch.boolMask(1, v.Width*v.Height),
		LandBounds:  newMaskBounds(v.Height),
		WaterBounds: newMaskBounds(v.Height),
	}
	mark := func(mask []bool, bounds *maskBounds, runs []MapRun) {
		for _, run := range runs {
			x0, y0 := sourceToRender(v, float64(run.X0), float64(run.Y))
			x1, y1 := sourceToRender(v, float64(run.X1+1), float64(run.Y+1))
			lowX, highX := maxInt(0, x0), minInt(v.Width, maxInt(x0+1, x1))
			if highX <= lowX {
				continue
			}
			for y := maxInt(0, y0); y < minInt(v.Height, maxInt(y0+1, y1)); y++ {
				row := mask[y*v.Width : y*v.Width+v.Width]
				for x := lowX; x < highX; x++ {
					row[x] = true
				}
				bounds.add(lowX, highX-1, y)
			}
		}
	}

	// Everything with geometry inside the viewport, land and water alike. This is
	// the whole visible world, not just the subject, so coastlines are complete.
	visible := make([]int, 0, 2048)
	for pid, info := range meta {
		if info.Area <= 0 || info.MaxX < v.MinX || info.MinX > v.MaxX || info.MaxY < v.MinY || info.MinY > v.MaxY {
			continue
		}
		visible = append(visible, pid)
	}
	sort.Ints(visible)
	if err := scratch.primeRuns(ctx, db, len(visible), false); err != nil {
		return nil, 0, err
	}

	type wash struct {
		pid   int
		color color.RGBA
	}
	washes := make([]wash, 0, 64)
	features := 0
	for _, pid := range visible {
		info := meta[pid]
		runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
		if err != nil {
			return nil, 0, err
		}
		if len(runs) == 0 {
			continue
		}
		isWater := info.BlockKind == "water" || info.WaterKind == "sea" || info.WaterKind == "impassable_sea"
		switch {
		case isWater:
			mark(ground.Water, ground.WaterBounds, runs)
		default:
			mark(ground.Land, ground.LandBounds, runs)
		}
		// Inland water and rock get their own wash once the sheet is washed.
		switch {
		case info.WaterKind == "lake":
			washes = append(washes, wash{pid, palette.Lake})
			features++
		case info.WaterKind == "river":
			washes = append(washes, wash{pid, mixRGBA(palette.Lake, palette.Water, 0.35)})
			features++
		case info.BlockKind == "impassable_mountain":
			washes = append(washes, wash{pid, palette.Mountain})
			features++
		}
	}

	// A pixel is open sea when no land covers it. Non-target land is land.
	falloff := metrics.pxf(110)
	if falloff < 14 {
		falloff = 14
	}
	coastReach := metrics.pxf(26)
	limit := int(math.Max(falloff, coastReach)) + 3
	ground.SeaDistance = distanceFromMask(ground.Land, v.Width, v.Height, limit)

	// Sea wash, deepening away from any shore.
	for y := 0; y < v.Height; y++ {
		rowLand := ground.Land[y*v.Width : y*v.Width+v.Width]
		rowDist := ground.SeaDistance[y*v.Width : y*v.Width+v.Width]
		for x := 0; x < v.Width; x++ {
			if rowLand[x] {
				continue
			}
			t := clampFloat(float64(rowDist[x])/falloff, 0, 1)
			seaColor := mixRGBA(palette.Sea, palette.SeaDeep, t)
			// Keep some paper grain visible through the wash.
			i := y*canvas.Stride + x*4
			blended := mixRGBA(color.RGBA{canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2], 255}, seaColor, 0.82)
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2] = blended.R, blended.G, blended.B
		}
	}

	drawEngravedCoastFromDistance(canvas, ground.Land, ground.SeaDistance, v.Width, v.Height, palette, metrics)

	// Lakes, rivers and rock, over the wash but under the politics.
	for _, item := range washes {
		runs, err := db.mapProvinceRuns(ctx, scratch, item.pid, false)
		if err != nil {
			return nil, 0, err
		}
		drawWashRuns(canvas, v, runs, item.color, 0.8)
		boundary, err := db.mapProvinceRuns(ctx, scratch, item.pid, true)
		if err != nil {
			return nil, 0, err
		}
		outline := color.RGBA{palette.Water.R, palette.Water.G, palette.Water.B, 150}
		if item.color == palette.Mountain {
			outline = color.RGBA{palette.InkSoft.R, palette.InkSoft.G, palette.InkSoft.B, 120}
		}
		// Blended, not copied: drawRuns writes the source alpha straight into the
		// canvas, which would leave the sheet translucent along every shoreline.
		drawWashRuns(canvas, v, boundary, outline, float64(outline.A)/255)
	}
	return ground, features, nil
}

// drawEngravedCoastFromDistance draws the coast lines from an existing distance
// field, so the sea wash and the engraving share one computation.
func drawEngravedCoastFromDistance(canvas *image.RGBA, landMask []bool, distance []int32,
	width, height int, palette parchmentPalette, metrics styleMetrics) {
	step := metrics.pxf(3.4)
	if step < 1.7 {
		step = 1.7
	}
	const bandCount = 5
	type band struct{ lo, hi, alpha float64 }
	bands := make([]band, 0, bandCount)
	position := step
	thickness := math.Max(1, metrics.pxf(1))
	for k := 0; k < bandCount; k++ {
		bands = append(bands, band{lo: position, hi: position + thickness, alpha: 1 - float64(k)/(bandCount+1)})
		position += step * (1 + 0.45*float64(k))
	}
	ink := palette.InkSoft
	for y := 0; y < height; y++ {
		rowLand := landMask[y*width : y*width+width]
		rowDist := distance[y*width : y*width+width]
		for x := 0; x < width; x++ {
			if rowLand[x] {
				continue
			}
			d := float64(rowDist[x])
			for _, item := range bands {
				if d >= item.lo && d < item.hi {
					blendPixel(canvas, x, y, color.RGBA{ink.R, ink.G, ink.B, uint8(clampFloat(item.alpha*135, 0, 255))})
					break
				}
			}
		}
	}
	// The shoreline itself: a firm pen line right at the water's edge.
	for y := 1; y < height-1; y++ {
		row := landMask[y*width : y*width+width]
		above := landMask[(y-1)*width : (y-1)*width+width]
		below := landMask[(y+1)*width : (y+1)*width+width]
		for x := 1; x < width-1; x++ {
			if !row[x] {
				continue
			}
			if !row[x-1] || !row[x+1] || !above[x] || !below[x] {
				// Kept light: on a coastal subject the political outline traces
				// almost the same edge, and two firm lines together read as a rope.
				blendPixel(canvas, x, y, color.RGBA{palette.Ink.R, palette.Ink.G, palette.Ink.B, 132})
			}
		}
	}
}

// entityLabelAnchor picks a point guaranteed to lie inside the entity, in source
// coordinates. An area centroid is not good enough: for a crescent or a
// multi-part territory it can land in a neighbour, which would put a label — and
// a pointer hit test — on the wrong realm.
//
// The anchor is the midpoint of the widest span near the entity's vertical
// middle, which is both inside by construction and in the part of the shape with
// room for lettering.
func entityLabelAnchor(runs []MapRun) (float64, float64, bool) {
	if len(runs) == 0 {
		return 0, 0, false
	}
	var weight, sumY float64
	for _, run := range runs {
		length := float64(run.X1-run.X0) + 1
		weight += length
		sumY += float64(run.Y) * length
	}
	if weight == 0 {
		return 0, 0, false
	}
	midY := sumY / weight
	spread := 0.0
	for _, run := range runs {
		if d := math.Abs(float64(run.Y) - midY); d > spread {
			spread = d
		}
	}
	if spread < 1 {
		spread = 1
	}
	best, bestScore := runs[0], -1.0
	for _, run := range runs {
		length := float64(run.X1-run.X0) + 1
		// Long spans win, but spans far from the vertical middle are penalised so
		// the anchor does not drift into a thin northern or southern tail.
		nearness := 1 - 0.6*math.Abs(float64(run.Y)-midY)/spread
		if score := length * nearness; score > bestScore {
			best, bestScore = run, score
		}
	}
	return (float64(best.X0) + float64(best.X1) + 1) / 2, float64(best.Y) + 0.5, true
}

// buildMapRenderOverlay describes every entity of the plate's primary level in
// output-pixel coordinates. This is what makes the basemap workflow work: the
// image carries the cartography, and this table carries the geometry a caller
// needs to lay HTML on top of it.
func (db *DB) buildMapRenderOverlay(ctx context.Context, scratch *mapRenderScratch,
	metric MapMetricResult, tints map[string]color.RGBA, v renderViewport,
	width, height int) (*MapRenderOverlay, error) {
	if metric.Level == "" || len(metric.Values) == 0 {
		return nil, nil
	}
	meta, err := scratch.provinceMeta(ctx, db)
	if err != nil {
		return nil, err
	}
	_, groups, err := db.mapMetricEntities(ctx, metric.Target, metric.Level)
	if err != nil {
		return nil, err
	}
	overlay := &MapRenderOverlay{
		Level:      metric.Level,
		CoordSpace: "output_pixels",
		Entities:   make([]MapOverlayEntity, 0, len(metric.Values)),
	}
	for _, item := range metric.Values {
		pids := groups[item.ID]
		if len(pids) == 0 {
			continue
		}
		entity := MapOverlayEntity{
			ID:        item.ID,
			Provinces: len(pids),
			MinX:      math.MaxInt, MinY: math.MaxInt, MaxX: -1, MaxY: -1,
		}
		var shape []MapRun
		for _, pid := range pids {
			info, ok := meta[pid]
			if !ok || info.Area <= 0 {
				continue
			}
			// Both corners of the province's source bounding box, projected.
			x0, y0 := sourceToRender(v, float64(info.MinX), float64(info.MinY))
			x1, y1 := sourceToRender(v, float64(info.MaxX+1), float64(info.MaxY+1))
			entity.MinX, entity.MinY = minInt(entity.MinX, x0), minInt(entity.MinY, y0)
			entity.MaxX, entity.MaxY = maxInt(entity.MaxX, x1), maxInt(entity.MaxY, y1)
			runs, err := db.mapProvinceRuns(ctx, scratch, pid, false)
			if err != nil {
				return nil, err
			}
			shape = append(shape, runs...)
		}
		if entity.MaxX < entity.MinX || entity.MaxY < entity.MinY {
			continue
		}
		// Clip to the plate: an entity may straddle the viewport edge.
		entity.MinX, entity.MinY = maxInt(0, entity.MinX), maxInt(0, entity.MinY)
		entity.MaxX, entity.MaxY = minInt(width, entity.MaxX), minInt(height, entity.MaxY)
		if ax, ay, ok := entityLabelAnchor(shape); ok {
			cx, cy := sourceToRender(v, ax, ay)
			entity.CenterX, entity.CenterY = float64(cx), float64(cy)
		} else {
			entity.CenterX = float64(entity.MinX+entity.MaxX) / 2
			entity.CenterY = float64(entity.MinY+entity.MaxY) / 2
		}
		localized := db.mapRenderLocalizedLabel(ctx, item.ID)
		entity.LabelZH, entity.LabelEN = localized.Chinese, localized.English
		if tint, ok := tints[item.ID]; ok {
			entity.Tint = rgbaHex(tint)
		}
		overlay.Entities = append(overlay.Entities, entity)
	}
	sort.Slice(overlay.Entities, func(i, j int) bool { return overlay.Entities[i].ID < overlay.Entities[j].ID })
	return overlay, nil
}

// drawWashRuns tints the spans without hiding the paper underneath, which is what
// separates a watercolour wash from a flat fill.
func drawWashRuns(canvas *image.RGBA, v renderViewport, runs []MapRun, c color.RGBA, opacity float64) {
	alpha := uint8(clampFloat(opacity*255, 0, 255))
	tint := color.RGBA{c.R, c.G, c.B, alpha}
	for _, run := range runs {
		x0, x1, y0, y1, ok := clipRunToCanvas(canvas, v, run)
		if !ok {
			continue
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				blendPixel(canvas, x, y, tint)
			}
		}
	}
}

// renderParchmentRelief draws hachured relief and the river network in pen.
func (db *DB) renderParchmentRelief(ctx context.Context, scratch *mapRenderScratch, canvas *image.RGBA,
	v renderViewport, ground *parchmentGround, palette parchmentPalette, metrics styleMetrics,
	strength string) (int, []string, error) {
	if strength == "none" || ground == nil {
		return 0, nil, nil
	}
	warnings := []string{}
	count := 0
	sourceX := scratch.sourceColumns(v)
	sourceY := scratch.sourceRows(v)

	elevation, err := db.loadMapPhysicalRaster(ctx, "hillshade")
	if err != nil {
		return 0, nil, err
	}
	if elevation == nil {
		warnings = append(warnings, "heightmap cache unavailable; relief hachures omitted")
	} else {
		count += drawHachures(canvas, ground.Land, ground.LandBounds, v.Width, v.Height,
			elevation, sourceX, sourceY, palette, metrics, strength)
	}

	rivers, err := db.loadMapPhysicalRaster(ctx, "rivers")
	if err != nil {
		return count, warnings, err
	}
	if rivers == nil {
		warnings = append(warnings, "river cache unavailable; rivers omitted")
	} else {
		gray := rivers.Image
		rect := gray.Rect
		columnX := make([]int, v.Width)
		for x := 0; x < v.Width; x++ {
			sx := int(sourceX[x])
			if sx < 0 || sx >= rivers.Width || sx < rect.Min.X || sx >= rect.Max.X {
				sx = -1
			}
			columnX[x] = sx
		}
		ink := color.RGBA{palette.Water.R, palette.Water.G, palette.Water.B, 215}
		for y := 0; y < v.Height; y++ {
			sy := int(sourceY[y])
			if sy < 0 || sy >= rivers.Height || sy < rect.Min.Y || sy >= rect.Max.Y {
				continue
			}
			base := gray.PixOffset(rect.Min.X, sy)
			row := ground.Land[y*v.Width : y*v.Width+v.Width]
			for x := 0; x < v.Width; x++ {
				sx := columnX[x]
				if sx < 0 || !row[x] {
					continue
				}
				if gray.Pix[base+sx-rect.Min.X] > 0 {
					blendPixel(canvas, x, y, ink)
				}
			}
		}
		count++
	}
	return count, warnings, nil
}
