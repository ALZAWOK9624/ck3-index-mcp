package indexer

import (
	"fmt"
	"strings"
	"testing"
)

func TestApplyGUIPreviewScenarioUsesExactProvidedSamples(t *testing.T) {
	root := GUIElement{
		Kind: "vbox", Size: &GUIVector{Width: "360", Height: "180"},
		Children: []GUIElement{
			{Kind: "button", Name: "first", Properties: []GUIProperty{
				{Name: "visible", Value: "[ShowRows]"}, {Name: "enabled", Value: "[CanAct]"}, {Name: "raw_text", Value: "[GetValue]"},
			}},
			{Kind: "button", Name: "second", Properties: []GUIProperty{{Name: "visible", Value: "[ShowRows]"}}},
			{Kind: "icon", Name: "portrait", Texture: "[Character.GetPortraitTexture]"},
		},
	}
	preview, err := RenderGUIPreview("scenario", "type", "gui/scenario.gui", root, 800, 450, 20)
	if err != nil {
		t.Fatal(err)
	}
	err = applyGUIPreviewScenario(&preview, []GUIScenarioSample{
		{Property: "visible", Expression: "[ShowRows]", Value: "false"},
		{Property: "enabled", Expression: "[CanAct]", Value: "false"},
		{Property: "text", Expression: "[GetValue]", Value: "Provided value 42"},
		{Property: "texture", Expression: "[Character.GetPortraitTexture]", Value: "gfx/interface/portraits/example.dds"},
		{Property: "text", Expression: "[Missing]", Value: "unused"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Scenario.Source != "provided" || preview.Scenario.Applied != 4 || preview.Scenario.Unused != 1 {
		t.Fatalf("unexpected scenario summary: %+v", preview.Scenario)
	}
	if preview.Scenario.Samples[0].MatchedNodes != 2 || preview.Nodes[1].Scenario == nil || preview.Nodes[2].Scenario == nil {
		t.Fatalf("shared expression did not update both exact matches: %+v", preview.Scenario.Samples)
	}
	if preview.Nodes[1].Scenario.Text == nil || *preview.Nodes[1].Scenario.Text != "Provided value 42" || guiPreviewNodeDisplayText(preview.Nodes[1]) != "Provided value 42" {
		t.Fatalf("provided text sample did not become the preview value: %+v", preview.Nodes[1].Scenario)
	}
	if preview.Nodes[1].Scenario.Visible == nil || *preview.Nodes[1].Scenario.Visible || preview.Nodes[1].Scenario.Enabled == nil || *preview.Nodes[1].Scenario.Enabled {
		t.Fatalf("provided boolean samples were not applied: %+v", preview.Nodes[1].Scenario)
	}
	if preview.Nodes[3].Semantics == nil || preview.Nodes[3].Semantics.RawTexture != "[Character.GetPortraitTexture]" ||
		preview.Nodes[3].Scenario == nil || preview.Nodes[3].Scenario.Texture == nil ||
		*preview.Nodes[3].Scenario.Texture != "gfx/interface/portraits/example.dds" ||
		preview.Nodes[3].Texture != "gfx/interface/portraits/example.dds" {
		t.Fatalf("provided texture sample did not preserve provenance and replace the render path: %+v", preview.Nodes[3])
	}

	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-scenario-source="provided"`, `data-sim-visible="false"`,
		`data-sim-enabled="false"`, `data-sim-text="Provided value 42"`,
		`data-ck3-raw-texture="[Character.GetPortraitTexture]"`,
		`data-ck3-scenario-texture="gfx/interface/portraits/example.dds"`,
		`is-sim-hidden is-sim-disabled`, `id="ck3-detail-scenario"`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("scenario inspector missing %q", expected)
		}
	}
}

func TestApplyGUIPreviewScenarioUsesTextureSampleForPortraitTexture(t *testing.T) {
	const portraitExpression = "[Character.GetAnimatedPortrait('environment_hud', 'camera_hud', 'idle', PdxGetWidgetScreenSize(PdxGuiWidget.Self))]"
	root := GUIElement{
		Kind: "portrait_button", Name: "portrait", Texture: "gfx/portraits/portrait_transparent.dds",
		Properties: []GUIProperty{{Name: "portrait_texture", Value: portraitExpression}},
	}
	preview, err := RenderGUIPreview("portrait", "element", "gui/shared/portraits.gui", root, 320, 240, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.PortraitTexture != portraitExpression {
		t.Fatalf("portrait texture expression was not preserved: %#v", preview.Nodes)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{{
		Property: "texture", Expression: portraitExpression, Value: "gfx/interface/portraits/example.dds",
	}}); err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if node.Scenario == nil || node.Scenario.Texture == nil || *node.Scenario.Texture != "gfx/interface/portraits/example.dds" || node.Texture != "gfx/interface/portraits/example.dds" {
		t.Fatalf("portrait texture sample did not replace the render texture: %#v", node)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-portrait-texture="[Character.GetAnimatedPortrait(&#39;environment_hud&#39;, &#39;camera_hud&#39;, &#39;idle&#39;, PdxGetWidgetScreenSize(PdxGuiWidget.Self))]"`,
		`data-ck3-scenario-texture="gfx/interface/portraits/example.dds"`,
		`portrait_texture [Character.GetAnimatedPortrait`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("portrait texture inspector missing %q", expected)
		}
	}
}

func TestApplyGUIPreviewScenarioUsesPosterSampleForVideo(t *testing.T) {
	const videoExpression = "[EventWindowBackgroundData.GetVideo]"
	root := GUIElement{
		Kind: "video_icon", Name: "event_video", Size: &GUIVector{Width: "320", Height: "180"},
		Properties: []GUIProperty{{Name: "video", Value: videoExpression}, {Name: "loop", Value: "no"}},
	}
	preview, err := RenderGUIPreview("event_video", "element", "gui/shared/event_windows.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Semantics == nil || preview.Nodes[0].Semantics.Video != videoExpression {
		t.Fatalf("video expression was not preserved: %#v", preview.Nodes)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{{
		Property: "video", Expression: videoExpression, Value: "gfx/interface/event_windows/video_poster.dds",
	}}); err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if node.Scenario == nil || !node.Scenario.Video || node.Scenario.Texture == nil || *node.Scenario.Texture != "gfx/interface/event_windows/video_poster.dds" || node.Texture != "gfx/interface/event_windows/video_poster.dds" {
		t.Fatalf("video poster sample did not replace the preview texture: %#v", node)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-video="[EventWindowBackgroundData.GetVideo]"`,
		`data-ck3-scenario-texture="gfx/interface/event_windows/video_poster.dds"`,
		`data-ck3-scenario-video="true"`,
		`video [EventWindowBackgroundData.GetVideo]`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("video poster inspector missing %q", expected)
		}
	}
}

func TestApplyGUIPreviewScenarioUsesTextureSampleForCoatOfArms(t *testing.T) {
	const coatOfArmsExpression = "[Title.GetTitleCoA.GetTexture('(int32)256','(int32)256')]"
	const coatOfArmsMaskExpression = "[GovernmentType.GetRealmMask]"
	const coatOfArmsOffsetExpression = "[GovernmentType.GetRealmMaskOffset]"
	const coatOfArmsScaleExpression = "[GovernmentType.GetRealmMaskScale]"
	root := GUIElement{
		Kind: "coat_of_arms_icon", Name: "title_arms", Texture: "gfx/interface/coat_of_arms/empty.dds",
		Properties: []GUIProperty{
			{Name: "coat_of_arms", Value: coatOfArmsExpression},
			{Name: "coat_of_arms_mask", Value: coatOfArmsMaskExpression},
			{Name: "coat_of_arms_offset", Value: coatOfArmsOffsetExpression},
			{Name: "coat_of_arms_scale", Value: coatOfArmsScaleExpression},
		},
	}
	preview, err := RenderGUIPreview("title_arms", "element", "gui/shared/coat_of_arms.gui", root, 320, 240, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{
		{Property: "texture", Expression: coatOfArmsExpression, Value: "gfx/interface/coat_of_arms/title_sample.dds"},
		{Property: "coat_of_arms_mask", Expression: coatOfArmsMaskExpression, Value: "gfx/interface/coat_of_arms/title_mask.dds"},
		{Property: "coat_of_arms_offset", Expression: coatOfArmsOffsetExpression, Value: "{ 0.0 0.07 }"},
		{Property: "coat_of_arms_scale", Expression: coatOfArmsScaleExpression, Value: "{ 0.9 0.9 }"},
	}); err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if node.Scenario == nil || node.Scenario.Texture == nil || *node.Scenario.Texture != "gfx/interface/coat_of_arms/title_sample.dds" || node.Texture != "gfx/interface/coat_of_arms/title_sample.dds" {
		t.Fatalf("coat-of-arms sample did not replace the render texture: %#v", node)
	}
	if !node.Scenario.CoatOfArms || node.Scenario.CoatOfArmsMask == nil || *node.Scenario.CoatOfArmsMask != "gfx/interface/coat_of_arms/title_mask.dds" || node.CoatOfArmsMask != "gfx/interface/coat_of_arms/title_mask.dds" || node.CoatOfArmsOffset == nil || node.CoatOfArmsOffset.X != "0" || node.CoatOfArmsOffset.Y != "0.07" || node.CoatOfArmsScale == nil || node.CoatOfArmsScale.X != "0.9" || node.CoatOfArmsScale.Y != "0.9" {
		t.Fatalf("coat-of-arms composition samples were not applied: %#v", node)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-coat-of-arms="[Title.GetTitleCoA.GetTexture(&#39;(int32)256&#39;,&#39;(int32)256&#39;)]"`,
		`data-ck3-scenario-texture="gfx/interface/coat_of_arms/title_sample.dds"`,
		`data-ck3-scenario-coat-of-arms="true"`,
		`data-ck3-scenario-coat-of-arms-mask="gfx/interface/coat_of_arms/title_mask.dds"`,
		`data-ck3-scenario-coat-of-arms-offset="{ 0 0.07 }"`,
		`data-ck3-scenario-coat-of-arms-scale="{ 0.9 0.9 }"`,
		`coat_of_arms [Title.GetTitleCoA.GetTexture`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("coat-of-arms inspector missing %q", expected)
		}
	}
}

