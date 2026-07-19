package indexer

import (
	"fmt"
	"strings"
	"testing"
)

func TestPrepareGUIPreviewRuntimeComposesAtomicFacts(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{
		{Index: 0, Semantics: &GUISemantics{Visible: "[And( Not( IsPauseMenuShown ), And( IsDefaultGUIMode, GetPlayer.IsValid ) )]"}},
		{Index: 1, Semantics: &GUISemantics{Enabled: "[Not( GreaterThan_CFixedPoint( GetPlayer.MakeScope.Var('hide_ui_top_bar').GetValue, '(CFixedPoint)0' ) )]"}},
	}}
	err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsPauseMenuShown", Value: false},
		{Expression: "IsDefaultGUIMode", Value: true},
		{Expression: "GetPlayer.IsValid", Value: true},
		{Expression: "GetPlayer.MakeScope.Var('hide_ui_top_bar').GetValue", Value: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Runtime == nil || preview.Runtime.Stats.Expressions != 2 || preview.Runtime.Stats.Evaluated != 2 || preview.Runtime.Stats.Unknown != 0 {
		t.Fatalf("unexpected runtime summary: %#v", preview.Runtime)
	}
	if binding := preview.Nodes[0].Runtime.Visible; binding == nil || binding.Result == nil || !*binding.Result || binding.Status != "evaluated" {
		t.Fatalf("visible expression did not evaluate true: %#v", binding)
	}
	if binding := preview.Nodes[1].Runtime.Enabled; binding == nil || binding.Result == nil || !*binding.Result || binding.Status != "evaluated" {
		t.Fatalf("enabled expression did not evaluate true: %#v", binding)
	}
	for _, fact := range preview.Runtime.Facts {
		if fact.References == 0 {
			t.Fatalf("expected referenced fact: %#v", fact)
		}
	}
}

func TestPrepareGUIPreviewRuntimeBindsNumericValue(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{
		{Index: 0, Kind: "progresspie", Semantics: &GUISemantics{Value: "[Timer.GetProgress]"}},
		{Index: 1, Kind: "progressbar", Semantics: &GUISemantics{Min: "0", Max: "100", Value: "25"}},
	}}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "Timer.GetProgress", Value: 0.75}}); err != nil {
		t.Fatal(err)
	}
	if preview.Runtime == nil || preview.Runtime.Stats.Expressions != 4 || preview.Runtime.Stats.Evaluated != 4 || preview.Runtime.Stats.Unknown != 0 {
		t.Fatalf("unexpected numeric runtime summary: %#v", preview.Runtime)
	}
	first := preview.Nodes[0].Runtime.Value
	second := preview.Nodes[1].Runtime.Value
	if first == nil || first.Result == nil || *first.Result != 0.75 || first.Status != "evaluated" {
		t.Fatalf("dynamic progress value did not evaluate: %#v", first)
	}
	if second == nil || second.Result == nil || *second.Result != 25 || second.Status != "evaluated" {
		t.Fatalf("literal progress value did not evaluate: %#v", second)
	}
	if preview.Nodes[1].Runtime.Min == nil || preview.Nodes[1].Runtime.Min.Result == nil || *preview.Nodes[1].Runtime.Min.Result != 0 ||
		preview.Nodes[1].Runtime.Max == nil || preview.Nodes[1].Runtime.Max.Result == nil || *preview.Nodes[1].Runtime.Max.Result != 100 {
		t.Fatalf("progress range did not evaluate: %#v", preview.Nodes[1].Runtime)
	}
	for _, binding := range []*GUIRuntimeNumberBinding{first, second} {
		plan := preview.Runtime.Plans[binding.PlanID]
		if plan.Kind != guiRuntimeKindNumber || plan.Number == nil {
			t.Fatalf("numeric plan lost its strong result kind: %#v", plan)
		}
	}
}

func TestGUIRuntimeThreeValuedShortCircuit(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{
		{Index: 0, Semantics: &GUISemantics{Visible: "[And( KnownFalse, Missing )]"}},
		{Index: 1, Semantics: &GUISemantics{Visible: "[Or( KnownTrue, Missing )]"}},
		{Index: 2, Semantics: &GUISemantics{Visible: "[And( KnownTrue, Missing )]"}},
	}}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "KnownFalse", Value: false}, {Expression: "KnownTrue", Value: true}}); err != nil {
		t.Fatal(err)
	}
	first := preview.Nodes[0].Runtime.Visible
	second := preview.Nodes[1].Runtime.Visible
	third := preview.Nodes[2].Runtime.Visible
	if first.Result == nil || *first.Result || second.Result == nil || !*second.Result {
		t.Fatalf("short-circuit results are wrong: first=%#v second=%#v", first, second)
	}
	if third.Result != nil || third.Status != "unknown" {
		t.Fatalf("missing fact should stay unknown: %#v", third)
	}
}

