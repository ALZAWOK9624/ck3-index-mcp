package indexer

import (
	"hash/fnv"
	"image"
	"image/color"
	"math"
	"sort"
)

// The map renderer draws one look: a medieval parchment atlas. Everything here
// serves that single aesthetic, so the palette, the paper substrate and the pen
// primitives live together rather than being spread across the layer passes.
//
// Two rules hold the style together and are worth stating once:
//
//  1. Political areas are light, low-chroma washes. Ink has to stay legible on
//     top of them, which is only possible if the fills never get dark or
//     saturated. Because lightness and chroma are pinned, hue is free, so
//     neighbouring titles can be pushed far apart in hue and still look like
//     they came from the same box of paints.
//  2. Everything that carries information is drawn in ink: coastlines, relief
//     hachures, borders, rivers, glyphs, lettering. Colour says "who owns
//     this"; ink says "what is here".
type parchmentPalette struct {
	Paper      color.RGBA // mid-tone of the sheet
	PaperLight color.RGBA // raised fibres
	PaperDark  color.RGBA // sunken fibres and mottling
	Foxing     color.RGBA // age spots
	Sea        color.RGBA // sea wash, cooler and greyer than the land
	SeaDeep    color.RGBA // outer sea, away from any coast
	Ink        color.RGBA // main pen
	InkSoft    color.RGBA // secondary pen: hachures, minor borders
	InkPale    color.RGBA // faintest pen: graticule, fine detail
	Water      color.RGBA // rivers and lake outlines
	Lake       color.RGBA // lake body fill
	Mountain   color.RGBA // impassable rock wash, still a wash not a block
}

func newParchmentPalette() parchmentPalette {
	return parchmentPalette{
		Paper:      color.RGBA{230, 214, 178, 255},
		PaperLight: color.RGBA{241, 229, 199, 255},
		PaperDark:  color.RGBA{205, 186, 148, 255},
		Foxing:     color.RGBA{168, 138, 96, 255},
		// The sea is cooler and paler than the land. Period plates rely on the
		// engraved shoreline to separate the two, but a small value and
		// temperature step keeps the distinction instant at a glance.
		Sea:      color.RGBA{214, 216, 207, 255},
		SeaDeep:  color.RGBA{195, 200, 196, 255},
		Ink:      color.RGBA{62, 47, 33, 255},
		InkSoft:  color.RGBA{104, 84, 60, 255},
		InkPale:  color.RGBA{141, 122, 95, 255},
		Water:    color.RGBA{86, 102, 106, 255},
		Lake:     color.RGBA{178, 186, 179, 255},
		Mountain: color.RGBA{213, 202, 176, 255},
	}
}

// parchmentWash converts a title's native colour into a tint that can sit under
// pen work. Hue survives, so a red kingdom still reads as red; lightness and
// chroma are forced into the watercolour range.
func parchmentWash(native color.RGBA) color.RGBA {
	lch := okLabToLCH(rgbaToOKLab(native))
	if lch.C < 0.012 {
		// Greys carry no usable hue. Spread them around the warm paper hues
		// instead of leaving identical neutral patches next to each other.
		lch.H = 74
	}
	lch.L = 0.868
	lch.C = 0.040
	return okLabToRGBA(okLCHToLab(lch), 255)
}

