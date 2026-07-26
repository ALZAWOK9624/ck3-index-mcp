package indexer

import (
	"image"
	"image/color"
	"math"
)

// Map symbols are drawn as pen work: an outline in ink over a thin wash, the way
// a settlement is drawn on a period plate. The previous glyphs were filled
// blocks of flat colour, which read as UI icons pasted onto the map.

// glyphInk returns the pen and wash for a symbol at the given emphasis.
func glyphInk(palette parchmentPalette, emphasis bool) (pen, wash color.RGBA) {
	pen = palette.Ink
	if !emphasis {
		pen = color.RGBA{palette.Ink.R, palette.Ink.G, palette.Ink.B, 225}
	}
	wash = color.RGBA{palette.PaperLight.R, palette.PaperLight.G, palette.PaperLight.B, 205}
	return pen, wash
}

// drawSettlementGlyph draws a holding. kind follows holdingSymbolKind.
func drawSettlementGlyph(canvas *image.RGBA, x, y, size int, kind string, capital bool, palette parchmentPalette) {
	s := float64(maxInt(4, size))
	pen, wash := glyphInk(palette, capital)
	stroke := math.Max(0.9, s/6)
	fx, fy := float64(x), float64(y)

	switch kind {
	case "castle", "settlement":
		// A single tower: narrow shaft, battlemented head, conical roof. Taller
		// than wide so it reads as a building rather than a box.
		shaftW, shaftH := s*0.52, s*0.95
		drawGlyphWash(canvas, image.Rect(int(fx-shaftW/2), int(fy-shaftH*0.45), int(fx+shaftW/2), int(fy+shaftH*0.55)), wash)
		drawInkStroke(canvas, fx-shaftW/2, fy+shaftH*0.55, fx-shaftW/2, fy-shaftH*0.35, stroke, pen)
		drawInkStroke(canvas, fx+shaftW/2, fy+shaftH*0.55, fx+shaftW/2, fy-shaftH*0.35, stroke, pen)
		drawInkStroke(canvas, fx-shaftW/2, fy+shaftH*0.55, fx+shaftW/2, fy+shaftH*0.55, stroke, pen)
		// Battlements: three merlons across the head.
		drawInkStroke(canvas, fx-shaftW*0.7, fy-shaftH*0.35, fx+shaftW*0.7, fy-shaftH*0.35, stroke, pen)
		for _, m := range []float64{-0.55, 0, 0.55} {
			mx := fx + m*shaftW
			drawInkStroke(canvas, mx, fy-shaftH*0.35, mx, fy-shaftH*0.52, stroke*0.8, pen)
		}
		if capital {
			// A pennant marks the county seat.
			drawInkStroke(canvas, fx, fy-shaftH*0.52, fx, fy-shaftH*0.95, stroke*0.8, pen)
			drawInkStroke(canvas, fx, fy-shaftH*0.95, fx+s*0.42, fy-shaftH*0.78, stroke*0.8, pen)
			drawInkStroke(canvas, fx+s*0.42, fy-shaftH*0.78, fx, fy-shaftH*0.62, stroke*0.8, pen)
		}
	case "city", "metropolis":
		// A ring is the conventional town mark and stays unambiguous at any size,
		// unlike a cluster of roofs which collapses into a glyph-like blob.
		radius := s * 0.52
		drawGlyphWash(canvas, image.Rect(int(fx-radius), int(fy-radius), int(fx+radius), int(fy+radius)), wash)
		drawInkCircle(canvas, fx, fy, radius, stroke, pen)
		if kind == "metropolis" {
			drawInkCircle(canvas, fx, fy, radius*0.55, stroke*0.85, pen)
		} else {
			stampInkDot(canvas, fx, fy, math.Max(0.8, s*0.13), pen)
		}
	case "church":
		// A chapel: gabled nave with a cross.
		baseY := fy + s*0.5
		drawGlyphWash(canvas, image.Rect(int(fx-s*0.5), int(fy-s*0.25), int(fx+s*0.5), int(baseY)), wash)
		drawInkRect(canvas, fx-s*0.5, fy-s*0.25, fx+s*0.5, baseY, stroke, pen)
		drawInkStroke(canvas, fx-s*0.5, fy-s*0.25, fx, fy-s*0.7, stroke, pen)
		drawInkStroke(canvas, fx+s*0.5, fy-s*0.25, fx, fy-s*0.7, stroke, pen)
		drawInkStroke(canvas, fx, fy-s*0.7, fx, fy-s*1.25, stroke*0.8, pen)
		drawInkStroke(canvas, fx-s*0.22, fy-s*1.05, fx+s*0.22, fy-s*1.05, stroke*0.8, pen)
	case "tribal", "nomad":
		// One tent, not a pair: two triangles side by side read as a letter.
		drawGlyphWash(canvas, image.Rect(int(fx-s*0.5), int(fy-s*0.5), int(fx+s*0.5), int(fy+s*0.5)), wash)
		drawInkStroke(canvas, fx-s*0.5, fy+s*0.5, fx, fy-s*0.6, stroke, pen)
		drawInkStroke(canvas, fx+s*0.5, fy+s*0.5, fx, fy-s*0.6, stroke, pen)
		drawInkStroke(canvas, fx-s*0.5, fy+s*0.5, fx+s*0.5, fy+s*0.5, stroke, pen)
		// Smoke hole, which also breaks the outline's symmetry.
		drawInkStroke(canvas, fx, fy-s*0.6, fx+s*0.22, fy-s*0.85, stroke*0.7, pen)
	case "ruins":
		// A broken enclosure: three sides of a square with the fourth fallen in.
		drawInkStroke(canvas, fx-s*0.5, fy+s*0.45, fx-s*0.5, fy-s*0.35, stroke, pen)
		drawInkStroke(canvas, fx-s*0.5, fy-s*0.35, fx+s*0.1, fy-s*0.35, stroke, pen)
		drawInkStroke(canvas, fx-s*0.5, fy+s*0.45, fx+s*0.5, fy+s*0.45, stroke, pen)
		drawInkStroke(canvas, fx+s*0.5, fy+s*0.45, fx+s*0.5, fy-s*0.05, stroke*0.85, pen)
	case "necropolis":
		// A barrow with a standing stone.
		steps := 14
		for i := 0; i <= steps; i++ {
			angle := math.Pi * float64(i) / float64(steps)
			px := fx - s*0.75*math.Cos(angle)
			py := fy + s*0.4 - s*0.55*math.Sin(angle)
			stampInkDot(canvas, px, py, stroke/2, pen)
		}
		drawInkStroke(canvas, fx, fy+s*0.4, fx, fy-s*0.75, stroke, pen)
	default:
		drawInkRect(canvas, fx-s*0.4, fy-s*0.4, fx+s*0.4, fy+s*0.4, stroke, pen)
	}
}