func TestPrepareGUIPreviewRuntimeRejectsUnsafeInputs(t *testing.T) {
	preview := GUIPreviewResult{}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "A", Value: map[string]any{"nested": true}}}); err == nil {
		t.Fatal("expected structured runtime value to be rejected")
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "A", Value: true}, {Expression: "[A]", Value: true}}); err == nil {
		t.Fatal("expected normalized duplicate fact to be rejected")
	}
	facts := make([]GUIRuntimeFactInput, GUIRuntimeMaxFacts+1)
	for index := range facts {
		facts[index] = GUIRuntimeFactInput{Expression: "Fact" + strings.Repeat("x", index), Value: true}
	}
	if err := prepareGUIPreviewRuntime(&preview, facts); err == nil {
		t.Fatal("expected runtime fact limit to be enforced")
	}
}

func TestPrepareGUIPreviewRuntimeReportsMalformedExpression(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{Index: 0, Semantics: &GUISemantics{Visible: "[And(A, Not(B)]"}}}}
	if err := prepareGUIPreviewRuntime(&preview, nil); err != nil {
		t.Fatal(err)
	}
	if preview.Runtime == nil || preview.Runtime.Stats.Unsupported != 1 || len(preview.Runtime.Unsupported) != 1 {
		t.Fatalf("malformed expression should be reported, not fail preview: %#v", preview.Runtime)
	}
	if preview.Nodes[0].Runtime.Visible.Status != "unsupported" {
		t.Fatalf("unexpected binding: %#v", preview.Nodes[0].Runtime.Visible)
	}
}

func TestPrepareGUIPreviewRuntimeCompilesGameViewActions(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{
		{Index: 0, Semantics: &GUISemantics{Visible: "[Not( IsGameViewOpen( 'outliner' ) )]", OnClick: "[ToggleGameView('outliner')]"}},
		{Index: 1, Semantics: &GUISemantics{OnClick: "[OpenGameView('outliner')]"}},
		{Index: 2, Semantics: &GUISemantics{OnClick: "[CloseGameView('outliner')]"}},
		{Index: 3, Semantics: &GUISemantics{OnClick: "[ExecuteConsoleCommand('unsafe')]"}},
	}}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{{Expression: "IsGameViewOpen('outliner')", Value: false}}); err != nil {
		t.Fatal(err)
	}
	if preview.Runtime.Stats.Actions != 3 || len(preview.Runtime.Actions) != 3 || len(preview.Runtime.Facts) != 1 {
		t.Fatalf("unexpected action compilation: %#v", preview.Runtime)
	}
	for index, operation := range []string{"toggle_game_view", "open_game_view", "close_game_view"} {
		binding := preview.Nodes[index].Runtime.Action
		if binding == nil || binding.Status != "compiled" || preview.Runtime.Actions[binding.PlanID].Operation != operation || preview.Runtime.Actions[binding.PlanID].Fact != 0 {
			t.Fatalf("node %d action mismatch: binding=%#v plans=%#v", index, binding, preview.Runtime.Actions)
		}
	}
	if preview.Nodes[3].Runtime != nil && preview.Nodes[3].Runtime.Action != nil {
		t.Fatal("unrecognized click action was compiled")
	}
}

func TestPrepareGUIPreviewRuntimeReplaysPressedStateAndMultipleClickEffects(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0,
		Semantics: &GUISemantics{
			Down:     "[IsGameViewOpen('travel_planner')]",
			Selected: "[Character.IsTraveling]",
			OnClick:  "[Character.ZoomCameraTo]",
			OnClicks: []string{
				"[ToggleGameViewData('travel_planner', TravelPlan.GetID)]",
				"[Character.ZoomCameraTo]",
			},
		},
	}}}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsGameViewOpen('travel_planner')", Value: false},
		{Expression: "Character.IsTraveling", Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	if preview.Runtime.Stats.Expressions != 2 || preview.Runtime.Stats.Evaluated != 2 || preview.Runtime.Stats.Actions != 1 {
		t.Fatalf("unexpected runtime summary: %#v", preview.Runtime)
	}
	node := preview.Nodes[0]
	if node.Runtime == nil || node.Runtime.Down == nil || node.Runtime.Down.Result == nil || *node.Runtime.Down.Result {
		t.Fatalf("down state did not evaluate false: %#v", node.Runtime)
	}
	if node.Runtime.Selected == nil || node.Runtime.Selected.Result == nil || !*node.Runtime.Selected.Result {
		t.Fatalf("selected state did not evaluate true: %#v", node.Runtime)
	}
	if len(node.Runtime.Actions) != 1 || node.Runtime.Action == nil {
		t.Fatalf("safe action from multiple onclick properties was lost: %#v", node.Runtime)
	}
	action := preview.Runtime.Actions[node.Runtime.Actions[0].PlanID]
	if action.Operation != "toggle_game_view_data" || action.Argument != "travel_planner" || action.DataExpression != "TravelPlan.GetID" {
		t.Fatalf("ToggleGameViewData was compiled incorrectly: %#v", action)
	}
}