// spreadWashHues pushes adjacent titles apart in hue. With lightness and chroma
// pinned by parchmentWash the only free axis is hue, and it is a generous one:
// a 30 degree separation is clearly visible yet still reads as one palette.
func spreadWashHues(base map[string]color.RGBA, neighbors map[string]map[string]bool) map[string]color.RGBA {
	ids := make([]string, 0, len(base))
	hue := make(map[string]float64, len(base))
	anchor := make(map[string]float64, len(base))
	for id, c := range base {
		ids = append(ids, id)
		h := okLabToLCH(rgbaToOKLab(c)).H
		hue[id], anchor[id] = h, h
	}
	sort.Strings(ids)
	adjacent := make(map[string][]string, len(ids))
	for _, id := range ids {
		list := make([]string, 0, len(neighbors[id]))
		for other := range neighbors[id] {
			if _, ok := base[other]; ok {
				list = append(list, other)
			}
		}
		sort.Strings(list)
		adjacent[id] = list
	}
	const wanted = 42.0 // degrees of hue between neighbours
	const budget = 70.0 // how far a title may drift from its native hue
	for round := 0; round < 24; round++ {
		moved := false
		for _, id := range ids {
			for _, other := range adjacent[id] {
				if id >= other {
					continue
				}
				delta := hueDelta(hue[id], hue[other])
				gap := math.Abs(delta)
				if gap >= wanted {
					continue
				}
				push := (wanted - gap) / 2
				if push > 3 {
					push = 3
				}
				direction := 1.0
				if delta < 0 {
					direction = -1
				}
				// Opposing nudges, each clamped to its own native anchor so no
				// title wanders far enough to misrepresent its heraldic colour.
				hue[id] = anchor[id] + clampFloat(hueDelta(hue[id]+direction*push, anchor[id]), -budget, budget)
				hue[other] = anchor[other] + clampFloat(hueDelta(hue[other]-direction*push, anchor[other]), -budget, budget)
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	out := make(map[string]color.RGBA, len(base))
	for id, c := range base {
		lch := okLabToLCH(rgbaToOKLab(c))
		lch.H = hue[id]
		out[id] = okLabToRGBA(okLCHToLab(lch), c.A)
	}
	return out
}

// parchmentPen pulls any colour into the ink range: dark enough to read as pen
// work on a light wash, desaturated enough not to compete with the tints. Hue
// survives, so a title's heraldic colour still shows in its boundary.
func parchmentPen(c color.RGBA) color.RGBA {
	lch := okLabToLCH(rgbaToOKLab(c))
	lch.L = clampFloat(lch.L*0.42, 0.20, 0.34)
	lch.C = math.Min(lch.C, 0.035)
	alpha := c.A
	if alpha == 0 {
		alpha = 255
	}
	return okLabToRGBA(okLCHToLab(lch), alpha)
}

// parchmentRamp is the sequential scale for numeric themes: a single ink-brown
// wash deepening from bare paper, the way a period map shades by density rather
// than by hue.
func parchmentRamp(name string) []color.RGBA {
	switch name {
	case "verdigris":
		return []color.RGBA{{233, 226, 205, 255}, {206, 214, 198, 255}, {174, 198, 186, 255}, {140, 176, 170, 255}, {104, 148, 146, 255}, {72, 114, 116, 255}}
	case "vermilion":
		return []color.RGBA{{236, 226, 203, 255}, {224, 202, 175, 255}, {209, 172, 142, 255}, {190, 137, 110, 255}, {166, 102, 84, 255}, {132, 71, 60, 255}}
	default: // sepia
		return []color.RGBA{{237, 229, 206, 255}, {221, 206, 173, 255}, {200, 180, 141, 255}, {174, 150, 111, 255}, {143, 119, 84, 255}, {107, 86, 59, 255}}
	}
}

// parchmentCategoryWashes are the hand-tint pots: distinct hues, all pinned to
// the same lightness and chroma so no single category shouts over the others.
var parchmentCategoryWashes = buildCategoryWashes()

func buildCategoryWashes() []color.RGBA {
	// Twenty hues spread around the wheel, deliberately unevenly so adjacent
	// entries stay far apart even when a theme only uses the first few.
	hues := []float64{28, 196, 96, 328, 260, 60, 158, 300, 12, 224, 122, 348, 244, 78, 178, 316, 44, 210, 140, 282}
	out := make([]color.RGBA, 0, len(hues))
	for _, h := range hues {
		out = append(out, okLabToRGBA(okLCHToLab(mapOKLCH{L: 0.868, C: 0.040, H: h}), 255))
	}
	return out
}

// styleMetrics converts design sizes into device pixels. Every ornament is
// specified at a nominal 1600px-wide plate and scaled from there, so a small
// preview and an 8K plate carry the same visual weight.
type styleMetrics struct {
	scale float64
}

func newStyleMetrics(spec MapRenderSpec) styleMetrics {
	scale := spec.deviceScale
	if scale <= 0 {
		scale = mapRenderOutputUIScale(spec.Width) * float64(maxInt(1, spec.Supersample))
	}
	return styleMetrics{scale: scale}
}

// px scales a design measurement, never collapsing a visible feature to nothing.
func (m styleMetrics) px(design float64) int {
	value := int(math.Round(design * m.scale))
	if design > 0 && value < 1 {
		return 1
	}
	return value
}

func (m styleMetrics) pxf(design float64) float64 { return design * m.scale }

// paperNoise is a value-noise field in [0,1). It replaces the previous
// per-pixel material hashing and is the only noise source the style uses.
func paperNoise(seed uint32, x, y, scale int) float64 {
	return materialNoise(seed, x, y, scale)
}

// drawParchmentPaper lays down the sheet: a mottled warm ground, directional
// fibre grain, sparse foxing and a darker margin where a bound sheet would have
// aged most. It writes every pixel, so it runs before any other pass.
func drawParchmentPaper(canvas *image.RGBA, palette parchmentPalette, metrics styleMetrics, seed uint32) {
	b := canvas.Bounds()
	width, height := b.Dx(), b.Dy()
	if width <= 0 || height <= 0 {
		return
	}
	mottleScale := maxInt(24, metrics.px(150))
	fibreScale := maxInt(6, metrics.px(11))
	foxScale := maxInt(12, metrics.px(70))
	halfW, halfH := float64(width)/2, float64(height)/2
	for y := 0; y < height; y++ {
		// Vignette: distance from the centre, normalised, biased so the corners
		// darken faster than the edges.
		ny := (float64(y) - halfH) / halfH
		row := canvas.Pix[y*canvas.Stride:]
		for x := 0; x < width; x++ {
			nx := (float64(x) - halfW) / halfW
			radial := math.Sqrt(nx*nx+ny*ny) / math.Sqrt2
			vignette := clampFloat((radial-0.55)/0.45, 0, 1)

			mottle := paperNoise(seed, x, y, mottleScale) - 0.5
			// Fibres run mostly along the sheet, so the grain is sampled with a
			// stretched x so streaks read as horizontal laid lines.
			fibre := paperNoise(seed^0x5bf03635, x/3, y, fibreScale) - 0.5
			speck := materialHash(seed^0x27d4eb2f, x, y) - 0.5

			tone := 0.62*mottle + 0.30*fibre + 0.12*speck
			base := palette.Paper
			if tone >= 0 {
				base = mixRGBA(palette.Paper, palette.PaperLight, tone*1.9)
			} else {
				base = mixRGBA(palette.Paper, palette.PaperDark, -tone*1.9)
			}
			if vignette > 0 {
				base = mixRGBA(base, palette.PaperDark, vignette*0.55)
			}
			// Foxing: rare, soft-edged brown blooms.
			fox := paperNoise(seed^0x1b56c4e9, x, y, foxScale)
			if fox > 0.90 {
				base = mixRGBA(base, palette.Foxing, (fox-0.90)/0.10*0.11)
			}
			i := x * 4
			row[i], row[i+1], row[i+2], row[i+3] = base.R, base.G, base.B, 255
		}
	}
}

// mixRGBA blends b into a by t in [0,1], ignoring alpha.
func mixRGBA(a, b color.RGBA, t float64) color.RGBA {
	t = clampFloat(t, 0, 1)
	return color.RGBA{
		R: uint8(math.Round(float64(a.R) + (float64(b.R)-float64(a.R))*t)),
		G: uint8(math.Round(float64(a.G) + (float64(b.G)-float64(a.G))*t)),
		B: uint8(math.Round(float64(a.B) + (float64(b.B)-float64(a.B))*t)),
		A: 255,
	}
}

// distanceFromMask returns, for every pixel, the approximate distance in pixels
// to the nearest set pixel of the mask. It is a two-pass 3-4 chamfer, which is
// within a few percent of true Euclidean distance and costs two linear scans.
// Distances are capped so the outer sea does not have to be measured.
func distanceFromMask(mask []bool, width, height, limit int) []int32 {
	const near, diag = 3, 4
	cap32 := int32(limit * near)
	out := make([]int32, len(mask))
	for i, set := range mask {
		if set {
			out[i] = 0
		} else {
			out[i] = cap32
		}
	}
	at := func(x, y int) int32 {
		if x < 0 || y < 0 || x >= width || y >= height {
			return cap32
		}
		return out[y*width+x]
	}
	relax := func(x, y int, candidates ...int32) {
		i := y*width + x
		best := out[i]
		for _, candidate := range candidates {
			if candidate < best {
				best = candidate
			}
		}
		if best > cap32 {
			best = cap32
		}
		out[i] = best
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			relax(x, y, at(x-1, y)+near, at(x, y-1)+near, at(x-1, y-1)+diag, at(x+1, y-1)+diag)
		}
	}
	for y := height - 1; y >= 0; y-- {
		for x := width - 1; x >= 0; x-- {
			relax(x, y, at(x+1, y)+near, at(x, y+1)+near, at(x+1, y+1)+diag, at(x-1, y+1)+diag)
		}
	}
	for i := range out {
		out[i] /= near
	}
	return out
}

// drawEngravedCoast is the signature mark of a period atlas: a set of lines
// echoing every coast, tight against the shore and loosening outward, fading as
// they go. The bands come from a distance field so they follow the coast exactly
// and never self-intersect the way an offset-per-polygon approach would.
func drawEngravedCoast(canvas *image.RGBA, landMask []bool, bounds *maskBounds, width, height int, palette parchmentPalette, metrics styleMetrics) {
	if bounds == nil || bounds.empty() {
		return
	}
	step := metrics.pxf(3.2)
	if step < 1.6 {
		step = 1.6
	}
	const bandCount = 5
	limit := int(step*bandCount) + 3
	distance := distanceFromMask(landMask, width, height, limit)

	// Band k sits at k*step from the shore with a widening gap, so the rhythm
	// opens up the way an engraver's hand would.
	type band struct {
		lo, hi float64
		alpha  float64
	}
	bands := make([]band, 0, bandCount)
	position := step
	for k := 0; k < bandCount; k++ {
		thickness := metrics.pxf(1)
		if thickness < 1 {
			thickness = 1
		}
		bands = append(bands, band{lo: position, hi: position + thickness, alpha: 1 - float64(k)/bandCount})
		position += step * (1 + 0.42*float64(k))
	}
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
					alpha := uint8(clampFloat(item.alpha*150, 0, 255))
					blendPixel(canvas, x, y, color.RGBA{palette.InkSoft.R, palette.InkSoft.G, palette.InkSoft.B, alpha})
					break
				}
			}
		}
	}
}

