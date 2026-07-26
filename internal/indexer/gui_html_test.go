package indexer

import (
	"strings"
	"testing"
)

func TestRenderGUIHTMLPreviewIsSafeDeterministicAndModelReadable(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: `panel\" onmouseover=\"bad`, Size: &GUIVector{Width: "400", Height: "200"},
		Children: []GUIElement{{
			Kind: "button", Name: "confirm", Size: &GUIVector{Width: "120", Height: "32"},
			Properties: []GUIProperty{
				{Name: "text", Value: `\"<script>alert(1)</script>\"`},
				{Name: "visible", Value: "[GetPlayer.IsAlive]"},
				{Name: "datacontext", Value: "Player"},
				{Name: "onclick", Value: "[SelectCharacter]"},
			},
		}},
	}
	preview, err := RenderGUIPreview("safe_panel", "type", "gui/test.gui", root, 800, 450, 20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RenderGUIHTMLPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGUIHTMLPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	if first.Document != second.Document || first.SHA256 != second.SHA256 {
		t.Fatal("same GUI scene did not produce deterministic HTML")
	}
	if first.SchemaVersion != GUIHTMLSchemaVersion || first.Mode != GUIHTMLModeStatic || first.ScriptPolicy != "none" || !first.ModelReadable || first.Scripts || first.ExternalRequests {
		t.Fatalf("unexpected HTML contract: %+v", first)
	}
	if first.Behaviors.VisibleExpressions != 1 || first.Behaviors.ClickActions != 1 {
		t.Fatalf("static preview omitted behavior summary: %+v", first.Behaviors)
	}
	for _, forbidden := range []string{"<script", "<form", " src=", " href=", "url("} {
		if strings.Contains(strings.ToLower(first.Document), forbidden) {
			t.Fatalf("HTML contains forbidden capability %q", forbidden)
		}
	}
	if strings.Contains(first.Document, `<script>alert(1)</script>`) || strings.Contains(first.Document, `onmouseover="bad`) {
		t.Fatalf("GUI text became executable markup: %s", first.Document)
	}
	for _, expected := range []string{
		`data-ck3-visible="[GetPlayer.IsAlive]"`,
		`data-ck3-data-context="Player"`,
		`data-ck3-on-click="[SelectCharacter]"`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		`style="left:40px;top:45px;width:720px;height:360px;z-index:1"`,
	} {
		if !strings.Contains(first.Document, expected) {
			t.Errorf("HTML missing %q", expected)
		}
	}
	if first.Bytes != len(first.Document) || first.NodeCount != 2 || first.Bytes > GUIHTMLMaxBytes {
		t.Fatalf("unexpected HTML size metadata: %+v", first)
	}
}

func TestRenderGUIHTMLInspectorIsDeterministicSafeAndInteractive(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: `panel\" data-escape=\"bad`, Size: &GUIVector{Width: "420", Height: "220"},
		Children: []GUIElement{
			{Kind: "button", Name: "confirm", Size: &GUIVector{Width: "140", Height: "36"}, Properties: []GUIProperty{
				{Name: "text", Value: `"[GetPlayer.GetName]"`},
				{Name: "visible", Value: "[GetPlayer.IsAlive]"},
				{Name: "enabled", Value: "[CanSelect]"},
				{Name: "onclick", Value: "[SelectCharacter]"},
				{Name: "state", Value: "[GetSelectionState]"},
			}},
			{Kind: "text_single", Name: "unsafe", Size: &GUIVector{Width: "120", Height: "24"}, Properties: []GUIProperty{
				{Name: "text", Value: `"</script><script>alert(1)</script>"`},
			}},
		},
	}
	preview, err := RenderGUIPreview("interactive_panel", "type", "gui/test.gui", root, 900, 540, 20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if first.Document != second.Document || first.SHA256 != second.SHA256 {
		t.Fatal("same GUI scene did not produce deterministic inspector HTML")
	}
	if first.Mode != GUIHTMLModeInspector || !first.Scripts || first.ExternalRequests || first.ScriptPolicy != "fixed-generator-script" || !strings.HasPrefix(first.ScriptSHA256, "sha256-") {
		t.Fatalf("unexpected inspector contract: %+v", first)
	}
	if !strings.Contains(first.Document, "script-src '"+first.ScriptSHA256+"'") || strings.Count(first.Document, "<script>") != 1 || strings.Count(first.Document, "</script>") != 1 {
		t.Fatalf("inspector script is not pinned by its exact CSP hash")
	}
	for _, expected := range []string{
		`id="ck3-tree"`, `id="ck3-viewport"`, `id="ck3-detail-panel"`, `id="ck3-search"`,
		`id="ck3-zoom"`, `id="ck3-language"`, `id="ck3-replay-clicks"`, `id="ck3-show-labels"`, `ck3-diagnostic-caption`, `id="ck3-sim-visible"`, `id="ck3-sim-enabled"`, `id="ck3-sim-text"`,
		`data-ck3-visible="[GetPlayer.IsAlive]"`, `data-ck3-on-click="[SelectCharacter]"`,
	} {
		if !strings.Contains(first.Document, expected) {
			t.Errorf("inspector HTML missing %q", expected)
		}
	}
	for _, forbidden := range []string{"eval(", "fetch(", "xmlhttprequest", "websocket", " src=", " href=", `data-escape="bad`} {
		if strings.Contains(strings.ToLower(first.Document), strings.ToLower(forbidden)) {
			t.Fatalf("inspector contains forbidden capability or unescaped markup %q", forbidden)
		}
	}
	if strings.Contains(first.Document, `</script><script>alert(1)</script>`) {
		t.Fatal("GUI text escaped out of its HTML data boundary")
	}
	if first.Behaviors.VisibleExpressions != 1 || first.Behaviors.EnabledExpressions != 1 || first.Behaviors.DynamicTexts != 1 || first.Behaviors.ClickActions != 1 || first.Behaviors.States != 1 {
		t.Fatalf("unexpected inspector behavior summary: %+v", first.Behaviors)
	}
}

func TestRenderGUIHTMLPreviewRejectsUnknownMode(t *testing.T) {
	if _, err := RenderGUIHTMLPreviewWithOptions(GUIPreviewResult{}, GUIHTMLRenderOptions{Mode: "runtime"}); err == nil {
		t.Fatal("unknown GUI HTML mode was accepted")
	}
}

func TestRenderGUIHTMLPreviewKeepsRuntimeExpressionsAsData(t *testing.T) {
	root := GUIElement{
		Kind: "text_single", Size: &GUIVector{Width: "200", Height: "30"},
		Properties: []GUIProperty{
			{Name: "raw_text", Value: "[GetPlayer.GetName]"},
			{Name: "enabled", Value: "[CanSelect]"},
			{Name: "tooltip", Value: "select_character_tt"},
		},
	}
	preview, err := RenderGUIPreview("runtime_text", "element", "gui/runtime.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-enabled="[CanSelect]"`,
		`data-ck3-tooltip="select_character_tt"`,
		`data-ck3-raw-text="[GetPlayer.GetName]"`,
		`>[GetPlayer.GetName]</span>`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("HTML missing preserved runtime fact %q", expected)
		}
	}
}

