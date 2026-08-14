package indexer

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"strings"
)

// The visual raster answers a different question from renderGUIPreviewPNG. The
// diagnostic raster shows where every node landed, painting kind-keyed boxes so
// a reader can audit the layout. This one approximates what a player sees, so
// it paints only what the GUI itself declares and leaves untouched anything the
// GUI does not draw.
//
// Alpha convention: the canvas is premultiplied, matching image/draw. Decoded
// textures and guiPreviewRGBA both hand back straight alpha -- guiPreviewRGBA
// is typed color.RGBA but stores the channels unmultiplied -- and blendPixel in
// the map renderer takes straight alpha too. Mixing the two conventions fringes
// edges, so every straight-alpha value converts through guiRasterPremultiply
// before it reaches the canvas, and blendPixel is never used here.

// guiRasterBackdrop is the neutral ground the scene composites onto. A mid tone
// keeps translucent panels legible whether the GUI paints light or dark; a
// window that declares its own background texture covers it entirely.
var guiRasterBackdrop = color.RGBA{96, 96, 100, 255}

// guiRasterPremultiply converts a straight-alpha colour to the premultiplied
// form image/draw expects.
func guiRasterPremultiply(c color.RGBA) color.RGBA {
	if c.A == 0xff {
		return c
	}
	alpha := float64(c.A) / 255
	return color.RGBA{
		R: uint8(math.Round(float64(c.R) * alpha)),
		G: uint8(math.Round(float64(c.G) * alpha)),
		B: uint8(math.Round(float64(c.B) * alpha)),
		A: c.A,
	}
}

// guiRasterScaleAlpha multiplies a straight-alpha colour by a 0..1 factor.
func guiRasterScaleAlpha(c color.RGBA, factor float64) color.RGBA {
	factor = math.Max(0, math.Min(1, factor))
	c.A = uint8(math.Round(float64(c.A) * factor))
	return c
}

// guiRasterTextures holds decoded textures keyed by indexed file path.
//
// This is deliberately not the HTML embed cache. That one downsamples to the
// largest rendered use and then keeps only base64, because a self-contained
// document pays for every byte twice; it also enforces the 384 KiB per-asset
// and 640 KiB per-page budgets that made the largest texture in a window
// undisplayable. A raster writes one PNG, so it keeps the decoded pixels at
// source resolution and samples them at blit time instead.
type guiRasterTextures map[string]*image.NRGBA

// loadGUIRasterTextures decodes every texture the scene references. A texture
// that cannot be read or decoded is omitted rather than fatal: one unsupported
// sprite should cost its own rectangle, not the whole picture.
func (db *DB) loadGUIRasterTextures(ctx context.Context, preview *GUIPreviewResult) (guiRasterTextures, error) {
	if preview == nil {
		return nil, nil
	}
	textures := guiRasterTextures{}
	for index := range preview.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, ref := range guiNodeTextureRefs(&preview.Nodes[index]) {
			if ref == nil || !ref.Resolved || ref.Dynamic || strings.TrimSpace(ref.filePath) == "" {
				continue
			}
			if _, seen := textures[ref.filePath]; seen {
				continue
			}
			if ref.fileID <= 0 || ref.fileSize <= 0 || ref.fileSize > guiTextureMaxSourceBytes {
				continue
			}
			data, _, err := db.ReadVerifiedIndexedFile(ctx, ref.fileID)
			if err != nil {
				var changed *SourceChangedError
				if errors.As(err, &changed) {
					return nil, err
				}
				continue
			}
			decoded, _, err := decodeGUITexture(data, ref.Kind)
			if err != nil || decoded == nil || decoded.Bounds().Empty() {
				continue
			}
			textures[ref.filePath] = decoded
		}
	}
	return textures, nil
}

// guiRasterBlendOver composites one premultiplied source pixel onto the canvas.
func guiRasterBlendOver(dst *image.RGBA, x, y int, src color.RGBA) {
	if src.A == 0 {
		return
	}
	offset := dst.PixOffset(x, y)
	if src.A == 0xff {
		dst.Pix[offset+0] = src.R
		dst.Pix[offset+1] = src.G
		dst.Pix[offset+2] = src.B
		dst.Pix[offset+3] = 0xff
		return
	}
	inverse := uint32(255 - src.A)
	dst.Pix[offset+0] = uint8(uint32(src.R) + uint32(dst.Pix[offset+0])*inverse/255)
	dst.Pix[offset+1] = uint8(uint32(src.G) + uint32(dst.Pix[offset+1])*inverse/255)
	dst.Pix[offset+2] = uint8(uint32(src.B) + uint32(dst.Pix[offset+2])*inverse/255)
	dst.Pix[offset+3] = uint8(uint32(src.A) + uint32(dst.Pix[offset+3])*inverse/255)
}

