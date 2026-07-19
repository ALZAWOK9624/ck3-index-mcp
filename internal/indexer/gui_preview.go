package indexer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	GUIPreviewDefaultWidth  = 1280
	GUIPreviewDefaultHeight = 720
	GUIPreviewMaxWidth      = 3840
	GUIPreviewMaxHeight     = 2160
	guiPreviewMaxDepth      = 64
)

// GUIPreviewResult is a deterministic, renderer-neutral CK3 GUI scene plus a
// diagnostic PNG. The renderer intentionally labels inferred dimensions as
// approximate instead of pretending to reproduce every Jomini layout policy.
type GUIPreviewResult struct {
	Symbol        string                      `json:"symbol"`
	SymbolKind    string                      `json:"symbol_kind"`
	Format        string                      `json:"format"`
	Source        string                      `json:"source,omitempty"`
	Width         int                         `json:"width"`
	Height        int                         `json:"height"`
	NativeBounds  GUIPreviewRect              `json:"native_bounds"`
	ViewScale     float64                     `json:"view_scale"`
	ViewOffsetX   int                         `json:"view_offset_x"`
	ViewOffsetY   int                         `json:"view_offset_y"`
	Bytes         int                         `json:"bytes"`
	Nodes         []GUIPreviewNode            `json:"nodes,omitempty"`
	TotalNodes    int                         `json:"total_nodes"`
	RenderedNodes int                         `json:"rendered_nodes"`
	Truncated     bool                        `json:"truncated,omitempty"`
	Approximate   bool                        `json:"approximate"`
	Warnings      []string                    `json:"warnings,omitempty"`
	Textures      GUIPreviewTextures          `json:"textures"`
	Language      string                      `json:"language"`
	Localization  GUIPreviewLocalizationStats `json:"localization"`
	Scenario      *GUIPreviewScenario         `json:"scenario,omitempty"`
	ModelSamples  *GUIPreviewModelSamples     `json:"model_samples,omitempty"`
	Runtime       *GUIPreviewRuntime          `json:"runtime,omitempty"`
	HTML          *GUIHTMLPreview             `json:"html,omitempty"`
	PNG           []byte                      `json:"-"`

	// runtimeLocalizationLookups contains the bounded active-index closure
	// used while compiling conditional text branches. It never leaves the
	// process or expands the public preview payload.
	runtimeLocalizationLookups map[string]map[string]string
}

type GUIPreviewTextures struct {
	Total       int `json:"total"`
	Resolved    int `json:"resolved"`
	Embedded    int `json:"embedded,omitempty"`
	Unsupported int `json:"unsupported,omitempty"`
	Dynamic     int `json:"dynamic"`
	Missing     int `json:"missing"`
}

type GUIPreviewNode struct {
	Index                 int                 `json:"index"`
	Parent                int                 `json:"parent"`
	Depth                 int                 `json:"depth"`
	Kind                  string              `json:"kind"`
	TypeChain             []string            `json:"type_chain,omitempty"`
	Name                  string              `json:"name,omitempty"`
	Source                string              `json:"source,omitempty"`
	Line                  int                 `json:"line,omitempty"`
	Bounds                GUIPreviewRect      `json:"bounds"`
	DeclaredPosition      *GUIVector          `json:"declared_position,omitempty"`
	DeclaredSize          *GUIVector          `json:"declared_size,omitempty"`
	Texture               string              `json:"texture,omitempty"`
	TextureRef            *GUITextureRef      `json:"texture_ref,omitempty"`
	NoProgressTextureRef  *GUITextureRef      `json:"no_progress_texture_ref,omitempty"`
	TextureFrames         *GUITextureFrames   `json:"texture_frames,omitempty"`
	TextureSlice          *GUITextureSlice    `json:"texture_slice,omitempty"`
	TextureBlendMode      string              `json:"texture_blend_mode,omitempty"`
	TextureBlendSupported bool                `json:"texture_blend_supported,omitempty"`
	Mirror                string              `json:"mirror,omitempty"`
	Text                  string              `json:"text,omitempty"`
	TextLocalization      *GUILocalizedText   `json:"text_localization,omitempty"`
	TooltipLocalization   *GUILocalizedText   `json:"tooltip_localization,omitempty"`
	Scenario              *GUINodeScenario    `json:"scenario,omitempty"`
	ModelRow              *GUIPreviewModelRow `json:"model_row,omitempty"`
	Runtime               *GUINodeRuntime     `json:"runtime,omitempty"`
	Semantics             *GUISemantics       `json:"semantics,omitempty"`
	StateDefinition       *GUIStateDefinition `json:"state_definition,omitempty"`
	Layout                *GUIPreviewLayout   `json:"layout,omitempty"`
	Overlay               *GUIPreviewOverlay  `json:"overlay,omitempty"`
	ClipBounds            *GUIPreviewRect     `json:"clip_bounds,omitempty"`
	BehaviorOnly          bool                `json:"behavior_only,omitempty"`
	Approximate           bool                `json:"approximate,omitempty"`

	// textureMaskDataURI is linked from the nearest textured ancestor only
	// while generating self-contained HTML. It preserves modify_texture alpha
	// semantics without exposing embedded image data in the preview JSON.
	textureMaskDataURI string
}

// GUIPreviewOverlay identifies visual subtrees that the engine displays
// outside normal parent layout. V1 recognizes tooltipwidget descendants and
// binds them to the owning control for bounded hover replay in the inspector.
type GUIPreviewOverlay struct {
	Role  string `json:"role"`
	Owner int    `json:"owner"`
}

// GUIPreviewLayout preserves the bounded subset of Jomini flow metadata that
// the inspector needs to recompute a container after a visible expression
// changes. Values are converted into final preview pixels before HTML output.
type GUIPreviewLayout struct {
	FlowDirection    string `json:"flow_direction,omitempty"`
	IgnoreInvisible  bool   `json:"ignore_invisible,omitempty"`
	Spacing          int    `json:"spacing,omitempty"`
	FlowItem         bool   `json:"flow_item,omitempty"`
	FillParent       bool   `json:"fill_parent,omitempty"`
	MarginLeft       int    `json:"margin_left,omitempty"`
	MarginRight      int    `json:"margin_right,omitempty"`
	MarginTop        int    `json:"margin_top,omitempty"`
	MarginBottom     int    `json:"margin_bottom,omitempty"`
	ExpandHorizontal bool   `json:"expand_horizontal,omitempty"`
	ExpandVertical   bool   `json:"expand_vertical,omitempty"`
	AllowOutside     bool   `json:"allow_outside,omitempty"`
	ScrollViewport   bool   `json:"scroll_viewport,omitempty"`
	ScrollDirection  string `json:"scroll_direction,omitempty"`
	ScrollContentW   int    `json:"scroll_content_width,omitempty"`
	ScrollContentH   int    `json:"scroll_content_height,omitempty"`
	ScrollStep       int    `json:"scroll_step,omitempty"`
	AutoResize       bool   `json:"auto_resize,omitempty"`
	Multiline        bool   `json:"multiline,omitempty"`
	MinWidth         int    `json:"min_width,omitempty"`
	MaxWidth         int    `json:"max_width,omitempty"`
	MinHeight        int    `json:"min_height,omitempty"`
	MaxHeight        int    `json:"max_height,omitempty"`
	GridColumns      int    `json:"grid_columns,omitempty"`
	GridColumnStep   int    `json:"grid_column_step,omitempty"`
	GridRowStep      int    `json:"grid_row_step,omitempty"`
	GridFlip         bool   `json:"grid_flip,omitempty"`
	GridItem         bool   `json:"grid_item,omitempty"`
	GridRow          int    `json:"grid_row,omitempty"`
	GridColumn       int    `json:"grid_column,omitempty"`
}

// GUIStateDefinition is a conservative subset of a Jomini state block that
// the HTML inspector can replay visually without evaluating script.
type GUIStateDefinition struct {
	Name     string `json:"name"`
	Alpha    string `json:"alpha,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// GUISemantics preserves the runtime-facing expressions that are most useful
// to a model reviewing a GUI. They are reported verbatim. A separate bounded
// evaluator may compose a safe subset from explicit facts; arbitrary Jomini
// code and engine data contexts are never executed.
type GUISemantics struct {
	Visible           string   `json:"visible,omitempty"`
	Enabled           string   `json:"enabled,omitempty"`
	Down              string   `json:"down,omitempty"`
	Selected          string   `json:"selected,omitempty"`
	Alpha             string   `json:"alpha,omitempty"`
	Min               string   `json:"min,omitempty"`
	Max               string   `json:"max,omitempty"`
	Value             string   `json:"value,omitempty"`
	TintColor         string   `json:"tint_color,omitempty"`
	FontTintColor     string   `json:"font_tint_color,omitempty"`
	DataContext       string   `json:"data_context,omitempty"`
	DataModel         string   `json:"data_model,omitempty"`
	OnClick           string   `json:"on_click,omitempty"`
	OnClicks          []string `json:"on_clicks,omitempty"`
	Tooltip           string   `json:"tooltip,omitempty"`
	RawText           string   `json:"raw_text,omitempty"`
	RawTexture        string   `json:"raw_texture,omitempty"`
	NoProgressTexture string   `json:"no_progress_texture,omitempty"`
	State             string   `json:"state,omitempty"`
}

type GUITextureRef struct {
	Path          string `json:"path"`
	Resolved      bool   `json:"resolved"`
	Dynamic       bool   `json:"dynamic,omitempty"`
	Source        string `json:"source,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Embedded      bool   `json:"embedded,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	SourceW       int    `json:"source_width,omitempty"`
	SourceH       int    `json:"source_height,omitempty"`
	Resized       bool   `json:"resized,omitempty"`
	FrameW        int    `json:"frame_width,omitempty"`
	FrameH        int    `json:"frame_height,omitempty"`
	FrameCols     int    `json:"frame_columns,omitempty"`
	FrameRows     int    `json:"frame_rows,omitempty"`
	FrameImages   int    `json:"frame_images,omitempty"`
	Format        string `json:"format,omitempty"`
	filePath      string
	dataURI       string
	frameDataURIs []string
}