func TestPrepareGUIPreviewRuntimeReplaysGameViewAndVariableSystemInSourceOrder(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0,
		Semantics: &GUISemantics{
			Down:    "[IsGameViewOpen('find_title')]",
			OnClick: "[GetVariableSystem.Toggle('extra_buttons_expand')]",
			OnClicks: []string{
				"[ToggleGameView('find_title')]",
				"[GetVariableSystem.Toggle('extra_buttons_expand')]",
			},
		},
	}}}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsGameViewOpen('find_title')", Value: false},
		{Expression: "GetVariableSystem.Exists('extra_buttons_expand')", Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if node.Runtime == nil || len(node.Runtime.Actions) != 2 {
		t.Fatalf("expected both safe click effects, got %#v", node.Runtime)
	}
	first := preview.Runtime.Actions[node.Runtime.Actions[0].PlanID]
	second := preview.Runtime.Actions[node.Runtime.Actions[1].PlanID]
	if first.Operation != "toggle_game_view" || first.Argument != "find_title" {
		t.Fatalf("first action order changed: %#v", first)
	}
	if second.Operation != "toggle_variable" || second.Argument != "extra_buttons_expand" {
		t.Fatalf("second action order changed: %#v", second)
	}
	if got := preview.Runtime.Facts[second.Fact].Expression; got != "GetVariableSystem.Exists('extra_buttons_expand')" {
		t.Fatalf("variable action fact = %q", got)
	}
	if preview.Runtime.Stats.Actions != 2 {
		t.Fatalf("runtime action count = %d", preview.Runtime.Stats.Actions)
	}
}

func TestGUIRuntimeCompilesSafeVariableSystemClearOnly(t *testing.T) {
	compiler := &guiRuntimeCompiler{
		factIndex: map[string]int{}, planIndex: map[string]int{}, actionIndex: map[string]int{}, textPlanIndex: map[string]int{}, unsupported: map[string]string{},
	}
	actionID, ok := compiler.action("[GetVariableSystem.Clear('extra_buttons_expand')]")
	if !ok {
		t.Fatal("static GetVariableSystem.Clear was not compiled")
	}
	action := compiler.actions[actionID]
	if action.Operation != "clear_variable" || action.Argument != "extra_buttons_expand" {
		t.Fatalf("clear action compiled incorrectly: %#v", action)
	}
	if len(action.Updates) != 2 {
		t.Fatalf("clear action updates = %#v", action.Updates)
	}
	if action.Updates[0].Expression != "GetVariableSystem.Exists('extra_buttons_expand')" ||
		action.Updates[0].Operation != guiRuntimeActionUpdateSet || action.Updates[0].Value != false {
		t.Fatalf("clear existence update = %#v", action.Updates[0])
	}
	if action.Updates[1].Expression != "GetVariableSystem.Get('extra_buttons_expand')" ||
		action.Updates[1].Operation != guiRuntimeActionUpdateUnset || action.Updates[1].Value != nil {
		t.Fatalf("clear value update = %#v", action.Updates[1])
	}
	for _, expression := range []string{
		"[GetVariableSystem.Toggle(DynamicVariable)]",
		"[GetVariableSystem.Clear('')]",
		"[GetVariableSystem.Clear('a', 'b')]",
		"[GetVariableSystem.Set('section', DynamicValue)]",
	} {
		if _, ok := compiler.action(expression); ok {
			t.Fatalf("unsafe variable action compiled: %q", expression)
		}
	}
}

func TestGUIRuntimeCompilesStaticMapModeAndVariableSet(t *testing.T) {
	compiler := &guiRuntimeCompiler{
		factIndex: map[string]int{}, planIndex: map[string]int{}, actionIndex: map[string]int{}, textPlanIndex: map[string]int{}, unsupported: map[string]string{},
	}
	mapModeID, ok := compiler.action("[SetMapMode('biozones')]")
	if !ok {
		t.Fatal("static SetMapMode was not compiled")
	}
	mapMode := compiler.actions[mapModeID]
	if mapMode.Operation != "set_map_mode" || mapMode.Argument != "biozones" || compiler.facts[mapMode.Fact].Expression != "IsMapMode('biozones')" {
		t.Fatalf("map mode action compiled incorrectly: %#v facts=%#v", mapMode, compiler.facts)
	}
	setID, ok := compiler.action("[GetVariableSystem.Set('lore_doc_section', 'foreword')]")
	if !ok {
		t.Fatal("static GetVariableSystem.Set was not compiled")
	}
	set := compiler.actions[setID]
	if set.Operation != "set_variable" || set.Argument != "lore_doc_section" || set.DataExpression != "'foreword'" ||
		compiler.facts[set.Fact].Expression != "GetVariableSystem.Exists('lore_doc_section')" {
		t.Fatalf("variable set action compiled incorrectly: %#v facts=%#v", set, compiler.facts)
	}
	if len(set.Updates) != 2 ||
		set.Updates[0].Expression != "GetVariableSystem.Exists('lore_doc_section')" || set.Updates[0].Value != true ||
		set.Updates[1].Expression != "GetVariableSystem.Get('lore_doc_section')" || set.Updates[1].Value != "foreword" {
		t.Fatalf("variable set typed updates = %#v", set.Updates)
	}
	for _, expression := range []string{
		"[SetMapMode(MapMode.GetKey)]",
		"[SetMapMode('')]",
		"[GetVariableSystem.Set('section')]",
		"[GetVariableSystem.Set('section', GetPlayer.GetID)]",
	} {
		if _, ok := compiler.action(expression); ok {
			t.Fatalf("dynamic or malformed action compiled: %q", expression)
		}
	}
}

