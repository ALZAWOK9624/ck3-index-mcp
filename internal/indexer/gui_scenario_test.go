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
		"bad property": {{Property: "state", Expression: "[State]", Value: "x"}},
		"bad boolean":  {{Property: "visible", Expression: "[Show]", Value: "maybe"}},
		"bad texture":  {{Property: "texture", Expression: "[Portrait]", Value: "https://example.invalid/portrait.png"}},
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