// GUITextureFrames preserves the bounded, literal sprite-sheet fields used by
// CK3 controls. Static frame is zero-based in icon/progress widgets; button
// state frame fields are one-based.
type GUITextureFrames struct {
	Width         int  `json:"width"`
	Height        int  `json:"height"`
	Frame         *int `json:"frame,omitempty"`
	UpFrame       *int `json:"up_frame,omitempty"`
	OverFrame     *int `json:"over_frame,omitempty"`
	DownFrame     *int `json:"down_frame,omitempty"`
	DisabledFrame *int `json:"disabled_frame,omitempty"`
}

// GUITextureSlice preserves the literal Corneredstretched/Corneredtiled
// metadata used for CK3 nine-slice frames.
type GUITextureSlice struct {
	SpriteType     string  `json:"sprite_type,omitempty"`
	BorderX        int     `json:"border_x"`
	BorderY        int     `json:"border_y"`
	TextureDensity float64 `json:"texture_density,omitempty"`
}

type GUIPreviewRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type guiPreviewLayout struct {
	width, height int
	limit         int
	total         int
	nodes         []GUIPreviewNode
	warnings      []string
	warningSeen   map[string]bool
	truncated     bool
	approximate   bool
}

type guiPreviewSize struct {
	w, h        int
	approximate bool
}

type guiPreviewMargins struct {
	left, right, top, bottom int
}

// RenderGUIPreview lays out a resolved GUI element using CK3/Jomini fields
// observed in active GUI files: parentanchor, widgetanchor, position, size,
// hbox/vbox flow, spacing, margins, percentage sizes, and expanding policies.
func RenderGUIPreview(symbol, symbolKind, source string, element GUIElement, width, height, nodeLimit int) (GUIPreviewResult, error) {
	if width <= 0 {
		width = GUIPreviewDefaultWidth
	}
	if height <= 0 {
		height = GUIPreviewDefaultHeight
	}
	if width > GUIPreviewMaxWidth || height > GUIPreviewMaxHeight {
		return GUIPreviewResult{}, fmt.Errorf("GUI preview dimensions exceed %dx%d", GUIPreviewMaxWidth, GUIPreviewMaxHeight)
	}
	if width < 64 || height < 64 {
		return GUIPreviewResult{}, fmt.Errorf("GUI preview dimensions must be at least 64x64")
	}
	if nodeLimit <= 0 {
		nodeLimit = 100
	}
	if nodeLimit > 500 {
		nodeLimit = 500
	}
	layout := &guiPreviewLayout{
		width: width, height: height, limit: nodeLimit, warningSeen: map[string]bool{},
	}
	viewport := GUIPreviewRect{Width: width, Height: height}
	layout.layoutElement(element, viewport, nil, 0, -1)
	applyGUIPreviewScrollClips(layout.nodes)
	displayNodes, nativeBounds, viewScale, viewOffsetX, viewOffsetY := fitGUIPreviewNodes(layout.nodes, width, height)
	pngData, err := renderGUIPreviewPNG(width, height, displayNodes)
	if err != nil {
		return GUIPreviewResult{}, err
	}
	return GUIPreviewResult{
		Symbol: symbol, SymbolKind: symbolKind, Format: "png", Source: source, Width: width, Height: height,
		NativeBounds: nativeBounds, ViewScale: viewScale, ViewOffsetX: viewOffsetX, ViewOffsetY: viewOffsetY,
		Bytes: len(pngData), Nodes: layout.nodes, TotalNodes: layout.total, RenderedNodes: len(layout.nodes),
		Truncated: layout.truncated, Approximate: layout.approximate, Warnings: layout.warnings, PNG: pngData,
	}, nil
}