// drawSeaWash tints open water and deepens it away from land, so the sheet
// still reads as a sea without resorting to a saturated blue.
func drawSeaWash(canvas *image.RGBA, landMask []bool, width, height int, palette parchmentPalette, metrics styleMetrics) {
	falloff := metrics.pxf(90)
	if falloff < 12 {
		falloff = 12
	}
	distance := distanceFromMask(landMask, width, height, int(falloff)+2)
	for y := 0; y < height; y++ {
		rowLand := landMask[y*width : y*width+width]
		rowDist := distance[y*width : y*width+width]
		for x := 0; x < width; x++ {
			if rowLand[x] {
				continue
			}
			t := clampFloat(float64(rowDist[x])/falloff, 0, 1)
			wash := mixRGBA(palette.Sea, palette.SeaDeep, t)
			i := y*canvas.Stride + x*4
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2] = wash.R, wash.G, wash.B
		}
	}
}

// drawHachures renders relief the way an engraver would: short strokes running
// downslope, denser and longer where the ground is steeper. Unlike a grey
// hillshade wash this leaves the political tint underneath fully visible, which
// is the whole point of a political atlas.
func drawHachures(canvas *image.RGBA, landMask []bool, bounds *maskBounds, width, height int,
	relief *mapPhysicalRaster, sourceX, sourceY []float64, palette parchmentPalette, metrics styleMetrics, strength string) int {
	if relief == nil || bounds == nil || bounds.empty() {
		return 0
	}
	gain := 1.0
	switch strength {
	case "none":
		return 0
	case "strong":
		gain = 1.55
	}
	// Hachures mark mountains, not every undulation. The threshold is high on
	// purpose: strokes scattered across gently rolling ground read as dirt on the
	// sheet rather than as relief.
	spacing := maxInt(5, metrics.px(8))
	strokeLen := metrics.pxf(5.0)
	const slopeFloor = 0.115
	// Sampling the relief a few source pixels apart gives the slope direction.
	probe := 3.4
	drawn := 0
	for y := maxInt(0, bounds.MinY); y <= bounds.MaxY; y++ {
		lo, hi := bounds.rowSpan(y, 0, width)
		row := landMask[y*width : y*width+width]
		sy := sourceY[y]
		for x := lo; x <= hi; x++ {
			if !row[x] {
				continue
			}
			// A jittered lattice keeps the strokes from lining up into a grid.
			if (x+y*2)%spacing != 0 {
				continue
			}
			if materialHash(0x48414348, x/spacing, y/spacing) < 0.42 {
				continue
			}
			sx := sourceX[x]
			east := samplePhysicalRaster(relief, sx+probe, sy)
			west := samplePhysicalRaster(relief, sx-probe, sy)
			south := samplePhysicalRaster(relief, sx, sy+probe)
			north := samplePhysicalRaster(relief, sx, sy-probe)
			gx, gy := east-west, south-north
			slope := math.Hypot(gx, gy) * gain
			if slope < slopeFloor {
				continue
			}
			// Strokes follow the fall line, which is the gradient direction, and
			// lengthen with steepness so ridges gather into visible ranges.
			length := strokeLen * clampFloat((slope-slopeFloor)*6+0.6, 0.6, 2.0)
			inv := 1 / math.Max(1e-6, math.Hypot(gx, gy))
			dx, dy := gx*inv*length, gy*inv*length
			alpha := uint8(clampFloat((slope-slopeFloor)*230+26, 22, 96))
			ink := color.RGBA{palette.InkSoft.R, palette.InkSoft.G, palette.InkSoft.B, alpha}
			drawInkStroke(canvas, float64(x)-dx/2, float64(y)-dy/2, float64(x)+dx/2, float64(y)+dy/2, metrics.pxf(0.75), ink)
			drawn++
		}
	}
	return drawn
}