func TestRenderGUIHTMLPreviewPreservesSelectedIndexAsModelMetadata(t *testing.T) {
	root := GUIElement{
		Kind: "dropdown", Name: "sort_order", Size: &GUIVector{Width: "200", Height: "30"},
		Properties: []GUIProperty{
			{Name: "datamodel", Value: "[SortOptions]"},
			{Name: "selectedindex", Value: "[CurrentSortIndex]"},
		},
	}
	preview, err := RenderGUIPreview("sort_order", "element", "gui/settings.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`data-ck3-selected-index="[CurrentSortIndex]"`, `selectedindex [CurrentSortIndex]`} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("HTML missing selectedindex model metadata %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorKeepsRawTooltipLiteralAndReadable(t *testing.T) {
	root := GUIElement{
		Kind:       "button",
		Name:       "raw_tooltip_owner",
		Properties: []GUIProperty{{Name: "raw_tooltip", Value: "#X Direct tooltip#!"}},
	}
	preview, err := RenderGUIPreview("raw_tooltip_owner", "element", "gui/runtime.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-raw-tooltip="#X Direct tooltip#!"`,
		`data-ck3-raw-tooltip-display="Direct tooltip"`,
		`node.dataset.ck3RawTooltipDisplay`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("HTML missing raw tooltip support %q", expected)
		}
	}
	if strings.Contains(result.Document, `data-ck3-tooltip="#X Direct tooltip#!"`) {
		t.Fatal("raw_tooltip was emitted as a localization tooltip")
	}
}

func TestRenderGUIHTMLInspectorReplaysTooltipWhenDisabled(t *testing.T) {
	root := GUIElement{
		Kind: "button",
		Name: "disabled_action",
		Properties: []GUIProperty{
			{Name: "enabled", Value: "[CanTakeAction]"},
			{Name: "tooltip", Value: "normal_action_tooltip"},
			{Name: "tooltip_when_disabled", Value: "[DisabledActionReason]"},
		},
	}
	preview, err := RenderGUIPreview("disabled_action", "element", "gui/runtime.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "CanTakeAction", Value: false},
		{Expression: "DisabledActionReason", Value: "Requires a hook"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"data-ck3-tooltip-when-disabled=\"[DisabledActionReason]\"",
		"data-ck3-tooltip-when-disabled-plan-raw=\"0\"",
		"data-sim-tooltip-when-disabled=\"Requires a hook\"",
		"function disabledTooltipFor(node)",
		"node.dataset.simTooltipWhenDisabled",
		"prefix==='tooltipWhenDisabled'?'TooltipWhenDisabled':'Tooltip'",
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("disabled tooltip inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReplaysAnimatedProgressValue(t *testing.T) {
	root := GUIElement{
		Kind: "progresspie",
		Properties: []GUIProperty{
			{Name: "min", Value: "0"},
			{Name: "max", Value: "100"},
			{Name: "animated_progress_value", Value: "[CurrentProgress]"},
		},
	}
	preview, err := RenderGUIPreview("animated_progress", "element", "gui/runtime.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "CurrentProgress", Value: 62.5}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"data-ck3-animated-progress-value=\"[CurrentProgress]\"",
		"data-ck3-animated-progress-value-plan=\"2\"",
		"data-sim-animated-progress-value=\"62.5\"",
		"--ck3-progress:0.625",
		"runtimeNodeNumber(node,'animatedProgressValue',NaN)",
		"['min','max','value','animatedProgressValue','alpha','scale','rotateUv','minWidth','maxWidth','minHeight','maxHeight','marginLeft','marginRight','marginTop','marginBottom','statePositionX','statePositionY']",
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("animated progress inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReplaysDynamicLayoutProperties(t *testing.T) {
	root := GUIElement{
		Kind: "flowcontainer", Size: &GUIVector{Width: "240", Height: "60"},
		Properties: []GUIProperty{{Name: "direction", Value: "horizontal"}},
		Children: []GUIElement{{
			Kind: "button", Name: "dynamic_layout", Size: &GUIVector{Width: "160", Height: "28"},
			Properties: []GUIProperty{
				{Name: "max_width", Value: "[Select_int32( IsCompact, '(int32)72', '(int32)128' )]"},
				{Name: "margin_left", Value: "[Select_int32( IsCompact, '(int32)-14', '(int32)6' )]"},
			},
		}},
	}
	preview, err := RenderGUIPreview("dynamic_layout", "type", "gui/event_windows/character_event.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "IsCompact", Value: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-max-width="[Select_int32( IsCompact, &#39;(int32)72&#39;, &#39;(int32)128&#39; )]"`,
		`data-ck3-max-width-plan="0"`, `data-sim-max-width="72"`,
		`data-ck3-margin-left-plan="1"`, `data-sim-margin-left="-14"`,
		`function runtimeLayoutNumber`, `function applyRuntimeLayoutConstraints`,
		`runtimeLayoutNumber(item,horizontal?'marginLeft':'marginTop',0)`,
		`target.width=clampRuntimeLayoutDimension(item,target.width,true)`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("dynamic layout inspector missing %q", expected)
		}
	}
	if result.Behaviors.RuntimeEvaluated != 2 {
		t.Fatalf("dynamic layout runtime summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorReplaysDynamicAllowOutside(t *testing.T) {
	root := GUIElement{
		Kind: "scrollarea", TypeChain: []string{"scrollbox"}, Name: "event_options", Size: &GUIVector{Width: "180", Height: "80"},
		Children: []GUIElement{{
			Kind: "scrollwidget",
			Children: []GUIElement{{
				Kind: "vbox", Size: &GUIVector{Width: "180", Height: "120"},
				Children: []GUIElement{{
					Kind: "widget", Name: "event_tools", Size: &GUIVector{Width: "180", Height: "30"},
					Properties: []GUIProperty{{Name: "allow_outside", Value: "[EventWindowData.ShowEventTools]"}},
				}},
			}},
		}},
	}
	preview, err := RenderGUIPreview("event_options", "element", "gui/shared/event_windows.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "EventWindowData.ShowEventTools", Value: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-allow-outside="[EventWindowData.ShowEventTools]"`,
		`data-ck3-allow-outside-plan="0"`,
		`data-sim-allow-outside="true"`,
		`data-ck3-initial-sim-allow-outside="true"`,
		`function runtimeAllowOutside(node)`,
		`function hasAllowOutsideAncestor(node,viewport)`,
		`!hasAllowOutsideAncestor(item,container)`,
		`'allowOutside'`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("dynamic allow_outside inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReplaysBoundedRuntimeFacts(t *testing.T) {
	root := GUIElement{Kind: "widget", Size: &GUIVector{Width: "300", Height: "120"}, Children: []GUIElement{{
		Kind: "button", Name: "runtime_button", Size: &GUIVector{Width: "160", Height: "32"},
		Properties: []GUIProperty{{Name: "visible", Value: "[And( Not( IsPauseMenuShown ), GetPlayer.IsValid )]"}, {Name: "enabled", Value: "[CanAct]"}, {Name: "onclick", Value: "[ToggleGameView('outliner')]"}},
	}}}
	preview, err := RenderGUIPreview("runtime_panel", "type", "gui/runtime.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsPauseMenuShown", Value: false},
		{Expression: "GetPlayer.IsValid", Value: true},
		{Expression: "CanAct", Value: false},
		{Expression: "IsGameViewOpen('outliner')", Value: false},
		{Expression: `Unsafe('</script><script>alert(1)</script>')`, Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`id="ck3-runtime-facts"`, `id="ck3-runtime-plans"`, `data-ck3-runtime-fact="0"`,
		`data-ck3-visible-plan="0"`, `data-ck3-enabled-plan="1"`,
		`data-ck3-action-plan="0"`, `data-ck3-runtime-action="0"`,
		`data-ck3-initial-sim-visible="true"`, `data-ck3-initial-sim-enabled="false"`,
		`function evaluateRuntimePlan`, `function applyRuntimePlans`, `function applyRuntimeAction`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("runtime inspector missing %q", expected)
		}
	}
	if strings.Count(result.Document, "<script>") != 1 || strings.Count(result.Document, "</script>") != 1 || strings.Contains(result.Document, `</script><script>alert(1)</script>`) {
		t.Fatal("runtime fact escaped its inert HTML boundary")
	}
	if result.Behaviors.RuntimePlans != 2 || result.Behaviors.RuntimeFacts != 5 || result.Behaviors.RuntimeEvaluated != 2 || result.Behaviors.RuntimeActions != 1 {
		t.Fatalf("runtime behavior summary is incomplete: %+v", result.Behaviors)
	}
	if !strings.Contains(result.Document, `class="ck3-node ck3-button is-sim-disabled"`) {
		t.Fatal("server-evaluated enabled=false was not reflected in initial HTML")
	}
}

func TestRenderGUIHTMLInspectorReplaysNumericProgress(t *testing.T) {
	dataURI := "data:image/png;base64,iVBORw0KGgo="
	emptyDataURI := "data:image/png;base64,aGVsbG8="
	preview := GUIPreviewResult{
		Width: 320, Height: 180,
		Nodes: []GUIPreviewNode{
			{
				Index: 0, Parent: -1, Kind: "progresspie", Bounds: GUIPreviewRect{X: 20, Y: 20, Width: 40, Height: 40},
				Semantics:  &GUISemantics{Value: "[Timer.GetProgress]"},
				TextureRef: &GUITextureRef{Path: "gfx/interface/progress.dds", Embedded: true, dataURI: dataURI},
			},
			{
				Index: 1, Parent: -1, Kind: "progressbar", Bounds: GUIPreviewRect{X: 20, Y: 80, Width: 160, Height: 20},
				Semantics:            &GUISemantics{Min: "0", Max: "100", Value: "35", NoProgressTexture: "gfx/interface/empty.dds"},
				TextureRef:           &GUITextureRef{Path: "gfx/interface/progress.dds", Embedded: true, dataURI: dataURI},
				NoProgressTextureRef: &GUITextureRef{Path: "gfx/interface/empty.dds", Embedded: true, dataURI: emptyDataURI},
			},
		},
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "Timer.GetProgress", Value: 0.25}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`class="ck3-node ck3-image ck3-progresspie ck3-has-texture"`,
		`class="ck3-node ck3-image ck3-progressbar ck3-has-texture"`,
		`data-ck3-value="[Timer.GetProgress]"`,
		`data-ck3-value-plan="0"`,
		`data-ck3-value-runtime-status="evaluated"`,
		`data-ck3-initial-sim-value="0.25"`,
		`data-ck3-min="0"`,
		`data-ck3-max="100"`,
		`data-ck3-initial-sim-value="35"`,
		`--ck3-progress-angle:90.000deg`,
		`--ck3-progress-inverse:75.000%`,
		`--ck3-progress-inverse:65.000%`,
		`.ck3-progresspie>.ck3-texture`,
		`.ck3-progressbar>.ck3-progress-fill`,
		`ck3-no-progress`,
		`ck3-progress-fill`,
		`function applyRuntimeValue`,
		`numeric binding(s) evaluated`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("numeric progress inspector missing %q", expected)
		}
	}
	if strings.Count(result.Document, dataURI) != 1 {
		t.Fatal("shared progress texture was not emitted exactly once")
	}
	if strings.Count(result.Document, emptyDataURI) != 1 {
		t.Fatal("empty progress texture was not emitted exactly once")
	}
	if result.Behaviors.ValueExpressions != 2 || result.Behaviors.RuntimeEvaluated != 4 {
		t.Fatalf("numeric progress behavior summary is incomplete: %+v", result.Behaviors)
	}
	staticResult, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeStatic})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staticResult.Document, `--ck3-progress-angle:90.000deg`) || !strings.Contains(staticResult.Document, `--ck3-progress-inverse:65.000%`) {
		t.Fatal("static HTML did not preserve server-evaluated progress values")
	}
}

func TestRenderGUIHTMLInspectorRendersPortraitBackgroundAndMask(t *testing.T) {
	mainDataURI := "data:image/png;base64,bWFpbg=="
	backgroundDataURI := "data:image/png;base64,YmFja2dyb3VuZA=="
	maskDataURI := "data:image/png;base64,bWFzaw=="
	preview := GUIPreviewResult{Width: 320, Height: 180, Nodes: []GUIPreviewNode{{
		Index: 0, Parent: -1, Kind: "portrait_button", Bounds: GUIPreviewRect{Width: 120, Height: 160},
		Texture: "gfx/portraits/portrait_sample.dds", TextureRef: &GUITextureRef{Path: "gfx/portraits/portrait_sample.dds", Resolved: true, Embedded: true, dataURI: mainDataURI},
		BackgroundTexture: "gfx/portraits/portrait_background.dds", BackgroundTextureRef: &GUITextureRef{Path: "gfx/portraits/portrait_background.dds", Resolved: true, Embedded: true, dataURI: backgroundDataURI},
		MaskTexture: "gfx/portraits/portrait_mask_head.dds", MaskTextureRef: &GUITextureRef{Path: "gfx/portraits/portrait_mask_head.dds", Resolved: true, Embedded: true, dataURI: maskDataURI},
	}}}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-background-texture="gfx/portraits/portrait_background.dds"`,
		`data-ck3-mask="gfx/portraits/portrait_mask_head.dds"`,
		`data-ck3-background-texture-resolved="true"`,
		`data-ck3-mask-resolved="true"`,
		`class="ck3-texture ck3-texture-a1 ck3-background"`,
		`class="ck3-texture ck3-texture-a0"`,
		`mask-image:var(--ck3-texture-image-a2)`,
		`mask-size:100% 100%`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("portrait layer inspector missing %q", expected)
		}
	}
	backgroundIndex := strings.Index(result.Document, `class="ck3-texture ck3-texture-a1 ck3-background"`)
	portraitIndex := strings.Index(result.Document, `class="ck3-texture ck3-texture-a0"`)
	if backgroundIndex < 0 || portraitIndex < 0 || backgroundIndex >= portraitIndex {
		t.Fatalf("portrait background was not emitted before the primary texture")
	}
}

func TestRenderGUIHTMLInspectorComposesProvidedCoatOfArmsMaskAndTransform(t *testing.T) {
	textureSample := "gfx/interface/coat_of_arms/title_sample.dds"
	preview := GUIPreviewResult{Width: 320, Height: 180, Nodes: []GUIPreviewNode{{
		Index: 0, Parent: -1, Kind: "coat_of_arms_icon", Bounds: GUIPreviewRect{Width: 120, Height: 120},
		Texture:              textureSample,
		TextureRef:           &GUITextureRef{Path: textureSample, Resolved: true, Embedded: true, dataURI: "data:image/png;base64,Y29hdA=="},
		BackgroundTexture:    "gfx/interface/coat_of_arms/background.dds",
		BackgroundTextureRef: &GUITextureRef{Path: "gfx/interface/coat_of_arms/background.dds", Resolved: true, Embedded: true, dataURI: "data:image/png;base64,YmFja2dyb3VuZA=="},
		CoatOfArmsMask:       "gfx/interface/coat_of_arms/title_mask.dds",
		CoatOfArmsMaskRef:    &GUITextureRef{Path: "gfx/interface/coat_of_arms/title_mask.dds", Resolved: true, Embedded: true, dataURI: "data:image/png;base64,bWFzaw=="},
		CoatOfArmsOffset:     &GUIVector{X: "0", Y: "0.07"},
		CoatOfArmsScale:      &GUIVector{X: "0.9", Y: "0.9"},
		Semantics:            &GUISemantics{CoatOfArmsTexture: "[Title.GetTitleCoA.GetTexture('(int32)256','(int32)256')]"},
		Scenario:             &GUINodeScenario{Source: "provided", Texture: &textureSample, CoatOfArms: true},
	}}}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-coat-of-arms-mask-resource="gfx/interface/coat_of_arms/title_mask.dds"`,
		`data-ck3-coat-of-arms-mask-resource-resolved="true"`,
		`class="ck3-texture ck3-texture-a1 ck3-background"`,
		`class="ck3-coa-layer"`,
		`class="ck3-coa-mask"`,
		`class="ck3-texture ck3-coa-content ck3-texture-a0"`,
		`mask-image:var(--ck3-texture-image-a2)`,
		`--ck3-coa-offset-x:0.000%`,
		`--ck3-coa-offset-y:7.000%`,
		`--ck3-coa-scale-x:0.900`,
		`--ck3-coa-scale-y:0.900`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("coat-of-arms composition preview missing %q", expected)
		}
	}
	if strings.Count(result.Document, `class="ck3-coa-layer"`) != 1 {
		t.Fatal("background texture incorrectly inherited coat-of-arms composition")
	}
}

func TestRenderGUIHTMLInspectorReplaysFitType(t *testing.T) {
	dataURI := "data:image/png;base64,ZmFsbGJhY2s="
	preview := GUIPreviewResult{Width: 320, Height: 180, Nodes: []GUIPreviewNode{
		{Index: 0, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{Width: 100, Height: 60}, TextureRef: &GUITextureRef{Embedded: true, dataURI: dataURI}, FitType: "centercrop"},
		{Index: 1, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{X: 105, Width: 100, Height: 60}, TextureRef: &GUITextureRef{Embedded: true, dataURI: dataURI}, FitType: "start"},
		{Index: 2, Parent: -1, Kind: "icon", Bounds: GUIPreviewRect{X: 210, Width: 100, Height: 60}, TextureRef: &GUITextureRef{Embedded: true, dataURI: dataURI}, FitType: "end"},
	}}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-fit-type="centercrop"`,
		`data-ck3-fit-type="start"`,
		`data-ck3-fit-type="end"`,
		`background-size:cover;background-position:center`,
		`background-size:cover;background-position:left top`,
		`background-size:cover;background-position:right bottom`,
		`fittype centercrop`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("fittype inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLReplaysAlign(t *testing.T) {
	preview := GUIPreviewResult{Width: 320, Height: 180, Nodes: []GUIPreviewNode{
		{Index: 0, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{Width: 100, Height: 30}, Align: "left|top", Text: "Left"},
		{Index: 1, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{X: 105, Width: 100, Height: 30}, Align: "center|vcenter", Text: "Center"},
		{Index: 2, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{X: 210, Width: 100, Height: 30}, Align: "right|bottom|nobaseline", Text: "Right"},
	}}
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "static", mode: GUIHTMLModeStatic},
		{name: "inspector", mode: GUIHTMLModeInspector},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: test.mode})
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{
				`data-ck3-align="left|top"`,
				`data-ck3-align="center|vcenter"`,
				`data-ck3-align="right|bottom|nobaseline"`,
				`text-align:left;justify-content:flex-start;align-items:flex-start`,
				`text-align:center;justify-content:center;align-items:center`,
				`text-align:right;justify-content:flex-end;align-items:flex-end`,
				`align left|top`,
			} {
				if !strings.Contains(result.Document, expected) {
					t.Errorf("%s align preview missing %q", test.name, expected)
				}
			}
		})
	}
}

func TestRenderGUIHTMLInspectorReplaysPressedStateAndGameViewData(t *testing.T) {
	root := GUIElement{
		Kind: "button", Name: "traveling", Size: &GUIVector{Width: "160", Height: "32"},
		Properties: []GUIProperty{
			{Name: "down", Value: "[IsGameViewOpen('travel_planner')]"},
			{Name: "selected", Value: "[Character.IsTraveling]"},
			{Name: "onclick", Value: "[ToggleGameViewData('travel_planner', TravelPlan.GetID)]"},
			{Name: "onclick", Value: "[GetVariableSystem.Toggle('extra_buttons_expand')]"},
			{Name: "onclick", Value: "[Character.ZoomCameraTo]"},
		},
	}
	preview, err := RenderGUIPreview("traveling", "element", "gui/hud.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsGameViewOpen('travel_planner')", Value: false},
		{Expression: "Character.IsTraveling", Value: true},
		{Expression: "GetVariableSystem.Exists('extra_buttons_expand')", Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-down="[IsGameViewOpen(&#39;travel_planner&#39;)]"`,
		`data-ck3-selected="[Character.IsTraveling]"`,
		`data-ck3-on-click-count="3"`,
		`data-ck3-action-plans="0,1"`,
		`data-ck3-runtime-operation="toggle_game_view_data"`,
		`data-ck3-runtime-data-expression="TravelPlan.GetID"`,
		`data-ck3-runtime-operation="toggle_variable"`,
		`data-ck3-runtime-argument="extra_buttons_expand"`,
		`id="ck3-sim-down"`,
		`id="ck3-sim-selected-state"`,
		`class="ck3-node ck3-button is-sim-selected-state"`,
		`aria-pressed="true"`,
		`function syncPressed`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("pressed-state inspector missing %q", expected)
		}
	}
	if result.Behaviors.DownExpressions != 1 || result.Behaviors.SelectedExpressions != 1 || result.Behaviors.ClickActions != 3 || result.Behaviors.RuntimeActions != 2 {
		t.Fatalf("pressed-state behavior summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorReplaysProvidedRightClick(t *testing.T) {
	const actionExpression = "[DefaultOnCharacterRightClick(Character.GetID)]"
	root := GUIElement{Kind: "button", Name: "portrait", Size: &GUIVector{Width: "64", Height: "64"}, Properties: []GUIProperty{{Name: "onrightclick", Value: actionExpression}}}
	preview, err := RenderGUIPreview("portrait", "element", "gui/shared/portraits.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntimeWithActions(&preview, []GUIRuntimeFactInput{
		{Expression: "CharacterContextMenuOpen", Value: false},
	}, []GUIRuntimeActionEffectInput{{
		Expression: actionExpression,
		Updates:    []GUIRuntimeActionUpdateInput{{Expression: "CharacterContextMenuOpen", Operation: "set", Value: true}},
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-on-right-click="[DefaultOnCharacterRightClick(Character.GetID)]"`,
		`data-ck3-right-click-action-plan="0"`,
		`id="ck3-sim-right-click"`,
		`function actionOwnerFor(node,kind)`,
		`canvas.addEventListener('contextmenu'`,
		`applyRuntimeAction(actionOwner,'right')`,
		`applyRuntimeAction(selected,'right')`,
		`data-ck3-runtime-operation="provided_effect"`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("right-click inspector missing %q", expected)
		}
	}
	if result.Behaviors.RightClickActions != 1 || result.Behaviors.RuntimeActions != 1 || result.Behaviors.RuntimeActionEffects != 1 {
		t.Fatalf("right-click behavior summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorReplaysScaleProperty(t *testing.T) {
	root := GUIElement{Kind: "widget", Name: "barber_head", Size: &GUIVector{Width: "60", Height: "60"}, Properties: []GUIProperty{{Name: "scale", Value: "[PortraitScale]"}}}
	preview, err := RenderGUIPreview("barber_head", "element", "gui/types_FB.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "PortraitScale", Value: 1.35}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-scale="[PortraitScale]"`,
		`data-ck3-scale-plan="0"`,
		`data-sim-scale="1.35"`,
		`--ck3-scale:1.350`,
		`function refreshRuntimeScale`,
		`['min','max','value','animatedProgressValue','alpha','scale','rotateUv','minWidth','maxWidth','minHeight','maxHeight','marginLeft','marginRight','marginTop','marginBottom','statePositionX','statePositionY']`,
		`scale(var(--ck3-scale,1)) scale(var(--ck3-state-scale,1))`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("scale inspector missing %q", expected)
		}
	}
	if result.Behaviors.ScaleExpressions != 1 {
		t.Fatalf("scale behavior count=%d want 1", result.Behaviors.ScaleExpressions)
	}
}

func TestRenderGUIHTMLInspectorReplaysRotateUV(t *testing.T) {
	root := GUIElement{Kind: "modify_texture", Name: "wheel", Size: &GUIVector{Width: "60", Height: "60"}, Properties: []GUIProperty{
		{Name: "rotate_uv", Value: "[WheelRotation]"},
		{Name: "mirror", Value: "horizontal"},
	}}
	preview, err := RenderGUIPreview("wheel", "element", "gui/window_tgp_dynastic_cycle.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "WheelRotation", Value: -90}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-rotate-uv="[WheelRotation]"`,
		`data-ck3-rotate-uv-plan="0"`,
		`data-sim-rotate-uv="-90"`,
		`--ck3-rotate-uv:-90.000deg`,
		`function refreshRuntimeRotateUV`,
		`['min','max','value','animatedProgressValue','alpha','scale','rotateUv','minWidth','maxWidth','minHeight','maxHeight','marginLeft','marginRight','marginTop','marginBottom','statePositionX','statePositionY']`,
		`rotate(var(--ck3-rotate-uv,0deg)) scale(var(--ck3-mirror-x,1),var(--ck3-mirror-y,1))`,
		`.ck3-texture.ck3-mirror-horizontal{--ck3-mirror-x:-1}`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("rotate_uv inspector missing %q", expected)
		}
	}
	if result.Behaviors.RotateUVExpressions != 1 {
		t.Fatalf("rotate_uv behavior count=%d want 1", result.Behaviors.RotateUVExpressions)
	}
}

func TestRenderGUIHTMLInspectorReplaysExclusiveMapModeSelection(t *testing.T) {
	root := GUIElement{
		Kind: "button_round", Name: "map_mode_biozones_button", Size: &GUIVector{Width: "32", Height: "32"},
		Properties: []GUIProperty{
			{Name: "down", Value: "[IsMapMode('biozones')]"},
			{Name: "onclick", Value: "[SetMapMode('biozones')]"},
		},
	}
	preview, err := RenderGUIPreview("map_mode_biozones_button", "element", "gui/shared/mapmodes.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsMapMode('realms')", Value: true},
		{Expression: "IsMapMode('biozones')", Value: false},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-runtime-expression="IsMapMode(&#39;realms&#39;)"`,
		`data-ck3-runtime-expression="IsMapMode(&#39;biozones&#39;)"`,
		`data-ck3-runtime-operation="set_map_mode"`,
		`data-ck3-runtime-argument="biozones"`,
		`item.dataset.ck3RuntimeExpression||''`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("map-mode inspector missing %q", expected)
		}
	}
	if result.Behaviors.DownExpressions != 1 || result.Behaviors.RuntimeActions != 1 {
		t.Fatalf("map-mode behavior summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorReplaysProvidedActionPostconditions(t *testing.T) {
	actionExpression := "[GetScriptedGui('biodiversity_map').Execute(GuiScope.End)]"
	shownExpression := "GetScriptedGui('biodiversity_map').IsShown(GuiScope.End)"
	root := GUIElement{
		Kind: "button_round", Name: "biodiversity", Size: &GUIVector{Width: "32", Height: "32"},
		Properties: []GUIProperty{
			{Name: "down", Value: "[And(GetScriptedGui('biodiversity_map').IsShown(GuiScope.End), IsMapMode('biodiversity_mapmode'))]"},
			{Name: "onclick", Value: actionExpression},
		},
	}
	preview, err := RenderGUIPreview("biodiversity", "element", "gui/shared/mapmodes.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntimeWithActions(&preview, []GUIRuntimeFactInput{
		{Expression: shownExpression, Value: false},
		{Expression: "IsMapMode('biodiversity_mapmode')", Value: true},
	}, []GUIRuntimeActionEffectInput{{
		Expression: actionExpression,
		Updates:    []GUIRuntimeActionUpdateInput{{Expression: shownExpression, Operation: "set", Value: true}},
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-runtime-operation="provided_effect"`,
		`data-ck3-runtime-source="provided"`,
		`data-ck3-runtime-updates="`,
		`function applyProvidedEffect`,
		`function applyActionUpdates`,
		`'provided postcondition'`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("provided action inspector missing %q", expected)
		}
	}
	if result.Behaviors.RuntimeActionEffects != 1 || result.Behaviors.RuntimeUnusedEffects != 0 || result.Behaviors.RuntimeActions != 1 {
		t.Fatalf("provided action behavior summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorReplaysTypedVariableSystemState(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Name: "magic_tabs", Size: &GUIVector{Width: "320", Height: "80"},
		Children: []GUIElement{
			{
				Kind: "button", Name: "generation_tab", Size: &GUIVector{Width: "100", Height: "40"},
				Properties: []GUIProperty{
					{Name: "onclick", Value: "[GetVariableSystem.Set('magic_tab', 'generation')]"},
				},
			},
			{
				Kind: "button", Name: "clear_tab", Size: &GUIVector{Width: "100", Height: "40"},
				Properties: []GUIProperty{
					{Name: "onclick", Value: "[GetVariableSystem.Clear('magic_tab')]"},
				},
			},
			{
				Kind: "icon", Name: "generation_selected", Size: &GUIVector{Width: "32", Height: "32"},
				Properties: []GUIProperty{
					{Name: "visible", Value: "[GetVariableSystem.HasValue('magic_tab', 'generation')]"},
				},
			},
		},
	}
	preview, err := RenderGUIPreview("magic_tabs", "type", "gui/shared/magic.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "GetVariableSystem.Exists('magic_tab')", Value: false},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-runtime-expression="GetVariableSystem.Exists(&#39;magic_tab&#39;)"`,
		`data-ck3-runtime-expression="GetVariableSystem.Get(&#39;magic_tab&#39;)"`,
		`data-ck3-runtime-operation="set_variable"`,
		`data-ck3-runtime-operation="clear_variable"`,
		`data-ck3-runtime-updates="`,
		`update.operation==='unset'`,
		`function applyActionUpdates`,
		`bounded '+action.operation`,
		`function actionOwnerFor`,
		`root.classList.contains('visual-mode')`,
		`applyRuntimeAction(actionOwner,'left')`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("typed variable inspector missing %q", expected)
		}
	}
	if result.Behaviors.RuntimeActions != 2 || preview.Runtime.Stats.Expressions != 1 {
		t.Fatalf("typed variable behavior summary is incomplete: behaviors=%+v runtime=%+v", result.Behaviors, preview.Runtime.Stats)
	}
}

func TestRenderGUIHTMLInspectorReflowsIgnoreInvisibleContainers(t *testing.T) {
	root := GUIElement{
		Kind: "flowcontainer", Size: &GUIVector{Width: "100", Height: "30"},
		Properties: []GUIProperty{
			{Name: "ignoreinvisible", Value: "yes"},
			{Name: "spacing", Value: "2"},
		},
		Children: []GUIElement{
			{Kind: "button_round", Name: "conditional", Properties: []GUIProperty{{Name: "visible", Value: "[ShowConditional]"}}},
			{Kind: "button_round", Name: "always"},
		},
	}
	preview, err := RenderGUIPreview("dynamic_flow", "type", "gui/runtime.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "ShowConditional", Value: false}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-flow-direction="horizontal"`,
		`data-ck3-ignore-invisible="true"`,
		`data-ck3-flow-spacing="6"`,
		`data-ck3-flow-item="true"`,
		`data-ck3-base-left="40"`,
		`.ck3-node.is-flow-ignored{display:none}`,
		`function reflowDynamicLayouts()`,
		`item.classList.toggle('is-flow-ignored'`,
		`reflowDynamicLayouts();`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("dynamic flow inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReflowsWeightedExpandingItems(t *testing.T) {
	root := GUIElement{
		Kind: "hbox", Size: &GUIVector{Width: "300", Height: "40"},
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
	preview, err := RenderGUIPreview("weighted_flow", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-stretch-horizontal="1"`,
		`data-ck3-stretch-horizontal="2"`,
		`function mainAxisStretch(node,horizontal)`,
		`var stretchTotal=0`,
		`allocation=Math.floor(remainingExtra*stretch/remainingStretch)`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("weighted flow inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReflowsFlippedGrid(t *testing.T) {
	root := GUIElement{
		Kind: "fixedgridbox", Size: &GUIVector{Width: "80", Height: "40"},
		Properties: []GUIProperty{
			{Name: "datamodel_wrap", Value: "2"},
			{Name: "addcolumn", Value: "40"},
			{Name: "addrow", Value: "20"},
			{Name: "flipdirection", Value: "yes"},
			{Name: "layoutanchor", Value: "bottomleft"},
		},
		Children: []GUIElement{
			{Kind: "button", Name: "one", Size: &GUIVector{Width: "20", Height: "10"}},
			{Kind: "button", Name: "two", Size: &GUIVector{Width: "20", Height: "10"}},
		},
	}
	preview, err := RenderGUIPreview("flipped_grid", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-grid-flip="true"`,
		`data-ck3-grid-layout-anchor="bottomleft"`,
		`var flip=container.dataset.ck3GridFlip==='true'`,
		`var rows=Math.ceil(visible.length/columns)`,
		`function gridOrigin(container,parent,contentWidth,contentHeight)`,
		`var origin=gridOrigin(container,parent,columns*columnStep,rows*rowStep)`,
		`if(flip){row=rows-1-row;column=columns-1-column;}`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("flipped grid inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorScrollboxClipsAndScrolls(t *testing.T) {
	root := GUIElement{
		Kind: "scrollbox", Name: "history_scroll", Size: &GUIVector{Width: "100", Height: "50"},
		Children: []GUIElement{
			{Kind: "button", Name: "one", Size: &GUIVector{Width: "90", Height: "30"}},
			{Kind: "button", Name: "two", Size: &GUIVector{Width: "90", Height: "30"}},
			{Kind: "button", Name: "three", Size: &GUIVector{Width: "90", Height: "30"}},
		},
	}
	preview, err := RenderGUIPreview("scrolling", "type", "gui/test.gui", root, 400, 200, 20)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if result.Behaviors.ScrollViewports != 1 {
		t.Fatalf("scroll viewport behavior count=%d want 1", result.Behaviors.ScrollViewports)
	}
	for _, expected := range []string{
		`data-ck3-scroll-viewport="true"`,
		`data-ck3-scroll-direction="vertical"`,
		`data-ck3-scroll-content-height="216"`,
		`data-ck3-scroll-step="144"`,
		`data-ck3-scroll-control="0"`,
		`type="range" min="0" max="96"`,
		`class="ck3-node ck3-button is-scroll-clipped"`,
		`.ck3-node.is-scroll-clipped{display:none}`,
		`function applyScrollViewport(container)`,
		`function applyScrollClipping()`,
		`function nearestScrollViewport(node)`,
		`function measureAutoResizeTextNodes()`,
		`.ck3-node[data-ck3-multiline=true]>.ck3-caption`,
		`canvas.addEventListener('wheel'`,
		`{passive:false}`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("scrollbox inspector missing %q", expected)
		}
	}
	staticResult, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeStatic})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staticResult.Document, `z-index:4;display:none;"`) {
		t.Fatal("static HTML did not clip the fully out-of-viewport scroll item")
	}
}

func TestRenderGUIHTMLInspectorShowsTooltipOverlayOnlyOnHover(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: "owner", Size: &GUIVector{Width: "80", Height: "30"},
		Children: []GUIElement{{
			Kind: "tooltipwidget",
			Children: []GUIElement{{
				Kind: "vbox", Name: "tooltip_panel", Size: &GUIVector{Width: "240", Height: "120"},
			}},
		}},
	}
	preview, err := RenderGUIPreview("tooltip_owner", "type", "gui/runtime.gui", root, 500, 300, 20)
	if err != nil {
		t.Fatal(err)
	}
	staticResult, err := RenderGUIHTMLPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(staticResult.Document, `id="ck3-node-1"`) || strings.Contains(staticResult.Document, `id="ck3-node-2"`) {
		t.Fatal("static preview rendered hover-only tooltip nodes as ordinary layout")
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-overlay-role="tooltip_root"`,
		`data-ck3-overlay-role="tooltip_content"`,
		`data-ck3-overlay-owner="0"`,
		`.ck3-tooltip-content.is-tooltip-open{display:flex`,
		`id="ck3-visual-mode" type="checkbox" checked`,
		`.ck3-inspector.visual-mode .ck3-node.is-effective-hidden{display:none}`,
		`function syncEffectiveVisibility()`,
		`function openTooltip(owner)`,
		`id="ck3-runtime-tooltip"`,
		`function openRuntimeTooltip(owner)`,
		`runtimeTooltip.textContent=value`,
		`function tooltipTextOwnerFor(node)`,
		`function tooltipOwnerFor(node)`,
		`sameTooltipGroup(node,related)`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("tooltip inspector missing %q", expected)
		}
	}
	if result.Behaviors.TooltipOverlays != 1 {
		t.Fatalf("tooltip overlay behavior count=%d want 1", result.Behaviors.TooltipOverlays)
	}
}

func TestRenderGUIHTMLInspectorHonorsTooltipVisible(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: "owner", Size: &GUIVector{Width: "80", Height: "30"},
		Properties: []GUIProperty{{Name: "tooltip_visible", Value: "[ShowTooltip]"}},
		Children: []GUIElement{{
			Kind:     "tooltipwidget",
			Children: []GUIElement{{Kind: "vbox", Name: "tooltip_panel", Size: &GUIVector{Width: "180", Height: "90"}}},
		}},
	}
	preview, err := RenderGUIPreview("tooltip_owner", "type", "gui/runtime.gui", root, 500, 300, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "ShowTooltip", Value: false}}); err != nil {
		t.Fatal(err)
	}
	if binding := preview.Nodes[0].Runtime; binding == nil || binding.TooltipVisible == nil || binding.TooltipVisible.Result == nil || *binding.TooltipVisible.Result {
		t.Fatalf("tooltip_visible runtime binding = %#v", binding)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-tooltip-visible="[ShowTooltip]"`,
		`data-ck3-tooltip-visible-plan="0"`,
		`data-sim-tooltip-visible="false"`,
		`data-ck3-initial-sim-tooltip-visible="false"`,
		`function tooltipAvailable(owner)`,
		`if(!tooltipAvailable(owner)||owner.dataset.ck3EffectiveHidden==='true'`,
		`if(!runtimeTooltip||!tooltipAvailable(owner)||owner.dataset.ck3EffectiveHidden==='true'`,
		`['visible','enabled','down','selected','checked','stateTrigger','allowOutside','tooltipVisible','grayscale']`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("tooltip_visible inspector missing %q", expected)
		}
	}
}

func TestRenderGUIHTMLInspectorReplaysCheckedState(t *testing.T) {
	root := GUIElement{
		Kind: "checkbutton", Name: "cloud_save", Size: &GUIVector{Width: "30", Height: "30"},
		Properties: []GUIProperty{{Name: "checked", Value: "[CloudSaveData.ShouldSaveToCloud]"}},
	}
	preview, err := RenderGUIPreview("cloud_save", "type", "gui/frontend_ingame_menu.gui", root, 300, 160, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "CloudSaveData.ShouldSaveToCloud", Value: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-checked="[CloudSaveData.ShouldSaveToCloud]"`,
		`data-ck3-checked-plan="0"`,
		`data-sim-checked="true"`,
		`data-ck3-initial-sim-checked="true"`,
		`data-ck3-checkable="true"`,
		`ck3-checkable`,
		`is-sim-checked`,
		`ck3-checkmark`,
		`ck3-sim-checked`,
		`function syncChecked(node)`,
		`['visible','enabled','down','selected','checked','stateTrigger','allowOutside','tooltipVisible','grayscale']`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("checked inspector missing %q", expected)
		}
	}
	if result.Behaviors.CheckedExpressions != 1 {
		t.Fatalf("checked behavior count=%d want 1", result.Behaviors.CheckedExpressions)
	}
}

func TestRenderGUIHTMLInspectorReplaysStateTrigger(t *testing.T) {
	root := GUIElement{
		Kind: "widget", Name: "stateful", Size: &GUIVector{Width: "100", Height: "40"},
		Children: []GUIElement{{
			Kind: "state", Name: "hide_when_modal", Properties: []GUIProperty{
				{Name: "alpha", Value: "0"},
				{Name: "scale", Value: "0.9"},
				{Name: "duration", Value: "0.2"},
				{Name: "trigger_when", Value: "[IsModalOpen]"},
			},
		}},
	}
	preview, err := RenderGUIPreview("stateful", "type", "gui/hud_outliner.gui", root, 300, 160, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "IsModalOpen", Value: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-state-trigger="[IsModalOpen]"`,
		`data-ck3-state-trigger-plan="0"`,
		`data-sim-state-trigger="true"`,
		`data-ck3-initial-sim-state-trigger="true"`,
		`function applyStateTriggers()`,
		`applyState(definition,'Triggered','trigger')`,
		`['visible','enabled','down','selected','checked','stateTrigger','allowOutside','tooltipVisible','grayscale']`,
		`<span>TW 1</span>`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("state trigger inspector missing %q", expected)
		}
	}
	if result.Behaviors.StateTriggers != 1 {
		t.Fatalf("state trigger behavior count=%d want 1", result.Behaviors.StateTriggers)
	}
}

func TestRenderGUIHTMLInspectorRendersColorProperty(t *testing.T) {
	root := GUIElement{
		Kind: "icon", Name: "divider", Size: &GUIVector{Width: "60", Height: "3"},
		Properties: []GUIProperty{{Name: "color", Values: []string{"0.1", "0.2", "0.3", "0.4"}}},
	}
	preview, err := RenderGUIPreview("divider", "type", "gui/shared/windows.gui", root, 200, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, nil); err != nil {
		t.Fatal(err)
	}
	if binding := preview.Nodes[0].Runtime; binding == nil || binding.Color == nil || binding.Color.Result == nil || *binding.Color.Result != "rgba(26,51,77,0.400)" {
		t.Fatalf("color runtime binding = %#v", binding)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-color="{ 0.1 0.2 0.3 0.4 }"`,
		`data-ck3-color-plan="0"`,
		`data-sim-color="rgba(26,51,77,0.400)"`,
		`--ck3-tint-color:rgba(26,51,77,0.400)`,
		`function normalizeRuntimeColor(raw)`,
		`function applyRuntimeColor(node,property)`,
		`['color','tintColor','fontTintColor']`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("color inspector missing %q", expected)
		}
	}
	if result.Behaviors.ColorExpressions != 1 {
		t.Fatalf("color behavior count=%d want 1", result.Behaviors.ColorExpressions)
	}
}

func TestRenderGUIHTMLInspectorReplaysGrayscale(t *testing.T) {
	root := GUIElement{
		Kind: "button", Name: "commander", Size: &GUIVector{Width: "100", Height: "40"},
		Properties: []GUIProperty{{Name: "grayscale", Value: "[Not(CharacterListItem.IsSelectable)]"}},
	}
	preview, err := RenderGUIPreview("commander", "type", "gui/window_army_select_commander.gui", root, 300, 160, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "CharacterListItem.IsSelectable", Value: false}}); err != nil {
		t.Fatal(err)
	}
	if binding := preview.Nodes[0].Runtime; binding == nil || binding.Grayscale == nil || binding.Grayscale.Result == nil || !*binding.Grayscale.Result {
		t.Fatalf("grayscale runtime binding = %#v", binding)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-grayscale="[Not(CharacterListItem.IsSelectable)]"`,
		`data-ck3-grayscale-plan="0"`,
		`data-sim-grayscale="true"`,
		`data-ck3-initial-sim-grayscale="true"`,
		`filter:grayscale(1)`,
		`property==='grayscale'`,
		`['visible','enabled','down','selected','checked','stateTrigger','allowOutside','tooltipVisible','grayscale']`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("grayscale inspector missing %q", expected)
		}
	}
	if result.Behaviors.GrayscaleExpressions != 1 {
		t.Fatalf("grayscale behavior count=%d want 1", result.Behaviors.GrayscaleExpressions)
	}
}