func TestApplyGUIPreviewScenarioUsesExactLineEndpointSamples(t *testing.T) {
	const fromExpression = "[TreeConnection.GetLineFrom]"
	const toExpression = "[TreeConnection.GetLineTo]"
	root := GUIElement{
		Kind: "container", Size: &GUIVector{Width: "200", Height: "120"},
		Children: []GUIElement{{
			Kind: "line", Properties: []GUIProperty{
				{Name: "parentanchor", Value: "hcenter"},
				{Name: "from", Value: fromExpression},
				{Name: "to", Value: toExpression},
				{Name: "width", Value: "4"},
				{Name: "line_cap", Value: "yes"},
			},
		}},
	}
	preview, err := RenderGUIPreview("tree_line", "element", "gui/window_character_lifestyle.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{
		{Property: "from", Expression: fromExpression, Value: "{ -40 15 }"},
		{Property: "to", Expression: toExpression, Value: "{ 70 45 }"},
	}); err != nil {
		t.Fatal(err)
	}
	line := preview.Nodes[1]
	if line.Scenario == nil || line.Scenario.LineFrom == nil || line.Scenario.LineTo == nil || line.LineGeometry == nil || line.LineGeometry.From == nil || line.LineGeometry.To == nil {
		t.Fatalf("line endpoint samples were not applied: %#v", line)
	}
	if *line.LineGeometry.From != (GUIPreviewPoint{X: 60, Y: 15}) || *line.LineGeometry.To != (GUIPreviewPoint{X: 170, Y: 45}) {
		t.Fatalf("sampled line endpoints=%#v", line.LineGeometry)
	}
	if line.Bounds != (GUIPreviewRect{X: 58, Y: 13, Width: 114, Height: 34}) {
		t.Fatalf("sampled line bounds=%+v", line.Bounds)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`class="ck3-line-stroke ck3-line-cap"`,
		`data-ck3-scenario-from="{ -40 15 }"`,
		`data-ck3-scenario-to="{ 70 45 }"`,
		`data-ck3-line-coordinate-space="parent_anchor"`,
		`data-ck3-line-from="{ `,
		`data-ck3-line-to="{ `,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("line scenario inspector missing %q", expected)
		}
	}
}