// blitTextureStretch draws src stretched across dstRect, modulated by tint.
//
// tint is the node's declared colour in straight alpha, and it multiplies the
// texel rather than replacing it. That is what makes a CK3 panel mask work:
// mask_rough_edges.dds is near-white with the ragged edge carried in its alpha,
// so multiplying by color = { 0 0 0 0.6 } yields a dark translucent panel with
// ragged edges. Painting the mask's own RGB instead -- which is what the HTML
// path does -- yields a light grey box.
func blitTextureStretch(dst *image.RGBA, dstRect, clip image.Rectangle, src *image.NRGBA, tint color.RGBA, alpha float64) {
	blitTextureRegion(dst, dstRect, clip, src, src.Bounds(), false, tint, alpha)
}

// blitTextureRegion draws one source rectangle into one destination rectangle,
// either stretched to fit or tiled at source scale.
func blitTextureRegion(dst *image.RGBA, dstRect, clip image.Rectangle, src *image.NRGBA, srcRect image.Rectangle, tiled bool, tint color.RGBA, alpha float64) {
	area := dstRect.Intersect(clip).Intersect(dst.Bounds())
	bounds := srcRect.Intersect(src.Bounds())
	if area.Empty() || bounds.Empty() || dstRect.Dx() <= 0 || dstRect.Dy() <= 0 {
		return
	}
	alpha = math.Max(0, math.Min(1, alpha))
	scaleX := float64(bounds.Dx()) / float64(dstRect.Dx())
	scaleY := float64(bounds.Dy()) / float64(dstRect.Dy())
	for y := area.Min.Y; y < area.Max.Y; y++ {
		var sourceY int
		if tiled {
			sourceY = bounds.Min.Y + (y-dstRect.Min.Y)%bounds.Dy()
		} else {
			sourceY = bounds.Min.Y + int(float64(y-dstRect.Min.Y)*scaleY)
		}
		if sourceY < bounds.Min.Y {
			sourceY = bounds.Min.Y
		} else if sourceY >= bounds.Max.Y {
			sourceY = bounds.Max.Y - 1
		}
		for x := area.Min.X; x < area.Max.X; x++ {
			var sourceX int
			if tiled {
				sourceX = bounds.Min.X + (x-dstRect.Min.X)%bounds.Dx()
			} else {
				sourceX = bounds.Min.X + int(float64(x-dstRect.Min.X)*scaleX)
			}
			if sourceX < bounds.Min.X {
				sourceX = bounds.Min.X
			} else if sourceX >= bounds.Max.X {
				sourceX = bounds.Max.X - 1
			}
			texel := src.NRGBAAt(sourceX, sourceY)
			if texel.A == 0 {
				continue
			}
			out := color.RGBA{
				R: uint8(uint32(texel.R) * uint32(tint.R) / 255),
				G: uint8(uint32(texel.G) * uint32(tint.G) / 255),
				B: uint8(uint32(texel.B) * uint32(tint.B) / 255),
				A: uint8(math.Round(float64(uint32(texel.A)*uint32(tint.A)/255) * alpha)),
			}
			guiRasterBlendOver(dst, x, y, guiRasterPremultiply(out))
		}
	}
}

