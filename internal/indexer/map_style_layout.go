package indexer

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
)

// Legend entries describe how a thing is drawn, not just what colour it is. The
// old legend painted a colour chip for everything, which mislabelled every line
// and every glyph on the plate.
const (
	LegendKindArea    = "area"    // a filled wash
	LegendKindLine    = "line"    // a drawn boundary, shown at its real weight
	LegendKindGlyph   = "glyph"   // a map symbol, shown as the symbol itself
	LegendKindHachure = "hachure" // relief shading
	LegendKindRamp    = "ramp"    // a sequential scale, shown as a gradient bar
	LegendKindTints   = "tints"   // a set of tints, shown as several chips in one row
)

// atlasLayout places the ornaments around the plate and remembers what it has
// used, so no two pieces of furniture can land on top of each other.
type atlasLayout struct {
	canvas   *image.RGBA
	palette  parchmentPalette
	metrics  styleMetrics
	text     *mapTextRenderer
	reserved []image.Rectangle
}

func newAtlasLayout(canvas *image.RGBA, palette parchmentPalette, metrics styleMetrics, text *mapTextRenderer) *atlasLayout {
	return &atlasLayout{canvas: canvas, palette: palette, metrics: metrics, text: text}
}

func (l *atlasLayout) reserve(r image.Rectangle) { l.reserved = append(l.reserved, r) }

// clearOf returns r pushed down until it no longer touches anything reserved.
// Ornaments are placed top-down, so vertical displacement is always available.
func (l *atlasLayout) clearOf(r image.Rectangle) image.Rectangle {
	gap := l.metrics.px(6)
	for moved := true; moved; {
		moved = false
		for _, taken := range l.reserved {
			if r.Overlaps(taken) {
				shift := taken.Max.Y + gap - r.Min.Y
				r = r.Add(image.Pt(0, shift))
				moved = true
			}
		}
	}
	return r
}

// drawFrame rules the sheet: a heavy outer line, a hairline inside it, and a
// small flourish at each corner.
func (l *atlasLayout) drawFrame() {
	b := l.canvas.Bounds()
	m := l.metrics
	outer := m.px(9)
	inner := m.px(15)
	l.strokeRect(image.Rect(outer, outer, b.Max.X-outer, b.Max.Y-outer), m.pxf(1.7), l.palette.Ink)
	l.strokeRect(image.Rect(inner, inner, b.Max.X-inner, b.Max.Y-inner), m.pxf(0.8), l.palette.InkSoft)

	arm := m.pxf(13)
	for _, corner := range [][3]int{
		{inner, inner, 1}, {b.Max.X - inner, inner, -1},
	} {
		x, y, dir := float64(corner[0]), float64(corner[1]), float64(corner[2])
		// A quarter-round flourish tucked inside each corner.
		for _, scale := range []float64{1, 0.55} {
			r := arm * scale
			steps := int(math.Max(8, r))
			previous := image.Pt(int(x+dir*r), int(y))
			for i := 1; i <= steps; i++ {
				angle := (math.Pi / 2) * float64(i) / float64(steps)
				point := image.Pt(int(x+dir*r*math.Cos(angle)), int(y+r*math.Sin(angle)))
				drawInkStroke(l.canvas, float64(previous.X), float64(previous.Y),
					float64(point.X), float64(point.Y), m.pxf(0.8), l.palette.InkSoft)
				previous = point
			}
		}
	}
	l.reserve(image.Rect(0, 0, b.Max.X, inner+m.px(2)))
}

func (l *atlasLayout) strokeRect(r image.Rectangle, width float64, c color.RGBA) {
	drawInkStroke(l.canvas, float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Min.Y), width, c)
	drawInkStroke(l.canvas, float64(r.Min.X), float64(r.Max.Y), float64(r.Max.X), float64(r.Max.Y), width, c)
	drawInkStroke(l.canvas, float64(r.Min.X), float64(r.Min.Y), float64(r.Min.X), float64(r.Max.Y), width, c)
	drawInkStroke(l.canvas, float64(r.Max.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y), width, c)
}