// drawTerrainGlyph draws vegetation and landform marks.
func drawTerrainGlyph(canvas *image.RGBA, x, y, size int, subtype string, rotation float64, palette parchmentPalette) {
	s := float64(maxInt(3, size))
	pen := color.RGBA{palette.InkSoft.R, palette.InkSoft.G, palette.InkSoft.B, 215}
	stroke := math.Max(0.8, s/7)
	fx, fy := float64(x), float64(y)

	switch subtype {
	case "conifer":
		// A small stand rather than one big tree: two or three firs of slightly
		// different height, so neighbouring marks never line up into a row of
		// identical triangles.
		count := 2 + int(materialHash(0x54524545, x, y)*2)
		for k := 0; k < count; k++ {
			jitter := materialHash(0x46495230+uint32(k), x, y)
			tx := fx + (float64(k)-float64(count-1)/2)*s*0.62
			ty := fy + (jitter-0.5)*s*0.5
			scale := 0.62 + 0.3*jitter
			drawInkStroke(canvas, tx, ty+s*0.55*scale, tx, ty+s*0.1*scale, stroke*0.9, pen)
			for i := 0; i <= 5; i++ {
				t := float64(i) / 5
				half := s * 0.3 * scale * (1 - t)
				cy := ty + s*0.18*scale - t*s*0.8*scale
				drawInkStroke(canvas, tx-half, cy, tx+half, cy, stroke*0.8, pen)
			}
		}
	case "palm":
		lean := math.Sin(rotation) * s * 0.3
		topX, topY := fx+lean, fy-s*0.9
		drawInkStroke(canvas, fx, fy+s*0.7, topX, topY, stroke, pen)
		for _, angle := range []float64{-2.7, -2.1, -1.55, -1.0, -0.4} {
			ex := topX + math.Cos(angle)*s*0.85
			ey := topY + math.Sin(angle)*s*0.5
			drawInkStroke(canvas, topX, topY, ex, ey, stroke*0.8, pen)
		}
	case "reeds":
		for i := -1; i <= 1; i++ {
			bx := fx + float64(i)*s*0.35
			drawInkStroke(canvas, bx, fy+s*0.7, bx+float64(i)*s*0.12, fy-s*0.8, stroke*0.8, pen)
		}
	case "scrub":
		for _, offset := range []float64{-0.55, 0, 0.55} {
			cx := fx + offset*s*0.7
			steps := 9
			for i := 0; i <= steps; i++ {
				angle := math.Pi * float64(i) / float64(steps)
				stampInkDot(canvas, cx-s*0.32*math.Cos(angle), fy+s*0.4-s*0.42*math.Sin(angle), stroke/2, pen)
			}
		}
	case "mountain":
		// The classic molehill: a shaded peak.
		drawInkStroke(canvas, fx-s*0.85, fy+s*0.55, fx, fy-s*0.85, stroke, pen)
		drawInkStroke(canvas, fx, fy-s*0.85, fx+s*0.85, fy+s*0.55, stroke, pen)
		for i := 1; i <= 3; i++ {
			t := float64(i) / 4
			drawInkStroke(canvas, fx+s*0.85*t*0.7, fy+s*0.55-s*0.3*(1-t), fx+s*0.85*t, fy+s*0.55, stroke*0.7, pen)
		}
	default: // broadleaf
		steps := 12
		for i := 0; i <= steps; i++ {
			angle := 2 * math.Pi * float64(i) / float64(steps)
			stampInkDot(canvas, fx+s*0.5*math.Cos(angle), fy-s*0.15+s*0.42*math.Sin(angle), stroke/2, pen)
		}
		drawInkStroke(canvas, fx, fy+s*0.7, fx, fy+s*0.1, stroke, pen)
	}
}

