package coatofarms

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// CK3 does not alpha-blend a coat of arms out of finished artwork. Every
// pattern and colored emblem texture is a mask: the channels say which of the
// three named colours each texel takes, and the artwork itself carries no
// colour at all. Compositing them as ordinary images produces the greyscale
// masks instead of the heraldry, so the channel behaviour from the shaders CK3
// ships is reproduced here:
//
//	pattern    r -> color1, g -> color2, b -> color3, taken as weights
//	emblem     start at color1, mix toward color2 by g, then toward color3 by r,
//	           overlay the b channel as shading at 0.2, coverage from alpha
//
// One line of the shader invites a specific mistake. It reads
// `EmblemColor.a *= MaskTex.g * 2`, but MaskTex is not the emblem — it is the
// global gfx/coat_of_arms/coa_mask_texture.dds sampled in pattern space. Taking
// the emblem's own green channel there erases every emblem drawn purely in
// color1, because those have no green at all.

// TextureSource supplies decoded texture data by the file name a definition
// refers to, such as "pattern_solid.dds". Returning a nil image with no error
// means the texture is genuinely absent, which the renderer reports rather than
// substituting a placeholder.
type TextureSource interface {
	Texture(name string) (*image.NRGBA, error)
}

// RenderOptions controls one rasterization.
type RenderOptions struct {
	// Size is the square edge in pixels. CK3's own render target is 512.
	Size int
}

// RenderResult carries the image together with what could not be drawn, so a
// caller never has to infer completeness from the picture.
type RenderResult struct {
	Image           *image.NRGBA `json:"-"`
	Size            int          `json:"size"`
	EmblemsDrawn    int          `json:"emblems_drawn"`
	InstancesDrawn  int          `json:"instances_drawn"`
	MissingTextures []string     `json:"missing_textures,omitempty"`
	Notes           []string     `json:"notes,omitempty"`
}

// Render rasterizes one definition. It draws the field from the pattern and
// then each emblem instance in depth order, and it never fails on a missing
// texture: the missing name is recorded and the rest of the design still draws,
// because a partial picture plus a named gap is more useful than an error.
func Render(definition Definition, textures TextureSource, options RenderOptions) (RenderResult, error) {
	size := options.Size
	if size <= 0 {
		size = 512
	}
	if size > 2048 {
		return RenderResult{}, fmt.Errorf("coat of arms render size %d exceeds the 2048 limit", size)
	}
	result := RenderResult{Size: size}
	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	result.Image = canvas

	if err := renderPattern(canvas, definition, textures, &result); err != nil {
		return result, err
	}

	type placement struct {
		emblem   Emblem
		instance Instance
		order    int
	}
	var placements []placement
	for emblemIndex, emblem := range definition.Emblems {
		for _, instance := range emblem.Instance {
			placements = append(placements, placement{emblem: emblem, instance: instance, order: emblemIndex})
		}
	}
	// Depth counts away from the viewer: the larger it is, the further back the
	// emblem sits, so the deepest is painted first and everything else lands on
	// top of it. Sorting the other way puts a full-field backdrop such as
	// ce_imperial_pattern_4 (depth 7.01) over the charges it is meant to sit
	// behind, and the result is a flat pattern with the whole design hidden
	// underneath. Equal depths keep declaration order so the same definition
	// always renders identically.
	sort.SliceStable(placements, func(i, j int) bool {
		if placements[i].instance.Depth != placements[j].instance.Depth {
			return placements[i].instance.Depth > placements[j].instance.Depth
		}
		return placements[i].order < placements[j].order
	})

	missing := map[string]bool{}
	drawnEmblems := map[int]bool{}
	for _, item := range placements {
		if item.emblem.Texture == "" {
			continue
		}
		texture, err := textures.Texture(item.emblem.Texture)
		if err != nil {
			return result, fmt.Errorf("emblem texture %q: %w", item.emblem.Texture, err)
		}
		if texture == nil {
			missing[item.emblem.Texture] = true
			continue
		}
		drawEmblemInstance(canvas, texture, definition.EffectiveColors(item.emblem), item.emblem.Textured, item.instance)
		result.InstancesDrawn++
		drawnEmblems[item.order] = true
	}
	result.EmblemsDrawn = len(drawnEmblems)
	for name := range missing {
		result.MissingTextures = append(result.MissingTextures, name)
	}
	sort.Strings(result.MissingTextures)
	if len(result.MissingTextures) > 0 {
		result.Notes = append(result.Notes, "the named textures were not found among the indexed resources; everything else in the design was drawn")
	}
	return result, nil
}

func renderPattern(canvas *image.NRGBA, definition Definition, textures TextureSource, result *RenderResult) error {
	fill := func(c color.NRGBA) {
		for i := 0; i < len(canvas.Pix); i += 4 {
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2], canvas.Pix[i+3] = c.R, c.G, c.B, 255
		}
	}
	colors := definition.Colors
	if definition.Pattern == "" {
		fill(colors[0].RGBA)
		result.Notes = append(result.Notes, "no pattern declared; the field was filled with color1")
		return nil
	}
	pattern, err := textures.Texture(definition.Pattern)
	if err != nil {
		return fmt.Errorf("pattern texture %q: %w", definition.Pattern, err)
	}
	if pattern == nil {
		fill(colors[0].RGBA)
		result.MissingTextures = append(result.MissingTextures, definition.Pattern)
		result.Notes = append(result.Notes, "the pattern texture was not found; the field was filled with color1 instead")
		return nil
	}
	size := canvas.Bounds().Dx()
	bounds := pattern.Bounds()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sample := sampleNRGBA(pattern, bounds, float64(x)/float64(size), float64(y)/float64(size))
			// The three channels are weights over the three colours. Vanilla
			// patterns keep them close to a partition of unity; normalizing
			// keeps a hand-made pattern from darkening or blowing out.
			r, g, b := float64(sample.R), float64(sample.G), float64(sample.B)
			total := r + g + b
			if total <= 0 {
				setNRGBA(canvas, x, y, colors[0].RGBA)
				continue
			}
			mixed := color.NRGBA{
				R: clampByte((r*float64(colors[0].RGBA.R) + g*float64(colors[1].RGBA.R) + b*float64(colors[2].RGBA.R)) / total),
				G: clampByte((r*float64(colors[0].RGBA.G) + g*float64(colors[1].RGBA.G) + b*float64(colors[2].RGBA.G)) / total),
				B: clampByte((r*float64(colors[0].RGBA.B) + g*float64(colors[1].RGBA.B) + b*float64(colors[2].RGBA.B)) / total),
				A: 255,
			}
			setNRGBA(canvas, x, y, mixed)
		}
	}
	return nil
}