func fitGUIPreviewNodes(nodes []GUIPreviewNode, width, height int) ([]GUIPreviewNode, GUIPreviewRect, float64, int, int) {
	if len(nodes) == 0 {
		return nil, GUIPreviewRect{}, 1, 0, 0
	}
	first := -1
	for index := range nodes {
		if !nodes[index].BehaviorOnly && nodes[index].Overlay == nil && !guiPreviewHasScrollAncestor(nodes, index) {
			first = index
			break
		}
	}
	if first < 0 {
		first = 0
	}
	minX, minY := nodes[first].Bounds.X, nodes[first].Bounds.Y
	maxX := nodes[first].Bounds.X + nodes[first].Bounds.Width
	maxY := nodes[first].Bounds.Y + nodes[first].Bounds.Height
	for index, node := range nodes {
		if index == first || node.BehaviorOnly || node.Overlay != nil || guiPreviewHasScrollAncestor(nodes, index) {
			continue
		}
		minX = minInt(minX, node.Bounds.X)
		minY = minInt(minY, node.Bounds.Y)
		maxX = maxInt(maxX, node.Bounds.X+node.Bounds.Width)
		maxY = maxInt(maxY, node.Bounds.Y+node.Bounds.Height)
	}
	native := GUIPreviewRect{X: minX, Y: minY, Width: maxInt(1, maxX-minX), Height: maxInt(1, maxY-minY)}
	padding := 40
	availableW, availableH := maxInt(1, width-padding*2), maxInt(1, height-padding*2)
	scale := math.Min(float64(availableW)/float64(native.Width), float64(availableH)/float64(native.Height))
	if scale > 4 {
		scale = 4
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	scaledW := int(math.Round(float64(native.Width) * scale))
	scaledH := int(math.Round(float64(native.Height) * scale))
	offsetX := (width-scaledW)/2 - int(math.Round(float64(native.X)*scale))
	offsetY := (height-scaledH)/2 - int(math.Round(float64(native.Y)*scale))
	transformed := make([]GUIPreviewNode, len(nodes))
	for index, node := range nodes {
		transformed[index] = node
		transformed[index].Bounds = GUIPreviewRect{
			X:      offsetX + int(math.Round(float64(node.Bounds.X)*scale)),
			Y:      offsetY + int(math.Round(float64(node.Bounds.Y)*scale)),
			Width:  guiPreviewScaleDimension(node.Bounds.Width, scale),
			Height: guiPreviewScaleDimension(node.Bounds.Height, scale),
		}
		if node.Layout != nil {
			layout := *node.Layout
			layout.Spacing = guiPreviewScaleLayoutValue(layout.Spacing, scale)
			layout.MarginLeft = guiPreviewScaleLayoutValue(layout.MarginLeft, scale)
			layout.MarginRight = guiPreviewScaleLayoutValue(layout.MarginRight, scale)
			layout.MarginTop = guiPreviewScaleLayoutValue(layout.MarginTop, scale)
			layout.MarginBottom = guiPreviewScaleLayoutValue(layout.MarginBottom, scale)
			layout.ScrollContentW = guiPreviewScaleLayoutValue(layout.ScrollContentW, scale)
			layout.ScrollContentH = guiPreviewScaleLayoutValue(layout.ScrollContentH, scale)
			layout.ScrollStep = guiPreviewScaleLayoutValue(layout.ScrollStep, scale)
			layout.MinWidth = guiPreviewScaleLayoutValue(layout.MinWidth, scale)
			layout.MaxWidth = guiPreviewScaleLayoutValue(layout.MaxWidth, scale)
			layout.MinHeight = guiPreviewScaleLayoutValue(layout.MinHeight, scale)
			layout.MaxHeight = guiPreviewScaleLayoutValue(layout.MaxHeight, scale)
			layout.GridColumnStep = guiPreviewScaleLayoutValue(layout.GridColumnStep, scale)
			layout.GridRowStep = guiPreviewScaleLayoutValue(layout.GridRowStep, scale)
			transformed[index].Layout = &layout
		}
		if node.ClipBounds != nil {
			transformed[index].ClipBounds = &GUIPreviewRect{
				X:      offsetX + int(math.Round(float64(node.ClipBounds.X)*scale)),
				Y:      offsetY + int(math.Round(float64(node.ClipBounds.Y)*scale)),
				Width:  maxInt(0, int(math.Round(float64(node.ClipBounds.Width)*scale))),
				Height: maxInt(0, int(math.Round(float64(node.ClipBounds.Height)*scale))),
			}
		}
	}
	return transformed, native, scale, offsetX, offsetY
}

func guiPreviewScaleDimension(value int, scale float64) int {
	if value <= 0 {
		return 0
	}
	return maxInt(1, int(math.Round(float64(value)*scale)))
}

func guiPreviewScaleLayoutValue(value int, scale float64) int {
	if value == 0 {
		return 0
	}
	return int(math.Round(float64(value) * scale))
}

func (layout *guiPreviewLayout) layoutElement(element GUIElement, parent GUIPreviewRect, forced *GUIPreviewRect, depth, parentIndex int) {
	if depth > guiPreviewMaxDepth {
		layout.truncated = true
		layout.warn("depth", fmt.Sprintf("GUI preview expansion stopped after %d layout levels", guiPreviewMaxDepth))
		return
	}
	structural := isGUIPreviewStructural(element.Kind)
	if structural {
		for index := range element.Children {
			if isGUIPreviewFillParentElement(element.Children[index]) {
				fillBounds := parent
				childNodeIndex := len(layout.nodes)
				layout.layoutElement(element.Children[index], parent, &fillBounds, depth, parentIndex)
				layout.markGUIPreviewFillParent(childNodeIndex, parentIndex)
				continue
			}
			layout.layoutElement(element.Children[index], parent, nil, depth, parentIndex)
		}
		return
	}

	layout.total++
	if len(layout.nodes) >= layout.limit {
		layout.truncated = true
		return
	}
	size := layout.measure(element, parent.Width, parent.Height, depth)
	bounds := layout.position(element, parent, size)
	if forced != nil {
		bounds = *forced
		if bounds.Width <= 0 {
			bounds.Width = size.w
		}
		if bounds.Height <= 0 {
			bounds.Height = size.h
		}
	}
	text := guiPreviewProperty(element, "text")
	texture := guiPreviewPrimaryTexture(element)
	textureBlendMode := guiPreviewTextureBlendMode(element)
	textureBlendSupported := guiPreviewTextureBlendSupported(textureBlendMode)
	approximate := size.approximate || (textureBlendMode != "" && !textureBlendSupported)
	node := GUIPreviewNode{
		Index: len(layout.nodes), Parent: parentIndex, Depth: depth, Kind: element.Kind, TypeChain: append([]string(nil), element.TypeChain...), Name: element.Name,
		Source: element.Source, Line: element.Span.Line, Bounds: bounds, Texture: texture,
		DeclaredPosition: cloneGUIVector(element.Position), DeclaredSize: cloneGUIVector(element.Size),
		TextureFrames: guiPreviewTextureFrames(element), TextureSlice: guiPreviewTextureSlice(element),
		TextureBlendMode: textureBlendMode, TextureBlendSupported: textureBlendSupported, Mirror: guiPreviewMirror(element),
		Text: text, Semantics: guiPreviewSemantics(element), StateDefinition: guiPreviewStateDefinition(element),
		Layout:       guiPreviewElementLayout(element),
		BehaviorOnly: strings.EqualFold(strings.TrimSpace(element.Kind), "state"), Approximate: approximate,
	}
	if element.modelRow != nil {
		node.ModelRow = &GUIPreviewModelRow{
			Source:     "provided",
			Collection: element.modelRow.Collection,
			Target:     element.modelRow.Target,
			DataModel:  element.modelRow.DataModel,
			ID:         element.modelRow.ID,
			Index:      element.modelRow.Index,
		}
	}
	node.Overlay = guiPreviewOverlayForNode(element, layout.nodes, parentIndex)
	if parentIndex < 0 {
		node.Parent = -1
	}
	layout.nodes = append(layout.nodes, node)
	currentIndex := node.Index
	if size.approximate {
		layout.approximate = true
	}
	if textureBlendMode != "" && !textureBlendSupported {
		layout.approximate = true
		layout.warn("texture_blend", "Some GUI texture blend modes are preserved as metadata but are not visually replayed")
	}

	if isGUIPreviewGrid(element) {
		layout.layoutGridChildren(element, bounds, depth+1, currentIndex)
		return
	}
	if isGUIPreviewScrollViewport(element) {
		layout.layoutScrollboxChildren(element, bounds, depth+1, currentIndex)
		layout.measureGUIPreviewScrollContent(currentIndex)
		return
	}
	if horizontal, isFlow := guiPreviewFlowDirection(element); isFlow {
		layout.layoutFlowChildren(element, bounds, depth+1, currentIndex, horizontal)
		return
	}
	for index := range element.Children {
		if isGUIPreviewFillParentElement(element.Children[index]) {
			backgroundBounds := bounds
			childNodeIndex := len(layout.nodes)
			layout.layoutElement(element.Children[index], bounds, &backgroundBounds, depth+1, currentIndex)
			layout.markGUIPreviewFillParent(childNodeIndex, currentIndex)
			continue
		}
		layout.layoutElement(element.Children[index], bounds, nil, depth+1, currentIndex)
	}
}

func cloneGUIVector(value *GUIVector) *GUIVector {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func guiPreviewPrimaryTexture(element GUIElement) string {
	if texture := strings.TrimSpace(element.Texture); texture != "" {
		return texture
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(element.Kind)), "progressbar") {
		return guiPreviewProgressTextureProperty(element, "progresstexture")
	}
	return ""
}

func guiPreviewProgressTextureProperty(element GUIElement, name string) string {
	if value := guiPreviewProperty(element, name); strings.TrimSpace(value) != "" {
		return value
	}
	for _, child := range element.Children {
		if !strings.EqualFold(strings.TrimSpace(child.Kind), "block") ||
			!strings.EqualFold(strings.TrimSpace(child.Slot), "progress_textures") {
			continue
		}
		if value := guiPreviewProperty(child, name); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func guiPreviewTextureSlice(element GUIElement) *GUITextureSlice {
	borderX := guiPreviewPropertyVectorDimension(element, "spriteborder", true)
	borderY := guiPreviewPropertyVectorDimension(element, "spriteborder", false)
	if borderX < 0 || borderY < 0 || (borderX == 0 && borderY == 0) {
		return nil
	}
	density := 1.0
	if raw := strings.Trim(strings.TrimSpace(guiPreviewProperty(element, "texture_density")), "\""); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value > 0 {
			density = value
		}
	}
	return &GUITextureSlice{
		SpriteType:     strings.ToLower(strings.Trim(strings.TrimSpace(guiPreviewProperty(element, "spritetype")), "\"")),
		BorderX:        borderX,
		BorderY:        borderY,
		TextureDensity: density,
	}
}

func guiPreviewTextureFrames(element GUIElement) *GUITextureFrames {
	width := guiPreviewPropertyVectorDimension(element, "framesize", true)
	height := guiPreviewPropertyVectorDimension(element, "framesize", false)
	if width <= 0 || height <= 0 {
		return nil
	}
	return &GUITextureFrames{
		Width:         width,
		Height:        height,
		Frame:         guiPreviewOptionalIntegerProperty(element, "frame"),
		UpFrame:       guiPreviewOptionalIntegerProperty(element, "upframe"),
		OverFrame:     guiPreviewOptionalIntegerProperty(element, "overframe"),
		DownFrame:     guiPreviewOptionalIntegerProperty(element, "downframe"),
		DisabledFrame: guiPreviewOptionalIntegerProperty(element, "disableframe"),
	}
}

func guiPreviewTextureBlendMode(element GUIElement) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(guiPreviewProperty(element, "blend_mode")), "\""))
}

func guiPreviewTextureBlendSupported(mode string) bool {
	switch mode {
	case "add", "multiply", "alphamultiply", "overlay", "screen", "colordodge":
		return true
	default:
		return false
	}
}

func guiPreviewOptionalIntegerProperty(element GUIElement, name string) *int {
	for index := len(element.Properties) - 1; index >= 0; index-- {
		property := element.Properties[index]
		if !strings.EqualFold(property.Name, name) {
			continue
		}
		value, ok := guiPreviewNumber(property.Value)
		if !ok {
			return nil
		}
		return &value
	}
	return nil
}

func guiPreviewMirror(element GUIElement) string {
	raw := strings.ToLower(strings.TrimSpace(guiPreviewProperty(element, "mirror")))
	if raw == "" {
		return ""
	}
	hasHorizontal := false
	hasVertical := false
	for _, part := range strings.Split(raw, "|") {
		switch strings.TrimSpace(part) {
		case "horizontal":
			hasHorizontal = true
		case "vertical":
			hasVertical = true
		}
	}
	switch {
	case hasHorizontal && hasVertical:
		return "horizontal|vertical"
	case hasHorizontal:
		return "horizontal"
	case hasVertical:
		return "vertical"
	default:
		return raw
	}
}

