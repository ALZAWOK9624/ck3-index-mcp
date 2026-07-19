package indexer

import (
	"fmt"
	"strings"
	"testing"
)

func TestGUIModelSamplesInstantiateAndIsolateGridRows(t *testing.T) {
	root := GUIElement{
		Kind: "dynamicgridbox",
		Name: "tradition_grid",
		Properties: []GUIProperty{
			{Name: "datamodel", Value: "[Grouping.GetTraditions]"},
			{Name: "datamodel_wrap", Value: "2"},
			{Name: "addcolumn", Value: "120"},
			{Name: "addrow", Value: "44"},
		},
		Children: []GUIElement{{
			Kind: "item",
			Children: []GUIElement{{
				Kind: "button", Name: "tradition",
				Size: &GUIVector{Width: "110", Height: "36"},
				Properties: []GUIProperty{
					{Name: "visible", Value: "[Tradition.IsVisible]"},
					{Name: "enabled", Value: "[Tradition.CanPick]"},
					{Name: "onclick", Value: "[Tradition.Select]"},
				},
				Children: []GUIElement{{
					Kind: "text_single", Name: "name",
					Properties: []GUIProperty{{Name: "text", Value: "[Tradition.GetName]"}},
				}},
			}},
		}},
	}
	collections := []GUIModelSampleCollection{{
		Target: "tradition_grid", DataModel: "[Grouping.GetTraditions]",
		Rows: []GUIModelSampleRow{
			{ID: "stalwart", Samples: []GUIScenarioSample{
				{Property: "text", Expression: "[Tradition.GetName]", Value: "Stalwart Defenders"},
				{Property: "visible", Expression: "[Tradition.IsVisible]", Value: "true"},
				{Property: "enabled", Expression: "[Tradition.CanPick]", Value: "true"},
			}},
			{ID: "maritime", Samples: []GUIScenarioSample{
				{Property: "text", Expression: "[Tradition.GetName]", Value: "Maritime Mercantilism"},
				{Property: "visible", Expression: "[Tradition.IsVisible]", Value: "true"},
				{Property: "enabled", Expression: "[Tradition.CanPick]", Value: "false"},
			}},
			{ID: "hidden", Samples: []GUIScenarioSample{
				{Property: "text", Expression: "[Tradition.GetName]", Value: "Hidden Tradition"},
				{Property: "visible", Expression: "[Tradition.IsVisible]", Value: "false"},
			}},
		},
	}}
	prepared, err := prepareGUIModelSamples("traditions", &root, collections)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 3 {
		t.Fatalf("item template was not expanded to three rows: %+v", root.Children)
	}
	preview, err := RenderGUIPreview("traditions", "element", "gui/test.gui", root, 600, 300, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{{
		Property: "text", Expression: "[Tradition.GetName]", Value: "global fallback",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewModelSamples(&preview, prepared); err != nil {
		t.Fatal(err)
	}
	if preview.ModelSamples == nil || preview.ModelSamples.Source != "provided" ||
		preview.ModelSamples.AppliedCollections != 1 || preview.ModelSamples.AppliedRows != 3 ||
		preview.ModelSamples.AppliedSamples != 8 || preview.ModelSamples.UnusedSamples != 0 {
		t.Fatalf("unexpected model sample report: %+v", preview.ModelSamples)
	}

	rowText := map[string]string{}
	rowVisible := map[string]*bool{}
	rowEnabled := map[string]*bool{}
	rowRootBounds := map[string]GUIPreviewRect{}
	for _, node := range preview.Nodes {
		if node.ModelRow == nil {
			continue
		}
		if node.ModelRow.Source != "provided" || node.ModelRow.Collection != 0 {
			t.Fatalf("model row provenance missing: %+v", node.ModelRow)
		}
		if node.Kind == "item" {
			rowRootBounds[node.ModelRow.ID] = node.Bounds
		}
		if node.Scenario != nil {
			if node.Scenario.Text != nil {
				rowText[node.ModelRow.ID] = *node.Scenario.Text
			}
			if node.Scenario.Visible != nil {
				rowVisible[node.ModelRow.ID] = node.Scenario.Visible
			}
			if node.Scenario.Enabled != nil {
				rowEnabled[node.ModelRow.ID] = node.Scenario.Enabled
			}
		}
	}
	if rowText["stalwart"] != "Stalwart Defenders" || rowText["maritime"] != "Maritime Mercantilism" || rowText["hidden"] != "Hidden Tradition" {
		t.Fatalf("row-local text did not override the global sample: %+v", rowText)
	}
	if rowVisible["hidden"] == nil || *rowVisible["hidden"] {
		t.Fatalf("hidden row visibility was not isolated: %+v", rowVisible)
	}
	if rowEnabled["maritime"] == nil || *rowEnabled["maritime"] {
		t.Fatalf("maritime row enabled state was not isolated: %+v", rowEnabled)
	}
	if rowRootBounds["stalwart"].X != 0 || rowRootBounds["stalwart"].Y != 0 ||
		rowRootBounds["maritime"].X != 120 || rowRootBounds["maritime"].Y != 0 ||
		rowRootBounds["hidden"].X != 0 || rowRootBounds["hidden"].Y != 44 {
		t.Fatalf("expanded rows did not enter the grid: %+v", rowRootBounds)
	}
}

func TestGUIModelSamplesTextureOverrideStaysRowLocal(t *testing.T) {
	root := GUIElement{
		Kind: "dynamicgridbox", Name: "portraits",
		Properties: []GUIProperty{{Name: "datamodel", Value: "[People]"}},
		Children: []GUIElement{{
			Kind: "item",
			Children: []GUIElement{{
				Kind: "icon", Name: "portrait", Texture: "[Person.GetPortraitTexture]",
			}},
		}},
	}
	collections := []GUIModelSampleCollection{{
		Target: "portraits",
		Rows: []GUIModelSampleRow{
			{ID: "alice", Samples: []GUIScenarioSample{{
				Property: "texture", Expression: "[Person.GetPortraitTexture]", Value: "gfx/interface/portraits/alice.dds",
			}}},
			{ID: "bob", Samples: []GUIScenarioSample{{
				Property: "texture", Expression: "[Person.GetPortraitTexture]", Value: "gfx/interface/portraits/bob.dds",
			}}},
		},
	}}
	prepared, err := prepareGUIModelSamples("portraits", &root, collections)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := RenderGUIPreview("portraits", "type", "gui/test.gui", root, 400, 200, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewScenario(&preview, []GUIScenarioSample{{
		Property: "texture", Expression: "[Person.GetPortraitTexture]", Value: "gfx/interface/portraits/fallback.dds",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewModelSamples(&preview, prepared); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, node := range preview.Nodes {
		if node.ModelRow != nil && node.Name == "portrait" {
			got[node.ModelRow.ID] = node.Texture
			if node.Semantics == nil || node.Semantics.RawTexture != "[Person.GetPortraitTexture]" {
				t.Fatalf("row %s lost the original dynamic texture expression: %+v", node.ModelRow.ID, node)
			}
		}
	}
	if got["alice"] != "gfx/interface/portraits/alice.dds" || got["bob"] != "gfx/interface/portraits/bob.dds" {
		t.Fatalf("row-local texture samples did not override the global fallback: %+v", got)
	}
}

func TestGUIModelSamplesRejectAmbiguousOrUnsafeInputs(t *testing.T) {
	t.Run("ambiguous datamodel", func(t *testing.T) {
		root := GUIElement{Kind: "vbox", Children: []GUIElement{
			modelSampleTestGrid("one", "[Rows]"),
			modelSampleTestGrid("two", "[Rows]"),
		}}
		_, err := prepareGUIModelSamples("root", &root, []GUIModelSampleCollection{{
			DataModel: "[Rows]",
			Rows: []GUIModelSampleRow{{ID: "row", Samples: []GUIScenarioSample{{
				Property: "text", Expression: "[Row.Name]", Value: "One",
			}}}},
		}})
		if err == nil || !strings.Contains(err.Error(), "disambiguate") {
			t.Fatalf("ambiguous datamodel error=%v", err)
		}
	})

	t.Run("duplicate row id", func(t *testing.T) {
		root := modelSampleTestGrid("grid", "[Rows]")
		_, err := prepareGUIModelSamples("root", &root, []GUIModelSampleCollection{{
			Target: "grid",
			Rows: []GUIModelSampleRow{
				{ID: "same", Samples: []GUIScenarioSample{{Property: "text", Expression: "[Row.Name]", Value: "One"}}},
				{ID: "same", Samples: []GUIScenarioSample{{Property: "text", Expression: "[Row.Name]", Value: "Two"}}},
			},
		}})
		if err == nil || !strings.Contains(err.Error(), "repeats row id") {
			t.Fatalf("duplicate row id error=%v", err)
		}
	})

	t.Run("multiple item templates", func(t *testing.T) {
		root := modelSampleTestGrid("grid", "[Rows]")
		root.Children = append(root.Children, root.Children[0])
		_, err := prepareGUIModelSamples("root", &root, []GUIModelSampleCollection{{
			Target: "grid",
			Rows: []GUIModelSampleRow{{ID: "row", Samples: []GUIScenarioSample{{
				Property: "text", Expression: "[Row.Name]", Value: "One",
			}}}},
		}})
		if err == nil || !strings.Contains(err.Error(), "exactly one item template") {
			t.Fatalf("multiple item template error=%v", err)
		}
	})
}

func TestGUIHTMLInspectorExposesModelRowsAndScopesInteractiveMatching(t *testing.T) {
	root := modelSampleTestGrid("grid", "[Rows]")
	prepared, err := prepareGUIModelSamples("root", &root, []GUIModelSampleCollection{{
		Target: "grid",
		Rows: []GUIModelSampleRow{
			{ID: "first", Samples: []GUIScenarioSample{{Property: "text", Expression: "[Row.Name]", Value: "First"}}},
			{ID: "second", Samples: []GUIScenarioSample{{Property: "text", Expression: "[Row.Name]", Value: "Second"}}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := RenderGUIPreview("root", "element", "gui/test.gui", root, 400, 200, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGUIPreviewModelSamples(&preview, prepared); err != nil {
		t.Fatal(err)
	}
	htmlPreview, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if htmlPreview.Behaviors.ModelRows != 2 {
		t.Fatalf("model row behavior count=%d want 2", htmlPreview.Behaviors.ModelRows)
	}
	for _, expected := range []string{
		`data-ck3-model-row-source="provided"`,
		`data-ck3-model-row-id="first"`,
		`data-ck3-model-row-id="second"`,
		`id="ck3-detail-model-row"`,
		`function sameModelRow(left,right)`,
		`function reflowGrid(container)`,
		`modelRowLabel(selected)`,
	} {
		if !strings.Contains(htmlPreview.Document, expected) {
			t.Errorf("inspector is missing %q", expected)
		}
	}
}

func modelSampleTestGrid(name, datamodel string) GUIElement {
	return GUIElement{
		Kind: "fixedgridbox", Name: name,
		Properties: []GUIProperty{
			{Name: "datamodel", Value: datamodel},
			{Name: "datamodel_wrap", Value: "2"},
			{Name: "addcolumn", Value: "100"},
			{Name: "addrow", Value: "30"},
		},
		Children: []GUIElement{{
			Kind: "item",
			Children: []GUIElement{{
				Kind: "text_single", Name: "name",
				Properties: []GUIProperty{{Name: "text", Value: "[Row.Name]"}},
			}},
		}},
	}
}

func BenchmarkGUIModelSamplesInspector(b *testing.B) {
	gridOne := modelSampleTestGrid("grid_one", "[Rows.One]")
	for index := 0; index < 8; index++ {
		gridOne.Children[0].Children = append(gridOne.Children[0].Children, GUIElement{
			Kind: "icon", Name: "decoration", Size: &GUIVector{Width: "24", Height: "24"},
		})
	}
	gridTwo := cloneGUIElement(gridOne)
	gridTwo.Name = "grid_two"
	gridTwo.Properties[0].Value = "[Rows.Two]"
	template := GUIElement{Kind: "vbox", Children: []GUIElement{gridOne, gridTwo}}
	rowsOne := make([]GUIModelSampleRow, GUIModelSamplesMaxRows)
	rowsTwo := make([]GUIModelSampleRow, GUIModelSamplesMaxRows)
	for index := range rowsOne {
		rowsOne[index] = GUIModelSampleRow{
			ID: fmt.Sprintf("row_%02d", index),
			Samples: []GUIScenarioSample{{
				Property: "text", Expression: "[Row.Name]", Value: fmt.Sprintf("Row %d", index),
			}},
		}
		rowsTwo[index] = rowsOne[index]
	}
	collections := []GUIModelSampleCollection{
		{Target: "grid_one", Rows: rowsOne},
		{Target: "grid_two", Rows: rowsTwo},
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		root := cloneGUIElement(template)
		prepared, err := prepareGUIModelSamples("grid_benchmark", &root, collections)
		if err != nil {
			b.Fatal(err)
		}
		preview, err := RenderGUIPreview("grid", "type", "gui/benchmark.gui", root, 1280, 720, 500)
		if err != nil {
			b.Fatal(err)
		}
		if err := applyGUIPreviewModelSamples(&preview, prepared); err != nil {
			b.Fatal(err)
		}
		if _, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector}); err != nil {
			b.Fatal(err)
		}
	}
}
