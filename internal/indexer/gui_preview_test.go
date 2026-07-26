package indexer

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestRenderGUIPreviewUsesJominiParentAndWidgetAnchors(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Size: &GUIVector{Width: "400", Height: "200"},
		Children: []GUIElement{{
			Kind: "icon", Size: &GUIVector{Width: "40", Height: "20"}, Position: &GUIVector{X: "-10", Y: "-5"},
			Properties: []GUIProperty{
				{Name: "parentanchor", Value: "bottom|right"},
				{Name: "widgetanchor", Value: "center"},
			},
		}},
	}
	result, err := RenderGUIPreview("anchored", "type", "gui/test.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("nodes=%d want 2: %+v", len(result.Nodes), result.Nodes)
	}
	child := result.Nodes[1].Bounds
	if child.X != 370 || child.Y != 185 || child.Width != 40 || child.Height != 20 {
		t.Fatalf("anchored child bounds=%+v want x=370 y=185 w=40 h=20", child)
	}
	decoded, err := png.Decode(bytes.NewReader(result.PNG))
	if err != nil {
		t.Fatalf("preview PNG: %v", err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 360 {
		t.Fatalf("preview PNG bounds=%v", decoded.Bounds())
	}
}

func TestRenderGUIPreviewPairsImplicitWidgetAnchorWithParentAnchor(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Size: &GUIVector{Width: "220", Height: "120"},
		Children: []GUIElement{
			{
				Kind: "icon", Name: "full", Size: &GUIVector{Width: "100%", Height: "100%"},
				Properties: []GUIProperty{{Name: "parentanchor", Value: "hcenter"}},
			},
			{
				Kind: "icon", Name: "stroke", Size: &GUIVector{Width: "90%", Height: "90%"},
				Properties: []GUIProperty{{Name: "parentanchor", Value: "center"}},
			},
		},
	}
	result, err := RenderGUIPreview("implicit_anchors", "type", "gui/test.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Nodes[1].Bounds; got != (GUIPreviewRect{Width: 220, Height: 120}) {
		t.Fatalf("full-size hcenter child drifted: %+v", got)
	}
	if got := result.Nodes[2].Bounds; got != (GUIPreviewRect{X: 11, Y: 6, Width: 198, Height: 108}) {
		t.Fatalf("centered 90%% child drifted: %+v", got)
	}
}

func TestRenderGUIPreviewPreservesExplicitZeroDimensionInFlow(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "220", Height: "30"},
		Children: []GUIElement{
			{
				Kind: "widget", Size: &GUIVector{Width: "0", Height: "30"},
				Children: []GUIElement{{
					Kind: "icon", Size: &GUIVector{Width: "30", Height: "30"}, Position: &GUIVector{X: "6", Y: "0"},
				}},
			},
			{Kind: "text_single", Size: &GUIVector{Width: "220", Height: "30"}},
		},
	}
	result, err := RenderGUIPreview("zero_width", "type", "gui/test.gui", root, 400, 160, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Nodes[1].Bounds; got.Width != 0 || got.Height != 30 {
		t.Fatalf("explicit zero-width widget was inferred as a default size: %+v", got)
	}
	if got := result.Nodes[2].Bounds; got.X != 6 || got.Width != 30 {
		t.Fatalf("outside child of zero-width widget moved: %+v", got)
	}
	if got := result.Nodes[3].Bounds; got.X != 0 || got.Width != 220 {
		t.Fatalf("zero-width flow item consumed horizontal space: %+v", got)
	}
}

func TestRenderGUIPreviewTreatsFontTemplateZeroWidthAsAutoText(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "220", Height: "30"},
		Children: []GUIElement{
			{
				Kind: "text_label_center", Size: &GUIVector{Width: "0", Height: "26"},
				Properties: []GUIProperty{
					{Name: "text", Value: "[CultureTradition.GetNameNoTooltip]"},
					{Name: "maximumsize", Values: []string{"220", "44"}},
					{Name: "multiline", Value: "yes"},
				},
			},
			{Kind: "widget", Size: &GUIVector{Width: "0", Height: "30"}},
		},
	}
	result, err := RenderGUIPreview("font_auto_width", "type", "gui/test.gui", root, 400, 160, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Nodes[1].Bounds; got.Width != 220 || got.Height != 26 {
		t.Fatalf("font template zero width did not auto-size within maximumsize: %+v", got)
	}
	if got := result.Nodes[2].Bounds; got.X != 220 || got.Width != 0 {
		t.Fatalf("hard-zero widget was not kept separate from auto text: %+v", got)
	}
}