func TestGUIRuntimeCompilesVariableSystemHasValueFromExistenceAndTypedValue(t *testing.T) {
	for _, test := range []struct {
		name   string
		facts  []GUIRuntimeFactInput
		result bool
	}{
		{
			name: "matching value",
			facts: []GUIRuntimeFactInput{
				{Expression: "GetVariableSystem.Exists('magic_tab')", Value: true},
				{Expression: "GetVariableSystem.Get('magic_tab')", Value: "generation"},
			},
			result: true,
		},
		{
			name: "missing variable short circuits",
			facts: []GUIRuntimeFactInput{
				{Expression: "GetVariableSystem.Exists('magic_tab')", Value: false},
			},
			result: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
				Index: 0,
				Semantics: &GUISemantics{
					Visible: "[GetVariableSystem.HasValue('magic_tab', 'generation')]",
				},
			}}}
			if err := prepareGUIPreviewRuntime(&preview, test.facts); err != nil {
				t.Fatal(err)
			}
			binding := preview.Nodes[0].Runtime.Visible
			if binding == nil || binding.Result == nil || *binding.Result != test.result {
				t.Fatalf("HasValue result = %#v, want %t; runtime=%#v", binding, test.result, preview.Runtime)
			}
			plan := preview.Runtime.Plans[binding.PlanID]
			if !plan.Supported || len(plan.Tokens) != 5 || plan.Tokens[len(plan.Tokens)-1].Op != "and" {
				t.Fatalf("HasValue was not lowered to Exists && Get == literal: %#v", plan)
			}
		})
	}
}

func TestGUIRuntimeRejectsDynamicVariableSystemHasValue(t *testing.T) {
	compiler := &guiRuntimeCompiler{
		factIndex: map[string]int{}, planIndex: map[string]int{}, actionIndex: map[string]int{}, textPlanIndex: map[string]int{}, unsupported: map[string]string{},
	}
	for _, expression := range []string{
		"[GetVariableSystem.HasValue(DynamicVariable, 'generation')]",
		"[GetVariableSystem.HasValue('magic_tab', DynamicValue)]",
		"[GetVariableSystem.HasValue('magic_tab')]",
	} {
		planID := compiler.plan(expression)
		if compiler.plans[planID].Supported {
			t.Fatalf("dynamic or malformed HasValue compiled: %q", expression)
		}
	}
}