// panel lays a translucent paper tablet behind furniture so lettering stays
// readable over whatever the map put there, then rules it like the frame.
func (l *atlasLayout) panel(r image.Rectangle) {
	wash := l.palette.PaperLight
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			blendPixel(l.canvas, x, y, color.RGBA{wash.R, wash.G, wash.B, 226})
		}
	}
	l.strokeRect(r, l.metrics.pxf(1.1), l.palette.Ink)
	inset := l.metrics.px(3)
	l.strokeRect(image.Rect(r.Min.X+inset, r.Min.Y+inset, r.Max.X-inset, r.Max.Y-inset), l.metrics.pxf(0.6), l.palette.InkPale)
}

// drawCartouche writes the title into a ruled tablet at the top of the plate.
// The tablet is measured from the text rather than a fixed height, which is what
// used to clip the subtitle in half.
func (l *atlasLayout) drawCartouche(title, subtitle string, year int) {
	m := l.metrics
	b := l.canvas.Bounds()
	title = strings.TrimSpace(title)
	if title == "" {
		title = "法理地图"
	}
	subtitle = strings.TrimSpace(subtitle)
	if year > 0 {
		stamp := fmt.Sprintf("ANNO %d", year)
		if subtitle == "" {
			subtitle = stamp
		} else {
			subtitle = subtitle + "  ·  " + stamp
		}
	}
	titleSize := m.px(21)
	subSize := m.px(9)
	titleH := l.text.HeightSize(titleSize)
	subH := 0
	if subtitle != "" {
		subH = l.text.HeightSize(subSize)
	}
	padX, padY := m.px(22), m.px(9)
	gap := m.px(2)
	contentW := maxInt(l.text.WidthSize(title, titleSize), l.text.WidthSize(subtitle, subSize))
	contentH := titleH + subH + gap
	panelW := contentW + 2*padX
	panelH := contentH + 2*padY
	box := image.Rect((b.Dx()-panelW)/2, m.px(20), (b.Dx()-panelW)/2+panelW, m.px(20)+panelH)
	box = l.clearOf(box)
	l.panel(box)

	y := box.Min.Y + padY
	l.text.DrawSize(l.canvas, box.Min.X+(panelW-l.text.WidthSize(title, titleSize))/2, y, title, l.palette.Ink, titleSize)
	if subtitle != "" {
		y += titleH + gap
		l.text.DrawSize(l.canvas, box.Min.X+(panelW-l.text.WidthSize(subtitle, subSize))/2, y, subtitle, l.palette.InkSoft, subSize)
	}
	l.reserve(box)
}

// drawCompass draws an eight-point rose. It reserves its own box before the
// legend is placed, which is why the two can no longer overlap.
func (l *atlasLayout) drawCompass() {
	m := l.metrics
	b := l.canvas.Bounds()
	radius := m.pxf(21)
	box := image.Rect(b.Max.X-m.px(30)-int(radius*2), m.px(22), b.Max.X-m.px(24), m.px(22)+int(radius*2.6))
	box = l.clearOf(box)
	cx := float64(box.Min.X+box.Max.X) / 2
	cy := float64(box.Min.Y) + radius + m.pxf(3)

	for i := 0; i < 8; i++ {
		angle := float64(i) * math.Pi / 4
		long := radius
		width := m.pxf(1.5)
		ink := l.palette.Ink
		if i%2 == 1 {
			long = radius * 0.52
			width = m.pxf(0.9)
			ink = l.palette.InkSoft
		}
		tipX, tipY := cx+long*math.Sin(angle), cy-long*math.Cos(angle)
		// Each point is a narrow triangle: two strokes from the hub to the tip.
		side := radius * 0.13
		leftX, leftY := cx+side*math.Sin(angle+math.Pi/2), cy-side*math.Cos(angle+math.Pi/2)
		rightX, rightY := cx+side*math.Sin(angle-math.Pi/2), cy-side*math.Cos(angle-math.Pi/2)
		drawInkStroke(l.canvas, leftX, leftY, tipX, tipY, width, ink)
		drawInkStroke(l.canvas, rightX, rightY, tipX, tipY, width, ink)
	}
	// Solid north point so the rose reads at a glance.
	northTipY := cy - radius
	for t := 0.0; t <= 1; t += 0.04 {
		halfWidth := radius * 0.13 * (1 - t)
		y := cy + (northTipY-cy)*t
		drawInkStroke(l.canvas, cx-halfWidth, y, cx+halfWidth, y, m.pxf(0.9), l.palette.Ink)
	}
	stampInkDot(l.canvas, cx, cy, m.pxf(2.4), l.palette.Ink)
	size := m.px(9)
	l.text.DrawSize(l.canvas, int(cx)-l.text.WidthSize("N", size)/2, int(cy+radius+m.pxf(1)), "N", l.palette.Ink, size)
	l.reserve(box)
}