// drawEmblemInstance composites one placed copy. The instance transform is
// inverted per destination pixel rather than forward-mapped, so rotation and
// scale leave no gaps.
func drawEmblemInstance(canvas *image.NRGBA, texture *image.NRGBA, colors [3]Color, textured bool, instance Instance) {
	size := canvas.Bounds().Dx()
	bounds := texture.Bounds()
	scaleX, scaleY := instance.Scale[0], instance.Scale[1]
	if scaleX == 0 || scaleY == 0 {
		return
	}
	radians := instance.Rotation * math.Pi / 180
	sin, cos := math.Sin(radians), math.Cos(radians)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Destination pixel to emblem space: undo the placement, then the
			// rotation, then the scale.
			dx := (float64(x)+0.5)/float64(size) - instance.Position[0]
			dy := (float64(y)+0.5)/float64(size) - instance.Position[1]
			rx := dx*cos + dy*sin
			ry := -dx*sin + dy*cos
			u := rx/scaleX + 0.5
			v := ry/scaleY + 0.5
			if u < 0 || u >= 1 || v < 0 || v >= 1 {
				continue
			}
			sample := sampleNRGBA(texture, bounds, u, v)
			var emblem color.NRGBA
			var alpha float64
			if textured {
				// A textured emblem carries finished artwork; only its own
				// alpha decides coverage.
				emblem = color.NRGBA{R: sample.R, G: sample.G, B: sample.B, A: 255}
				alpha = float64(sample.A) / 255
			} else {
				emblem, alpha = coloredEmblemTexel(sample, colors)
			}
			if alpha <= 0 {
				continue
			}
			blendOver(canvas, x, y, emblem, alpha)
		}
	}
}

// coloredEmblemTexel applies the colored-emblem shader to one texel.
func coloredEmblemTexel(sample color.NRGBA, colors [3]Color) (color.NRGBA, float64) {
	g := float64(sample.G) / 255
	r := float64(sample.R) / 255
	shade := float64(sample.B) / 255

	mixChannel := func(pick func(color.NRGBA) uint8) float64 {
		base := float64(pick(colors[0].RGBA)) / 255
		base = base + (float64(pick(colors[1].RGBA))/255-base)*g
		base = base + (float64(pick(colors[2].RGBA))/255-base)*r
		// The blue channel is shading, applied as an overlay blend at 0.2
		// rather than a multiply; a multiply drags every emblem toward black.
		return base + (overlay(base, shade)-base)*0.2
	}
	out := color.NRGBA{
		R: clampByte(mixChannel(func(c color.NRGBA) uint8 { return c.R }) * 255),
		G: clampByte(mixChannel(func(c color.NRGBA) uint8 { return c.G }) * 255),
		B: clampByte(mixChannel(func(c color.NRGBA) uint8 { return c.B }) * 255),
		A: 255,
	}
	return out, float64(sample.A) / 255
}

func overlay(base, blend float64) float64 {
	if base < 0.5 {
		return 2 * base * blend
	}
	return 1 - 2*(1-base)*(1-blend)
}

func blendOver(canvas *image.NRGBA, x, y int, src color.NRGBA, alpha float64) {
	offset := canvas.PixOffset(x, y)
	dst := canvas.Pix[offset : offset+4 : offset+4]
	dst[0] = clampByte(float64(src.R)*alpha + float64(dst[0])*(1-alpha))
	dst[1] = clampByte(float64(src.G)*alpha + float64(dst[1])*(1-alpha))
	dst[2] = clampByte(float64(src.B)*alpha + float64(dst[2])*(1-alpha))
	dst[3] = 255
}

func setNRGBA(canvas *image.NRGBA, x, y int, c color.NRGBA) {
	offset := canvas.PixOffset(x, y)
	dst := canvas.Pix[offset : offset+4 : offset+4]
	dst[0], dst[1], dst[2], dst[3] = c.R, c.G, c.B, 255
}

// sampleNRGBA reads a texture at normalized coordinates with nearest-neighbour
// sampling. The channels are masks whose exact values select colours, so
// interpolating them would invent intermediate mask values the artwork never
// declared.
func sampleNRGBA(img *image.NRGBA, bounds image.Rectangle, u, v float64) color.NRGBA {
	width, height := bounds.Dx(), bounds.Dy()
	x := bounds.Min.X + int(u*float64(width))
	y := bounds.Min.Y + int(v*float64(height))
	if x < bounds.Min.X {
		x = bounds.Min.X
	}
	if x >= bounds.Max.X {
		x = bounds.Max.X - 1
	}
	if y < bounds.Min.Y {
		y = bounds.Min.Y
	}
	if y >= bounds.Max.Y {
		y = bounds.Max.Y - 1
	}
	return img.NRGBAAt(x, y)
}