func TestRenderGUIHTMLScenarioOverridesRuntimePerProperty(t *testing.T) {
	falseValue, trueValue := false, true
	preview := GUIPreviewResult{Width: 320, Height: 180, Symbol: "override", Nodes: []GUIPreviewNode{{
		Index: 0, Parent: -1, Kind: "button", Bounds: GUIPreviewRect{X: 0, Y: 0, Width: 100, Height: 30},
		Scenario: &GUINodeScenario{Source: "provided", Visible: &trueValue},
		Runtime: &GUINodeRuntime{
			Visible: &GUIRuntimeBinding{PlanID: 0, Status: "evaluated", Result: &falseValue},
			Enabled: &GUIRuntimeBinding{PlanID: 1, Status: "evaluated", Result: &falseValue},
		},
	}}}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Document, `data-ck3-visible-override="scenario"`) || !strings.Contains(result.Document, `data-sim-visible="true"`) {
		t.Fatal("exact scenario did not override runtime visibility")
	}
	if !strings.Contains(result.Document, `data-sim-enabled="false"`) {
		t.Fatal("runtime enabled value should still apply independently")
	}
}

func TestRenderGUIHTMLInspectorRecomputesDynamicTextSafely(t *testing.T) {
	root := GUIElement{Kind: "vbox", Size: &GUIVector{Width: "320", Height: "100"}, Children: []GUIElement{{
		Kind: "text_single", Name: "character_name", Size: &GUIVector{Width: "260", Height: "28"},
		Properties: []GUIProperty{{Name: "raw_text", Value: "[Character.GetName]"}, {Name: "tooltip", Value: "[Character.GetTooltip]"}},
	}}}
	preview, err := RenderGUIPreview("dynamic_text_panel", "type", "gui/dynamic.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	unsafeName := `Alice </script><script>alert(1)</script>`
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "Character.GetName", Value: unsafeName}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-ck3-text-plan-raw="0"`, `data-ck3-tooltip-plan-raw="1"`,
		`data-sim-text="Alice &lt;/script&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`,
		`data-sim-tooltip="&lt;unknown&gt;"`, `data-ck3-runtime-text-plan="0"`,
		`function evaluateRuntimeTextPlan`, `function applyRuntimeTexts`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("dynamic text inspector missing %q", expected)
		}
	}
	if strings.Contains(result.Document, unsafeName) || strings.Count(result.Document, "<script>") != 1 || strings.Count(result.Document, "</script>") != 1 {
		t.Fatal("dynamic text escaped its inert HTML boundary")
	}
	if result.Behaviors.RuntimeTextPlans != 2 || result.Behaviors.RuntimeTextReady != 1 || result.Behaviors.RuntimeTextPartial != 1 {
		t.Fatalf("dynamic text behavior summary is incomplete: %+v", result.Behaviors)
	}
}