// blitNineSlice draws a bordered sprite so its corners keep their scale.
//
// CK3 panels, buttons and label plates are Cornered sprites: the border pixels
// are the frame and only the middle may grow. Stretching the whole texture
// instead -- which is what a plain blit does -- drags the frame's soft edge
// across the panel and turns a 3px plate edge into a hard band.
//
// spriteborder is measured in source pixels while texture_density says how many
// source pixels make one interface pixel, so the destination border is the
// declared border divided by that density.
func blitNineSlice(dst *image.RGBA, dstRect, clip image.Rectangle, src *image.NRGBA, slice *GUITextureSlice, tint color.RGBA, alpha float64) {
	bounds := src.Bounds()
	borderX, borderY := slice.BorderX, slice.BorderY
	if borderX <= 0 && borderY <= 0 {
		blitTextureStretch(dst, dstRect, clip, src, tint, alpha)
		return
	}
	density := slice.TextureDensity
	if density <= 0 {
		density = 1
	}
	destX := int(math.Round(float64(borderX) / density))
	destY := int(math.Round(float64(borderY) / density))
	// A border cannot claim more than half the box in either axis, or opposing
	// corners would overlap and the middle would invert.
	destX = minInt(destX, dstRect.Dx()/2)
	destY = minInt(destY, dstRect.Dy()/2)
	borderX = minInt(borderX, bounds.Dx()/2)
	borderY = minInt(borderY, bounds.Dy()/2)
	tiled := strings.Contains(strings.ToLower(slice.SpriteType), "tiled")

	sx := [4]int{bounds.Min.X, bounds.Min.X + borderX, bounds.Max.X - borderX, bounds.Max.X}
	sy := [4]int{bounds.Min.Y, bounds.Min.Y + borderY, bounds.Max.Y - borderY, bounds.Max.Y}
	dx := [4]int{dstRect.Min.X, dstRect.Min.X + destX, dstRect.Max.X - destX, dstRect.Max.X}
	dy := [4]int{dstRect.Min.Y, dstRect.Min.Y + destY, dstRect.Max.Y - destY, dstRect.Max.Y}

	for row := 0; row < 3; row++ {
		for col := 0; col < 3; col++ {
			source := image.Rect(sx[col], sy[row], sx[col+1], sy[row+1])
			target := image.Rect(dx[col], dy[row], dx[col+1], dy[row+1])
			if source.Empty() || target.Empty() {
				continue
			}
			// Corners never repeat or stretch; edges and the middle follow the
			// sprite type.
			corner := (row == 0 || row == 2) && (col == 0 || col == 2)
			blitTextureRegion(dst, target, clip, src, source, tiled && !corner, tint, alpha)
		}
	}
}