// drawLegend renders each entry in its own visual language: washes as chips,
// boundaries as line samples at their true weight, symbols as symbols, relief as
// a hachure patch and numeric scales as a gradient bar.
func (l *atlasLayout) drawLegend(items []MapLegendItem) {
	if len(items) == 0 {
		return
	}
	m := l.metrics
	b := l.canvas.Bounds()
	labelSize := m.px(9)
	rowH := maxInt(l.text.HeightSize(labelSize)+m.px(5), m.px(15))
	headingSize := m.px(10)
	sampleW := m.px(26)
	padding := m.px(11)

	widest := l.text.WidthSize("图例 / LEGEND", headingSize)
	for _, item := range items {
		if w := l.text.WidthSize(item.Label, labelSize); w > widest {
			widest = w
		}
	}
	panelW := sampleW + m.px(8) + widest + 2*padding
	panelH := 2*padding + l.text.HeightSize(headingSize) + m.px(4) + len(items)*rowH
	box := image.Rect(b.Max.X-m.px(24)-panelW, m.px(22), b.Max.X-m.px(24), m.px(22)+panelH)
	box = l.clearOf(box)
	l.panel(box)

	x := box.Min.X + padding
	y := box.Min.Y + padding
	l.text.DrawSize(l.canvas, x, y, "图例 / LEGEND", l.palette.Ink, headingSize)
	y += l.text.HeightSize(headingSize) + m.px(4)
	drawInkStroke(l.canvas, float64(x), float64(y-m.px(2)), float64(box.Max.X-padding), float64(y-m.px(2)), m.pxf(0.6), l.palette.InkPale)

	for _, item := range items {
		sample := image.Rect(x, y+m.px(2), x+sampleW, y+rowH-m.px(3))
		l.drawLegendSample(item, sample)
		l.text.DrawSize(l.canvas, x+sampleW+m.px(8), y+(rowH-l.text.HeightSize(labelSize))/2-m.px(1),
			item.Label, l.palette.Ink, labelSize)
		y += rowH
	}
	l.reserve(box)
}

func (l *atlasLayout) drawLegendSample(item MapLegendItem, r image.Rectangle) {
	m := l.metrics
	c := parseRenderColor(item.Color, l.palette.InkSoft)
	midY := float64(r.Min.Y+r.Max.Y) / 2
	switch item.Kind {
	case LegendKindLine:
		width := m.pxf(math.Max(1, float64(item.Weight)))
		drawInkStroke(l.canvas, float64(r.Min.X), midY, float64(r.Max.X), midY, width, c)
	case LegendKindGlyph:
		drawLegendGlyph(l.canvas, item.Glyph, (r.Min.X+r.Max.X)/2, int(midY), m.px(9), l.palette)
	case LegendKindHachure:
		// A patch of the same strokes the relief pass uses.
		for i := 0; i < 6; i++ {
			gx := float64(r.Min.X) + float64(i)*float64(r.Dx()-int(m.pxf(4)))/6 + m.pxf(1)
			drawInkStroke(l.canvas, gx, float64(r.Max.Y)-m.pxf(1), gx+m.pxf(3.2), float64(r.Min.Y)+m.pxf(1), m.pxf(0.8), l.palette.InkSoft)
		}
	case LegendKindTints:
		// One chip per pot, side by side, because a political plate tints by
		// title and no single colour can stand for it.
		const chips = 4
		for i := 0; i < chips; i++ {
			cell := image.Rect(r.Min.X+i*r.Dx()/chips, r.Min.Y, r.Min.X+(i+1)*r.Dx()/chips, r.Max.Y)
			for y := cell.Min.Y; y < cell.Max.Y; y++ {
				for x := cell.Min.X; x < cell.Max.X; x++ {
					blendPixel(l.canvas, x, y, parchmentCategoryWashes[i*5%len(parchmentCategoryWashes)])
				}
			}
		}
		l.strokeRect(r, m.pxf(0.6), l.palette.InkSoft)
	case LegendKindRamp:
		for x := r.Min.X; x < r.Max.X; x++ {
			t := float64(x-r.Min.X) / math.Max(1, float64(r.Dx()-1))
			shade := mixRGBA(l.palette.PaperLight, c, 0.15+0.85*t)
			for y := r.Min.Y; y < r.Max.Y; y++ {
				blendPixel(l.canvas, x, y, shade)
			}
		}
		l.strokeRect(r, m.pxf(0.6), l.palette.InkSoft)
	default: // LegendKindArea
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				blendPixel(l.canvas, x, y, c)
			}
		}
		l.strokeRect(r, m.pxf(0.6), l.palette.InkSoft)
	}
}

