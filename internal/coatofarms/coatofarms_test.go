package coatofarms

import (
	"image"
	"image/color"
	"testing"

	"ck3-index/internal/script"
)

func testPalette() Palette {
	palette := Palette{}
	parsed := script.Parse(`colors = {
	red = hsv { 0.02 0.8 0.45 }
	black = hsv { 0.1 0.25 0.10 }
	white = hsv { 0.08 0.02 0.8 }
	han = { 211 44 44 }
	english = { 0.8 0.2 0.2 }
	brown = hsv360 { 021 074 045 }
}`)
	palette.ParseNamedColors(parsed.Nodes)
	return palette
}

func TestNamedColorsReadAllThreeNotations(t *testing.T) {
	palette := testPalette()
	// { 211 44 44 } exceeds 1, so it is bytes; { 0.8 0.2 0.2 } is unit floats.
	if got := palette["han"]; got != (color.NRGBA{R: 211, G: 44, B: 44, A: 255}) {
		t.Fatalf("byte-valued list read as %+v", got)
	}
	if got := palette["english"]; got != (color.NRGBA{R: 204, G: 51, B: 51, A: 255}) {
		t.Fatalf("unit-float list read as %+v", got)
	}
	// hsv360 { 21 74 45 }: hue 21/360, sat 0.74, value 0.45.
	brown := palette["brown"]
	if brown.R <= brown.G || brown.G <= brown.B {
		t.Fatalf("hsv360 brown came out as %+v, want a red-dominant warm colour", brown)
	}
	if _, ok := palette["red"]; !ok {
		t.Fatal("hsv colour was not recorded")
	}
}