func guiPreviewSemantics(element GUIElement) *GUISemantics {
	onClicks := guiPreviewProperties(element, "onclick")
	alpha := ""
	if !strings.EqualFold(strings.TrimSpace(element.Kind), "state") {
		alpha = guiPreviewProperty(element, "alpha")
	}
	rawTexture := strings.TrimSpace(guiPreviewPrimaryTexture(element))
	if !strings.Contains(rawTexture, "[") && !strings.Contains(rawTexture, "]") {
		rawTexture = ""
	}
	semantics := GUISemantics{
		Visible:           guiPreviewProperty(element, "visible"),
		Enabled:           guiPreviewProperty(element, "enabled"),
		Down:              guiPreviewProperty(element, "down"),
		Selected:          guiPreviewProperty(element, "selected"),
		Alpha:             alpha,
		Min:               guiPreviewProperty(element, "min"),
		Max:               guiPreviewProperty(element, "max"),
		Value:             guiPreviewProperty(element, "value"),
		TintColor:         guiPreviewColorProperty(element, "tintcolor"),
		FontTintColor:     guiPreviewColorProperty(element, "fonttintcolor"),
		DataContext:       guiPreviewProperty(element, "datacontext"),
		DataModel:         guiPreviewProperty(element, "datamodel"),
		OnClick:           guiPreviewProperty(element, "onclick"),
		Tooltip:           guiPreviewProperty(element, "tooltip"),
		RawText:           guiPreviewProperty(element, "raw_text"),
		RawTexture:        rawTexture,
		NoProgressTexture: guiPreviewProgressTextureProperty(element, "noprogresstexture"),
		State:             guiPreviewProperty(element, "state"),
	}
	if len(onClicks) > 1 {
		semantics.OnClicks = onClicks
	}
	if semantics.Visible == "" && semantics.Enabled == "" && semantics.Down == "" && semantics.Selected == "" && semantics.Alpha == "" &&
		semantics.Min == "" && semantics.Max == "" && semantics.Value == "" &&
		semantics.TintColor == "" && semantics.FontTintColor == "" &&
		semantics.DataContext == "" && semantics.DataModel == "" && semantics.OnClick == "" &&
		semantics.Tooltip == "" && semantics.RawText == "" && semantics.RawTexture == "" &&
		semantics.NoProgressTexture == "" && semantics.State == "" {
		return nil
	}
	return &semantics
}

func guiPreviewColorProperty(element GUIElement, name string) string {
	for index := len(element.Properties) - 1; index >= 0; index-- {
		property := element.Properties[index]
		if !strings.EqualFold(property.Name, name) {
			continue
		}
		if value := strings.TrimSpace(property.Value); value != "" {
			return value
		}
		if len(property.Values) >= 3 && len(property.Values) <= 4 {
			return "{ " + strings.Join(property.Values, " ") + " }"
		}
		return ""
	}
	return ""
}

func guiPreviewStateDefinition(element GUIElement) *GUIStateDefinition {
	if !strings.EqualFold(strings.TrimSpace(element.Kind), "state") {
		return nil
	}
	return &GUIStateDefinition{
		Name:     strings.TrimSpace(element.Name),
		Alpha:    guiPreviewProperty(element, "alpha"),
		Duration: guiPreviewProperty(element, "duration"),
	}
}

func (layout *guiPreviewLayout) layoutFlowChildren(element GUIElement, bounds GUIPreviewRect, depth, parentIndex int, horizontal bool) {
	children := guiPreviewFlattenFlowChildren(element.Children)
	if len(children) == 0 {
		return
	}
	flowIndices := make([]int, 0, len(children))
	for index := range children {
		if isGUIPreviewFillParentElement(children[index]) {
			backgroundBounds := bounds
			childNodeIndex := len(layout.nodes)
			layout.layoutElement(children[index], bounds, &backgroundBounds, depth, parentIndex)
			layout.markGUIPreviewFillParent(childNodeIndex, parentIndex)
			continue
		}
		if isGUIPreviewOverlayElement(children[index]) {
			layout.layoutElement(children[index], bounds, nil, depth, parentIndex)
			continue
		}
		flowIndices = append(flowIndices, index)
	}
	if len(flowIndices) == 0 {
		return
	}
	spacing, _ := guiPreviewNumber(guiPreviewProperty(element, "spacing"))
	type measuredChild struct {
		size      guiPreviewSize
		margins   guiPreviewMargins
		expanding bool
	}
	measured := make([]measuredChild, len(flowIndices))
	fixedSize := 0
	expanding := 0
	for index, childIndex := range flowIndices {
		child := children[childIndex]
		margins := guiPreviewElementMargins(child)
		size := layout.measure(child, bounds.Width, bounds.Height, depth)
		policy := "layoutpolicy_vertical"
		if horizontal {
			policy = "layoutpolicy_horizontal"
		}
		isExpanding := strings.EqualFold(guiPreviewProperty(child, policy), "expanding") || strings.EqualFold(child.Kind, "expand")
		measured[index] = measuredChild{size: size, margins: margins, expanding: isExpanding}
		if isExpanding {
			expanding++
		} else if horizontal {
			fixedSize += size.w + margins.left + margins.right
		} else {
			fixedSize += size.h + margins.top + margins.bottom
		}
	}
	if len(flowIndices) > 1 {
		fixedSize += spacing * (len(flowIndices) - 1)
	}
	available := bounds.Height
	if horizontal {
		available = bounds.Width
	}
	extra := 0
	if expanding > 0 && available > fixedSize {
		extra = (available - fixedSize) / expanding
	}
	scrollViewport := isGUIPreviewScrollViewport(element)
	cursor := 0
	for index, childIndex := range flowIndices {
		child := children[childIndex]
		item := measured[index]
		childBounds := GUIPreviewRect{}
		if horizontal {
			cursor += item.margins.left
			childBounds = GUIPreviewRect{X: bounds.X + cursor, Y: bounds.Y + item.margins.top, Width: item.size.w, Height: item.size.h}
			if item.expanding && extra > 0 {
				childBounds.Width = extra
				if scrollViewport && !strings.EqualFold(strings.TrimSpace(child.Kind), "expand") {
					childBounds.Width = maxInt(item.size.w, childBounds.Width)
				}
			}
			if strings.EqualFold(guiPreviewProperty(child, "layoutpolicy_vertical"), "expanding") {
				childBounds.Height = maxInt(1, bounds.Height-item.margins.top-item.margins.bottom)
			}
			cursor += childBounds.Width + item.margins.right + spacing
		} else {
			cursor += item.margins.top
			childBounds = GUIPreviewRect{X: bounds.X + item.margins.left, Y: bounds.Y + cursor, Width: item.size.w, Height: item.size.h}
			if item.expanding && extra > 0 {
				childBounds.Height = extra
				if scrollViewport && !strings.EqualFold(strings.TrimSpace(child.Kind), "expand") {
					childBounds.Height = maxInt(item.size.h, childBounds.Height)
				}
			}
			if strings.EqualFold(guiPreviewProperty(child, "layoutpolicy_horizontal"), "expanding") {
				childBounds.Width = maxInt(1, bounds.Width-item.margins.left-item.margins.right)
			}
			cursor += childBounds.Height + item.margins.bottom + spacing
		}
		childNodeIndex := len(layout.nodes)
		layout.layoutElement(child, bounds, &childBounds, depth, parentIndex)
		layout.markGUIPreviewFlowItem(childNodeIndex, parentIndex, item.margins)
	}
}

// layoutScrollboxChildren keeps the engine chrome (scrollbar, background and
// fades) out of the vertical content flow. Resolved custom scrollbox types
// retain a primitive scrollarea kind, so the type lineage is the authoritative
// signal that identifies this viewport.
func (layout *guiPreviewLayout) layoutScrollboxChildren(element GUIElement, bounds GUIPreviewRect, depth, parentIndex int) {
	children := guiPreviewFlattenFlowChildren(element.Children)
	content := guiPreviewScrollContentChildren(children)
	contentSet := map[int]bool{}
	for _, index := range content {
		contentSet[index] = true
	}
	hasScrollwidget := false
	for index := range children {
		if strings.EqualFold(strings.TrimSpace(children[index].Kind), "scrollwidget") {
			hasScrollwidget = true
			break
		}
	}
	if !hasScrollwidget {
		// Native/minimal scrollboxes may put content directly in the viewport.
		// Reuse the normal flow allocator so `expand` and expanding policies
		// consume the remaining viewport, while any explicit chrome remains
		// positioned outside that flow.
		flowElement := element
		flowElement.Children = nil
		for index := range children {
			child := children[index]
			if contentSet[index] || isGUIPreviewFillParentElement(child) || isGUIPreviewOverlayElement(child) {
				flowElement.Children = append(flowElement.Children, child)
				continue
			}
			layout.layoutElement(child, bounds, nil, depth, parentIndex)
		}
		layout.layoutFlowChildren(flowElement, bounds, depth, parentIndex, false)
		return
	}
	cursor := 0
	for index := range children {
		child := children[index]
		if isGUIPreviewFillParentElement(child) {
			backgroundBounds := bounds
			childNodeIndex := len(layout.nodes)
			layout.layoutElement(child, bounds, &backgroundBounds, depth, parentIndex)
			layout.markGUIPreviewFillParent(childNodeIndex, parentIndex)
			continue
		}
		if isGUIPreviewOverlayElement(child) {
			layout.layoutElement(child, bounds, nil, depth, parentIndex)
			continue
		}
		if !contentSet[index] {
			layout.layoutElement(child, bounds, nil, depth, parentIndex)
			continue
		}
		margins := guiPreviewElementMargins(child)
		size := layout.measure(child, bounds.Width, 0, depth)
		cursor += margins.top
		childBounds := GUIPreviewRect{
			X: bounds.X + margins.left, Y: bounds.Y + cursor,
			Width: size.w, Height: size.h,
		}
		if strings.EqualFold(guiPreviewProperty(child, "layoutpolicy_horizontal"), "expanding") {
			childBounds.Width = maxInt(childBounds.Width, maxInt(0, bounds.Width-margins.left-margins.right))
		}
		childNodeIndex := len(layout.nodes)
		layout.layoutElement(child, bounds, &childBounds, depth, parentIndex)
		layout.markGUIPreviewFlowItem(childNodeIndex, parentIndex, margins)
		cursor += childBounds.Height + margins.bottom
	}
}