func TestRenderGUIHTMLInspectorEmbedsConditionalTextRuntime(t *testing.T) {
	root := GUIElement{Kind: "text_single", Name: "conditional", Size: &GUIVector{Width: "260", Height: "28"},
		Properties: []GUIProperty{{Name: "raw_text", Value: "[Select_CString(IsReady, 'Ready', 'Waiting')]"}}}
	preview, err := RenderGUIPreview("conditional_text_panel", "type", "gui/conditional.gui", root, 640, 360, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "IsReady", Value: true}}); err != nil {
		t.Fatal(err)
	}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`data-sim-text="Ready"`,
		`data-ck3-runtime-text-plan="0"`,
		`function evaluateRuntimeTokens`,
		`function evaluateRuntimeTextTokens`,
		`function openRuntimeTooltip(owner)`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("conditional text inspector missing %q", expected)
		}
	}
	if strings.Count(result.Document, "<script>") != 1 || strings.Count(result.Document, "</script>") != 1 {
		t.Fatal("conditional text introduced an unsafe script boundary")
	}
}

func TestRenderGUIHTMLTextureBlendModesUseFixedClasses(t *testing.T) {
	parentTexture := &GUITextureRef{Embedded: true, dataURI: "data:image/png;base64,cGFyZW50"}
	modifierTexture := &GUITextureRef{Embedded: true, dataURI: "data:image/png;base64,bW9kaWZpZXI="}
	preview := GUIPreviewResult{Width: 320, Height: 180, Symbol: "blend", Nodes: []GUIPreviewNode{
		{Index: 0, Parent: -1, Kind: "button", TextureRef: parentTexture, Bounds: GUIPreviewRect{Width: 30, Height: 30}},
		{Index: 1, Parent: 0, Kind: "modify_texture", TextureRef: modifierTexture, TextureBlendMode: "add", Bounds: GUIPreviewRect{Width: 30, Height: 30}},
		{Index: 2, Parent: -1, Kind: "modify_texture", TextureBlendMode: `screen";display:none`, Bounds: GUIPreviewRect{X: 40, Width: 30, Height: 30}},
	}}
	result, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`class="ck3-node ck3-image ck3-texture-modifier ck3-blend-screen ck3-has-texture"`,
		`data-ck3-texture-blend-mode="add"`,
		`.ck3-blend-screen{mix-blend-mode:screen}`,
		`mask-image:var(--ck3-texture-image-a0)`,
	} {
		if !strings.Contains(result.Document, expected) {
			t.Errorf("texture blend inspector missing %q", expected)
		}
	}
	if strings.Count(result.Document, "ck3-texture-modifier ck3-blend-screen") != 1 {
		t.Fatal("an unrecognized blend mode was promoted to an executable CSS class")
	}
	if strings.Contains(result.Document, `style="display:none`) {
		t.Fatal("unrecognized blend metadata escaped into a style attribute")
	}
}