// drawScaleBar draws an alternating black-and-white chequer bar with real
// numbers on it. sourcePerRenderPixel converts the bar's drawn length back into
// source-map pixels, the only distance unit this index actually knows.
func (l *atlasLayout) drawScaleBar(sourcePerRenderPixel float64) {
	if sourcePerRenderPixel <= 0 || math.IsInf(sourcePerRenderPixel, 0) {
		return
	}
	m := l.metrics
	b := l.canvas.Bounds()
	// Choose a round distance whose drawn length lands near the target width.
	target := m.pxf(210)
	raw := target * sourcePerRenderPixel
	magnitude := math.Pow(10, math.Floor(math.Log10(math.Max(1, raw))))
	var span float64
	for _, step := range []float64{1, 2, 2.5, 5, 10} {
		span = step * magnitude
		if span/sourcePerRenderPixel >= target*0.62 {
			break
		}
	}
	barLen := span / sourcePerRenderPixel
	if barLen < m.pxf(40) || barLen > float64(b.Dx()) {
		return
	}
	const segments = 4
	height := m.pxf(5)
	x0 := float64(m.px(30))
	labelSize := m.px(8)
	y0 := float64(b.Max.Y) - m.pxf(24) - float64(l.text.HeightSize(labelSize))

	for i := 0; i < segments; i++ {
		left := x0 + barLen*float64(i)/segments
		right := x0 + barLen*float64(i+1)/segments
		if i%2 == 0 {
			for y := y0; y < y0+height; y++ {
				drawInkStroke(l.canvas, left, y, right, y, m.pxf(0.9), l.palette.Ink)
			}
		}
	}
	l.strokeRect(image.Rect(int(x0), int(y0), int(x0+barLen), int(y0+height)), m.pxf(0.9), l.palette.Ink)

	for i := 0; i <= segments; i += segments / 2 {
		value := span * float64(i) / segments
		text := fmt.Sprintf("%.0f", value)
		tx := x0 + barLen*float64(i)/segments - float64(l.text.WidthSize(text, labelSize))/2
		l.text.DrawSize(l.canvas, int(tx), int(y0+height+m.pxf(2)), text, l.palette.Ink, labelSize)
	}
	unit := "源图像素 / MAP PX"
	l.text.DrawSize(l.canvas, int(x0+barLen+m.pxf(9)), int(y0+height/2-float64(l.text.HeightSize(labelSize))/2+m.pxf(1)),
		unit, l.palette.InkSoft, labelSize)
}