// guiRasterFontPath picks the face the raster draws with.
//
// CK3 ships the font it actually renders Simplified Chinese with at
// game/fonts/Noto_Sans_SC/NotoSansSC-Medium.otf, which is the most faithful
// choice, but fonts/ is not an indexed resource root so it cannot be read
// through the verified-file path the textures use. Until a config key exists,
// CK3_INDEX_GUI_FONT points at it explicitly and the map renderer's existing
// CK3_INDEX_MAP_FONT is the fallback.
func guiRasterFontPath() string {
	for _, name := range []string{"CK3_INDEX_GUI_FONT", "CK3_INDEX_MAP_FONT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// guiRasterTextColor resolves the colour a node draws its text in.
func guiRasterTextColor(node GUIPreviewNode) color.RGBA {
	raw := ""
	if node.Runtime != nil {
		if value, known := guiNodeEffectiveColor(node.Runtime.FontTintColor); known {
			raw = value
		}
	}
	if raw == "" && node.Semantics != nil {
		for _, candidate := range []string{node.Semantics.FontColor, node.Semantics.FontTintColor} {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			if value, ok := normalizeGUIRuntimeColor(candidate); ok {
				raw = value
				break
			}
		}
	}
	if raw != "" {
		if parsed, ok := guiPreviewRGBA(raw); ok {
			return parsed
		}
	}
	// CK3's default body text is a warm off-white.
	return color.RGBA{R: 0xe8, G: 0xdf, B: 0xc8, A: 0xff}
}

// guiRasterWrapText breaks content to fit within maxWidth.
//
// Chinese has no inter-word spaces, so a space-only rule would never break and
// the line would simply overflow the card. Breaking between any two runes is
// correct for CJK and acceptable for the Latin text CK3 mixes in, with one
// exception worth honouring: a line must not begin with closing punctuation.
func guiRasterWrapText(content string, maxWidth, size int, text *mapTextRenderer) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	var lines []string
	for _, paragraph := range strings.Split(content, "\n") {
		runes := []rune(paragraph)
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		start := 0
		for start < len(runes) {
			end := start + 1
			for end < len(runes) && text.WidthSize(string(runes[start:end+1]), size) <= maxWidth {
				end++
			}
			if end < len(runes) && strings.ContainsRune("。，、；：？！）」』】》,.;:?!)]}", runes[end]) && end > start+1 {
				end--
			}
			lines = append(lines, string(runes[start:end]))
			start = end
		}
	}
	return lines
}

// drawGUIRasterText renders a node's resolved text inside its box.
func drawGUIRasterText(canvas *image.RGBA, rect image.Rectangle, node GUIPreviewNode, content string, text *mapTextRenderer, textScale, alpha float64) {
	if rect.Empty() || strings.TrimSpace(content) == "" {
		return
	}
	size := node.FontSize
	if size <= 0 {
		size = 13
	}
	// jomini's fontsize is a pixel em size; opentype.FaceOptions.Size is in
	// points and mapTextRenderer builds faces at 96 DPI, so a face requested at
	// N renders at N*96/72 pixels. Passing the GUI value straight through drew
	// every label a third too large, which wrapped and truncated text that fits
	// in game.
	size = maxInt(7, int(math.Round(float64(size)*textScale*72/96)))
	tint := guiRasterTextColor(node)
	tint = guiRasterScaleAlpha(tint, alpha)
	if tint.A == 0 {
		return
	}
	lines := []string{content}
	if node.Layout != nil && node.Layout.Multiline {
		lines = guiRasterWrapText(content, rect.Dx(), size, text)
	}
	lineHeight := text.HeightSize(size)
	if lineHeight <= 0 {
		lineHeight = size + 2
	}
	align := strings.ToLower(node.Align)
	y := rect.Min.Y
	// A single line centres vertically; wrapped text starts at the top so the
	// first line stays put as the content grows.
	if len(lines) == 1 && rect.Dy() > lineHeight {
		y += (rect.Dy() - lineHeight) / 2
	}
	for _, line := range lines {
		if y >= rect.Max.Y {
			break
		}
		x := rect.Min.X
		switch {
		case strings.Contains(align, "hcenter") || strings.Contains(align, "center"):
			x += maxInt(0, (rect.Dx()-text.WidthSize(line, size))/2)
		case strings.Contains(align, "right"):
			x += maxInt(0, rect.Dx()-text.WidthSize(line, size))
		}
		text.DrawSize(canvas, x, y, line, tint, size)
		y += lineHeight
	}
}

// guiRasterNodeFill resolves the colour a node paints with, in straight alpha.
// tintcolor wins over color, matching the cascade guiHTMLColorStyle relies on.
func guiRasterNodeFill(node GUIPreviewNode) (color.RGBA, bool) {
	if node.Runtime == nil {
		return color.RGBA{}, false
	}
	raw := ""
	if value, known := guiNodeEffectiveColor(node.Runtime.Color); known {
		raw = value
	}
	if value, known := guiNodeEffectiveColor(node.Runtime.TintColor); known {
		raw = value
	}
	if raw == "" {
		return color.RGBA{}, false
	}
	return guiPreviewRGBA(raw)
}

// guiRasterNodeRect returns the on-canvas rectangle a node occupies after its
// scroll clip is applied.
func guiRasterNodeRect(node GUIPreviewNode, bounds image.Rectangle) image.Rectangle {
	rect := image.Rect(
		node.Bounds.X,
		node.Bounds.Y,
		node.Bounds.X+node.Bounds.Width,
		node.Bounds.Y+node.Bounds.Height,
	).Intersect(bounds)
	if node.ClipBounds != nil {
		rect = rect.Intersect(image.Rect(
			node.ClipBounds.X,
			node.ClipBounds.Y,
			node.ClipBounds.X+node.ClipBounds.Width,
			node.ClipBounds.Y+node.ClipBounds.Height,
		))
	}
	return rect
}

// guiRasterNodeHidden reports whether a node hides itself, ignoring ancestors.
//
// A picture may only show what the game would show. That makes an unresolved
// visible= stricter here than in the diagnostic raster: the audit paints every
// laid-out node on purpose, but a portrait hover glow or a selection ring whose
// condition nobody evaluated is almost never on screen, and painting it is a
// worse error than omitting it. Nodes that never declare visible= at all are
// unconditional and still paint.
func guiRasterNodeHidden(node GUIPreviewNode) bool {
	if node.BehaviorOnly || node.Overlay != nil {
		return true
	}
	// A modify_texture node is not a layer of its own: it reshapes the nearest
	// textured ancestor through a blend mode. Blitting its texture directly
	// paints the raw mask over the parent -- a 3px divider becomes a white band
	// across the window. Until the blend compositor lands it contributes
	// nothing, which is closer to the truth than painting it.
	if node.Kind == "modify_texture" {
		return true
	}
	if node.Scenario != nil && node.Scenario.Visible != nil && !*node.Scenario.Visible {
		return true
	}
	if visible, known := guiNodeEffectiveVisible(node); known {
		return !visible
	}
	return node.Semantics != nil && strings.TrimSpace(node.Semantics.Visible) != ""
}

// guiRasterHiddenSubtrees marks every node that must not paint, propagating a
// hidden parent to its whole subtree. Nodes arrive in pre-order, so a parent is
// always resolved before its children.
//
// The root is exempt from the unresolved-visible rule because it is the subject
// of the picture: a window's own visible= is the scripted condition that opened
// it, and honouring it would render an empty canvas every time.
func guiRasterHiddenSubtrees(nodes []GUIPreviewNode) []bool {
	hidden := make([]bool, len(nodes))
	for index := range nodes {
		if index == 0 {
			hidden[index] = nodes[index].BehaviorOnly
			continue
		}
		parent := nodes[index].Parent
		if parent >= 0 && parent < index && hidden[parent] {
			hidden[index] = true
			continue
		}
		hidden[index] = guiRasterNodeHidden(nodes[index])
	}
	return hidden
}

func renderGUIVisualPNG(width, height int, nodes []GUIPreviewNode, textures guiRasterTextures, text *mapTextRenderer, textScale float64) ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(guiRasterBackdrop), image.Point{}, draw.Src)
	hidden := guiRasterHiddenSubtrees(nodes)
	// Nothing paints outside the window. Vanilla templates carry helpers parked
	// at negative offsets that only make sense once a runtime anchor moves them.
	frame := canvas.Bounds()
	if len(nodes) > 0 {
		frame = frame.Intersect(image.Rect(
			nodes[0].Bounds.X,
			nodes[0].Bounds.Y,
			nodes[0].Bounds.X+nodes[0].Bounds.Width,
			nodes[0].Bounds.Y+nodes[0].Bounds.Height,
		))
	}
	// A node whose size the layout had to infer is not trustworthy on its
	// own: an unsized text_label_center comes back wider than the card it
	// sits in, and blitting its background across that box smears a band
	// over the whole window. Confining inferred boxes to their parent is
	// the containment the declared sizes already have. Declared boxes are
	// left alone, because overflowing a parent on purpose is ordinary in
	// CK3 -- portrait_button overhangs portrait_head by five pixels.
	rects := make([]image.Rectangle, len(nodes))
	// Containment has to reach the whole subtree, not just the inferred node.
	// A text label's own box is marked approximate, but the background and
	// modify_texture children it carries are not -- they simply inherit the
	// same oversized box, so clipping only the parent still smeared their
	// textures across the window.
	confined := make([]bool, len(nodes))
	for index, node := range nodes {
		rect := guiRasterNodeRect(node, frame)
		parent := node.Parent
		inherits := parent >= 0 && parent < index && confined[parent]
		confined[index] = node.Approximate || inherits
		if confined[index] && parent >= 0 && parent < index {
			if parentRect := rects[parent]; !parentRect.Empty() {
				rect = rect.Intersect(parentRect)
			}
		}
		rects[index] = rect
	}
	for index, node := range nodes {
		if hidden[index] {
			continue
		}
		rect := rects[index]
		if rect.Empty() {
			continue
		}
		// The stretch mapping uses the node's own box while the write is
		// limited to the clipped rectangle. Feeding the clipped rectangle in
		// as the destination would squeeze the texture into whatever survived
		// the scroll clip instead of scrolling it.
		full := image.Rect(
			node.Bounds.X,
			node.Bounds.Y,
			node.Bounds.X+node.Bounds.Width,
			node.Bounds.Y+node.Bounds.Height,
		)
		alpha := 1.0
		if value, known := guiNodeEffectiveAlpha(node); known {
			alpha = value
		}
		tint, tinted := guiRasterNodeFill(node)
		if !tinted {
			tint = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		}
		painted := false
		for _, ref := range []*GUITextureRef{node.BackgroundTextureRef, node.TextureRef} {
			if ref == nil {
				continue
			}
			source := textures[ref.filePath]
			if source == nil {
				continue
			}
			if node.TextureSlice != nil {
				blitNineSlice(canvas, full, rect, source, node.TextureSlice, tint, alpha)
			} else {
				blitTextureStretch(canvas, full, rect, source, tint, alpha)
			}
			painted = true
		}
		if !painted && tinted {
			if fill := guiRasterScaleAlpha(tint, alpha); fill.A > 0 {
				draw.Draw(canvas, rect, image.NewUniform(guiRasterPremultiply(fill)), image.Point{}, draw.Over)
			}
		}
	}
	// Text is a second pass. Painting it inline let a later sibling's
	// background bury a label that the game shows on top -- the leader
	// caption vanished under the panel drawn after it.
	if text != nil && text.SupportsLocalizedText() {
		for index, node := range nodes {
			if hidden[index] || rects[index].Empty() {
				continue
			}
			content := guiPreviewNodeDisplayText(node)
			if content == "" {
				continue
			}
			alpha := 1.0
			if value, known := guiNodeEffectiveAlpha(node); known {
				alpha = value
			}
			drawGUIRasterText(canvas, rects[index], node, content, text, textScale, alpha)
		}
	}
	var output bytes.Buffer
	encoder, _ := mapRenderPNGEncoder(int64(width) * int64(height))
	if err := encoder.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// frameGUIVisualOnRoot re-frames an already-fitted scene onto the root