// drawInkStroke draws an antialiased line of the given width. Pen work needs
// smooth edges: the old renderer stamped hard discs, which is what made the
// borders look like blurred toothpaste rather than drawn lines.
func drawInkStroke(canvas *image.RGBA, x0, y0, x1, y1, width float64, c color.RGBA) {
	if width < 0.6 {
		width = 0.6
	}
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	if length < 1e-9 {
		stampInkDot(canvas, x0, y0, width/2, c)
		return
	}
	steps := int(math.Ceil(length))
	radius := width / 2
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		stampInkDot(canvas, x0+dx*t, y0+dy*t, radius, c)
	}
}

// stampInkDot lays a soft round nib at a subpixel position.
func stampInkDot(canvas *image.RGBA, cx, cy, radius float64, c color.RGBA) {
	if radius < 0.35 {
		radius = 0.35
	}
	minX, maxX := int(math.Floor(cx-radius-1)), int(math.Ceil(cx+radius+1))
	minY, maxY := int(math.Floor(cy-radius-1)), int(math.Ceil(cy+radius+1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			coverage := clampFloat(radius+0.5-d, 0, 1)
			if coverage <= 0 {
				continue
			}
			alpha := uint8(clampFloat(float64(c.A)*coverage, 0, 255))
			if alpha == 0 {
				continue
			}
			blendPixel(canvas, x, y, color.RGBA{c.R, c.G, c.B, alpha})
		}
	}
}