// The parser splits `color1 = rgb { 1 2 3 }` into an atom carrying the keyword
// and a following anonymous block carrying the numbers. Reading only the atom
// yields the literal string "rgb" as a colour name.
func TestParseReadsLiteralColorNotationsSplitAcrossSiblings(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "pattern_solid.dds"
	color1 = black
	color2 = rgb { 203 195 195 }
	color3 = hsv { 0.58 0.8 0.4 }
}`)
	defs := Parse(parsed.Nodes, testPalette())
	if len(defs) != 1 {
		t.Fatalf("parsed %d definitions, want 1", len(defs))
	}
	def := defs[0]
	if def.Colors[1].Notation != ColorNotationRGB {
		t.Fatalf("color2 notation is %q, want rgb", def.Colors[1].Notation)
	}
	if got := def.Colors[1].RGBA; got != (color.NRGBA{R: 203, G: 195, B: 195, A: 255}) {
		t.Fatalf("color2 resolved to %+v", got)
	}
	if def.Colors[2].Notation != ColorNotationHSV {
		t.Fatalf("color3 notation is %q, want hsv", def.Colors[2].Notation)
	}
	if def.Colors[0].Name != "black" {
		t.Fatalf("color1 name is %q, want black", def.Colors[0].Name)
	}
	if len(def.Warnings) != 0 {
		t.Fatalf("a well-formed definition produced warnings: %v", def.Warnings)
	}
}

func TestParseReadsEmblemsInstancesAndFallbackColors(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "pattern_solid.dds"
	color1 = black
	color2 = white
	color3 = red
	colored_emblem = {
		texture = "ce_a.dds"
		instance = { depth = 2.01 }
	}
	colored_emblem = {
		color1 = white
		texture = "ce_b.dds"
		instance = { position = { 0.51 0.49 } scale = { 0.75 0.75 } rotation = 45 }
		instance = { position = { 0.2 0.3 } }
	}
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	if len(def.Emblems) != 2 {
		t.Fatalf("read %d emblems, want 2", len(def.Emblems))
	}
	if len(def.Emblems[1].Instance) != 2 {
		t.Fatalf("second emblem has %d instances, want 2", len(def.Emblems[1].Instance))
	}
	second := def.Emblems[1].Instance[1]
	if second.Scale != [2]float64{1, 1} || second.Position != [2]float64{0.2, 0.3} {
		t.Fatalf("an instance without scale did not default to full size: %+v", second)
	}
	// The first emblem names no colours at all and inherits all three.
	inherited := def.EffectiveColors(def.Emblems[0])
	if inherited[0].Name != "black" || inherited[1].Name != "white" || inherited[2].Name != "red" {
		t.Fatalf("emblem colours did not fall back to the definition: %+v", inherited)
	}
	// The second names only color1 and inherits the other two.
	partial := def.EffectiveColors(def.Emblems[1])
	if partial[0].Name != "white" || partial[1].Name != "white" || partial[2].Name != "red" {
		t.Fatalf("partial emblem colours resolved wrongly: %+v", partial)
	}
	if got := def.TextureReferences(); len(got) != 3 || got[0] != "pattern_solid.dds" {
		t.Fatalf("texture references are %v, want the pattern first then both emblems", got)
	}
}

// A colour name no file defines renders as black in game, which looks
// deliberate. It has to be reported rather than resolved to a default.
func TestParseReportsUnresolvedColorNames(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "pattern_solid.dds"
	color1 = gh_invented_colour
	color2 = black
	color3 = black
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	if names := def.UnresolvedColorNames(); len(names) != 1 || names[0] != "gh_invented_colour" {
		t.Fatalf("unresolved names are %v, want the one invented colour", names)
	}
	if len(def.Warnings) == 0 {
		t.Fatal("an unresolved colour produced no warning")
	}
}

type stubTextures map[string]*image.NRGBA

func (s stubTextures) Texture(name string) (*image.NRGBA, error) { return s[name], nil }

// solidMask builds a texture whose every texel carries the given channels, so a
// render result can be reasoned about exactly.
func solidMask(size int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// A pattern channel is a weight over the three colours, not artwork: a fully
// red pattern texel must come out as color1 exactly.
func TestRenderPatternChannelsSelectTheNamedColors(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "p.dds"
	color1 = rgb { 10 20 30 }
	color2 = rgb { 200 0 0 }
	color3 = rgb { 0 200 0 }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	textures := stubTextures{"p.dds": solidMask(4, color.NRGBA{R: 255, A: 255})}
	result, err := Render(def, textures, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Image.NRGBAAt(4, 4); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("an all-red pattern texel rendered as %+v, want color1", got)
	}

	textures["p.dds"] = solidMask(4, color.NRGBA{G: 255, A: 255})
	result, err = Render(def, textures, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Image.NRGBAAt(4, 4); got != (color.NRGBA{R: 200, A: 255}) {
		t.Fatalf("an all-green pattern texel rendered as %+v, want color2", got)
	}
}

// Coverage comes from the emblem's own alpha. The shader's MaskTex.g term
// belongs to the global mask texture, so an emblem drawn purely in color1 --
// which has no green at all -- must still be fully visible.
func TestRenderColoredEmblemCoverageComesFromItsOwnAlpha(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "p.dds"
	color1 = rgb { 0 0 0 }
	color2 = rgb { 255 255 255 }
	color3 = rgb { 0 0 255 }
	colored_emblem = { texture = "e.dds" }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]

	// An opaque texel with no green and no red is color1 at full coverage.
	// Blue is 128 because that is what vanilla emblems carry: the channel is
	// shading, and 0.5 is its neutral value under the overlay blend.
	primaryOnly := stubTextures{
		"p.dds": solidMask(4, color.NRGBA{R: 255, A: 255}),
		"e.dds": solidMask(4, color.NRGBA{B: 128, A: 255}),
	}
	def.Colors[0] = newColor(ColorNotationRGB, "", color.NRGBA{R: 12, G: 34, B: 56, A: 255})
	result, err := Render(def, primaryOnly, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Image.NRGBAAt(4, 4); got.R != 12 || got.G != 34 || got.B != 56 {
		t.Fatalf("an emblem drawn purely in color1 rendered as %+v, want color1", got)
	}

	// A transparent texel draws nothing regardless of its colour channels.
	clear := stubTextures{
		"p.dds": solidMask(4, color.NRGBA{R: 255, A: 255}),
		"e.dds": solidMask(4, color.NRGBA{G: 255, B: 128, A: 0}),
	}
	result, err = Render(def, clear, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Image.NRGBAAt(4, 4); got.R != 12 || got.G != 34 || got.B != 56 {
		t.Fatalf("a fully transparent emblem texel drew %+v over the field", got)
	}

	visible := stubTextures{
		"p.dds": solidMask(4, color.NRGBA{R: 255, A: 255}),
		"e.dds": solidMask(4, color.NRGBA{G: 255, B: 128, A: 255}),
	}
	result, err = Render(def, visible, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Image.NRGBAAt(4, 4)
	if got.R < 200 || got.G < 200 || got.B < 200 {
		t.Fatalf("a fully green-masked emblem texel drew %+v, want close to color2 white", got)
	}
	if result.EmblemsDrawn != 1 || result.InstancesDrawn != 1 {
		t.Fatalf("render counted %d emblems / %d instances, want 1 / 1", result.EmblemsDrawn, result.InstancesDrawn)
	}
}

// A missing texture must not fail the render: the rest of the design is still
// worth looking at, and the gap has to be named rather than drawn as a guess.
func TestRenderReportsMissingTexturesWithoutFailing(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "absent_pattern.dds"
	color1 = rgb { 1 2 3 }
	color2 = black
	color3 = black
	colored_emblem = { texture = "absent_emblem.dds" }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	result, err := Render(def, stubTextures{}, RenderOptions{Size: 4})
	if err != nil {
		t.Fatalf("a missing texture failed the render: %v", err)
	}
	if len(result.MissingTextures) != 2 {
		t.Fatalf("missing textures are %v, want both names", result.MissingTextures)
	}
	if got := result.Image.NRGBAAt(2, 2); got != (color.NRGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Fatalf("the field fell back to %+v, want color1", got)
	}
}

// Depth counts away from the viewer, so the deepest emblem is painted first
// and everything shallower lands on top of it. A full-field backdrop declared
// at a high depth must not cover the charges it sits behind.
func TestRenderPaintsDeeperEmblemsFirst(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "p.dds"
	color1 = rgb { 0 0 0 }
	color2 = black
	color3 = black
	colored_emblem = { color1 = rgb { 10 10 10 } texture = "backdrop.dds" instance = { depth = 7.01 } }
	colored_emblem = { color1 = rgb { 250 250 250 } texture = "charge.dds" instance = { depth = 1.01 } }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	textures := stubTextures{
		"p.dds":        solidMask(4, color.NRGBA{R: 255, A: 255}),
		"backdrop.dds": solidMask(4, color.NRGBA{B: 128, A: 255}),
		"charge.dds":   solidMask(4, color.NRGBA{B: 128, A: 255}),
	}
	result, err := Render(def, textures, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Image.NRGBAAt(4, 4); got.R < 200 {
		t.Fatalf("the shallow charge rendered as %+v; the deep backdrop was painted over it", got)
	}
}

// Equal depths must keep declaration order or the same definition renders
// differently between runs.
func TestRenderOrdersInstancesByDepthThenDeclaration(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "p.dds"
	color1 = rgb { 0 0 0 }
	color2 = rgb { 255 0 0 }
	color3 = black
	colored_emblem = { texture = "high.dds" instance = { depth = 9 } }
	colored_emblem = { texture = "low.dds" instance = { depth = 1 } }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	textures := stubTextures{
		"p.dds":    solidMask(4, color.NRGBA{R: 255, A: 255}),
		"low.dds":  solidMask(4, color.NRGBA{G: 255, A: 255}),
		"high.dds": solidMask(4, color.NRGBA{G: 255, A: 255}),
	}
	first, err := Render(def, textures, RenderOptions{Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		again, err := Render(def, textures, RenderOptions{Size: 4})
		if err != nil {
			t.Fatal(err)
		}
		if again.Image.NRGBAAt(2, 2) != first.Image.NRGBAAt(2, 2) {
			t.Fatal("the same definition rendered differently between runs")
		}
	}
	if first.InstancesDrawn != 2 {
		t.Fatalf("drew %d instances, want 2", first.InstancesDrawn)
	}
}

// The blue channel is shading applied as an overlay blend, and vanilla emblems
// fill it with 128. That value has to be a no-op, or every stock emblem in the
// game would render 20% off its declared colour.
func TestRenderNeutralShadingChannelChangesNothing(t *testing.T) {
	parsed := script.Parse(`dyn_test = {
	pattern = "p.dds"
	color1 = rgb { 200 100 50 }
	color2 = black
	color3 = black
	colored_emblem = { texture = "e.dds" }
}`)
	def := Parse(parsed.Nodes, testPalette())[0]
	textures := stubTextures{
		"p.dds": solidMask(4, color.NRGBA{R: 255, A: 255}),
		"e.dds": solidMask(4, color.NRGBA{B: 128, A: 255}),
	}
	result, err := Render(def, textures, RenderOptions{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Image.NRGBAAt(4, 4)
	if abs(int(got.R)-200) > 1 || abs(int(got.G)-100) > 1 || abs(int(got.B)-50) > 1 {
		t.Fatalf("a neutral shading channel shifted the colour to %+v, want color1 unchanged", got)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