func TestPrepareGUIPreviewRuntimeBindsProvidedScriptedGUIEffect(t *testing.T) {
	actionExpression := "[GetScriptedGui('biodiversity_map').Execute(GuiScope.End)]"
	shownExpression := "GetScriptedGui('biodiversity_map').IsShown(GuiScope.End)"
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0,
		Semantics: &GUISemantics{
			Down:    "[And(GetScriptedGui('biodiversity_map').IsShown(GuiScope.End), IsMapMode('biodiversity_mapmode'))]",
			OnClick: actionExpression,
		},
	}}}
	err := prepareGUIPreviewRuntimeWithActions(&preview, []GUIRuntimeFactInput{
		{Expression: shownExpression, Value: false},
		{Expression: "IsMapMode('biodiversity_mapmode')", Value: true},
	}, []GUIRuntimeActionEffectInput{{
		Expression: actionExpression,
		Updates: []GUIRuntimeActionUpdateInput{{
			Expression: shownExpression, Operation: "set", Value: true,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	node := preview.Nodes[0]
	if node.Runtime == nil || len(node.Runtime.Actions) != 1 {
		t.Fatalf("provided action effect was not bound: %#v", node.Runtime)
	}
	action := preview.Runtime.Actions[node.Runtime.Actions[0].PlanID]
	if action.Operation != "provided_effect" || action.Source != "provided" || len(action.Updates) != 1 {
		t.Fatalf("provided action plan = %#v", action)
	}
	update := action.Updates[0]
	if update.Expression != shownExpression || update.Operation != "set" || update.Value != true {
		t.Fatalf("provided action update = %#v", update)
	}
	if preview.Runtime.Facts[update.Fact].References != 2 {
		t.Fatalf("updated fact references = %d, want down plus action", preview.Runtime.Facts[update.Fact].References)
	}
	if preview.Runtime.Stats.ActionEffects != 1 || preview.Runtime.Stats.UnusedEffects != 0 || preview.Runtime.Stats.Actions != 1 {
		t.Fatalf("provided action stats = %#v", preview.Runtime.Stats)
	}
}

func TestPrepareGUIPreviewRuntimeSupportsTypedAtomicActionUpdates(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0, Semantics: &GUISemantics{OnClick: "[Custom.Apply]"},
	}}}
	if err := prepareGUIPreviewRuntimeWithActions(&preview, []GUIRuntimeFactInput{
		{Expression: "Counter", Value: 1},
		{Expression: "Label", Value: "old"},
		{Expression: "Expanded", Value: false},
	}, []GUIRuntimeActionEffectInput{{
		Expression: "[Custom.Apply]",
		Updates: []GUIRuntimeActionUpdateInput{
			{Expression: "Counter", Operation: "set", Value: 2.5},
			{Expression: "Label", Operation: "set", Value: "new"},
			{Expression: "Expanded", Operation: "toggle"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	action := preview.Runtime.Actions[0]
	if len(action.Updates) != 3 || action.Updates[0].Value != 2.5 || action.Updates[1].Value != "new" || action.Updates[2].Operation != "toggle" {
		t.Fatalf("typed action updates = %#v", action.Updates)
	}
	for _, update := range action.Updates {
		if preview.Runtime.Facts[update.Fact].References != 1 {
			t.Fatalf("update fact %q references = %d", update.Expression, preview.Runtime.Facts[update.Fact].References)
		}
	}
}

func TestPrepareGUIPreviewRuntimeRejectsUnsafeActionEffectContracts(t *testing.T) {
	base := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0, Semantics: &GUISemantics{OnClick: "[Unsafe.Execute]"},
	}}}
	tests := []struct {
		name    string
		facts   []GUIRuntimeFactInput
		effects []GUIRuntimeActionEffectInput
	}{
		{name: "empty updates", effects: []GUIRuntimeActionEffectInput{{Expression: "[Unsafe.Execute]"}}},
		{name: "duplicate effects", effects: []GUIRuntimeActionEffectInput{
			{Expression: "[Unsafe.Execute]", Updates: []GUIRuntimeActionUpdateInput{{Expression: "Shown", Operation: "set", Value: true}}},
			{Expression: "Unsafe.Execute", Updates: []GUIRuntimeActionUpdateInput{{Expression: "Other", Operation: "set", Value: true}}},
		}},
		{name: "duplicate updates", effects: []GUIRuntimeActionEffectInput{{
			Expression: "[Unsafe.Execute]", Updates: []GUIRuntimeActionUpdateInput{
				{Expression: "Shown", Operation: "set", Value: true},
				{Expression: " Shown ", Operation: "toggle"},
			},
		}}},
		{name: "toggle with value", effects: []GUIRuntimeActionEffectInput{{
			Expression: "[Unsafe.Execute]", Updates: []GUIRuntimeActionUpdateInput{{Expression: "Shown", Operation: "toggle", Value: false}},
		}}},
		{name: "non scalar set", effects: []GUIRuntimeActionEffectInput{{
			Expression: "[Unsafe.Execute]", Updates: []GUIRuntimeActionUpdateInput{{Expression: "Shown", Operation: "set", Value: []string{"no"}}},
		}}},
		{name: "fact type conflict", facts: []GUIRuntimeFactInput{{Expression: "Shown", Value: "yes"}}, effects: []GUIRuntimeActionEffectInput{{
			Expression: "[Unsafe.Execute]", Updates: []GUIRuntimeActionUpdateInput{{Expression: "Shown", Operation: "set", Value: true}},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview := base
			preview.Nodes = append([]GUIPreviewNode(nil), base.Nodes...)
			if err := prepareGUIPreviewRuntimeWithActions(&preview, test.facts, test.effects); err == nil {
				t.Fatal("unsafe action effect contract was accepted")
			}
		})
	}
}

func TestPrepareGUIPreviewRuntimeReportsUnusedAndDoesNotOverrideBuiltinAction(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{
		Index: 0, Semantics: &GUISemantics{OnClick: "[ToggleGameView('outliner')]"},
	}}}
	effects := []GUIRuntimeActionEffectInput{{
		Expression: "[ToggleGameView('outliner')]",
		Updates:    []GUIRuntimeActionUpdateInput{{Expression: "Unexpected", Operation: "set", Value: true}},
	}}
	if err := prepareGUIPreviewRuntimeWithActions(&preview, nil, effects); err != nil {
		t.Fatal(err)
	}
	action := preview.Runtime.Actions[preview.Nodes[0].Runtime.Actions[0].PlanID]
	if action.Operation != "toggle_game_view" || action.Source != "" {
		t.Fatalf("provided effect overrode builtin action: %#v", action)
	}
	if preview.Runtime.Stats.UnusedEffects != 1 || len(preview.Warnings) == 0 {
		t.Fatalf("unused builtin override was not reported: stats=%#v warnings=%#v", preview.Runtime.Stats, preview.Warnings)
	}
}

func TestGUIRuntimeRejectsUnsafeGameViewDataActionShapes(t *testing.T) {
	compiler := &guiRuntimeCompiler{
		factIndex: map[string]int{}, planIndex: map[string]int{}, actionIndex: map[string]int{}, textPlanIndex: map[string]int{}, unsupported: map[string]string{},
	}
	for _, expression := range []string{
		"[ToggleGameViewData(DynamicView, TravelPlan.GetID)]",
		"[ToggleGameViewData('travel_planner')]",
		"[ToggleGameViewData('travel_planner', )]",
		"[ToggleGameViewData('travel_planner', A\nB)]",
	} {
		if _, ok := compiler.action(expression); ok {
			t.Fatalf("unsafe action shape compiled: %q", expression)
		}
	}
}

func TestPrepareGUIPreviewRuntimeInterpolatesDynamicText(t *testing.T) {
	preview := GUIPreviewResult{Language: GUIPreviewLanguageBilingual, Nodes: []GUIPreviewNode{
		{Index: 0, Kind: "text_single", Text: "[Character.GetName]"},
		{Index: 1, Kind: "text_single", Semantics: &GUISemantics{RawText: "#T;V;underline [Scheme.GetAgentCharges|0]#!#T;V /#! [MonthlyIncome|+=2]"}},
		{Index: 2, Kind: "text_single", Text: "DYNAMIC_RESOURCE", TextLocalization: &GUILocalizedText{
			SelectedLanguage: GUIPreviewLanguageBilingual,
			English:          &GUILocalizedValue{Value: "[magic|E]: [MagicValue|1]", Dynamic: true},
			SimpChinese:      &GUILocalizedValue{Value: "[magic|E]：[MagicValue|1]", Dynamic: true},
		}},
		{Index: 3, Kind: "button", Semantics: &GUISemantics{Tooltip: "[Character.GetTooltip]"}},
	}}
	inputs := []GUIRuntimeFactInput{
		{Expression: "Character.GetName", Value: "Arthen"},
		{Expression: "Scheme.GetAgentCharges", Value: 7},
		{Expression: "MonthlyIncome", Value: 2.5},
		{Expression: "MagicValue", Value: 87.26},
	}
	if err := prepareGUIPreviewRuntime(&preview, inputs); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[0].Runtime.Text, GUIPreviewLanguageRaw); !ok || got != "Arthen" {
		t.Fatalf("direct dynamic text = %q, %v", got, ok)
	}
	if got, _ := resolvedGUIRuntimeText(preview.Nodes[1].Runtime.Text, GUIPreviewLanguageRaw); got != "7 / +2.50" {
		t.Fatalf("formatted dynamic text = %q, want %q", got, "7 / +2.50")
	}
	if got, _ := resolvedGUIRuntimeText(preview.Nodes[2].Runtime.Text, GUIPreviewLanguageBilingual); got != "magic：87.3 / magic: 87.3" {
		t.Fatalf("localized dynamic text = %q", got)
	}
	tooltip := preview.Nodes[3].Runtime.Tooltip.Raw
	if tooltip == nil || tooltip.Status != "partial" || tooltip.Result != "<unknown>" || len(tooltip.MissingFacts) != 1 {
		t.Fatalf("missing tooltip fact was not explicit: %#v", tooltip)
	}
	if preview.Runtime.Stats.TextReady < 4 || preview.Runtime.Stats.TextPartial != 1 {
		t.Fatalf("unexpected text runtime stats: %#v", preview.Runtime.Stats)
	}
}

func TestPrepareGUIPreviewRuntimeEvaluatesConditionalTextBranchesLazily(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{
		{Index: 0, Kind: "text_single", Text: "[Select_CString( UseTitle, Title.GetName, Province.GetName )]"},
		{Index: 1, Kind: "text_single", Text: "State: [Select_CString( IsReady, 'ready', 'waiting' )][AddTextIf( ShowSuffix, ' now' )]"},
	}}
	inputs := []GUIRuntimeFactInput{
		{Expression: "UseTitle", Value: true},
		{Expression: "Title.GetName", Value: "The Apple March"},
		{Expression: "IsReady", Value: false},
		{Expression: "ShowSuffix", Value: true},
	}
	if err := prepareGUIPreviewRuntime(&preview, inputs); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[0].Runtime.Text, GUIPreviewLanguageRaw); !ok || got != "The Apple March" {
		t.Fatalf("selected dynamic branch = %q, %v", got, ok)
	}
	if binding := preview.Nodes[0].Runtime.Text.Raw; binding.Status != "evaluated" || len(binding.MissingFacts) != 0 {
		t.Fatalf("unselected missing branch affected the result: %#v", binding)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[1].Runtime.Text, GUIPreviewLanguageRaw); !ok || got != "State: waiting now" {
		t.Fatalf("static conditional text = %q, %v", got, ok)
	}
	var unselected *GUIRuntimeFact
	for index := range preview.Runtime.Facts {
		if preview.Runtime.Facts[index].Expression == "Province.GetName" {
			unselected = &preview.Runtime.Facts[index]
			break
		}
	}
	if unselected == nil || unselected.Provided || unselected.References == 0 {
		t.Fatalf("unselected branch was not preserved as an editable fact: %#v", unselected)
	}
}