// drawPigmentWash lays a political tint the way a brush would: unevenly. A flat
// blend of one colour reads as a vector fill no matter how well the colour is
// chosen, so the wash is modulated three ways.
//
//   - Low-frequency blotching, seeded per entity, so each title's tint pools and
//     thins across its own area instead of being uniform.
//   - Fine granulation, so pigment settles into the paper's tooth and the fibre
//     grain underneath stays visible.
//   - Edge pooling along the province perimeter, which is the most recognisable
//     watercolour tell: the wash darkens where it dries against a boundary.
//
// boundary may be nil, in which case the pooling pass is skipped.
func drawPigmentWash(canvas *image.RGBA, v renderViewport, runs, boundary []MapRun,
	c color.RGBA, seed uint32, metrics styleMetrics, opacity float64) {
	if len(runs) == 0 {
		return
	}
	blotchScale := maxInt(14, metrics.px(46))
	streakScale := maxInt(6, metrics.px(15))
	for _, run := range runs {
		x0, x1, y0, y1, ok := clipRunToCanvas(canvas, v, run)
		if !ok {
			continue
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				blotch := paperNoise(seed, x, y, blotchScale) - 0.5
				// The streak field is sampled with a stretched x so the variation
				// runs along the sheet like a brush stroke rather than as blobs.
				streak := paperNoise(seed^0x9e3779b9, x/2, y, streakScale) - 0.5
				grain := materialHash(seed^0x85ebca6b, x, y) - 0.5
				load := opacity * (1 + 0.32*blotch + 0.18*streak + 0.09*grain)
				alpha := uint8(clampFloat(load*255, 0, 255))
				if alpha == 0 {
					continue
				}
				blendPixel(canvas, x, y, color.RGBA{c.R, c.G, c.B, alpha})
			}
		}
	}
	if len(boundary) == 0 {
		return
	}
	// Pooling: a faint extra pass of the same pigment just inside the perimeter.
	// Kept deliberately weak — pushed any harder it stops reading as pigment
	// settling and starts reading as a coloured outline competing with the ink.
	radius := metrics.pxf(0.9)
	rim := uint8(clampFloat(opacity*50, 0, 255))
	for _, run := range boundary {
		x0, x1, y0, y1, ok := clipRunToCanvas(canvas, v, run)
		if !ok {
			continue
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				stampInkDot(canvas, float64(x), float64(y), radius, color.RGBA{c.R, c.G, c.B, rim})
			}
		}
	}
}