// guiPreviewScrollContentChildren returns source indexes. A resolved CK3
// scrollbox owns one scrollwidget; other direct children are engine chrome.
// Synthetic/minimal fixtures may omit scrollwidget, in which case ordinary
// non-chrome children form the bounded vertical content flow.
func guiPreviewScrollContentChildren(children []GUIElement) []int {
	var scrollwidgets []int
	for index := range children {
		if strings.EqualFold(strings.TrimSpace(children[index].Kind), "scrollwidget") {
			scrollwidgets = append(scrollwidgets, index)
		}
	}
	if len(scrollwidgets) > 0 {
		return scrollwidgets
	}
	content := make([]int, 0, len(children))
	for index := range children {
		child := children[index]
		kind := strings.ToLower(strings.TrimSpace(child.Kind))
		if isGUIPreviewFillParentElement(child) || kind == "scrollbar" || strings.Contains(kind, "scrollbar_") ||
			strings.EqualFold(strings.TrimSpace(child.Name), "scrollbar_fade") || isGUIPreviewOverlayElement(child) {
			continue
		}
		content = append(content, index)
	}
	return content
}

func (layout *guiPreviewLayout) layoutGridChildren(element GUIElement, bounds GUIPreviewRect, depth, parentIndex int) {
	children := guiPreviewFlattenFlowChildren(element.Children)
	if len(children) == 0 {
		return
	}
	if guiPreviewBool(guiPreviewProperty(element, "flipdirection")) {
		layout.approximate = true
		layout.warn("grid-flip", "Grid flipdirection is preserved as metadata; the bounded preview keeps source-order cells until engine direction semantics are available")
	}
	items := make([]GUIElement, 0, len(children))
	for index := range children {
		if isGUIPreviewFillParentElement(children[index]) {
			backgroundBounds := bounds
			childNodeIndex := len(layout.nodes)
			layout.layoutElement(children[index], bounds, &backgroundBounds, depth, parentIndex)
			layout.markGUIPreviewFillParent(childNodeIndex, parentIndex)
			continue
		}
		if isGUIPreviewOverlayElement(children[index]) {
			layout.layoutElement(children[index], bounds, nil, depth, parentIndex)
			continue
		}
		items = append(items, children[index])
	}
	if len(items) == 0 {
		return
	}
	columns, columnStep, rowStep := guiPreviewGridMetrics(element, items, bounds.Width, bounds.Height, layout, depth)
	for index := range items {
		row, column := index/columns, index%columns
		item := items[index]
		size := layout.measure(item, bounds.Width, bounds.Height, depth)
		margins := guiPreviewElementMargins(item)
		itemBounds := GUIPreviewRect{
			X:     bounds.X + column*columnStep + margins.left,
			Y:     bounds.Y + row*rowStep + margins.top,
			Width: size.w, Height: size.h,
		}
		if strings.EqualFold(guiPreviewProperty(item, "layoutpolicy_horizontal"), "expanding") && columnStep > margins.left+margins.right {
			itemBounds.Width = columnStep - margins.left - margins.right
		}
		if strings.EqualFold(guiPreviewProperty(item, "layoutpolicy_vertical"), "expanding") && rowStep > margins.top+margins.bottom {
			itemBounds.Height = rowStep - margins.top - margins.bottom
		}
		childNodeIndex := len(layout.nodes)
		layout.layoutElement(item, bounds, &itemBounds, depth, parentIndex)
		layout.markGUIPreviewGridItem(childNodeIndex, parentIndex, row, column, margins)
	}
}

func (layout *guiPreviewLayout) markGUIPreviewGridItem(nodeIndex, parentIndex, row, column int, margins guiPreviewMargins) {
	if nodeIndex < 0 || nodeIndex >= len(layout.nodes) || layout.nodes[nodeIndex].Parent != parentIndex {
		return
	}
	node := &layout.nodes[nodeIndex]
	if node.Layout == nil {
		node.Layout = &GUIPreviewLayout{}
	}
	node.Layout.GridItem = true
	node.Layout.GridRow = row
	node.Layout.GridColumn = column
	node.Layout.MarginLeft = margins.left
	node.Layout.MarginRight = margins.right
	node.Layout.MarginTop = margins.top
	node.Layout.MarginBottom = margins.bottom
}

func (layout *guiPreviewLayout) measureGUIPreviewScrollContent(viewportIndex int) {
	if viewportIndex < 0 || viewportIndex >= len(layout.nodes) || layout.nodes[viewportIndex].Layout == nil {
		return
	}
	viewport := layout.nodes[viewportIndex].Bounds
	maxRight := viewport.X + viewport.Width
	maxBottom := viewport.Y + viewport.Height
	for index := viewportIndex + 1; index < len(layout.nodes); index++ {
		node := layout.nodes[index]
		if node.BehaviorOnly || node.Overlay != nil || !guiPreviewBelongsToScrollContent(layout.nodes, index, viewportIndex) ||
			guiPreviewHasAllowOutsideAncestor(layout.nodes, index, viewportIndex) {
			continue
		}
		maxRight = maxInt(maxRight, node.Bounds.X+node.Bounds.Width)
		maxBottom = maxInt(maxBottom, node.Bounds.Y+node.Bounds.Height)
	}
	layout.nodes[viewportIndex].Layout.ScrollContentW = maxInt(viewport.Width, maxRight-viewport.X)
	layout.nodes[viewportIndex].Layout.ScrollContentH = maxInt(viewport.Height, maxBottom-viewport.Y)
}

func (layout *guiPreviewLayout) markGUIPreviewFlowItem(nodeIndex, parentIndex int, margins guiPreviewMargins) {
	if nodeIndex < 0 || nodeIndex >= len(layout.nodes) || layout.nodes[nodeIndex].Parent != parentIndex {
		return
	}
	node := &layout.nodes[nodeIndex]
	if node.Layout == nil {
		node.Layout = &GUIPreviewLayout{}
	}
	node.Layout.FlowItem = true
	node.Layout.MarginLeft = margins.left
	node.Layout.MarginRight = margins.right
	node.Layout.MarginTop = margins.top
	node.Layout.MarginBottom = margins.bottom
}

func (layout *guiPreviewLayout) markGUIPreviewFillParent(nodeIndex, parentIndex int) {
	if nodeIndex < 0 || nodeIndex >= len(layout.nodes) || layout.nodes[nodeIndex].Parent != parentIndex {
		return
	}
	node := &layout.nodes[nodeIndex]
	if node.Layout == nil {
		node.Layout = &GUIPreviewLayout{}
	}
	node.Layout.FillParent = true
}

func guiPreviewElementLayout(element GUIElement) *GUIPreviewLayout {
	result := GUIPreviewLayout{
		ExpandHorizontal: strings.EqualFold(strings.TrimSpace(guiPreviewProperty(element, "layoutpolicy_horizontal")), "expanding"),
		ExpandVertical:   strings.EqualFold(strings.TrimSpace(guiPreviewProperty(element, "layoutpolicy_vertical")), "expanding"),
		AllowOutside:     guiPreviewBool(guiPreviewProperty(element, "allow_outside")),
		AutoResize:       guiPreviewBool(guiPreviewProperty(element, "autoresize")),
		Multiline:        strings.EqualFold(strings.TrimSpace(element.Kind), "text_multi") || guiPreviewBool(guiPreviewProperty(element, "multiline")),
	}
	result.MinWidth, result.MaxWidth = guiPreviewDimensionConstraints(element, true)
	result.MinHeight, result.MaxHeight = guiPreviewDimensionConstraints(element, false)
	if isGUIPreviewGrid(element) {
		result.GridColumns, _ = guiPreviewNumber(guiPreviewProperty(element, "datamodel_wrap"))
		if result.GridColumns <= 0 {
			result.GridColumns = 1
		}
		result.GridColumnStep, _ = guiPreviewNumber(guiPreviewProperty(element, "addcolumn"))
		result.GridRowStep, _ = guiPreviewNumber(guiPreviewProperty(element, "addrow"))
		result.GridFlip = guiPreviewBool(guiPreviewProperty(element, "flipdirection"))
		result.IgnoreInvisible = guiPreviewBool(guiPreviewProperty(element, "ignoreinvisible"))
	}
	if isGUIPreviewScrollViewport(element) {
		result.ScrollViewport = true
		result.ScrollDirection = "vertical"
		result.ScrollStep = 60
		if step, ok := guiPreviewNumber(guiPreviewProperty(element, "wheelstep")); ok && step > 0 {
			result.ScrollStep = step
		}
	}
	if horizontal, isFlow := guiPreviewFlowDirection(element); isFlow {
		if horizontal {
			result.FlowDirection = "horizontal"
		} else {
			result.FlowDirection = "vertical"
		}
		result.IgnoreInvisible = guiPreviewBool(guiPreviewProperty(element, "ignoreinvisible"))
		result.Spacing, _ = guiPreviewNumber(guiPreviewProperty(element, "spacing"))
	}
	if result.FlowDirection == "" && !result.ExpandHorizontal && !result.ExpandVertical && !result.AllowOutside && !result.ScrollViewport &&
		!result.AutoResize && !result.Multiline && result.MinWidth == 0 && result.MaxWidth == 0 && result.MinHeight == 0 && result.MaxHeight == 0 &&
		!isGUIPreviewGrid(element) {
		return nil
	}
	return &result
}