// drawLegendGlyph draws the sample used in the legend for a glyph entry.
func drawLegendGlyph(canvas *image.RGBA, glyph string, x, y, size int, palette parchmentPalette) {
	switch glyph {
	case "vegetation":
		drawTerrainGlyph(canvas, x, y, size, "conifer", 0, palette)
	case "mountain":
		drawTerrainGlyph(canvas, x, y, size, "mountain", 0, palette)
	case "lake":
		pen := color.RGBA{palette.Water.R, palette.Water.G, palette.Water.B, 230}
		steps := 16
		for i := 0; i <= steps; i++ {
			angle := 2 * math.Pi * float64(i) / float64(steps)
			stampInkDot(canvas, float64(x)+float64(size)*0.62*math.Cos(angle), float64(y)+float64(size)*0.4*math.Sin(angle), 0.7, pen)
		}
	case "portal":
		pen := palette.Ink
		drawInkStroke(canvas, float64(x-size/2), float64(y), float64(x+size/2), float64(y), 1.1, pen)
		drawInkStroke(canvas, float64(x), float64(y-size/2), float64(x), float64(y+size/2), 1.1, pen)
	default:
		drawSettlementGlyph(canvas, x, y, size, glyph, false, palette)
	}
}

// glyphBox is the space a symbol of the given size occupies, used to claim room
// against place names. Symbols are drawn from their centre and reach further up
// than down, because towers, spires and trees all grow upward.
func glyphBox(x, y, size int) image.Rectangle {
	s := maxInt(3, size)
	return image.Rect(x-s, y-s*3/2, x+s, y+s)
}

// drawBitmapInk is the lettering fallback when no font is configured. It blends
// rather than overwriting, so translucent ink never punches holes through the
// sheet, and it uses the same 5x7 glyph table as before.
func drawBitmapInk(canvas *image.RGBA, x, y int, text string, c color.RGBA) {
	cursor := x
	for _, r := range text {
		glyph, ok := tinyGlyphs[r]
		if !ok {
			glyph = tinyGlyphs[' ']
		}
		for gy, row := range glyph {
			for gx := 0; gx < 5; gx++ {
				if row&(1<<uint(4-gx)) != 0 {
					blendPixel(canvas, cursor+gx, y+gy, c)
				}
			}
		}
		cursor += 6
	}
}

// drawInkHatch fills the spans with diagonal pen hatching, used to flag areas
// that need attention without hiding what is underneath.
func drawInkHatch(canvas *image.RGBA, v renderViewport, runs []MapRun, c color.RGBA, spacing int) {
	if spacing <= 0 {
		spacing = 8
	}
	for _, run := range runs {
		x0, x1, y0, y1, ok := clipRunToCanvas(canvas, v, run)
		if !ok {
			continue
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if (x+y)%spacing == 0 {
					blendPixel(canvas, x, y, c)
				}
			}
		}
	}
}

// drawInkCircle strokes a circle in pen.
func drawInkCircle(canvas *image.RGBA, cx, cy, radius, width float64, c color.RGBA) {
	steps := maxInt(10, int(radius*6))
	previousX, previousY := cx+radius, cy
	for i := 1; i <= steps; i++ {
		angle := 2 * math.Pi * float64(i) / float64(steps)
		x, y := cx+radius*math.Cos(angle), cy+radius*math.Sin(angle)
		drawInkStroke(canvas, previousX, previousY, x, y, width, c)
		previousX, previousY = x, y
	}
}

// drawInkRect strokes a rectangle in pen.
func drawInkRect(canvas *image.RGBA, x0, y0, x1, y1, width float64, c color.RGBA) {
	drawInkStroke(canvas, x0, y0, x1, y0, width, c)
	drawInkStroke(canvas, x1, y0, x1, y1, width, c)
	drawInkStroke(canvas, x1, y1, x0, y1, width, c)
	drawInkStroke(canvas, x0, y1, x0, y0, width, c)
}

// drawGlyphWash lays the pale ground a glyph sits on so it stays readable over a
// political tint without hiding it.
func drawGlyphWash(canvas *image.RGBA, r image.Rectangle, wash color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			blendPixel(canvas, x, y, wash)
		}
	}
}
