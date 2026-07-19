package indexer

import "testing"

func TestBuildGUIModelPreservesLayoutAndSourceSpans(t *testing.T) {
	model := BuildGUIModel(`types HUD {
	type sample_panel = container {
		name = "sample"
		position = { -12 8 }
		size = { 100% 64 }
		icon = {
			texture = "gfx/interface/sample.dds"
		}
		blockoverride "content" {
			text_single = { text = "sample_title" }
		}
	}
}`)
	if len(model.ParseErrors) != 0 || len(model.Namespaces) != 1 || len(model.Namespaces[0].Types) != 1 {
		t.Fatalf("unexpected model header: %+v", model)
	}
	typ := model.Namespaces[0].Types[0]
	if typ.Name != "sample_panel" || typ.Base != "container" || typ.Element.Name != "sample" {
		t.Fatalf("unexpected type: %+v", typ)
	}
	if typ.Element.Position == nil || typ.Element.Position.X != "-12" || typ.Element.Position.Y != "8" {
		t.Fatalf("position was not normalized: %+v", typ.Element.Position)
	}
	if typ.Element.Size == nil || typ.Element.Size.Width != "100%" || typ.Element.Size.Height != "64" || !typ.Element.Size.Percent {
		t.Fatalf("size was not normalized: %+v", typ.Element.Size)
	}
	if len(typ.Element.Children) != 2 || typ.Element.Children[0].Kind != "icon" || typ.Element.Children[0].Texture != "gfx/interface/sample.dds" {
		t.Fatalf("GUI children/texture were not preserved: %+v", typ.Element.Children)
	}
	override := typ.Element.Children[1]
	if !override.Override || override.Slot != "content" || override.Span.EndLine != 11 {
		t.Fatalf("blockoverride/source span was not preserved: %+v", override)
	}
}

func TestBuildGUIModelKeepsInheritedElementTemplate(t *testing.T) {
	model := BuildGUIModel(`custom_button = base_button {
	position = { 1 2 }
}`)
	if len(model.Roots) != 1 {
		t.Fatalf("roots=%+v want one", model.Roots)
	}
	root := model.Roots[0]
	if root.Kind != "custom_button" || root.Template != "base_button" || root.Position == nil {
		t.Fatalf("inherited element not normalized: %+v", root)
	}
}

func TestBuildGUIModelExtractsTemplates(t *testing.T) {
	model := BuildGUIModel(`template SharedAnimation { duration = 0.2 }
local_template LocalSpacing { margin = { 4 8 } }`)
	if len(model.Templates) != 2 || model.Templates[0].Name != "SharedAnimation" || model.Templates[0].Local {
		t.Fatalf("global template not extracted: %+v", model.Templates)
	}
	if model.Templates[1].Name != "LocalSpacing" || !model.Templates[1].Local {
		t.Fatalf("local template not extracted: %+v", model.Templates[1])
	}
}

func TestBuildGUIModelKeepsVisualVectorsAsProperties(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type framed_button = button {
		spriteborder = { 3 3 }
		framesize = { 249 78 }
		maximumsize = { 320 180 }
		color = { 0.15 0.15 0.15 1 }
		modify_texture = { texture = "gfx/interface/overlay.dds" }
	}
}`)
	if len(model.Namespaces) != 1 || len(model.Namespaces[0].Types) != 1 {
		t.Fatalf("type missing: %+v", model)
	}
	element := model.Namespaces[0].Types[0].Element
	for _, name := range []string{"spriteborder", "framesize", "maximumsize", "color"} {
		found := false
		for _, property := range element.Properties {
			if property.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("visual vector %q was not retained as a property: %+v", name, element)
		}
	}
	if len(element.Children) != 1 || element.Children[0].Kind != "modify_texture" {
		t.Fatalf("visual vectors leaked into layout children: %+v", element.Children)
	}
}