// buildParchmentLegend assembles the key. Thematic entries produced by the fill
// layers come first, then the structural marks the style always draws, each
// tagged with how it should be sampled.
func buildParchmentLegend(spec MapRenderSpec, thematic []MapLegendItem) []MapLegendItem {
	palette := newParchmentPalette()
	out := make([]MapLegendItem, 0, len(thematic)+8)

	levelNames := map[string][2]string{
		"barony":  {"男爵领", "BARONY"},
		"county":  {"伯爵领", "COUNTY"},
		"duchy":   {"公国", "DUCHY"},
		"kingdom": {"王国", "KINGDOM"},
		"empire":  {"帝国", "EMPIRE"},
	}
	// A political plate tints by title, so a single chip cannot represent it.
	// Show a few of the actual tint pots instead, then explain the boundaries.
	primary := atlasPrimaryLevel(spec)
	if spec.Theme == "political" && primary != "" {
		if name, ok := levelNames[primary]; ok {
			out = append(out, MapLegendItem{
				Label: name[0] + "分色 / " + name[1] + " TINTS",
				Kind:  LegendKindTints,
			})
		}
	}
	out = append(out, thematic...)

	// Boundary ranks, drawn as lines at the weights the plate actually uses.
	weights := map[string]int{"barony": 1, "county": 2, "duchy": 3, "kingdom": 4, "empire": 5}
	seen := map[string]bool{}
	for _, layer := range spec.Layers {
		if layer.Type != "borders" || layer.Source == "outer" {
			continue
		}
		level := strings.ToLower(layer.Level)
		if level == "" || seen[level] {
			continue
		}
		name, ok := levelNames[level]
		if !ok {
			continue
		}
		seen[level] = true
		out = append(out, MapLegendItem{
			Label:  name[0] + "界 / " + name[1] + " BOUNDARY",
			Color:  rgbaHex(palette.Ink),
			Kind:   LegendKindLine,
			Weight: weights[level],
		})
	}
	for _, layer := range spec.Layers {
		if layer.Type == "borders" && layer.Source == "outer" {
			out = append(out, MapLegendItem{Label: "所辖范围 / SUBJECT OUTLINE", Color: rgbaHex(palette.Ink), Kind: LegendKindLine, Weight: 4})
			break
		}
	}

	if spec.ReliefStrength != "none" {
		out = append(out, MapLegendItem{Label: "山地晕滴 / RELIEF HACHURES", Color: rgbaHex(palette.Ink), Kind: LegendKindHachure})
	}
	out = append(out, MapLegendItem{Label: "水系 / RIVERS & LAKES", Color: rgbaHex(palette.Water), Kind: LegendKindLine, Weight: 2})
	for _, layer := range spec.Layers {
		if layer.Type != "markers" {
			continue
		}
		switch layer.Source {
		case "vegetation":
			out = append(out, MapLegendItem{Label: "林木 / WOODLAND", Color: rgbaHex(palette.InkSoft), Kind: LegendKindGlyph, Glyph: "vegetation"})
		case "holdings":
			out = append(out, MapLegendItem{Label: "城堡与市镇 / HOLDINGS", Color: rgbaHex(palette.Ink), Kind: LegendKindGlyph, Glyph: "castle"})
		case "lakes":
			out = append(out, MapLegendItem{Label: "湖泊 / LAKES", Color: rgbaHex(palette.Water), Kind: LegendKindGlyph, Glyph: "lake"})
		case "strategic_portals":
			out = append(out, MapLegendItem{Label: "关隘门户 / PASSAGES", Color: rgbaHex(palette.Ink), Kind: LegendKindGlyph, Glyph: "portal"})
		}
	}
	return out
}

// drawProvenance stamps the evidence class in the bottom-right corner, the way a
// period map credits its survey.
func (l *atlasLayout) drawProvenance(provenance []string) {
	if len(provenance) == 0 {
		return
	}
	labels := map[string]string{
		"indexed": "索引事实 / INDEXED",
		"derived": "派生指标 / DERIVED",
		"model":   "模型推演 / MODELLED",
	}
	parts := make([]string, 0, len(provenance))
	for _, item := range provenance {
		if text, ok := labels[item]; ok {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return
	}
	m := l.metrics
	b := l.canvas.Bounds()
	size := m.px(8)
	text := strings.Join(parts, "  ·  ")
	l.text.DrawSize(l.canvas, b.Max.X-m.px(28)-l.text.WidthSize(text, size), b.Max.Y-m.px(24)-l.text.HeightSize(size),
		text, l.palette.InkSoft, size)
}