func TestPrepareGUIPreviewRuntimeLocalizesConditionalBranches(t *testing.T) {
	englishLookup := map[string]string{
		"FILTER_BLOOD": "Blood rituals",
		"FILTER_ALL":   "All rituals",
		"WARNING":      "Requires an offering",
	}
	chineseLookup := map[string]string{
		"FILTER_BLOOD": "鲜血仪式",
		"FILTER_ALL":   "全部仪式",
		"WARNING":      "需要祭品",
	}
	preview := GUIPreviewResult{
		Language: GUIPreviewLanguageBilingual,
		runtimeLocalizationLookups: map[string]map[string]string{
			GUIPreviewLanguageEnglish:     englishLookup,
			GUIPreviewLanguageSimpChinese: chineseLookup,
		},
		Nodes: []GUIPreviewNode{
			{Index: 0, Kind: "text_single", Text: "[SelectLocalization( HasBloodFilter, 'FILTER_BLOOD', 'FILTER_ALL' )]"},
			{Index: 1, Kind: "text_single", Text: "WARNING_LABEL", TextLocalization: &GUILocalizedText{
				SelectedLanguage: GUIPreviewLanguageEnglish,
				English: &GUILocalizedValue{
					Language:      GUIPreviewLanguageEnglish,
					Value:         "[AddLocalizationIf( ShowWarning, 'WARNING' )]",
					Dynamic:       true,
					runtimeLookup: englishLookup,
				},
			}},
		},
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "HasBloodFilter", Value: true},
		{Expression: "ShowWarning", Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[0].Runtime.Text, GUIPreviewLanguageBilingual); !ok || got != "鲜血仪式 / Blood rituals" {
		t.Fatalf("bilingual conditional localization = %q, %v", got, ok)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[1].Runtime.Text, GUIPreviewLanguageEnglish); !ok || got != "Requires an offering" {
		t.Fatalf("AddLocalizationIf result = %q, %v", got, ok)
	}
}