func guiPreviewBool(raw string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(raw), "\"")) {
	case "yes", "true", "1":
		return true
	default:
		return false
	}
}

func (layout *guiPreviewLayout) measure(element GUIElement, parentW, parentH, depth int) guiPreviewSize {
	if depth > guiPreviewMaxDepth {
		return guiPreviewSize{w: 1, h: 1, approximate: true}
	}
	w, wKnown, wApprox := guiPreviewDimension(element.Size, true, parentW)
	h, hKnown, hApprox := guiPreviewDimension(element.Size, false, parentH)
	if wKnown && w == 0 && guiPreviewZeroDimensionIsAuto(element, true) {
		wKnown = false
	}
	if hKnown && h == 0 && guiPreviewZeroDimensionIsAuto(element, false) {
		hKnown = false
	}
	approximate := wApprox || hApprox
	if isGUIPreviewScrollViewport(element) {
		contentW, contentH := layout.measureGUIPreviewScrollIntrinsic(element, parentW, depth)
		expandW := strings.EqualFold(guiPreviewProperty(element, "layoutpolicy_horizontal"), "expanding")
		expandH := strings.EqualFold(guiPreviewProperty(element, "layoutpolicy_vertical"), "expanding")
		if contentW > 0 && (!wKnown || (expandW && contentW > w)) {
			w, wKnown, approximate = contentW, true, true
		}
		if contentH > 0 && (!hKnown || (expandH && contentH > h)) {
			h, hKnown, approximate = contentH, true, true
		}
		if approximate && (contentW > 0 || contentH > 0) {
			layout.warn("scroll-intrinsic", "An expanding scrollbox without its game parent uses its resolved content extent as the bounded preview viewport")
		}
	}
	if isGUIPreviewGrid(element) && (!wKnown || !hKnown) {
		contentW, contentH := layout.measureGUIPreviewGridContent(element, parentW, parentH, depth)
		if !wKnown && contentW > 0 {
			w, wKnown = contentW, true
			approximate = true
		}
		if !hKnown && contentH > 0 {
			h, hKnown = contentH, true
			approximate = true
		}
	}
	if !wKnown || !hKnown {
		flow := ""
		if horizontal, isFlow := guiPreviewFlowDirection(element); isFlow {
			if horizontal {
				flow = "hbox"
			} else {
				flow = "vbox"
			}
		}
		spacing, _ := guiPreviewNumber(guiPreviewProperty(element, "spacing"))
		contentW, contentH := 0, 0
		childParentW, childParentH := 0, 0
		if wKnown && w > 0 {
			childParentW = w
		}
		if hKnown && h > 0 {
			childParentH = h
		}
		// An expanding child in a flow consumes remaining main-axis space at
		// layout time. Feeding the viewport into intrinsic measurement would
		// count that remaining space once per ancestor and inflate the tree.
		if flow == "hbox" {
			childParentW = 0
		} else if flow == "vbox" {
			childParentH = 0
		}
		children := element.Children
		if flow != "" {
			children = guiPreviewFlattenFlowChildren(children)
		}
		for index := range children {
			if isGUIPreviewFillParentElement(children[index]) {
				continue
			}
			if isGUIPreviewOverlayElement(children[index]) {
				continue
			}
			child := layout.measure(children[index], childParentW, childParentH, depth+1)
			margins := guiPreviewElementMargins(children[index])
			switch flow {
			case "hbox":
				contentW += child.w + margins.left + margins.right
				contentH = maxInt(contentH, child.h+margins.top+margins.bottom)
			case "vbox":
				contentW = maxInt(contentW, child.w+margins.left+margins.right)
				contentH += child.h + margins.top + margins.bottom
			default:
				px, pxKnown := guiPreviewVectorNumber(element.Children[index].Position, true)
				py, pyKnown := guiPreviewVectorNumber(element.Children[index].Position, false)
				if !pxKnown || !pyKnown {
					approximate = true
				}
				contentW = maxInt(contentW, maxInt(0, px)+child.w+margins.left+margins.right)
				contentH = maxInt(contentH, maxInt(0, py)+child.h+margins.top+margins.bottom)
			}
		}
		if len(children) > 1 {
			if flow == "hbox" {
				contentW += spacing * (len(children) - 1)
			} else if flow == "vbox" {
				contentH += spacing * (len(children) - 1)
			}
		}
		if !wKnown && contentW > 0 {
			w, wKnown = contentW, true
			approximate = true
		}
		if !hKnown && contentH > 0 {
			h, hKnown = contentH, true
			approximate = true
		}
	}
	if !wKnown {
		if strings.EqualFold(guiPreviewProperty(element, "layoutpolicy_horizontal"), "expanding") && parentW > 0 {
			w = parentW
		} else {
			w = guiPreviewDefaultSize(element, true)
		}
		approximate = true
		layout.warn("inferred-size", "Some GUI elements have no static size; the preview infers conservative diagnostic dimensions")
	}
	if !hKnown {
		if strings.EqualFold(guiPreviewProperty(element, "layoutpolicy_vertical"), "expanding") && parentH > 0 {
			h = parentH
		} else {
			h = guiPreviewDefaultSize(element, false)
		}
		approximate = true
		layout.warn("inferred-size", "Some GUI elements have no static size; the preview infers conservative diagnostic dimensions")
	}
	minWidth, maxWidth := guiPreviewDimensionConstraints(element, true)
	minHeight, maxHeight := guiPreviewDimensionConstraints(element, false)
	w = guiPreviewClampDimension(w, minWidth, maxWidth)
	h = guiPreviewClampDimension(h, minHeight, maxHeight)
	return guiPreviewSize{w: maxInt(0, w), h: maxInt(0, h), approximate: approximate}
}

func guiPreviewZeroDimensionIsAuto(element GUIElement, horizontal bool) bool {
	kind := strings.ToLower(strings.TrimSpace(element.Kind))
	if strings.Contains(kind, "text") {
		return true
	}
	if guiPreviewBool(guiPreviewProperty(element, "autoresize")) {
		return true
	}
	policy := "layoutpolicy_vertical"
	if horizontal {
		policy = "layoutpolicy_horizontal"
	}
	return strings.EqualFold(strings.TrimSpace(guiPreviewProperty(element, policy)), "expanding")
}

func (layout *guiPreviewLayout) position(element GUIElement, parent GUIPreviewRect, size guiPreviewSize) GUIPreviewRect {
	px, pxKnown := guiPreviewVectorNumber(element.Position, true)
	py, pyKnown := guiPreviewVectorNumber(element.Position, false)
	if element.Position != nil && (!pxKnown || !pyKnown) {
		layout.approximate = true
		layout.warn("dynamic-position", "Dynamic GUI position expressions cannot be evaluated without runtime data and are shown at zero offset")
	}
	parentAnchor := guiPreviewProperty(element, "parentanchor")
	widgetAnchor := guiPreviewProperty(element, "widgetanchor")
	// Jomini aligns the same edge/center on both rectangles when only a
	// parentanchor is supplied. widgetanchor is an explicit pivot override.
	if strings.TrimSpace(widgetAnchor) == "" {
		widgetAnchor = parentAnchor
	}
	parentX, parentY := guiPreviewAnchor(parentAnchor, parent.Width, parent.Height)
	widgetX, widgetY := guiPreviewAnchor(widgetAnchor, size.w, size.h)
	return GUIPreviewRect{
		X:     parent.X + parentX + px - widgetX,
		Y:     parent.Y + parentY + py - widgetY,
		Width: size.w, Height: size.h,
	}
}

func guiPreviewOverlayForNode(element GUIElement, nodes []GUIPreviewNode, parentIndex int) *GUIPreviewOverlay {
	if isGUIPreviewOverlayElement(element) {
		return &GUIPreviewOverlay{Role: "tooltip_root", Owner: parentIndex}
	}
	if parentIndex < 0 || parentIndex >= len(nodes) || nodes[parentIndex].Overlay == nil {
		return nil
	}
	return &GUIPreviewOverlay{Role: "tooltip_content", Owner: nodes[parentIndex].Overlay.Owner}
}

func isGUIPreviewOverlayElement(element GUIElement) bool {
	return strings.EqualFold(strings.TrimSpace(element.Kind), "tooltipwidget")
}

func isGUIPreviewFillParentElement(element GUIElement) bool {
	if element.Size != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(element.Kind)) {
	case "background", "modify_texture":
		return true
	default:
		return false
	}
}

func guiPreviewFlowDirection(element GUIElement) (horizontal, isFlow bool) {
	if isGUIPreviewScrollViewport(element) {
		return false, true
	}
	switch strings.ToLower(strings.TrimSpace(element.Kind)) {
	case "hbox":
		return true, true
	case "vbox":
		return false, true
	case "flowcontainer":
		return !strings.EqualFold(strings.TrimSpace(guiPreviewProperty(element, "direction")), "vertical"), true
	default:
		return false, false
	}
}