func BenchmarkRenderGUIHTMLPreview(b *testing.B) {
	root := GUIElement{Kind: "vbox", Size: &GUIVector{Width: "600", Height: "700"}}
	for index := 0; index < 200; index++ {
		root.Children = append(root.Children, GUIElement{Kind: "button", Name: "row", Size: &GUIVector{Width: "300", Height: "24"}})
	}
	preview, err := RenderGUIPreview("benchmark", "type", "gui/benchmark.gui", root, 1280, 720, 250)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := RenderGUIHTMLPreview(preview); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderGUIHTMLInspector(b *testing.B) {
	root := GUIElement{Kind: "vbox", Size: &GUIVector{Width: "600", Height: "700"}}
	for index := 0; index < 200; index++ {
		root.Children = append(root.Children, GUIElement{Kind: "button", Name: "row", Size: &GUIVector{Width: "300", Height: "24"}, Properties: []GUIProperty{{Name: "visible", Value: "[Row.IsVisible]"}}})
	}
	preview, err := RenderGUIPreview("benchmark", "type", "gui/benchmark.gui", root, 1280, 720, 250)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderGUIHTMLScrollableInspector(b *testing.B) {
	root := GUIElement{Kind: "scrollbox", Size: &GUIVector{Width: "600", Height: "700"}}
	for index := 0; index < 200; index++ {
		root.Children = append(root.Children, GUIElement{
			Kind: "button", Name: "row", Size: &GUIVector{Width: "580", Height: "24"},
			Properties: []GUIProperty{{Name: "visible", Value: "[Row.IsVisible]"}},
		})
	}
	preview, err := RenderGUIPreview("scroll_benchmark", "type", "gui/benchmark.gui", root, 1280, 720, 250)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := RenderGUIHTMLPreviewWithOptions(preview, GUIHTMLRenderOptions{Mode: GUIHTMLModeInspector}); err != nil {
			b.Fatal(err)
		}
	}
}