func TestPrepareGUIPreviewRuntimeEvaluatesNestedConditionalText(t *testing.T) {
	lookup := map[string]string{"ONE": "I", "TWO": "II", "OTHER": "Other"}
	preview := GUIPreviewResult{
		Language: GUIPreviewLanguageEnglish,
		runtimeLocalizationLookups: map[string]map[string]string{
			GUIPreviewLanguageEnglish: lookup,
		},
		Nodes: []GUIPreviewNode{{
			Index: 0, Kind: "text_single",
			Text: "[SelectLocalization( IsOne, 'ONE', SelectLocalization( IsTwo, 'TWO', 'OTHER' ) )]",
		}},
	}
	if err := prepareGUIPreviewRuntime(&preview, []GUIRuntimeFactInput{
		{Expression: "IsOne", Value: false},
		{Expression: "IsTwo", Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolvedGUIRuntimeText(preview.Nodes[0].Runtime.Text, GUIPreviewLanguageEnglish); !ok || got != "II" {
		t.Fatalf("nested conditional localization = %q, %v", got, ok)
	}
}

func TestPrepareGUIPreviewRuntimeRejectsUnsafeConditionalTextShapes(t *testing.T) {
	nested := "'done'"
	for index := 0; index < guiRuntimeTextConditionalDepth+1; index++ {
		nested = fmt.Sprintf("Select_CString( Condition%d, 'branch', %s )", index, nested)
	}
	for _, expression := range []string{
		"[Select_CString( IsReady, 'ready' )]",
		"[AddTextIf( 42, 'not boolean' )]",
		"[" + nested + "]",
	} {
		preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{Index: 0, Kind: "text_single", Text: expression}}}
		if err := prepareGUIPreviewRuntime(&preview, nil); err != nil {
			t.Fatal(err)
		}
		if preview.Runtime == nil || preview.Runtime.Stats.Unsupported != 1 || len(preview.Runtime.Unsupported) != 1 {
			t.Fatalf("unsafe conditional text %q was not rejected: %#v", expression, preview.Runtime)
		}
		if binding := preview.Nodes[0].Runtime.Text.Raw; binding == nil || binding.Status != "unsupported" {
			t.Fatalf("unsafe conditional text binding was not explicit: %#v", binding)
		}
	}
}

func TestPrepareGUIPreviewRuntimeRejectsOversizedTextTemplate(t *testing.T) {
	preview := GUIPreviewResult{Nodes: []GUIPreviewNode{{Index: 0, Text: "[" + strings.Repeat("x", guiRuntimeTextMaxRunes) + "]"}}}
	if err := prepareGUIPreviewRuntime(&preview, nil); err != nil {
		t.Fatal(err)
	}
	if preview.Runtime.Stats.Unsupported != 1 || len(preview.Runtime.Unsupported) != 1 || preview.Nodes[0].Runtime.Text.Raw.Status != "unsupported" {
		t.Fatalf("oversized dynamic text must remain explicit: %#v", preview.Runtime)
	}
}

func BenchmarkPrepareGUIPreviewRuntime300Nodes(b *testing.B) {
	base := GUIPreviewResult{Nodes: make([]GUIPreviewNode, 300)}
	for index := range base.Nodes {
		base.Nodes[index] = GUIPreviewNode{Index: index, Semantics: &GUISemantics{
			Visible: "[And( Not( IsPauseMenuShown ), And( IsDefaultGUIMode, GetPlayer.IsValid ) )]",
			Enabled: "[Not( GreaterThan_CFixedPoint( GetPlayer.MakeScope.Var('hide_ui_top_bar').GetValue, '(CFixedPoint)0' ) )]",
		}}
	}
	facts := []GUIRuntimeFactInput{
		{Expression: "IsPauseMenuShown", Value: false},
		{Expression: "IsDefaultGUIMode", Value: true},
		{Expression: "GetPlayer.IsValid", Value: true},
		{Expression: "GetPlayer.MakeScope.Var('hide_ui_top_bar').GetValue", Value: 0},
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{Nodes: append([]GUIPreviewNode(nil), base.Nodes...)}
		if err := prepareGUIPreviewRuntime(&preview, facts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareGUIPreviewRuntime300NumericValueNodes(b *testing.B) {
	base := GUIPreviewResult{Nodes: make([]GUIPreviewNode, 300)}
	for index := range base.Nodes {
		base.Nodes[index] = GUIPreviewNode{Index: index, Kind: "progresspie", Semantics: &GUISemantics{
			Visible: "[And(UsesTimerLocking, Not(PdxGuiWidget.IsTooltipLocked))]",
			Min:     "0",
			Max:     "1",
			Value:   "[PdxGuiWidget.GetTooltipLockProgress]",
		}}
	}
	facts := []GUIRuntimeFactInput{
		{Expression: "UsesTimerLocking", Value: true},
		{Expression: "PdxGuiWidget.IsTooltipLocked", Value: false},
		{Expression: "PdxGuiWidget.GetTooltipLockProgress", Value: 0.62},
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{Nodes: append([]GUIPreviewNode(nil), base.Nodes...)}
		if err := prepareGUIPreviewRuntime(&preview, facts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareGUIPreviewRuntime300DynamicTextNodes(b *testing.B) {
	base := GUIPreviewResult{Nodes: make([]GUIPreviewNode, 300)}
	for index := range base.Nodes {
		base.Nodes[index] = GUIPreviewNode{Index: index, Semantics: &GUISemantics{
			Visible: "[And( Not( IsPauseMenuShown ), GetPlayer.IsValid )]",
			RawText: "#T;V [Character.GetName]#! [MagicValue|1] / [MonthlyIncome|+=2]",
		}}
	}
	facts := []GUIRuntimeFactInput{
		{Expression: "IsPauseMenuShown", Value: false},
		{Expression: "GetPlayer.IsValid", Value: true},
		{Expression: "Character.GetName", Value: "Arthen"},
		{Expression: "MagicValue", Value: 125.4},
		{Expression: "MonthlyIncome", Value: 2.75},
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{Nodes: append([]GUIPreviewNode(nil), base.Nodes...)}
		if err := prepareGUIPreviewRuntime(&preview, facts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareGUIPreviewRuntime300PressedActionNodes(b *testing.B) {
	base := GUIPreviewResult{Nodes: make([]GUIPreviewNode, 300)}
	for index := range base.Nodes {
		base.Nodes[index] = GUIPreviewNode{Index: index, Semantics: &GUISemantics{
			Down:     "[IsGameViewOpen('military')]",
			Selected: "[Character.IsTraveling]",
			OnClick:  "[Character.ZoomCameraTo]",
			OnClicks: []string{
				"[ToggleGameViewData('military', GetPlayer.GetID)]",
				"[Character.ZoomCameraTo]",
			},
		}}
	}
	facts := []GUIRuntimeFactInput{
		{Expression: "IsGameViewOpen('military')", Value: false},
		{Expression: "Character.IsTraveling", Value: true},
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{Nodes: append([]GUIPreviewNode(nil), base.Nodes...)}
		if err := prepareGUIPreviewRuntime(&preview, facts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareGUIPreviewRuntime300ProvidedActionEffectNodes(b *testing.B) {
	action := "[GetScriptedGui('biodiversity_map').Execute(GuiScope.End)]"
	shown := "GetScriptedGui('biodiversity_map').IsShown(GuiScope.End)"
	base := GUIPreviewResult{Nodes: make([]GUIPreviewNode, 300)}
	for index := range base.Nodes {
		base.Nodes[index] = GUIPreviewNode{Index: index, Semantics: &GUISemantics{
			Down:    "[And(GetScriptedGui('biodiversity_map').IsShown(GuiScope.End), IsMapMode('biodiversity_mapmode'))]",
			OnClick: action,
		}}
	}
	facts := []GUIRuntimeFactInput{
		{Expression: shown, Value: false},
		{Expression: "IsMapMode('biodiversity_mapmode')", Value: true},
	}
	effects := []GUIRuntimeActionEffectInput{{
		Expression: action,
		Updates:    []GUIRuntimeActionUpdateInput{{Expression: shown, Operation: "set", Value: true}},
	}}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		preview := GUIPreviewResult{Nodes: append([]GUIPreviewNode(nil), base.Nodes...)}
		if err := prepareGUIPreviewRuntimeWithActions(&preview, facts, effects); err != nil {
			b.Fatal(err)
		}
	}
}