func isGUIPreviewScrollViewport(element GUIElement) bool {
	return strings.EqualFold(strings.TrimSpace(element.Kind), "scrollbox") || guiPreviewHasType(element, "scrollbox")
}

func guiPreviewHasType(element GUIElement, name string) bool {
	for _, value := range element.TypeChain {
		if strings.EqualFold(strings.TrimSpace(value), name) {
			return true
		}
	}
	return false
}

func isGUIPreviewGrid(element GUIElement) bool {
	switch strings.ToLower(strings.TrimSpace(element.Kind)) {
	case "fixedgridbox", "dynamicgridbox":
		return true
	default:
		return false
	}
}

func guiPreviewGridMetrics(element GUIElement, items []GUIElement, parentW, parentH int, layout *guiPreviewLayout, depth int) (int, int, int) {
	columns, _ := guiPreviewNumber(guiPreviewProperty(element, "datamodel_wrap"))
	if columns <= 0 {
		columns = 1
	}
	if len(items) > 0 {
		columns = minInt(columns, len(items))
	}
	columnStep, _ := guiPreviewNumber(guiPreviewProperty(element, "addcolumn"))
	rowStep, _ := guiPreviewNumber(guiPreviewProperty(element, "addrow"))
	if columnStep <= 0 || rowStep <= 0 {
		maxWidth, maxHeight := 1, 1
		for index := range items {
			size := layout.measure(items[index], parentW, parentH, depth+1)
			margins := guiPreviewElementMargins(items[index])
			maxWidth = maxInt(maxWidth, size.w+margins.left+margins.right)
			maxHeight = maxInt(maxHeight, size.h+margins.top+margins.bottom)
		}
		if columnStep <= 0 {
			columnStep = maxWidth
		}
		if rowStep <= 0 {
			rowStep = maxHeight
		}
	}
	return maxInt(1, columns), maxInt(1, columnStep), maxInt(1, rowStep)
}

func (layout *guiPreviewLayout) measureGUIPreviewScrollIntrinsic(element GUIElement, parentW, depth int) (int, int) {
	children := guiPreviewFlattenFlowChildren(element.Children)
	indexes := guiPreviewScrollContentChildren(children)
	contentW, contentH := 0, 0
	for _, index := range indexes {
		child := layout.measure(children[index], parentW, 0, depth+1)
		margins := guiPreviewElementMargins(children[index])
		contentW = maxInt(contentW, child.w+margins.left+margins.right)
		contentH += child.h + margins.top + margins.bottom
	}
	return contentW, contentH
}

func (layout *guiPreviewLayout) measureGUIPreviewGridContent(element GUIElement, parentW, parentH, depth int) (int, int) {
	children := guiPreviewFlattenFlowChildren(element.Children)
	items := make([]GUIElement, 0, len(children))
	for index := range children {
		if isGUIPreviewFillParentElement(children[index]) || isGUIPreviewOverlayElement(children[index]) {
			continue
		}
		items = append(items, children[index])
	}
	if len(items) == 0 {
		return 0, 0
	}
	columns, columnStep, rowStep := guiPreviewGridMetrics(element, items, parentW, parentH, layout, depth)
	contentWidth, contentHeight := 0, 0
	for index := range items {
		row, column := index/columns, index%columns
		size := layout.measure(items[index], parentW, parentH, depth+1)
		margins := guiPreviewElementMargins(items[index])
		contentWidth = maxInt(contentWidth, column*columnStep+margins.left+size.w+margins.right)
		contentHeight = maxInt(contentHeight, row*rowStep+margins.top+size.h+margins.bottom)
	}
	return contentWidth, contentHeight
}

func guiPreviewFlattenFlowChildren(children []GUIElement) []GUIElement {
	if len(children) == 0 {
		return nil
	}
	result := make([]GUIElement, 0, len(children))
	var appendChildren func([]GUIElement)
	appendChildren = func(items []GUIElement) {
		for index := range items {
			if isGUIPreviewStructural(items[index].Kind) {
				appendChildren(items[index].Children)
				continue
			}
			result = append(result, items[index])
		}
	}
	appendChildren(children)
	return result
}

func guiPreviewBelongsToScrollContent(nodes []GUIPreviewNode, nodeIndex, viewportIndex int) bool {
	if nodeIndex < 0 || nodeIndex >= len(nodes) || viewportIndex < 0 || viewportIndex >= len(nodes) {
		return false
	}
	current := nodeIndex
	for current >= 0 && current < len(nodes) {
		parent := nodes[current].Parent
		if parent == viewportIndex {
			return nodes[current].Layout != nil && nodes[current].Layout.FlowItem
		}
		if parent < 0 || parent >= len(nodes) {
			return false
		}
		current = parent
	}
	return false
}

func guiPreviewHasAllowOutsideAncestor(nodes []GUIPreviewNode, nodeIndex, viewportIndex int) bool {
	if nodeIndex < 0 || nodeIndex >= len(nodes) {
		return false
	}
	current := nodeIndex
	for current >= 0 && current < len(nodes) && current != viewportIndex {
		if nodes[current].Layout != nil && nodes[current].Layout.AllowOutside {
			return true
		}
		current = nodes[current].Parent
	}
	return false
}

func guiPreviewHasScrollAncestor(nodes []GUIPreviewNode, nodeIndex int) bool {
	if nodeIndex < 0 || nodeIndex >= len(nodes) {
		return false
	}
	parent := nodes[nodeIndex].Parent
	for parent >= 0 && parent < len(nodes) {
		if nodes[parent].Layout != nil && nodes[parent].Layout.ScrollViewport {
			return true
		}
		parent = nodes[parent].Parent
	}
	return false
}

func applyGUIPreviewScrollClips(nodes []GUIPreviewNode) {
	for index := range nodes {
		nodes[index].ClipBounds = nil
		if nodes[index].BehaviorOnly || nodes[index].Overlay != nil {
			continue
		}
		var clip *GUIPreviewRect
		parent := nodes[index].Parent
		for parent >= 0 && parent < len(nodes) {
			if nodes[parent].Layout != nil && nodes[parent].Layout.ScrollViewport {
				next := nodes[parent].Bounds
				if clip == nil {
					clip = &next
				} else {
					intersection := guiPreviewRectIntersection(*clip, next)
					clip = &intersection
				}
			}
			parent = nodes[parent].Parent
		}
		nodes[index].ClipBounds = clip
	}
}

func guiPreviewRectIntersection(left, right GUIPreviewRect) GUIPreviewRect {
	minX := maxInt(left.X, right.X)
	minY := maxInt(left.Y, right.Y)
	maxX := minInt(left.X+left.Width, right.X+right.Width)
	maxY := minInt(left.Y+left.Height, right.Y+right.Height)
	return GUIPreviewRect{
		X: minX, Y: minY,
		Width: maxInt(0, maxX-minX), Height: maxInt(0, maxY-minY),
	}
}

func guiPreviewDimension(vector *GUIVector, horizontal bool, parent int) (int, bool, bool) {
	if vector == nil {
		return 0, false, false
	}
	raw := vector.Height
	if horizontal {
		raw = vector.Width
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, false
	}
	if strings.HasSuffix(raw, "%") {
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "%")), 64)
		if err != nil || parent <= 0 {
			return 0, false, true
		}
		return int(math.Round(float64(parent) * value / 100)), true, false
	}
	value, ok := guiPreviewNumber(raw)
	if !ok {
		return 0, false, true
	}
	if value < 0 && parent > 0 {
		value = parent + value
	}
	return value, true, false
}

func guiPreviewVectorNumber(vector *GUIVector, horizontal bool) (int, bool) {
	if vector == nil {
		return 0, true
	}
	raw := vector.Y
	if horizontal {
		raw = vector.X
	}
	if strings.TrimSpace(raw) == "" {
		return 0, true
	}
	return guiPreviewNumber(raw)
}

func guiPreviewNumber(raw string) (int, bool) {
	raw = strings.Trim(strings.TrimSpace(raw), "\"")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return int(math.Round(value)), true
}

func guiPreviewDimensionConstraints(element GUIElement, horizontal bool) (int, int) {
	minName, maxName := "min_height", "max_height"
	if horizontal {
		minName, maxName = "min_width", "max_width"
	}
	minimum, _ := guiPreviewNumber(guiPreviewProperty(element, minName))
	maximum, _ := guiPreviewNumber(guiPreviewProperty(element, maxName))
	if minimum <= 0 {
		minimum = guiPreviewPropertyVectorDimension(element, "minimumsize", horizontal)
	}
	if maximum <= 0 {
		maximum = guiPreviewPropertyVectorDimension(element, "maximumsize", horizontal)
	}
	return maxInt(0, minimum), maxInt(0, maximum)
}

func guiPreviewPropertyVectorDimension(element GUIElement, name string, horizontal bool) int {
	for index := len(element.Properties) - 1; index >= 0; index-- {
		property := element.Properties[index]
		if !strings.EqualFold(property.Name, name) {
			continue
		}
		valueIndex := 1
		if horizontal {
			valueIndex = 0
		}
		if valueIndex >= len(property.Values) {
			return 0
		}
		value, ok := guiPreviewNumber(property.Values[valueIndex])
		if !ok {
			return 0
		}
		return value
	}
	return 0
}