// element's own box.
//
// The diagnostic fit spans the union of every laid-out node so nothing escapes
// the audit. That is the wrong frame for a picture: tooltip owners and other
// helpers sit outside the window, so raising the node budget silently shrank
// and shifted the window instead of showing more of it. A picture of a window
// should be the window, and anything outside it clips away.
//
// Both transforms are a uniform scale plus a translation, so composing them is
// exact. Only Bounds and ClipBounds are adjusted because those are the only
// geometry the raster reads.
func frameGUIVisualOnRoot(nodes []GUIPreviewNode, width, height int) []GUIPreviewNode {
	if len(nodes) == 0 {
		return nodes
	}
	root := nodes[0].Bounds
	if root.Width <= 0 || root.Height <= 0 {
		return nodes
	}
	scale := math.Min(float64(width)/float64(root.Width), float64(height)/float64(root.Height))
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return nodes
	}
	offsetX := (float64(width) - float64(root.Width)*scale) / 2
	offsetY := (float64(height) - float64(root.Height)*scale) / 2
	remap := func(rect GUIPreviewRect) GUIPreviewRect {
		return GUIPreviewRect{
			X:      int(math.Round(offsetX + float64(rect.X-root.X)*scale)),
			Y:      int(math.Round(offsetY + float64(rect.Y-root.Y)*scale)),
			Width:  maxInt(1, int(math.Round(float64(rect.Width)*scale))),
			Height: maxInt(1, int(math.Round(float64(rect.Height)*scale))),
		}
	}
	framed := make([]GUIPreviewNode, len(nodes))
	for index, node := range nodes {
		framed[index] = node
		framed[index].Bounds = remap(node.Bounds)
		if node.ClipBounds != nil {
			clip := remap(*node.ClipBounds)
			framed[index].ClipBounds = &clip
		}
	}
	return framed
}

func refreshGUIVisualPNG(preview *GUIPreviewResult, textures guiRasterTextures) error {
	if preview == nil {
		return nil
	}
	// fontsize is authored in native units, so it has to follow the same
	// native-to-canvas scale the geometry took.
	nativeRootWidth := 0
	if len(preview.Nodes) > 0 {
		nativeRootWidth = preview.Nodes[0].Bounds.Width
	}
	displayNodes := frameGUIVisualOnRoot(fitGUIPreviewScene(preview), preview.Width, preview.Height)
	textScale := 1.0
	if nativeRootWidth > 0 && len(displayNodes) > 0 {
		textScale = float64(displayNodes[0].Bounds.Width) / float64(nativeRootWidth)
	}
	textRenderer, fontWarnings := loadMapTextRenderer(guiRasterFontPath())
	defer textRenderer.Close()
	// A missing face silently drops every label, which reads as a layout bug
	// rather than a configuration one. Say so instead.
	preview.Warnings = append(preview.Warnings, fontWarnings...)
	pngData, err := renderGUIVisualPNG(preview.Width, preview.Height, displayNodes, textures, textRenderer, textScale)
	if err != nil {
		return err
	}
	preview.PNG = pngData
	preview.Bytes = len(pngData)
	return nil
}