func TestRenderGUIPreviewRecognizesResolvedScrollboxType(t *testing.T) {
	model := BuildGUIModel(`types Demo {
		type scrollbox = scrollarea {
			size = { 100 100 }
			scrollwidget = { vbox = {
				button = { size = { 220 120 } }
				button = { size = { 220 120 } }
			} }
		}
		type list = vbox {
			layoutpolicy_horizontal = expanding
			layoutpolicy_vertical = expanding
			scrollbox = {
				layoutpolicy_horizontal = expanding
				layoutpolicy_vertical = expanding
			}
		}
	}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/test.gui", Model: model}})
	var list GUIElement
	for _, typeRule := range resolved.Types {
		if typeRule.Name == "list" {
			list = typeRule.Element
		}
	}
	result, err := RenderGUIPreview("list", "type", "gui/test.gui", list, 800, 500, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 6 {
		t.Fatalf("nodes=%d want 6: %+v", len(result.Nodes), result.Nodes)
	}
	viewport := result.Nodes[1]
	if viewport.Kind != "scrollarea" || strings.Join(viewport.TypeChain, ",") != "scrollbox" {
		t.Fatalf("resolved scrollbox identity was lost: %+v", viewport)
	}
	if viewport.Layout == nil || !viewport.Layout.ScrollViewport || viewport.Layout.FlowDirection != "vertical" {
		t.Fatalf("resolved scrollbox was not a vertical viewport: %+v", viewport.Layout)
	}
	if viewport.Bounds.Width != 220 || viewport.Bounds.Height != 240 || result.NativeBounds.Width != 220 || result.NativeBounds.Height != 240 {
		t.Fatalf("expanding standalone scrollbox did not adopt content extent: viewport=%+v native=%+v", viewport.Bounds, result.NativeBounds)
	}
	if viewport.Layout.ScrollContentW != 220 || viewport.Layout.ScrollContentH != 240 {
		t.Fatalf("scroll content extent is wrong: %+v", viewport.Layout)
	}
	for _, index := range []int{4, 5} {
		if result.Nodes[index].ClipBounds == nil || *result.Nodes[index].ClipBounds != viewport.Bounds {
			t.Fatalf("scroll descendant %d was not clipped to the viewport: %+v", index, result.Nodes[index])
		}
	}
}

func TestRenderGUIPreviewScrollExtentIgnoresAllowOutsideSubtree(t *testing.T) {
	root := GUIElement{
		Kind: "scrollarea", TypeChain: []string{"scrollbox"}, Size: &GUIVector{Width: "100", Height: "50"},
		Children: []GUIElement{{
			Kind: "scrollwidget",
			Children: []GUIElement{{
				Kind: "vbox", Size: &GUIVector{Width: "100", Height: "80"},
				Children: []GUIElement{
					{Kind: "button", Size: &GUIVector{Width: "100", Height: "40"}},
					{
						Kind: "widget", Size: &GUIVector{Width: "0", Height: "40"},
						Properties: []GUIProperty{{Name: "allow_outside", Value: "yes"}},
						Children: []GUIElement{{
							Kind: "icon", Position: &GUIVector{X: "120", Y: "0"}, Size: &GUIVector{Width: "20", Height: "20"},
						}},
					},
				},
			}},
		}},
	}
	result, err := RenderGUIPreview("allow_outside", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	viewport := result.Nodes[0]
	if viewport.Layout == nil || !viewport.Layout.ScrollViewport {
		t.Fatalf("root was not recognized as a scroll viewport: %+v", viewport)
	}
	if viewport.Layout.ScrollContentW != 100 || viewport.Layout.ScrollContentH != 80 {
		t.Fatalf("allow_outside subtree inflated scroll extent: %+v", viewport.Layout)
	}
}

func TestRenderGUIPreviewPreservesTextureMirror(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Size: &GUIVector{Width: "100", Height: "100"},
		Children: []GUIElement{
			{Kind: "icon", Name: "horizontal", Properties: []GUIProperty{{Name: "mirror", Value: "horizontal"}}},
			{Kind: "icon", Name: "both", Properties: []GUIProperty{{Name: "mirror", Value: "vertical|horizontal"}}},
		},
	}
	result, err := RenderGUIPreview("mirrors", "type", "gui/test.gui", root, 200, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[1].Mirror != "horizontal" || result.Nodes[2].Mirror != "horizontal|vertical" {
		t.Fatalf("mirror metadata was not normalized: %+v", result.Nodes)
	}
}

func TestRenderGUIPreviewPreservesFitType(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Size: &GUIVector{Width: "100", Height: "100"},
		Children: []GUIElement{
			{Kind: "icon", Name: "center", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{{Name: "fittype", Value: "CenterCrop"}}},
			{Kind: "icon", Name: "start", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{{Name: "fittype", Value: "start"}}},
			{Kind: "icon", Name: "end", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{{Name: "fittype", Value: "end"}}},
			{Kind: "icon", Name: "unknown", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{{Name: "fittype", Value: "future_fit"}}},
			{Kind: "icon", Name: "framed", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{{Name: "fittype", Value: "centercrop"}, {Name: "framesize", Values: []string{"20", "20"}}}},
		},
	}
	result, err := RenderGUIPreview("fits", "type", "gui/test.gui", root, 200, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 6 {
		t.Fatalf("nodes=%d want 6: %+v", len(result.Nodes), result.Nodes)
	}
	for index, expected := range []string{"centercrop", "start", "end"} {
		node := result.Nodes[index+1]
		if node.FitType != expected || node.Approximate {
			t.Fatalf("supported fittype at node %d was not preserved exactly: %+v", index+1, node)
		}
	}
	if node := result.Nodes[4]; node.FitType != "future_fit" || !node.Approximate {
		t.Fatalf("unknown fittype was not preserved as approximate: %+v", node)
	}
	if node := result.Nodes[5]; node.FitType != "centercrop" || !node.Approximate {
		t.Fatalf("framed fittype was not retained as an explicit approximation: %+v", node)
	}
	if !result.Approximate || len(result.Warnings) == 0 {
		t.Fatalf("unsupported fittype combinations did not make the preview approximate: %+v", result)
	}
}

func TestRenderGUIPreviewPreservesAlign(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Size: &GUIVector{Width: "100", Height: "100"},
		Children: []GUIElement{
			{Kind: "text_single", Name: "left", Size: &GUIVector{Width: "80", Height: "20"}, Properties: []GUIProperty{{Name: "align", Value: "left|top"}}},
			{Kind: "text_single", Name: "center", Size: &GUIVector{Width: "80", Height: "20"}, Properties: []GUIProperty{{Name: "align", Value: "center|vcenter"}}},
			{Kind: "text_single", Name: "right", Size: &GUIVector{Width: "80", Height: "20"}, Properties: []GUIProperty{{Name: "align", Value: "right|bottom|nobaseline"}}},
			{Kind: "text_single", Name: "unknown", Size: &GUIVector{Width: "80", Height: "20"}, Properties: []GUIProperty{{Name: "align", Value: "diagonal"}}},
		},
	}
	result, err := RenderGUIPreview("alignments", "type", "gui/preload/labels.gui", root, 200, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 5 {
		t.Fatalf("nodes=%d want 5: %+v", len(result.Nodes), result.Nodes)
	}
	for index, expected := range []string{"left|top", "center|vcenter", "right|bottom|nobaseline"} {
		node := result.Nodes[index+1]
		if node.Align != expected || node.Approximate {
			t.Fatalf("supported align at node %d was not preserved exactly: %+v", index+1, node)
		}
	}
	if node := result.Nodes[4]; node.Align != "diagonal" || !node.Approximate {
		t.Fatalf("unknown align was not preserved as approximate: %+v", node)
	}
	if !result.Approximate || len(result.Warnings) == 0 {
		t.Fatalf("unknown align did not make the preview explicitly approximate: %+v", result)
	}
}

func TestRenderGUIPreviewPreservesPortraitBackgroundAndMaskTextures(t *testing.T) {
	const portraitExpression = "[Character.GetAnimatedPortrait('environment_hud', 'camera_hud', 'idle', PdxGetWidgetScreenSize(PdxGuiWidget.Self))]"
	root := GUIElement{
		Kind: "portrait_button", Name: "portrait", Size: &GUIVector{Width: "120", Height: "180"},
		Properties: []GUIProperty{
			{Name: "texture", Value: "gfx/portraits/portrait_transparent.dds"},
			{Name: "portrait_texture", Value: portraitExpression},
			{Name: "background_texture", Value: "gfx/portraits/portrait_background.dds"},
			{Name: "mask", Value: "gfx/portraits/portrait_mask_head.dds"},
		},
	}
	result, err := RenderGUIPreview("portrait", "type", "gui/shared/portraits.gui", root, 320, 240, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("nodes=%d want 1: %+v", len(result.Nodes), result.Nodes)
	}
	node := result.Nodes[0]
	if node.BackgroundTexture != "gfx/portraits/portrait_background.dds" || node.MaskTexture != "gfx/portraits/portrait_mask_head.dds" {
		t.Fatalf("portrait layers were not preserved: %+v", node)
	}
	if node.Semantics == nil || node.Semantics.PortraitTexture != portraitExpression {
		t.Fatalf("portrait runtime texture was lost: %+v", node.Semantics)
	}
}

func TestRenderGUIPreviewPreservesCoatOfArmsTexture(t *testing.T) {
	const coatOfArmsExpression = "[Title.GetTitleCoA.GetTexture('(int32)256','(int32)256')]"
	root := GUIElement{
		Kind: "coat_of_arms_icon", Name: "title_arms",
		Properties: []GUIProperty{
			{Name: "coat_of_arms", Value: coatOfArmsExpression},
			{Name: "coat_of_arms_mask", Value: "gfx/interface/coat_of_arms/title_mask.dds"},
			{Name: "coat_of_arms_offset", Values: []string{"0.0", "0.07"}},
			{Name: "coat_of_arms_scale", Values: []string{"0.9", "0.9"}},
		},
	}
	preview, err := RenderGUIPreview("title_arms", "element", "gui/shared/coat_of_arms.gui", root, 320, 240, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.CoatOfArmsTexture != coatOfArmsExpression {
		t.Fatalf("coat_of_arms texture expression was not preserved: %#v", preview.Nodes)
	}
	node := preview.Nodes[0]
	if node.CoatOfArmsMask != "gfx/interface/coat_of_arms/title_mask.dds" || node.CoatOfArmsOffset == nil || node.CoatOfArmsOffset.X != "0.0" || node.CoatOfArmsOffset.Y != "0.07" || node.CoatOfArmsScale == nil || node.CoatOfArmsScale.X != "0.9" || node.CoatOfArmsScale.Y != "0.9" {
		t.Fatalf("literal coat-of-arms composition fields were not preserved: %#v", node)
	}
}

func TestRenderGUIPreviewMakesModifyTextureFillItsParent(t *testing.T) {
	root := GUIElement{
		Kind: "button", Size: &GUIVector{Width: "30", Height: "30"},
		Children: []GUIElement{{
			Kind: "block", Slot: "button_icon_modify_texture",
			Children: []GUIElement{{
				Kind: "modify_texture",
				Properties: []GUIProperty{
					{Name: "texture", Value: `"gfx/interface/colors/colors_textured.dds"`},
					{Name: "blend_mode", Value: "add"},
					{Name: "rotate_uv", Value: "90"},
					{Name: "framesize", Values: []string{"96", "96"}},
				},
			}},
		}},
	}
	result, err := RenderGUIPreview("modified_button", "type", "gui/test.gui", root, 200, 120, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("nodes=%d want 2: %+v", len(result.Nodes), result.Nodes)
	}
	parent, modifier := result.Nodes[0], result.Nodes[1]
	if modifier.Bounds != parent.Bounds {
		t.Fatalf("modify_texture did not inherit parent bounds: parent=%+v modifier=%+v", parent.Bounds, modifier.Bounds)
	}
	if modifier.Layout == nil || !modifier.Layout.FillParent {
		t.Fatalf("modify_texture did not preserve fill-parent metadata: %+v", modifier.Layout)
	}
	if modifier.TextureFrames == nil || modifier.TextureFrames.Width != 96 || modifier.TextureFrames.Height != 96 {
		t.Fatalf("modify_texture frame metadata was lost: %+v", modifier.TextureFrames)
	}
	if modifier.TextureBlendMode != "add" {
		t.Fatalf("modify_texture blend mode was lost: %+v", modifier)
	}
	if !modifier.TextureBlendSupported {
		t.Fatalf("known modify_texture blend mode was not marked supported: %+v", modifier)
	}
	if modifier.Semantics == nil || modifier.Semantics.RotateUV != "90" {
		t.Fatalf("modify_texture rotate_uv was not preserved: %+v", modifier.Semantics)
	}
}

func TestRenderGUIPreviewPreservesUnsupportedTextureBlendAsApproximate(t *testing.T) {
	root := GUIElement{
		Kind: "modify_texture", Size: &GUIVector{Width: "30", Height: "30"},
		Properties: []GUIProperty{{Name: "blend_mode", Value: "future_shader_mode"}},
	}
	result, err := RenderGUIPreview("unknown_blend", "element", "gui/test.gui", root, 200, 120, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].TextureBlendMode != "future_shader_mode" ||
		result.Nodes[0].TextureBlendSupported || !result.Nodes[0].Approximate {
		t.Fatalf("unsupported blend mode was not preserved conservatively: %+v", result.Nodes)
	}
	if !result.Approximate || len(result.Warnings) == 0 {
		t.Fatalf("unsupported blend mode did not make the preview explicitly approximate: %+v", result)
	}
}

func TestRenderGUIPreviewPreservesTextureFrameSemantics(t *testing.T) {
	root := GUIElement{
		Kind: "button", Size: &GUIVector{Width: "40", Height: "40"},
		Properties: []GUIProperty{
			{Name: "framesize", Values: []string{"12", "40"}},
			{Name: "frame", Value: "0"},
			{Name: "upframe", Value: "1"},
			{Name: "overframe", Value: "2"},
			{Name: "downframe", Value: "2"},
			{Name: "disableframe", Value: "1"},
			{Name: "spritetype", Value: "Corneredtiled"},
			{Name: "spriteborder", Values: []string{"0", "3"}},
			{Name: "texture_density", Value: "2"},
		},
	}
	result, err := RenderGUIPreview("frames", "type", "gui/test.gui", root, 200, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	frames := result.Nodes[0].TextureFrames
	if frames == nil || frames.Width != 12 || frames.Height != 40 ||
		frames.Frame == nil || *frames.Frame != 0 ||
		frames.UpFrame == nil || *frames.UpFrame != 1 ||
		frames.OverFrame == nil || *frames.OverFrame != 2 ||
		frames.DownFrame == nil || *frames.DownFrame != 2 ||
		frames.DisabledFrame == nil || *frames.DisabledFrame != 1 {
		t.Fatalf("texture frame metadata was not preserved: %+v", frames)
	}
	slice := result.Nodes[0].TextureSlice
	if slice == nil || slice.SpriteType != "corneredtiled" || slice.BorderX != 0 || slice.BorderY != 3 || slice.TextureDensity != 2 {
		t.Fatalf("texture slice metadata was not preserved: %+v", slice)
	}
}

func TestRenderGUIPreviewUsesProgressFillTextureAndRangeSemantics(t *testing.T) {
	root := GUIElement{
		Kind: "progressbar", Size: &GUIVector{Width: "200", Height: "20"},
		Properties: []GUIProperty{
			{Name: "min", Value: "0"},
			{Name: "max", Value: "100"},
			{Name: "value", Value: "35"},
		},
		Children: []GUIElement{{
			Kind: "block", Slot: "progress_textures",
			Properties: []GUIProperty{
				{Name: "progresstexture", Value: `"gfx/interface/progressbars/progress_standard.dds"`},
				{Name: "noprogresstexture", Value: `"gfx/interface/progressbars/progress_red.dds"`},
			},
		}},
	}
	result, err := RenderGUIPreview("progressbar_standard", "type", "gui/shared/progressbars.gui", root, 400, 120, 20)
	if err != nil {
		t.Fatal(err)
	}
	node := result.Nodes[0]
	if node.Texture != `"gfx/interface/progressbars/progress_standard.dds"` {
		t.Fatalf("progress fill texture was not selected: %#v", node)
	}
	if node.Semantics == nil || node.Semantics.Min != "0" || node.Semantics.Max != "100" || node.Semantics.Value != "35" ||
		node.Semantics.NoProgressTexture != `"gfx/interface/progressbars/progress_red.dds"` {
		t.Fatalf("progress range semantics were not preserved: %#v", node.Semantics)
	}
}

func TestRenderGUIPreviewFlowsHBoxAndDistributesExpand(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "300", Height: "50"},
		Properties: []GUIProperty{{Name: "spacing", Value: "10"}},
		Children: []GUIElement{
			{Kind: "button", Size: &GUIVector{Width: "40", Height: "20"}},
			{Kind: "expand", Size: &GUIVector{Height: "20"}},
		},
	}
	result, err := RenderGUIPreview("flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 3 {
		t.Fatalf("nodes=%d want 3: %+v", len(result.Nodes), result.Nodes)
	}
	first, second := result.Nodes[1].Bounds, result.Nodes[2].Bounds
	if first.X != 0 || first.Width != 40 || second.X != 50 || second.Width != 250 {
		t.Fatalf("unexpected hbox flow: first=%+v second=%+v", first, second)
	}
}

func TestRenderGUIPreviewWeightsExpandingFlowItems(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "300", Height: "50"},
		Children: []GUIElement{
			{Kind: "widget", Name: "short", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{
				{Name: "layoutpolicy_horizontal", Value: "expanding"},
				{Name: "layoutstretchfactor_horizontal", Value: "1"},
			}},
			{Kind: "widget", Name: "long", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{
				{Name: "layoutpolicy_horizontal", Value: "expanding"},
				{Name: "layoutstretchfactor_horizontal", Value: "2"},
			}},
		},
	}
	result, err := RenderGUIPreview("weighted_flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	first, second := result.Nodes[1], result.Nodes[2]
	if first.Bounds != (GUIPreviewRect{X: 0, Y: 0, Width: 100, Height: 20}) ||
		second.Bounds != (GUIPreviewRect{X: 100, Y: 0, Width: 200, Height: 20}) {
		t.Fatalf("weighted hbox flow: first=%+v second=%+v", first.Bounds, second.Bounds)
	}
	if first.Layout == nil || second.Layout == nil || first.Layout.StretchHorizontal != 1 || second.Layout.StretchHorizontal != 2 {
		t.Fatalf("stretch metadata: first=%+v second=%+v", first.Layout, second.Layout)
	}
	vertical := GUIElement{
		Kind: "vbox", Size: &GUIVector{Width: "50", Height: "300"},
		Children: []GUIElement{
			{Kind: "widget", Name: "short", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{
				{Name: "layoutpolicy_vertical", Value: "expanding"},
				{Name: "layoutstretchfactor_vertical", Value: "1"},
			}},
			{Kind: "widget", Name: "long", Size: &GUIVector{Width: "20", Height: "20"}, Properties: []GUIProperty{
				{Name: "layoutpolicy_vertical", Value: "expanding"},
				{Name: "layoutstretchfactor_vertical", Value: "2"},
			}},
		},
	}
	result, err = RenderGUIPreview("weighted_vertical_flow", "type", "gui/test.gui", vertical, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	first, second = result.Nodes[1], result.Nodes[2]
	if first.Bounds != (GUIPreviewRect{X: 0, Y: 0, Width: 20, Height: 100}) ||
		second.Bounds != (GUIPreviewRect{X: 0, Y: 100, Width: 20, Height: 200}) {
		t.Fatalf("weighted vbox flow: first=%+v second=%+v", first.Bounds, second.Bounds)
	}
	if first.Layout == nil || second.Layout == nil || first.Layout.StretchVertical != 1 || second.Layout.StretchVertical != 2 {
		t.Fatalf("vertical stretch metadata: first=%+v second=%+v", first.Layout, second.Layout)
	}
}

func TestRenderGUIPreviewDisplaysRawTextWithoutLocalizingIt(t *testing.T) {
	raw := strings.Repeat("x", 24)
	root := GUIElement{
		Kind:       "text_single",
		Properties: []GUIProperty{{Name: "raw_text", Value: raw}},
	}
	result, err := RenderGUIPreview("raw_caption", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	node := result.Nodes[0]
	if node.Text != "" || node.Semantics == nil || node.Semantics.RawText != raw {
		t.Fatalf("raw_text should stay separate from localization text: %+v", node)
	}
	if guiPreviewNodeDisplayText(node) != raw {
		t.Fatalf("raw_text display fallback=%q want %q", guiPreviewNodeDisplayText(node), raw)
	}
	if wantWidth := len([]rune(raw))*7 + 16; node.Bounds.Width != wantWidth {
		t.Fatalf("raw_text intrinsic width=%d want %d", node.Bounds.Width, wantWidth)
	}
}

func TestRenderGUIPreviewPreservesRawTooltipWithoutLocalizingIt(t *testing.T) {
	raw := "#X Not yet implemented button.#!"
	root := GUIElement{
		Kind:       "button",
		Name:       "raw_tooltip_owner",
		Properties: []GUIProperty{{Name: "raw_tooltip", Value: raw}},
	}
	result, err := RenderGUIPreview("raw_tooltip_owner", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	node := result.Nodes[0]
	if node.Semantics == nil || node.Semantics.RawTooltip != raw {
		t.Fatalf("raw_tooltip should stay separate from localization tooltip: %+v", node)
	}
	if node.TooltipLocalization != nil || guiPreviewTooltipText(node.Semantics) != raw {
		t.Fatalf("raw_tooltip was treated as a localization binding: %+v", node)
	}
	if got := guiPreviewTooltipText(&GUISemantics{Tooltip: "regular_tooltip_key", RawTooltip: raw}); got != "regular_tooltip_key" {
		t.Fatalf("regular tooltip must retain precedence when both forms exist: %q", got)
	}
}

func TestRenderGUIPreviewPreservesTooltipWhenDisabled(t *testing.T) {
	root := GUIElement{
		Kind: "button",
		Name: "disabled_action",
		Properties: []GUIProperty{
			{Name: "tooltip", Value: "normal_action_tooltip"},
			{Name: "tooltip_when_disabled", Value: "[DisabledActionReason]"},
		},
	}
	result, err := RenderGUIPreview("disabled_action", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	node := result.Nodes[0]
	if node.Semantics == nil || node.Semantics.Tooltip != "normal_action_tooltip" || node.Semantics.TooltipWhenDisabled != "[DisabledActionReason]" {
		t.Fatalf("tooltip_when_disabled was not preserved independently: %+v", node.Semantics)
	}
}

func TestRenderGUIPreviewPreservesTooltipVisible(t *testing.T) {
	root := GUIElement{
		Kind:       "button",
		Name:       "tooltip_owner",
		Size:       &GUIVector{Width: "120", Height: "30"},
		Properties: []GUIProperty{{Name: "tooltip", Value: "detail"}, {Name: "tooltip_visible", Value: "no"}},
	}
	result, err := RenderGUIPreview("tooltip_owner", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[0].Semantics == nil || result.Nodes[0].Semantics.TooltipVisible != "no" {
		t.Fatalf("tooltip_visible was not preserved: %+v", result.Nodes[0].Semantics)
	}
}

func TestRenderGUIPreviewPreservesChecked(t *testing.T) {
	root := GUIElement{
		Kind:       "checkbutton",
		Name:       "cloud_save",
		Size:       &GUIVector{Width: "30", Height: "30"},
		Properties: []GUIProperty{{Name: "checked", Value: "[CloudSaveData.ShouldSaveToCloud]"}},
	}
	result, err := RenderGUIPreview("cloud_save", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[0].Semantics == nil || result.Nodes[0].Semantics.Checked != "[CloudSaveData.ShouldSaveToCloud]" {
		t.Fatalf("checked was not preserved: %+v", result.Nodes[0].Semantics)
	}
}

func TestRenderGUIPreviewTreatsBackgroundAsFlowOverlay(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "200", Height: "50"},
		Children: []GUIElement{
			{Kind: "background", Texture: "gfx/interface/panel.dds"},
			{Kind: "button", Size: &GUIVector{Width: "40", Height: "20"}},
		},
	}
	result, err := RenderGUIPreview("background", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 3 {
		t.Fatalf("nodes=%d want 3: %+v", len(result.Nodes), result.Nodes)
	}
	background, button := result.Nodes[1].Bounds, result.Nodes[2].Bounds
	if background.Width != 200 || background.Height != 50 || button.X != 0 {
		t.Fatalf("background consumed hbox flow space: background=%+v button=%+v", background, button)
	}
}

func TestRenderGUIPreviewFlowsFlowcontainerHorizontallyByDefault(t *testing.T) {
	root := GUIElement{
		Kind: "flowcontainer",
		Children: []GUIElement{
			{Kind: "button_round", Name: "one"},
			{Kind: "button_round", Name: "two"},
			{Kind: "button_round", Name: "three"},
		},
	}
	result, err := RenderGUIPreview("default_flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeBounds.Width != 96 || result.NativeBounds.Height != 32 {
		t.Fatalf("default flowcontainer intrinsic bounds = %+v", result.NativeBounds)
	}
	if len(result.Nodes) != 4 {
		t.Fatalf("nodes=%d want 4: %+v", len(result.Nodes), result.Nodes)
	}
	for index, wantX := range []int{0, 32, 64} {
		child := result.Nodes[index+1].Bounds
		if child.X != wantX || child.Y != 0 || child.Width != 32 || child.Height != 32 {
			t.Fatalf("child %d bounds=%+v", index, child)
		}
	}
}

func TestRenderGUIPreviewHonorsVerticalFlowcontainerDirection(t *testing.T) {
	root := GUIElement{
		Kind:       "flowcontainer",
		Properties: []GUIProperty{{Name: "direction", Value: "vertical"}, {Name: "spacing", Value: "3"}},
		Children: []GUIElement{
			{Kind: "button_round", Name: "one"},
			{Kind: "button_round", Name: "two"},
		},
	}
	result, err := RenderGUIPreview("vertical_flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeBounds.Width != 32 || result.NativeBounds.Height != 67 {
		t.Fatalf("vertical flowcontainer intrinsic bounds = %+v", result.NativeBounds)
	}
	first, second := result.Nodes[1].Bounds, result.Nodes[2].Bounds
	if first.Y != 0 || second.Y != 35 || second.X != 0 {
		t.Fatalf("vertical flow positions: first=%+v second=%+v", first, second)
	}
}

func TestRenderGUIPreviewPreservesFlowReflowMetadata(t *testing.T) {
	root := GUIElement{
		Kind: "flowcontainer", Size: &GUIVector{Width: "140", Height: "40"},
		Properties: []GUIProperty{
			{Name: "ignoreinvisible", Value: "yes"},
			{Name: "spacing", Value: "4"},
		},
		Children: []GUIElement{{
			Kind: "button", Size: &GUIVector{Width: "30", Height: "20"},
			Properties: []GUIProperty{
				{Name: "margin_left", Value: "3"},
				{Name: "margin_right", Value: "5"},
				{Name: "layoutpolicy_vertical", Value: "expanding"},
			},
		}},
	}
	result, err := RenderGUIPreview("dynamic_flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 || result.Nodes[0].Layout == nil || result.Nodes[1].Layout == nil {
		t.Fatalf("flow layout metadata missing: %+v", result.Nodes)
	}
	container := result.Nodes[0].Layout
	if container.FlowDirection != "horizontal" || !container.IgnoreInvisible || container.Spacing != 4 {
		t.Fatalf("container flow metadata=%+v", container)
	}
	child := result.Nodes[1].Layout
	if !child.FlowItem || child.MarginLeft != 3 || child.MarginRight != 5 || !child.ExpandVertical {
		t.Fatalf("child flow metadata=%+v", child)
	}
}

func TestRenderGUIPreviewLaysOutBoundedGridRows(t *testing.T) {
	root := GUIElement{
		Kind: "fixedgridbox",
		Properties: []GUIProperty{
			{Name: "datamodel_wrap", Value: "2"},
			{Name: "addcolumn", Value: "40"},
			{Name: "addrow", Value: "30"},
			{Name: "flipdirection", Value: "yes"},
		},
		Children: []GUIElement{
			{Kind: "item", Name: "one", Size: &GUIVector{Width: "20", Height: "10"}},
			{Kind: "item", Name: "two", Size: &GUIVector{Width: "20", Height: "10"}},
			{Kind: "item", Name: "three", Size: &GUIVector{Width: "20", Height: "10"}},
		},
	}
	result, err := RenderGUIPreview("grid", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeBounds.Width != 60 || result.NativeBounds.Height != 40 || len(result.Nodes) != 4 {
		t.Fatalf("grid bounds/nodes are wrong: bounds=%+v nodes=%+v", result.NativeBounds, result.Nodes)
	}
	grid := result.Nodes[0].Layout
	if grid == nil || grid.GridColumns != 2 || grid.GridColumnStep != 40 || grid.GridRowStep != 30 || !grid.GridFlip {
		t.Fatalf("grid metadata=%+v", grid)
	}
	for index, expected := range []GUIPreviewRect{
		{X: 40, Y: 30, Width: 20, Height: 10},
		{X: 0, Y: 30, Width: 20, Height: 10},
		{X: 40, Y: 0, Width: 20, Height: 10},
	} {
		node := result.Nodes[index+1]
		if node.Bounds != expected || node.Layout == nil || !node.Layout.GridItem ||
			node.Layout.GridRow != []int{1, 1, 0}[index] || node.Layout.GridColumn != []int{1, 0, 1}[index] {
			t.Fatalf("grid item %d=%+v want bounds %+v", index, node, expected)
		}
	}
	for _, warning := range result.Warnings {
		if strings.Contains(strings.ToLower(warning), "flipdirection") {
			t.Fatalf("flipdirection should not remain an approximation warning: %v", result.Warnings)
		}
	}
}

func TestRenderGUIPreviewHonorsGridLayoutAnchor(t *testing.T) {
	root := GUIElement{
		Kind: "fixedgridbox", Size: &GUIVector{Width: "100", Height: "100"},
		Properties: []GUIProperty{
			{Name: "datamodel_wrap", Value: "2"},
			{Name: "addcolumn", Value: "40"},
			{Name: "addrow", Value: "30"},
			{Name: "flipdirection", Value: "yes"},
			{Name: "layoutanchor", Value: "bottomleft"},
		},
		Children: []GUIElement{
			{Kind: "item", Name: "one", Size: &GUIVector{Width: "20", Height: "10"}},
			{Kind: "item", Name: "two", Size: &GUIVector{Width: "20", Height: "10"}},
		},
	}
	result, err := RenderGUIPreview("anchored_grid", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Nodes[1].Bounds; got != (GUIPreviewRect{X: 40, Y: 70, Width: 20, Height: 10}) {
		t.Fatalf("bottomleft flipped first item=%+v", got)
	}
	if got := result.Nodes[2].Bounds; got != (GUIPreviewRect{X: 0, Y: 70, Width: 20, Height: 10}) {
		t.Fatalf("bottomleft flipped second item=%+v", got)
	}
	if result.Nodes[0].Layout == nil || result.Nodes[0].Layout.GridLayoutAnchor != "bottomleft" {
		t.Fatalf("grid layout anchor metadata=%+v", result.Nodes[0].Layout)
	}
}

func TestRenderGUIPreviewTreatsScrollboxAsClippedVerticalViewport(t *testing.T) {
	root := GUIElement{
		Kind: "scrollbox", Size: &GUIVector{Width: "100", Height: "50"},
		Children: []GUIElement{{
			Kind: "blockoverride", Name: "scrollbox_content",
			Children: []GUIElement{
				{Kind: "button", Name: "one", Size: &GUIVector{Width: "90", Height: "30"}},
				{Kind: "button", Name: "two", Size: &GUIVector{Width: "90", Height: "30"}},
				{Kind: "button", Name: "three", Size: &GUIVector{Width: "90", Height: "30"}},
			},
		}},
	}
	result, err := RenderGUIPreview("scrolling", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeBounds.Width != 100 || result.NativeBounds.Height != 50 {
		t.Fatalf("scroll content distorted viewport bounds: %+v", result.NativeBounds)
	}
	if len(result.Nodes) != 4 || result.Nodes[0].Layout == nil {
		t.Fatalf("scroll layout metadata missing: %+v", result.Nodes)
	}
	scroll := result.Nodes[0].Layout
	if !scroll.ScrollViewport || scroll.ScrollDirection != "vertical" || scroll.FlowDirection != "vertical" ||
		scroll.ScrollContentW != 100 || scroll.ScrollContentH != 90 || scroll.ScrollStep != 60 {
		t.Fatalf("scroll viewport metadata=%+v", scroll)
	}
	for index, wantY := range []int{0, 30, 60} {
		node := result.Nodes[index+1]
		if node.Bounds.Y != wantY || node.Layout == nil || !node.Layout.FlowItem {
			t.Fatalf("scroll item %d=%+v", index, node)
		}
		if node.ClipBounds == nil || *node.ClipBounds != (GUIPreviewRect{Width: 100, Height: 50}) {
			t.Fatalf("scroll item %d clip=%+v", index, node.ClipBounds)
		}
	}
}

func TestRenderGUIPreviewDoesNotShrinkExpandingScrollContent(t *testing.T) {
	root := GUIElement{
		Kind: "scrollbox", Size: &GUIVector{Width: "100", Height: "50"},
		Children: []GUIElement{{
			Kind: "vbox",
			Properties: []GUIProperty{
				{Name: "layoutpolicy_horizontal", Value: "expanding"},
				{Name: "layoutpolicy_vertical", Value: "expanding"},
			},
			Children: []GUIElement{
				{Kind: "button", Size: &GUIVector{Width: "90", Height: "30"}},
				{Kind: "button", Size: &GUIVector{Width: "90", Height: "30"}},
				{Kind: "button", Size: &GUIVector{Width: "90", Height: "30"}},
			},
		}},
	}
	result, err := RenderGUIPreview("scrolling_expand", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 5 {
		t.Fatalf("nodes=%d want 5: %+v", len(result.Nodes), result.Nodes)
	}
	if result.Nodes[1].Bounds.Height != 90 || result.Nodes[0].Layout.ScrollContentH != 90 {
		t.Fatalf("expanding scroll content was shrunk: viewport=%+v content=%+v", result.Nodes[0], result.Nodes[1])
	}
}

func TestRenderGUIPreviewPreservesAutoResizeTextConstraints(t *testing.T) {
	root := GUIElement{
		Kind: "scrollbox", Size: &GUIVector{Width: "325", Height: "300"},
		Children: []GUIElement{
			{
				Kind: "text_multi",
				Properties: []GUIProperty{
					{Name: "text", Value: "TEST_DESCRIPTION"},
					{Name: "autoresize", Value: "yes"},
					{Name: "min_width", Value: "275"},
					{Name: "max_width", Value: "275"},
				},
			},
			{Kind: "expand"},
		},
	}
	result, err := RenderGUIPreview("scrolling_text", "type", "gui/test.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 3 {
		t.Fatalf("nodes=%d want 3: %+v", len(result.Nodes), result.Nodes)
	}
	textNode := result.Nodes[1]
	if textNode.Bounds.Width != 275 || textNode.Layout == nil || !textNode.Layout.AutoResize || !textNode.Layout.Multiline ||
		textNode.Layout.MinWidth != 275 || textNode.Layout.MaxWidth != 275 {
		t.Fatalf("auto-resize text constraints=%+v", textNode)
	}
	if result.Nodes[2].Bounds.Height != 276 || result.Nodes[0].Layout.ScrollContentH != 300 {
		t.Fatalf("expand did not consume remaining scroll viewport: viewport=%+v expand=%+v", result.Nodes[0], result.Nodes[2])
	}
}

func TestRenderGUIPreviewTreatsTooltipWidgetAsOverlay(t *testing.T) {
	root := GUIElement{
		Kind: "widget",
		Children: []GUIElement{
			{Kind: "button", Name: "owner", Size: &GUIVector{Width: "50", Height: "20"}},
			{Kind: "tooltipwidget", Children: []GUIElement{{
				Kind: "widget", Name: "tooltip_panel", Size: &GUIVector{Width: "300", Height: "200"},
			}}},
		},
	}
	result, err := RenderGUIPreview("tooltip_overlay", "type", "gui/test.gui", root, 400, 240, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeBounds.Width != 50 || result.NativeBounds.Height != 20 {
		t.Fatalf("tooltip distorted normal native bounds: %+v", result.NativeBounds)
	}
	if len(result.Nodes) != 4 {
		t.Fatalf("nodes=%d want 4: %+v", len(result.Nodes), result.Nodes)
	}
	if result.Nodes[2].Overlay == nil || result.Nodes[2].Overlay.Role != "tooltip_root" || result.Nodes[2].Overlay.Owner != 0 {
		t.Fatalf("tooltip root metadata=%+v", result.Nodes[2].Overlay)
	}
	if result.Nodes[3].Overlay == nil || result.Nodes[3].Overlay.Role != "tooltip_content" || result.Nodes[3].Overlay.Owner != 0 {
		t.Fatalf("tooltip content metadata=%+v", result.Nodes[3].Overlay)
	}
}

func TestRenderGUIPreviewTreatsStateBlocksAsBehavior(t *testing.T) {
	root := GUIElement{
		Kind: "button", Name: "stateful", Size: &GUIVector{Width: "35", Height: "35"},
		Children: []GUIElement{
			{Kind: "state", Name: "_mouse_enter", Properties: []GUIProperty{{Name: "alpha", Value: "1"}, {Name: "scale", Value: "1.35"}, {Name: "position_x", Value: "-4"}, {Name: "position_y", Value: "3"}, {Name: "duration", Value: "0.7"}}},
			{Kind: "state", Name: "_mouse_leave", Properties: []GUIProperty{{Name: "alpha", Value: "0.5"}, {Name: "scale", Value: "1.3"}, {Name: "duration", Value: "0.2"}}},
		},
	}
	result, err := RenderGUIPreview("stateful", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 3 || result.NativeBounds.Width != 35 || result.NativeBounds.Height != 35 {
		t.Fatalf("state blocks distorted visual bounds: bounds=%+v nodes=%+v", result.NativeBounds, result.Nodes)
	}
	for index, name := range []string{"_mouse_enter", "_mouse_leave"} {
		node := result.Nodes[index+1]
		if !node.BehaviorOnly || node.StateDefinition == nil || node.StateDefinition.Name != name || node.StateDefinition.Scale == "" {
			t.Fatalf("state node %d was not preserved as behavior: %+v", index+1, node)
		}
	}
	if result.Nodes[1].StateDefinition.PositionX != "-4" || result.Nodes[1].StateDefinition.PositionY != "3" {
		t.Fatalf("state position was not preserved: %+v", result.Nodes[1].StateDefinition)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(result, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if htmlPreview.Behaviors.States != 2 || !bytes.Contains([]byte(htmlPreview.Document), []byte(`data-ck3-state-name="_mouse_enter"`)) || !bytes.Contains([]byte(htmlPreview.Document), []byte(`data-ck3-state-scale="1.35"`)) || !bytes.Contains([]byte(htmlPreview.Document), []byte(`data-ck3-state-position-x="-4"`)) || !bytes.Contains([]byte(htmlPreview.Document), []byte(`data-ck3-state-position-y="3"`)) || !bytes.Contains([]byte(htmlPreview.Document), []byte(`style.setProperty('--ck3-state-offset-x'`)) || !bytes.Contains([]byte(htmlPreview.Document), []byte(`id="ck3-apply-state"`)) {
		t.Fatalf("state behavior is missing from inspector: %+v", htmlPreview.Behaviors)
	}
}

func TestRenderGUIPreviewPreservesStateTriggerWhen(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: "stateful", Size: &GUIVector{Width: "100", Height: "40"},
		Children: []GUIElement{{
			Kind: "state", Name: "hide_when_modal", Properties: []GUIProperty{
				{Name: "alpha", Value: "0"},
				{Name: "scale", Value: "0.9"},
				{Name: "trigger_when", Value: "[IsModalOpen]"},
			},
		}},
	}
	result, err := RenderGUIPreview("stateful", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 || result.Nodes[1].StateDefinition == nil || result.Nodes[1].StateDefinition.TriggerWhen != "[IsModalOpen]" {
		t.Fatalf("state trigger_when was not preserved: %+v", result.Nodes)
	}
}

func TestRenderGUIPreviewPreservesPressedStateAndRepeatedClickProperties(t *testing.T) {
	root := GUIElement{
		Kind: "button", Name: "traveling",
		Properties: []GUIProperty{
			{Name: "down", Value: "[IsGameViewOpen('travel_planner')]"},
			{Name: "selected", Value: "[Character.IsTraveling]"},
			{Name: "onclick", Value: "[ToggleGameViewData('travel_planner', TravelPlan.GetID)]"},
			{Name: "onclick", Value: "[Character.ZoomCameraTo]"},
			{Name: "onrightclick", Value: "[DefaultOnCharacterRightClick(Character.GetID)]"},
		},
	}
	preview, err := RenderGUIPreview("traveling", "element", "gui/hud.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil {
		t.Fatalf("missing preview semantics: %#v", preview.Nodes)
	}
	semantics := preview.Nodes[0].Semantics
	if semantics.Down == "" || semantics.Selected == "" || semantics.OnClick != "[Character.ZoomCameraTo]" {
		t.Fatalf("pressed or legacy click semantics changed: %#v", semantics)
	}
	if len(semantics.OnClicks) != 2 || semantics.OnClicks[0] != "[ToggleGameViewData('travel_planner', TravelPlan.GetID)]" {
		t.Fatalf("repeated onclick properties were not preserved in source order: %#v", semantics.OnClicks)
	}
	if semantics.OnRightClick != "[DefaultOnCharacterRightClick(Character.GetID)]" {
		t.Fatalf("portrait-style onrightclick semantics were not preserved: %#v", semantics)
	}
}

func TestRenderGUIPreviewPreservesSelectedIndexAsModelMetadata(t *testing.T) {
	root := GUIElement{
		Kind: "dropdown", Name: "sort_order",
		Properties: []GUIProperty{
			{Name: "datamodel", Value: "[SortOptions]"},
			{Name: "selectedindex", Value: "[CurrentSortIndex]"},
		},
	}
	preview, err := RenderGUIPreview("sort_order", "element", "gui/settings.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.SelectedIndex != "[CurrentSortIndex]" {
		t.Fatalf("dropdown selectedindex was not retained as model metadata: %#v", preview.Nodes)
	}
}

func TestRenderGUIPreviewUsesFontColorAsFontTintFallback(t *testing.T) {
	root := GUIElement{
		Kind: "text_single", Name: "track_name",
		Properties: []GUIProperty{
			{Name: "raw_text", Value: "Track"},
			{Name: "fontcolor", Value: "[MusicPlayer.GetTrackTextColor]"},
		},
	}
	preview, err := RenderGUIPreview("track_name", "element", "gui/music.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.FontTintColor != "[MusicPlayer.GetTrackTextColor]" {
		t.Fatalf("fontcolor did not enter the text-color preview path: %#v", preview.Nodes)
	}
}

func TestRenderGUIPreviewUsesParsedFontColorVectorAsFontTintFallback(t *testing.T) {
	model := BuildGUIModel(`types Defaults {
	type textbox = textbox {
		fontcolor = { 0.61 0.6 0.56 1.0 }
	}
}`)
	root := model.Namespaces[0].Types[0].Element
	preview, err := RenderGUIPreview("textbox", "type", "gui/preload/defaults.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.FontTintColor != "{ 0.61 0.6 0.56 1.0 }" {
		t.Fatalf("parsed fontcolor vector did not enter the text-color preview path: %#v", preview.Nodes)
	}
}

func TestRenderGUIPreviewRendersLiteralLineGeometry(t *testing.T) {
	root := GUIElement{
		Kind: "container", Size: &GUIVector{Width: "200", Height: "120"},
		Children: []GUIElement{{
			Kind: "line", Properties: []GUIProperty{
				{Name: "parentanchor", Value: "hcenter"},
				{Name: "from", Values: []string{"0", "-20"}},
				{Name: "to", Values: []string{"0", "30"}},
				{Name: "width", Value: "6"},
				{Name: "line_cap", Value: "yes"},
				{Name: "color", Values: []string{"0.2", "0.4", "0.6", "1"}},
			},
		}},
	}
	preview, err := RenderGUIPreview("line", "element", "gui/shared/lines.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 2 {
		t.Fatalf("nodes=%d want 2: %#v", len(preview.Nodes), preview.Nodes)
	}
	line := preview.Nodes[1]
	if line.LineGeometry == nil || line.LineGeometry.From == nil || line.LineGeometry.To == nil {
		t.Fatalf("literal line geometry was not retained: %#v", line)
	}
	if line.LineGeometry.Origin != (GUIPreviewPoint{X: 100, Y: 0}) || *line.LineGeometry.From != (GUIPreviewPoint{X: 100, Y: -20}) || *line.LineGeometry.To != (GUIPreviewPoint{X: 100, Y: 30}) || line.LineGeometry.Width != 6 || !line.LineGeometry.LineCap || line.LineGeometry.CoordinateSpace != "parent_anchor" {
		t.Fatalf("line geometry=%#v", line.LineGeometry)
	}
	if line.Bounds != (GUIPreviewRect{X: 97, Y: -23, Width: 6, Height: 56}) {
		t.Fatalf("line bounds=%+v", line.Bounds)
	}
	if line.Semantics == nil || line.Semantics.LineFrom != "{ 0 -20 }" || line.Semantics.LineTo != "{ 0 30 }" {
		t.Fatalf("line source endpoints were not retained: %#v", line.Semantics)
	}
	if !line.Approximate || len(preview.PNG) == 0 {
		t.Fatalf("line preview should remain explicitly approximate and render PNG output: %#v", line)
	}
}

func TestRenderGUIPreviewRejectsUnboundedDimensions(t *testing.T) {
	_, err := RenderGUIPreview("bad", "type", "", GUIElement{Kind: "widget"}, GUIPreviewMaxWidth+1, 720, 20)
	if err == nil {
		t.Fatal("expected oversized GUI preview to fail")
	}
}