// entityWashSeed derives a stable per-entity noise seed, so a title's blotching
// is the same on every render but different from its neighbours'.
func entityWashSeed(id string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return h.Sum32() | 1
}

// fillMaskHoles adds every enclosed gap to the mask. The subject outline needs
// this because impassable mountains and lakes are not part of any title, so they
// punched holes in the political mask and the outline traced a heavy ring around
// each one instead of following the realm's actual edge.
//
// It floods the complement inward from the canvas border: whatever the flood
// cannot reach is enclosed, and therefore interior.
func fillMaskHoles(mask []int32, width, height int, fill int32) {
	if width <= 0 || height <= 0 {
		return
	}
	outside := make([]bool, len(mask))
	queue := make([]int32, 0, width*2)
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= width || y >= height {
			return
		}
		i := y*width + x
		if outside[i] || mask[i] != 0 {
			return
		}
		outside[i] = true
		queue = append(queue, int32(i))
	}
	for x := 0; x < width; x++ {
		push(x, 0)
		push(x, height-1)
	}
	for y := 0; y < height; y++ {
		push(0, y)
		push(width-1, y)
	}
	for len(queue) > 0 {
		i := int(queue[len(queue)-1])
		queue = queue[:len(queue)-1]
		x, y := i%width, i/width
		push(x-1, y)
		push(x+1, y)
		push(x, y-1)
		push(x, y+1)
	}
	for i := range mask {
		if mask[i] == 0 && !outside[i] {
			mask[i] = fill
		}
	}
}

// drawInkPolyline strokes a connected run of points as one pen movement.
func drawInkPolyline(canvas *image.RGBA, points []image.Point, width float64, c color.RGBA) {
	for i := 1; i < len(points); i++ {
		drawInkStroke(canvas, float64(points[i-1].X), float64(points[i-1].Y),
			float64(points[i].X), float64(points[i].Y), width, c)
	}
}