func guiPreviewClampDimension(value, minimum, maximum int) int {
	if minimum > 0 {
		value = maxInt(value, minimum)
	}
	if maximum > 0 {
		value = minInt(value, maximum)
	}
	return value
}

func guiPreviewAnchor(raw string, width, height int) (int, int) {
	raw = strings.ToLower(strings.Trim(strings.TrimSpace(raw), "\""))
	x, y := 0, 0
	if raw == "center" {
		return width / 2, height / 2
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '|' || r == ',' || r == ' ' }) {
		switch part {
		case "right":
			x = width
		case "hcenter":
			x = width / 2
		case "bottom":
			y = height
		case "vcenter":
			y = height / 2
		case "center":
			x, y = width/2, height/2
		}
	}
	return x, y
}

func guiPreviewElementMargins(element GUIElement) guiPreviewMargins {
	var margins guiPreviewMargins
	for _, property := range element.Properties {
		switch strings.ToLower(property.Name) {
		case "margin":
			if len(property.Values) > 0 {
				value, _ := guiPreviewNumber(property.Values[0])
				margins.left, margins.right = value, value
			}
			if len(property.Values) > 1 {
				value, _ := guiPreviewNumber(property.Values[1])
				margins.top, margins.bottom = value, value
			}
		case "margin_left":
			margins.left, _ = guiPreviewNumber(property.Value)
		case "margin_right":
			margins.right, _ = guiPreviewNumber(property.Value)
		case "margin_top":
			margins.top, _ = guiPreviewNumber(property.Value)
		case "margin_bottom":
			margins.bottom, _ = guiPreviewNumber(property.Value)
		}
	}
	return margins
}

func guiPreviewProperty(element GUIElement, name string) string {
	for index := len(element.Properties) - 1; index >= 0; index-- {
		if strings.EqualFold(element.Properties[index].Name, name) {
			return element.Properties[index].Value
		}
	}
	return ""
}

func guiPreviewProperties(element GUIElement, name string) []string {
	values := make([]string, 0, 2)
	for _, property := range element.Properties {
		if strings.EqualFold(property.Name, name) && strings.TrimSpace(property.Value) != "" {
			values = append(values, property.Value)
		}
	}
	return values
}

func guiPreviewDefaultSize(element GUIElement, horizontal bool) int {
	kind := strings.ToLower(element.Kind)
	if strings.Contains(kind, "text") {
		if horizontal {
			text := guiPreviewProperty(element, "text")
			return maxInt(80, minInt(320, len([]rune(text))*7+16))
		}
		return 24
	}
	if strings.Contains(kind, "button") {
		if strings.Contains(kind, "round") || strings.Contains(kind, "icon") || strings.Contains(kind, "close") {
			return 32
		}
		if horizontal {
			return 120
		}
		return 32
	}
	if kind == "icon" || strings.Contains(kind, "icon") {
		return 32
	}
	if kind == "expand" {
		return 8
	}
	if horizontal {
		return 96
	}
	return 28
}

func isGUIPreviewStructural(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "template", "block", "blockoverride":
		return true
	default:
		return false
	}
}

func (layout *guiPreviewLayout) warn(key, message string) {
	if layout.warningSeen[key] {
		return
	}
	layout.warningSeen[key] = true
	layout.warnings = append(layout.warnings, message)
}

func renderGUIPreviewPNG(width, height int, nodes []GUIPreviewNode) ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{18, 22, 30, 255}), image.Point{}, draw.Src)
	grid := color.RGBA{43, 49, 61, 120}
	for x := 0; x < width; x += 64 {
		draw.Draw(canvas, image.Rect(x, 0, minInt(x+1, width), height), image.NewUniform(grid), image.Point{}, draw.Over)
	}
	for y := 0; y < height; y += 64 {
		draw.Draw(canvas, image.Rect(0, y, width, minInt(y+1, height)), image.NewUniform(grid), image.Point{}, draw.Over)
	}
	for _, node := range nodes {
		if node.BehaviorOnly || node.Overlay != nil {
			continue
		}
		if node.Scenario != nil && node.Scenario.Visible != nil && !*node.Scenario.Visible {
			continue
		}
		rect := image.Rect(node.Bounds.X, node.Bounds.Y, node.Bounds.X+node.Bounds.Width, node.Bounds.Y+node.Bounds.Height).Intersect(canvas.Bounds())
		if node.ClipBounds != nil {
			clip := image.Rect(
				node.ClipBounds.X,
				node.ClipBounds.Y,
				node.ClipBounds.X+node.ClipBounds.Width,
				node.ClipBounds.Y+node.ClipBounds.Height,
			)
			rect = rect.Intersect(clip)
		}
		if rect.Empty() {
			continue
		}
		fill, border := guiPreviewColors(node.Kind, node.Approximate)
		if node.Scenario != nil && node.Scenario.Enabled != nil && !*node.Scenario.Enabled {
			border = color.RGBA{217, 107, 107, 255}
		}
		draw.Draw(canvas, rect, image.NewUniform(fill), image.Point{}, draw.Over)
		drawGUIRectOutline(canvas, rect, border)
		label := node.Kind
		if node.Name != "" {
			label += " " + node.Name
		}
		if displayText := guiPreviewNodeDisplayText(node); displayText != "" {
			label += " · " + displayText
		} else if node.Texture != "" {
			label += " · " + node.Texture
		}
		label = guiPreviewTrimLabel(label, maxInt(0, rect.Dx()-8))
		if label != "" && rect.Dx() >= 24 && rect.Dy() >= 14 {
			drawer := font.Drawer{Dst: canvas, Src: image.NewUniform(border), Face: basicfont.Face7x13, Dot: fixed.P(rect.Min.X+4, rect.Min.Y+14)}
			drawer.DrawString(label)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func refreshGUIPreviewPNG(preview *GUIPreviewResult) error {
	if preview == nil {
		return nil
	}
	displayNodes, _, _, _, _ := fitGUIPreviewNodes(preview.Nodes, preview.Width, preview.Height)
	pngData, err := renderGUIPreviewPNG(preview.Width, preview.Height, displayNodes)
	if err != nil {
		return err
	}
	preview.PNG = pngData
	preview.Bytes = len(pngData)
	return nil
}

func guiPreviewNodeDisplayText(node GUIPreviewNode) string {
	if node.Scenario != nil && node.Scenario.Text != nil {
		return *node.Scenario.Text
	}
	language := GUIPreviewLanguageRaw
	if node.TextLocalization != nil && node.TextLocalization.SelectedLanguage != "" {
		language = node.TextLocalization.SelectedLanguage
	}
	if node.Runtime != nil {
		if value, ok := resolvedGUIRuntimeText(node.Runtime.Text, language); ok && value != "" {
			return value
		}
	}
	if node.TextLocalization != nil && node.TextLocalization.SelectedLanguage != GUIPreviewLanguageRaw && node.TextLocalization.SelectedText != "" {
		return node.TextLocalization.SelectedText
	}
	return node.Text
}

func guiPreviewColors(kind string, approximate bool) (color.RGBA, color.RGBA) {
	kind = strings.ToLower(kind)
	fill := color.RGBA{84, 102, 122, 75}
	border := color.RGBA{139, 168, 199, 255}
	switch {
	case kind == "hbox" || kind == "vbox" || strings.Contains(kind, "container") || kind == "widget":
		fill, border = color.RGBA{46, 100, 150, 70}, color.RGBA{88, 166, 230, 255}
	case strings.Contains(kind, "button"):
		fill, border = color.RGBA{160, 96, 34, 85}, color.RGBA{235, 161, 73, 255}
	case strings.Contains(kind, "text"):
		fill, border = color.RGBA{45, 124, 85, 80}, color.RGBA{92, 204, 142, 255}
	case strings.Contains(kind, "icon") || kind == "background":
		fill, border = color.RGBA{103, 66, 149, 80}, color.RGBA{177, 122, 229, 255}
	case kind == "expand":
		fill, border = color.RGBA{100, 100, 100, 40}, color.RGBA{150, 150, 150, 200}
	}
	if approximate {
		border = color.RGBA{224, 188, 92, 255}
	}
	return fill, border
}

func drawGUIRectOutline(canvas *image.RGBA, rect image.Rectangle, c color.RGBA) {
	if rect.Empty() {
		return
	}
	draw.Draw(canvas, image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, minInt(rect.Min.Y+1, rect.Max.Y)), image.NewUniform(c), image.Point{}, draw.Over)
	draw.Draw(canvas, image.Rect(rect.Min.X, maxInt(rect.Max.Y-1, rect.Min.Y), rect.Max.X, rect.Max.Y), image.NewUniform(c), image.Point{}, draw.Over)
	draw.Draw(canvas, image.Rect(rect.Min.X, rect.Min.Y, minInt(rect.Min.X+1, rect.Max.X), rect.Max.Y), image.NewUniform(c), image.Point{}, draw.Over)
	draw.Draw(canvas, image.Rect(maxInt(rect.Max.X-1, rect.Min.X), rect.Min.Y, rect.Max.X, rect.Max.Y), image.NewUniform(c), image.Point{}, draw.Over)
}

func guiPreviewTrimLabel(value string, pixels int) string {
	if pixels < 14 {
		return ""
	}
	limit := pixels / 7
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return ""
	}
	return string(runes[:limit-1]) + "…"
}
