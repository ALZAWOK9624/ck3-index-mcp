package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestClassifyRelIndexesGUIResources(t *testing.T) {
	tests := map[string]string{
		"gfx/FX/pdxgui_default.shader":  "resource",
		"gfx/interface/video/intro.bk2": "resource",
		"gfx/interface/splash.jpg":      "resource",
		"gfx/map/environment/masks.txt": "resource",
		"gfx/models/props.asset":        "resource",
		"gui/window.gui":                "script",
	}
	for path, want := range tests {
		if got := classifyRel(path); got != want {
			t.Errorf("classifyRel(%q)=%q want=%q", path, got, want)
		}
	}
}

func TestExtractGUIObjectsAndInheritance(t *testing.T) {
	parsed := script.ParseGUI(`template SharedSpacing { margin = { 4 4 } }
types Demo {
	type base_panel = container {}
	type child_panel = base_panel {
		using = SharedSpacing
		icon = { texture = "gfx/interface/test.dds" }
	}
}`)
	rec := fileRecord{ID: 7, RelPath: "gui/test.gui", Path: "gui/test.gui", SourceName: "test"}
	objects := extractObjects(rec, parsed.Nodes)
	if len(objects) != 3 {
		t.Fatalf("objects=%+v want a template and two GUI type definitions", objects)
	}
	if objects[0].Type != "gui_template" || objects[0].Name != "SharedSpacing" || objects[1].Name != "base_panel" || objects[2].Name != "child_panel" {
		t.Fatalf("unexpected GUI objects: %+v", objects)
	}

	refs := extractRefs(rec, parsed.Nodes, objects)
	foundInheritance := false
	foundTexture := false
	foundTemplate := false
	for _, ref := range refs {
		if ref.Kind == "gui" && ref.Name == "base_panel" && ref.FromName == "child_panel" {
			foundInheritance = true
		}
		if ref.Kind == "resource" && ref.Name == "gfx/interface/test.dds" {
			foundTexture = true
		}
		if ref.Kind == "gui_template" && ref.Name == "SharedSpacing" && ref.FromName == "child_panel" {
			foundTemplate = true
		}
	}
	if !foundInheritance {
		t.Fatalf("missing child_panel -> base_panel GUI inheritance ref: %+v", refs)
	}
	if !foundTexture {
		t.Fatalf("missing GUI texture resource ref: %+v", refs)
	}
	if !foundTemplate {
		t.Fatalf("missing GUI using -> template ref: %+v", refs)
	}
}

func TestGUIPercentSizeAndParentanchorAreNotCategoricalErrors(t *testing.T) {
	parsed := script.ParseGUI(`types Demo {
	type bad = hbox {
		size = { 100% 40 }
		parentanchor = center
	}
}`)
	diags := checkGUISafety(parsed.Nodes, "gui/test.gui")
	var crash, layout bool
	for _, diag := range diags {
		crash = crash || diag.code == "gui_crash_risk"
		layout = layout || diag.code == "gui_layout_misuse"
	}
	if crash || layout {
		t.Fatalf("got diagnostics=%+v want no categorical GUI finding", diags)
	}
}