func BenchmarkApplyGUIPreviewScenario(b *testing.B) {
	root := GUIElement{Kind: "vbox", Size: &GUIVector{Width: "800", Height: "700"}}
	for index := 0; index < 200; index++ {
		root.Children = append(root.Children, GUIElement{Kind: "button", Name: fmt.Sprintf("row_%d", index), Properties: []GUIProperty{
			{Name: "visible", Value: "[RowsVisible]"}, {Name: "enabled", Value: "[RowsEnabled]"}, {Name: "raw_text", Value: "[RowLabel]"},
		}})
	}
	base, err := RenderGUIPreview("scenario_bench", "type", "gui/bench.gui", root, 1280, 720, 250)
	if err != nil {
		b.Fatal(err)
	}
	samples := []GUIScenarioSample{
		{Property: "visible", Expression: "[RowsVisible]", Value: "true"},
		{Property: "enabled", Expression: "[RowsEnabled]", Value: "false"},
		{Property: "text", Expression: "[RowLabel]", Value: "Provided row"},
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		preview := base
		preview.Nodes = append([]GUIPreviewNode(nil), base.Nodes...)
		if err := applyGUIPreviewScenario(&preview, samples); err != nil {
			b.Fatal(err)
		}
	}
}

func TestApplyGUIPreviewScenarioRejectsAmbiguousOrInvalidSamples(t *testing.T) {
	preview := GUIPreviewResult{}
	for name, samples := range map[string][]GUIScenarioSample{
		"bad property":     {{Property: "state", Expression: "[State]", Value: "x"}},
		"bad boolean":      {{Property: "visible", Expression: "[Show]", Value: "maybe"}},
		"bad texture":      {{Property: "texture", Expression: "[Portrait]", Value: "https://example.invalid/portrait.png"}},
		"bad video poster": {{Property: "video", Expression: "[Event.GetVideo]", Value: "gfx/interface/animation/poster.bk2"}},
		"bad coat vector":  {{Property: "coat_of_arms_offset", Expression: "[Government.GetRealmMaskOffset]", Value: "0;display:none"}},
		"bad line vector":  {{Property: "from", Expression: "[Tree.GetLineFrom]", Value: "0;display:none"}},
		"duplicate": {
			{Property: "text", Expression: "[Name]", Value: "one"},
			{Property: "text", Expression: "[Name]", Value: "two"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := applyGUIPreviewScenario(&preview, samples); err == nil {
				t.Fatal("invalid GUI scenario was accepted")
			}
		})
	}
}
