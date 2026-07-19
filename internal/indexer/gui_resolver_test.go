package indexer

import (
	"strings"
	"testing"
)

func TestResolveGUIModelsExpandsInheritanceTemplateAndBlockoverride(t *testing.T) {
	model := BuildGUIModel(`template SharedSpacing { spacing = 8 }
types Demo {
	type base_panel = container {
		size = { 100 50 }
		vbox = { block "content" { text_single = { text = "base" } } }
	}
	type child_panel = base_panel {
		using = SharedSpacing
		size = { 200 50 }
		blockoverride "content" { icon = { texture = "gfx/interface/child.dds" } }
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/test.gui", Model: model}})
	if len(resolved.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", resolved.Diagnostics)
	}
	var child ResolvedGUIType
	for _, item := range resolved.Types {
		if item.Name == "child_panel" {
			child = item
		}
	}
	if child.Name == "" || child.Element.Size == nil || child.Element.Size.Width != "200" {
		t.Fatalf("type inheritance did not preserve child size: %+v", child)
	}
	if strings.Join(child.Element.TypeChain, ",") != "base_panel,child_panel" {
		t.Fatalf("type inheritance did not preserve semantic lineage: %+v", child.Element.TypeChain)
	}
	if propertyValue(child.Element.Properties, "spacing") != "8" {
		t.Fatalf("template property was not applied: %+v", child.Element.Properties)
	}
	if len(child.Element.Children) != 1 || child.Element.Children[0].Kind != "vbox" || len(child.Element.Children[0].Children) != 1 {
		t.Fatalf("slot override was not resolved: %+v", child.Element.Children)
	}
	slot := child.Element.Children[0].Children[0]
	if slot.Slot != "content" || slot.Override {
		t.Fatalf("nested slot metadata was not preserved: %+v", slot)
	}
	if len(slot.Children) != 1 || slot.Children[0].Kind != "icon" || slot.Children[0].Texture != "gfx/interface/child.dds" {
		t.Fatalf("slot replacement content missing: %+v", slot)
	}
}

func TestMergeGUIPropertiesPreservesRepeatedOnClickSequence(t *testing.T) {
	base := []GUIProperty{
		{Name: "onclick", Value: "[BaseAction]"},
		{Name: "tooltip", Value: "BASE_TT"},
	}
	overlay := []GUIProperty{
		{Name: "onclick", Value: "[ToggleGameViewData('travel_planner', TravelPlan.GetID)]"},
		{Name: "onclick", Value: "[Character.ZoomCameraTo]"},
		{Name: "tooltip", Value: "TRAVEL_TT"},
	}
	merged := mergeGUIProperties(base, overlay)
	var clicks []string
	for _, property := range merged {
		if strings.EqualFold(property.Name, "onclick") {
			clicks = append(clicks, property.Value)
		}
	}
	if len(clicks) != 2 || clicks[0] != overlay[0].Value || clicks[1] != overlay[1].Value {
		t.Fatalf("repeatable onclick sequence was collapsed or inherited incorrectly: %#v", merged)
	}
	if got := guiPreviewProperty(GUIElement{Properties: merged}, "tooltip"); got != "TRAVEL_TT" {
		t.Fatalf("ordinary scalar property did not retain override semantics: %q", got)
	}
}

func TestResolveGUIModelsReportsCyclesAndUnknownSlots(t *testing.T) {
	model := BuildGUIModel(`template A { using = B }
template B { using = A }
types Demo {
	type first = second {}
	type second = first {}
	type bad_override = container { blockoverride "missing" {} }
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/bad.gui", Model: model}})
	want := map[string]bool{
		"gui_template_cycle": false, "gui_inheritance_cycle": false, "gui_unknown_blockoverride": false,
	}
	for _, diagnostic := range resolved.Diagnostics {
		if _, exists := want[diagnostic.Code]; exists {
			want[diagnostic.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing %s diagnostic: %+v", code, resolved.Diagnostics)
		}
	}
}

func TestResolveGUIModelsKeepsOverridesOnOpaqueBuiltinSlotSurface(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type framed_icon = icon {
		blockoverride "button_frames" { effectname = "NoHighlight" }
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/icon_frames.gui", Model: model}})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("engine-provided icon slots cannot be proven unknown from loadable GUI sources: %+v", diagnostic)
		}
	}
	var icon ResolvedGUIType
	for _, typeRule := range resolved.Types {
		if typeRule.Name == "framed_icon" {
			icon = typeRule
		}
	}
	if len(icon.Element.Children) != 1 || icon.Element.Children[0].Slot != "button_frames" || !icon.Element.Children[0].Override {
		t.Fatalf("unresolved engine slot override was not retained: %+v", icon.Element.Children)
	}
}

func TestResolveGUIModelsExpandsCustomChildType(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type icon_base = icon { texture = "gfx/interface/base.dds" }
	type panel = container { icon_base = { name = "instance" } }
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/test.gui", Model: model}})
	var panel ResolvedGUIType
	for _, item := range resolved.Types {
		if item.Name == "panel" {
			panel = item
		}
	}
	if len(panel.Element.Children) != 1 || panel.Element.Children[0].Kind != "icon" || panel.Element.Children[0].Texture != "gfx/interface/base.dds" || panel.Element.Children[0].Name != "instance" {
		t.Fatalf("custom child type was not expanded: %+v", panel.Element.Children)
	}
	if strings.Join(panel.Element.Children[0].TypeChain, ",") != "icon_base" {
		t.Fatalf("custom child type lost its semantic identity: %+v", panel.Element.Children[0].TypeChain)
	}
}

func TestResolveGUIModelsBoundsRecursiveCustomInstances(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type recursive_item = container { recursive_item = {} }
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/recursive.gui", Model: model}})
	found := false
	for _, diagnostic := range resolved.Diagnostics {
		found = found || diagnostic.Code == "gui_instance_cycle"
	}
	if !found || len(resolved.Types) != 1 {
		t.Fatalf("recursive instance was not bounded: %+v", resolved)
	}
}

func TestResolveGUIModelsDoesNotRejectSlotsOnExternalElementTypes(t *testing.T) {
	model := BuildGUIModel(`header_pattern = {
	blockoverride "header_text" { text_single = { text = "title" } }
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/external.gui", Model: model}})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("external GUI type has no known slot catalog and must not produce a false warning: %+v", diagnostic)
		}
	}
}

func TestResolveGUIModelsDoesNotRejectSlotsWhenBaseTreeUsesMissingTemplate(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type partial_panel = container {
		vbox = { using = EngineProvidedTemplate }
	}
	type child_panel = partial_panel {
		blockoverride "engine_slot" {}
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/partial.gui", Model: model}})
	missingTemplate := false
	for _, diagnostic := range resolved.Diagnostics {
		missingTemplate = missingTemplate || diagnostic.Code == "gui_missing_template"
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("incomplete base tree cannot prove a slot unknown: %+v", diagnostic)
		}
	}
	if !missingTemplate {
		t.Fatalf("fixture did not preserve the reason the base tree is incomplete: %+v", resolved.Diagnostics)
	}
}

func TestGUIResolutionSummaryRanksUnresolvedSymbolHotspots(t *testing.T) {
	resolution := GUIResolution{Diagnostics: []GUIDiagnostic{
		{Code: "gui_missing_template", Symbol: "Animation_Common", Source: "gui/a.gui"},
		{Code: "gui_missing_template", Symbol: "Animation_Common", Source: "gui/b.gui"},
		{Code: "gui_unresolved_external_type", Symbol: "button_standard", Source: "gui/a.gui"},
		{Code: "gui_duplicate_type", Symbol: "ignored", Source: "gui/a.gui"},
	}}
	summary := resolution.Summary()
	if len(summary.UnresolvedHotspots) != 2 {
		t.Fatalf("unexpected unresolved hotspot count: %+v", summary.UnresolvedHotspots)
	}
	first := summary.UnresolvedHotspots[0]
	if first.Code != "gui_missing_template" || first.Symbol != "Animation_Common" || first.Count != 2 || first.Sources != 2 {
		t.Fatalf("hotspots were not ranked or source-deduplicated: %+v", summary.UnresolvedHotspots)
	}
}

func TestGUIResolutionSummaryRanksRuntimePropertyGaps(t *testing.T) {
	resolution := GUIResolution{
		Types: []ResolvedGUIType{{Element: GUIElement{
			Kind: "widget",
			Properties: []GUIProperty{
				{Name: "visible", Value: "[IsShown]"},
				{Name: "alpha", Value: "[GetOpacity]"},
				{Name: "alpha", Value: "[GetSecondaryOpacity]"},
				{Name: "rotation", Value: "[GetRotation]"},
				{Name: "texture", Value: "gfx/interface/panel.dds"},
			},
			Children: []GUIElement{{Kind: "icon", Properties: []GUIProperty{{Name: "alpha", Value: "0.5"}}}},
		}}},
	}
	summary := resolution.Summary()
	if len(summary.RuntimeHotspots) != 2 {
		t.Fatalf("unexpected runtime property hotspots: %+v", summary.RuntimeHotspots)
	}
	if summary.RuntimeHotspots[0].Name != "alpha" || summary.RuntimeHotspots[0].Count != 3 ||
		summary.RuntimeHotspots[0].Expressions != 2 || summary.RuntimeHotspots[0].Support != "unmodeled" {
		t.Fatalf("runtime property ranking is wrong: %+v", summary.RuntimeHotspots)
	}
	if summary.RuntimeHotspots[1].Name != "rotation" || summary.RuntimeHotspots[1].Expressions != 1 {
		t.Fatalf("secondary runtime property ranking is wrong: %+v", summary.RuntimeHotspots)
	}
}

func TestResolveGUIModelsAppliesUsingBeforeBlockoverride(t *testing.T) {
	model := BuildGUIModel(`template SharedSlots {
	block "template_slot" { text_single = { text = "base" } }
}
types Demo {
	type templated_panel = container {
		using = SharedSlots
		blockoverride "template_slot" { icon = { texture = "gfx/interface/replacement.dds" } }
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/template_slot.gui", Model: model}})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("using template was applied after blockoverride: %+v", diagnostic)
		}
	}
	var panel ResolvedGUIType
	for _, typeRule := range resolved.Types {
		if typeRule.Name == "templated_panel" {
			panel = typeRule
		}
	}
	if len(panel.Element.Children) != 1 || panel.Element.Children[0].Slot != "template_slot" ||
		len(panel.Element.Children[0].Children) != 1 || panel.Element.Children[0].Children[0].Texture != "gfx/interface/replacement.dds" {
		t.Fatalf("template slot override was not retained: %+v", panel.Element.Children)
	}
}

func TestResolveGUIModelsInjectsBlockoverridePropertiesIntoOwningElement(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type main_tab = widget {
		button_normal = {
			block "maintab_button" {}
		}
	}
}
main_tab = {
	name = "military_tab"
	blockoverride "maintab_button" {
		texture = "gfx/interface/military.dds"
		onclick = "[ToggleGameViewData('military', GetPlayer.GetID)]"
		onclick = "[GetVariableSystem.Toggle('tabs')]"
		down = "[IsGameViewOpen('military')]"
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/hud.gui", Model: model}})
	root, ok := findNamedGUIElement(resolved.Roots, "military_tab")
	if !ok || len(root.Children) != 1 {
		t.Fatalf("resolved main tab missing: %+v", resolved.Roots)
	}
	button := root.Children[0]
	if button.Texture != "gfx/interface/military.dds" || guiPreviewProperty(button, "down") != "[IsGameViewOpen('military')]" {
		t.Fatalf("blockoverride properties did not reach owning button: %+v", button)
	}
	clicks := guiPreviewProperties(button, "onclick")
	if len(clicks) != 2 || clicks[0] != "[ToggleGameViewData('military', GetPlayer.GetID)]" {
		t.Fatalf("blockoverride click sequence was not preserved: %#v", clicks)
	}
	if len(button.Children) != 1 || button.Children[0].Slot != "maintab_button" || len(button.Children[0].Properties) != 0 {
		t.Fatalf("structural block retained runtime properties: %+v", button.Children)
	}
}

func TestResolveGUIModelsExpandsCustomSiblingsBeforeBlockoverride(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type child_with_slot = widget { block "child_slot" {} }
	type parent = hbox {
		blockoverride "child_slot" { icon = { texture = "gfx/interface/child_slot.dds" } }
		child_with_slot = {}
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/sibling_slot.gui", Model: model}})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("custom child was not expanded before sibling override: %+v", diagnostic)
		}
	}
	var parent ResolvedGUIType
	for _, typeRule := range resolved.Types {
		if typeRule.Name == "parent" {
			parent = typeRule
		}
	}
	if len(parent.Element.Children) != 1 || len(parent.Element.Children[0].Children) != 1 ||
		parent.Element.Children[0].Children[0].Slot != "child_slot" ||
		len(parent.Element.Children[0].Children[0].Children) != 1 ||
		parent.Element.Children[0].Children[0].Children[0].Texture != "gfx/interface/child_slot.dds" {
		t.Fatalf("custom child slot override was not retained: %+v", parent.Element.Children)
	}
}

func TestResolveGUIModelsExpandsCustomChildIntroducedByBlockoverride(t *testing.T) {
	model := BuildGUIModel(`types Demo {
	type shared_scrollbox = widget {
		scrollwidget = {
			vbox = {
				block "scrollbox_content" {}
			}
		}
	}
	type row_card = widget {
		size = { 220 184 }
		vbox = {
			button_standard = {
				block "onclick" {}
				icon = { name = "layer" texture = "[Row.GetTexture]" }
			}
		}
	}
	type list_panel = vbox {
		shared_scrollbox = {
			blockoverride "scrollbox_content" {
				fixedgridbox = {
					item = {
						row_card = {
							name = "row"
							blockoverride "onclick" { onclick = "[Row.Select]" }
						}
					}
				}
			}
		}
	}
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/blockoverride_child.gui", Model: model}})
	var panel ResolvedGUIType
	for _, typeRule := range resolved.Types {
		if typeRule.Name == "list_panel" {
			panel = typeRule
		}
	}
	var row *GUIElement
	var walk func(*GUIElement)
	walk = func(element *GUIElement) {
		if row != nil {
			return
		}
		if element.Name == "row" {
			row = element
			return
		}
		for index := range element.Children {
			walk(&element.Children[index])
		}
	}
	walk(&panel.Element)
	if row == nil || row.Kind != "widget" || len(row.Children) != 1 ||
		len(row.Children[0].Children) != 1 || len(row.Children[0].Children[0].Children) != 2 ||
		row.Children[0].Children[0].Children[1].Name != "layer" ||
		row.Children[0].Children[0].Children[1].Texture != "[Row.GetTexture]" {
		t.Fatalf("blockoverride-introduced custom child was not expanded: %+v", panel.Element)
	}
}

func TestResolveGUIModelsLinksTooltipwidgetTemplateForSlotOverrides(t *testing.T) {
	model := BuildGUIModel(`template demo_tooltip {
	tooltipwidget = { block "extra_data" {} }
}
types Demo {
	type demo_button = button { tooltipwidget = demo_tooltip }
}
demo_button = {
	blockoverride "extra_data" { text_single = { text = "details" } }
}`)
	resolved := ResolveGUIModels([]GUIModelInput{{Path: "gui/tooltip_link.gui", Model: model}})
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "gui_unknown_blockoverride" {
			t.Fatalf("tooltipwidget template link was not included in slot lookup: %+v", diagnostic)
		}
	}
	if len(resolved.Roots) != 1 || len(resolved.Roots[0].Linked) != 1 {
		t.Fatalf("tooltipwidget template link missing: %+v", resolved.Roots)
	}
	linked := resolved.Roots[0].Linked[0]
	slot := findGUIElementBySlot(&linked.Element, "extra_data")
	if linked.Property != "tooltipwidget" || linked.Target != "demo_tooltip" || slot == nil ||
		len(slot.Children) != 1 || slot.Children[0].Kind != "text_single" {
		t.Fatalf("tooltipwidget slot override missing from linked tree: %+v", linked)
	}
}

func findGUIElementBySlot(element *GUIElement, slot string) *GUIElement {
	if element.Slot == slot {
		return element
	}
	for index := range element.Children {
		if found := findGUIElementBySlot(&element.Children[index], slot); found != nil {
			return found
		}
	}
	for index := range element.Linked {
		if found := findGUIElementBySlot(&element.Linked[index].Element, slot); found != nil {
			return found
		}
	}
	return nil
}

func propertyValue(properties []GUIProperty, name string) string {
	for _, property := range properties {
		if property.Name == name {
			return property.Value
		}
	}
	return ""
}
