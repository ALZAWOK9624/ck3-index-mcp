package indexer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	GUIRuntimeSchemaVersion      = "ck3-gui-runtime/v1"
	GUIRuntimeMaxFacts           = 64
	GUIRuntimeMaxActionEffects   = 32
	GUIRuntimeMaxActionUpdates   = 8
	guiRuntimeMaxExpressionDepth = 16
	guiRuntimeMaxPlans           = 1024
	guiRuntimeKindBoolean        = "boolean"
	guiRuntimeKindNumber         = "number"
	guiRuntimeKindString         = "string"
	guiRuntimeKindColor          = "color"
	guiRuntimeKindUnknown        = "unknown"
	guiRuntimeKindMixed          = "mixed"
	guiRuntimeActionUpdateSet    = "set"
	guiRuntimeActionUpdateToggle = "toggle"
	guiRuntimeActionUpdateUnset  = "unset"
)

// GUIRuntimeFactInput is a caller-provided atomic value used by the bounded
// GUI expression evaluator. Value is restricted at validation time to JSON
// boolean, number, or string. It is never described as observed game state.
type GUIRuntimeFactInput struct {
	Expression string `json:"expression"`
	Value      any    `json:"value"`
}

// GUIRuntimeActionEffectInput is a caller-provided, declarative postcondition
// for one exact onclick expression. It never executes the expression itself.
type GUIRuntimeActionEffectInput struct {
	Expression string                        `json:"expression"`
	Updates    []GUIRuntimeActionUpdateInput `json:"updates"`
}

type GUIRuntimeActionUpdateInput struct {
	Expression string `json:"expression"`
	Operation  string `json:"operation"`
	Value      any    `json:"value,omitempty"`
}

type GUIPreviewRuntime struct {
	SchemaVersion string                  `json:"schema_version"`
	Source        string                  `json:"source"`
	Facts         []GUIRuntimeFact        `json:"facts,omitempty"`
	Plans         []GUIRuntimePlan        `json:"plans,omitempty"`
	TextPlans     []GUIRuntimeTextPlan    `json:"text_plans,omitempty"`
	Actions       []GUIRuntimeActionPlan  `json:"actions,omitempty"`
	Stats         GUIRuntimeStats         `json:"stats"`
	Unsupported   []GUIRuntimeUnsupported `json:"unsupported,omitempty"`
}

type GUIRuntimeStats struct {
	Expressions   int `json:"expressions"`
	Evaluated     int `json:"evaluated"`
	Unknown       int `json:"unknown"`
	Unsupported   int `json:"unsupported"`
	Provided      int `json:"provided_facts"`
	Unused        int `json:"unused_facts"`
	Actions       int `json:"actions"`
	ActionEffects int `json:"provided_action_effects"`
	UnusedEffects int `json:"unused_action_effects"`
	TextPlans     int `json:"text_plans"`
	TextReady     int `json:"text_ready"`
	TextPartial   int `json:"text_partial"`
}

type GUIRuntimeFact struct {
	Index      int    `json:"index"`
	Expression string `json:"expression"`
	Kind       string `json:"kind"`
	Provided   bool   `json:"provided"`
	Value      any    `json:"value,omitempty"`
	References int    `json:"references"`
	value      guiRuntimeValue
}

type GUIRuntimePlan struct {
	ID           int               `json:"id"`
	Expression   string            `json:"expression"`
	Kind         string            `json:"kind"`
	Supported    bool              `json:"supported"`
	Tokens       []GUIRuntimeToken `json:"tokens,omitempty"`
	Result       *bool             `json:"result,omitempty"`
	Number       *float64          `json:"number,omitempty"`
	Color        *string           `json:"color,omitempty"`
	MissingFacts []int             `json:"missing_facts,omitempty"`
	Error        string            `json:"error,omitempty"`
	status       string
}

type GUIRuntimeToken struct {
	Op        string            `json:"o"`
	Fact      *int              `json:"f,omitempty"`
	Bool      *bool             `json:"b,omitempty"`
	Number    *float64          `json:"n,omitempty"`
	Text      *string           `json:"s,omitempty"`
	Arity     int               `json:"a,omitempty"`
	Condition []GUIRuntimeToken `json:"c,omitempty"`
	WhenTrue  []GUIRuntimeToken `json:"t,omitempty"`
	WhenFalse []GUIRuntimeToken `json:"e,omitempty"`
}

type GUIRuntimeUnsupported struct {
	Expression string `json:"expression"`
	Reason     string `json:"reason"`
}

type GUINodeRuntime struct {
	Visible       *GUIRuntimeBinding        `json:"visible,omitempty"`
	Enabled       *GUIRuntimeBinding        `json:"enabled,omitempty"`
	Down          *GUIRuntimeBinding        `json:"down,omitempty"`
	Selected      *GUIRuntimeBinding        `json:"selected,omitempty"`
	Alpha         *GUIRuntimeNumberBinding  `json:"alpha,omitempty"`
	Min           *GUIRuntimeNumberBinding  `json:"min,omitempty"`
	Max           *GUIRuntimeNumberBinding  `json:"max,omitempty"`
	Value         *GUIRuntimeNumberBinding  `json:"value,omitempty"`
	TintColor     *GUIRuntimeColorBinding   `json:"tint_color,omitempty"`
	FontTintColor *GUIRuntimeColorBinding   `json:"font_tint_color,omitempty"`
	Text          *GUIRuntimeTextBindingSet `json:"text,omitempty"`
	Tooltip       *GUIRuntimeTextBindingSet `json:"tooltip,omitempty"`
	Action        *GUIRuntimeActionBinding  `json:"action,omitempty"`
	Actions       []GUIRuntimeActionBinding `json:"actions,omitempty"`
}

type GUIRuntimeBinding struct {
	PlanID int    `json:"plan_id"`
	Status string `json:"status"`
	Result *bool  `json:"result,omitempty"`
}

type GUIRuntimeNumberBinding struct {
	PlanID int      `json:"plan_id"`
	Status string   `json:"status"`
	Result *float64 `json:"result,omitempty"`
}

type GUIRuntimeColorBinding struct {
	PlanID int     `json:"plan_id"`
	Status string  `json:"status"`
	Result *string `json:"result,omitempty"`
}

type GUIRuntimeActionPlan struct {
	ID             int                      `json:"id"`
	Expression     string                   `json:"expression"`
	Operation      string                   `json:"operation"`
	Fact           int                      `json:"fact"`
	Argument       string                   `json:"argument"`
	DataExpression string                   `json:"data_expression,omitempty"`
	Source         string                   `json:"source,omitempty"`
	Updates        []GUIRuntimeActionUpdate `json:"updates,omitempty"`
}

type GUIRuntimeActionUpdate struct {
	Fact       int    `json:"fact"`
	Expression string `json:"expression"`
	Operation  string `json:"operation"`
	Value      any    `json:"value,omitempty"`
}

type GUIRuntimeActionBinding struct {
	PlanID int    `json:"plan_id"`
	Status string `json:"status"`
}

type guiRuntimeCompiler struct {
	facts         []GUIRuntimeFact
	factIndex     map[string]int
	plans         []GUIRuntimePlan
	planIndex     map[string]int
	actions       []GUIRuntimeActionPlan
	actionIndex   map[string]int
	textPlans     []GUIRuntimeTextPlan
	textPlanIndex map[string]int
	unsupported   map[string]string

	runtimeLocalizationLookups map[string]map[string]string
}

type preparedGUIRuntimeActionEffect struct {
	expression string
	updates    []preparedGUIRuntimeActionUpdate
	used       bool
}

type preparedGUIRuntimeActionUpdate struct {
	expression  string
	operation   string
	value       guiRuntimeValue
	publicValue any
}

type guiExprNode struct {
	op    string
	fact  int
	value guiRuntimeValue
	args  []*guiExprNode
}

type guiRuntimeValue struct {
	kind   string
	known  bool
	boolV  bool
	number float64
	text   string
}

// prepareGUIPreviewRuntime compiles visible/enabled/down/selected expressions into a small
// RPN plan catalog and evaluates them with caller-provided atomic facts. The
// same plans are embedded into inspector HTML for safe client-side replay.
func prepareGUIPreviewRuntime(preview *GUIPreviewResult, inputs []GUIRuntimeFactInput) error {
	return prepareGUIPreviewRuntimeWithActions(preview, inputs, nil)
}

func prepareGUIPreviewRuntimeWithActions(preview *GUIPreviewResult, inputs []GUIRuntimeFactInput, actionEffects []GUIRuntimeActionEffectInput) error {
	if preview == nil {
		return nil
	}
	if len(inputs) > GUIRuntimeMaxFacts {
		return fmt.Errorf("GUI runtime has %d facts; maximum is %d", len(inputs), GUIRuntimeMaxFacts)
	}
	compiler := &guiRuntimeCompiler{
		factIndex: map[string]int{}, planIndex: map[string]int{}, actionIndex: map[string]int{}, textPlanIndex: map[string]int{}, unsupported: map[string]string{},
		runtimeLocalizationLookups: preview.runtimeLocalizationLookups,
	}
	for index, input := range inputs {
		expression := normalizeGUIRuntimeExpression(input.Expression)
		if expression == "" {
			return fmt.Errorf("GUI runtime fact %d requires an expression", index)
		}
		if len([]rune(expression)) > guiScenarioMaxExpression {
			return fmt.Errorf("GUI runtime fact %d expression exceeds %d characters", index, guiScenarioMaxExpression)
		}
		value, publicValue, err := normalizeGUIRuntimeFactValue(input.Value)
		if err != nil {
			return fmt.Errorf("GUI runtime fact %d: %w", index, err)
		}
		factKey := guiRuntimeFactKey(expression)
		if previous, exists := compiler.factIndex[factKey]; exists {
			if !equalGUIRuntimeValue(compiler.facts[previous].value, value) {
				return fmt.Errorf("GUI runtime has conflicting facts for expression %q", expression)
			}
			return fmt.Errorf("GUI runtime repeats fact expression %q", expression)
		}
		compiler.factIndex[factKey] = len(compiler.facts)
		compiler.facts = append(compiler.facts, GUIRuntimeFact{
			Index: len(compiler.facts), Expression: expression, Kind: value.kind,
			Provided: true, Value: publicValue, value: value,
		})
	}
	preparedEffects, err := prepareGUIRuntimeActionEffects(actionEffects)
	if err != nil {
		return err
	}

	for index := range preview.Nodes {
		node := &preview.Nodes[index]
		compiler.bindNodeRuntimeText(node)
		if node.Semantics == nil {
			continue
		}
		if expression := strings.TrimSpace(node.Semantics.Visible); expression != "" {
			planID := compiler.plan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Visible = &GUIRuntimeBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Enabled); expression != "" {
			planID := compiler.plan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Enabled = &GUIRuntimeBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Down); expression != "" {
			planID := compiler.plan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Down = &GUIRuntimeBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Selected); expression != "" {
			planID := compiler.plan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Selected = &GUIRuntimeBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Alpha); expression != "" {
			planID := compiler.numberPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Alpha = &GUIRuntimeNumberBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Min); expression != "" {
			planID := compiler.numberPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Min = &GUIRuntimeNumberBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Max); expression != "" {
			planID := compiler.numberPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Max = &GUIRuntimeNumberBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.Value); expression != "" {
			planID := compiler.numberPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.Value = &GUIRuntimeNumberBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.TintColor); expression != "" {
			planID := compiler.colorPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.TintColor = &GUIRuntimeColorBinding{PlanID: planID}
		}
		if expression := strings.TrimSpace(node.Semantics.FontTintColor); expression != "" {
			planID := compiler.colorPlan(expression)
			compiler.referencePlan(planID)
			if node.Runtime == nil {
				node.Runtime = &GUINodeRuntime{}
			}
			node.Runtime.FontTintColor = &GUIRuntimeColorBinding{PlanID: planID}
		}
		clickExpressions := node.Semantics.OnClicks
		if len(clickExpressions) == 0 && strings.TrimSpace(node.Semantics.OnClick) != "" {
			clickExpressions = []string{node.Semantics.OnClick}
		}
		for _, expression := range clickExpressions {
			expression = strings.TrimSpace(expression)
			if expression == "" {
				continue
			}
			if actionID, ok := compiler.action(expression); ok {
				compiler.referenceAction(actionID)
				if node.Runtime == nil {
					node.Runtime = &GUINodeRuntime{}
				}
				binding := GUIRuntimeActionBinding{PlanID: actionID, Status: "compiled"}
				node.Runtime.Actions = append(node.Runtime.Actions, binding)
				if node.Runtime.Action == nil {
					legacy := binding
					node.Runtime.Action = &legacy
				}
				continue
			}
			if effect := preparedEffects[guiRuntimeFactKey(expression)]; effect != nil {
				actionID, err := compiler.providedAction(expression, effect)
				if err != nil {
					return err
				}
				compiler.referenceAction(actionID)
				if node.Runtime == nil {
					node.Runtime = &GUINodeRuntime{}
				}
				binding := GUIRuntimeActionBinding{PlanID: actionID, Status: "compiled"}
				node.Runtime.Actions = append(node.Runtime.Actions, binding)
				if node.Runtime.Action == nil {
					legacy := binding
					node.Runtime.Action = &legacy
				}
			}
		}
	}

	runtime := &GUIPreviewRuntime{SchemaVersion: GUIRuntimeSchemaVersion, Source: "interactive"}
	if len(inputs) > 0 || len(actionEffects) > 0 {
		runtime.Source = "provided"
	}
	for index := range compiler.plans {
		plan := &compiler.plans[index]
		if !plan.Supported {
			plan.status = "unsupported"
			runtime.Stats.Unsupported++
			continue
		}
		value, missing := evaluateGUIRuntimeTokens(plan.Tokens, compiler.facts)
		plan.MissingFacts = missing
		if value.known && value.kind == plan.Kind {
			switch value.kind {
			case guiRuntimeKindBoolean:
				result := value.boolV
				plan.Result = &result
			case guiRuntimeKindNumber:
				result := value.number
				plan.Number = &result
			}
			plan.status = "evaluated"
			runtime.Stats.Evaluated++
		} else if plan.Kind == guiRuntimeKindColor && value.known && value.kind == guiRuntimeKindString {
			if color, ok := normalizeGUIRuntimeColor(value.text); ok {
				plan.Color = &color
				plan.status = "evaluated"
				runtime.Stats.Evaluated++
			} else {
				plan.status = "unknown"
				runtime.Stats.Unknown++
			}
		} else {
			plan.status = "unknown"
			runtime.Stats.Unknown++
		}
	}
	for index := range compiler.textPlans {
		plan := &compiler.textPlans[index]
		if !plan.Supported {
			runtime.Stats.Unsupported++
			continue
		}
		result, missing, unresolved := evaluateGUIRuntimeTextTokens(plan.Tokens, compiler.facts)
		plan.Result = result
		plan.MissingFacts = missing
		if len(missing) == 0 && !unresolved {
			plan.Status = "evaluated"
			runtime.Stats.TextReady++
		} else {
			plan.Status = "partial"
			runtime.Stats.TextPartial++
		}
	}
	for index := range preview.Nodes {
		node := &preview.Nodes[index]
		if node.Runtime == nil {
			continue
		}
		bindGUIRuntimeResult(node.Runtime.Visible, compiler.plans)
		bindGUIRuntimeResult(node.Runtime.Enabled, compiler.plans)
		bindGUIRuntimeResult(node.Runtime.Down, compiler.plans)
		bindGUIRuntimeResult(node.Runtime.Selected, compiler.plans)
		bindGUIRuntimeNumberResult(node.Runtime.Alpha, compiler.plans)
		bindGUIRuntimeNumberResult(node.Runtime.Min, compiler.plans)
		bindGUIRuntimeNumberResult(node.Runtime.Max, compiler.plans)
		bindGUIRuntimeNumberResult(node.Runtime.Value, compiler.plans)
		bindGUIRuntimeColorResult(node.Runtime.TintColor, compiler.plans)
		bindGUIRuntimeColorResult(node.Runtime.FontTintColor, compiler.plans)
		bindGUIRuntimeTextSet(node.Runtime.Text, compiler.textPlans)
		bindGUIRuntimeTextSet(node.Runtime.Tooltip, compiler.textPlans)
	}
	for _, fact := range compiler.facts {
		if fact.Provided {
			runtime.Stats.Provided++
			if fact.References == 0 {
				runtime.Stats.Unused++
			}
		}
	}
	runtime.Stats.Expressions = len(compiler.plans)
	runtime.Stats.Actions = len(compiler.actions)
	runtime.Stats.ActionEffects = len(actionEffects)
	for _, effect := range preparedEffects {
		if !effect.used {
			runtime.Stats.UnusedEffects++
		}
	}
	runtime.Stats.TextPlans = len(compiler.textPlans)
	runtime.Facts = compiler.facts
	runtime.Plans = compiler.plans
	runtime.TextPlans = compiler.textPlans
	runtime.Actions = compiler.actions
	keys := make([]string, 0, len(compiler.unsupported))
	for expression := range compiler.unsupported {
		keys = append(keys, expression)
	}
	sort.Strings(keys)
	for _, expression := range keys {
		runtime.Unsupported = append(runtime.Unsupported, GUIRuntimeUnsupported{Expression: expression, Reason: compiler.unsupported[expression]})
	}
	preview.Runtime = runtime
	if runtime.Stats.Unused > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d provided GUI runtime fact(s) were not referenced by bounded GUI expressions or actions", runtime.Stats.Unused))
	}
	if runtime.Stats.Unsupported > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d GUI runtime expression(s) use syntax outside the bounded evaluator", runtime.Stats.Unsupported))
	}
	if runtime.Stats.UnusedEffects > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d provided GUI action effect(s) did not exactly match an unsupported preview onclick expression", runtime.Stats.UnusedEffects))
	}
	return nil
}

func (compiler *guiRuntimeCompiler) referenceAction(actionID int) {
	if actionID < 0 || actionID >= len(compiler.actions) {
		return
	}
	action := compiler.actions[actionID]
	if len(action.Updates) > 0 {
		for _, update := range action.Updates {
			if update.Fact >= 0 && update.Fact < len(compiler.facts) {
				compiler.facts[update.Fact].References++
			}
		}
		return
	}
	if action.Fact >= 0 && action.Fact < len(compiler.facts) {
		compiler.facts[action.Fact].References++
	}
}

func prepareGUIRuntimeActionEffects(inputs []GUIRuntimeActionEffectInput) (map[string]*preparedGUIRuntimeActionEffect, error) {
	if len(inputs) > GUIRuntimeMaxActionEffects {
		return nil, fmt.Errorf("GUI runtime has %d action effects; maximum is %d", len(inputs), GUIRuntimeMaxActionEffects)
	}
	result := make(map[string]*preparedGUIRuntimeActionEffect, len(inputs))
	for effectIndex, input := range inputs {
		expression := normalizeGUIRuntimeExpression(input.Expression)
		if expression == "" {
			return nil, fmt.Errorf("GUI runtime action effect %d requires an expression", effectIndex)
		}
		if len([]rune(expression)) > guiScenarioMaxExpression {
			return nil, fmt.Errorf("GUI runtime action effect %d expression exceeds %d characters", effectIndex, guiScenarioMaxExpression)
		}
		if len(input.Updates) == 0 {
			return nil, fmt.Errorf("GUI runtime action effect %d requires at least one update", effectIndex)
		}
		if len(input.Updates) > GUIRuntimeMaxActionUpdates {
			return nil, fmt.Errorf("GUI runtime action effect %d has %d updates; maximum is %d", effectIndex, len(input.Updates), GUIRuntimeMaxActionUpdates)
		}
		key := guiRuntimeFactKey(expression)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("GUI runtime repeats action effect expression %q", expression)
		}
		effect := &preparedGUIRuntimeActionEffect{expression: strings.TrimSpace(input.Expression)}
		seenUpdates := map[string]struct{}{}
		for updateIndex, inputUpdate := range input.Updates {
			updateExpression := normalizeGUIRuntimeExpression(inputUpdate.Expression)
			if updateExpression == "" {
				return nil, fmt.Errorf("GUI runtime action effect %d update %d requires an expression", effectIndex, updateIndex)
			}
			if len([]rune(updateExpression)) > guiScenarioMaxExpression {
				return nil, fmt.Errorf("GUI runtime action effect %d update %d expression exceeds %d characters", effectIndex, updateIndex, guiScenarioMaxExpression)
			}
			updateKey := guiRuntimeFactKey(updateExpression)
			if _, exists := seenUpdates[updateKey]; exists {
				return nil, fmt.Errorf("GUI runtime action effect %d repeats update expression %q", effectIndex, updateExpression)
			}
			seenUpdates[updateKey] = struct{}{}
			operation := strings.ToLower(strings.TrimSpace(inputUpdate.Operation))
			prepared := preparedGUIRuntimeActionUpdate{expression: updateExpression, operation: operation}
			switch operation {
			case guiRuntimeActionUpdateSet:
				value, publicValue, err := normalizeGUIRuntimeFactValue(inputUpdate.Value)
				if err != nil {
					return nil, fmt.Errorf("GUI runtime action effect %d update %d: %w", effectIndex, updateIndex, err)
				}
				prepared.value = value
				prepared.publicValue = publicValue
			case guiRuntimeActionUpdateToggle:
				if inputUpdate.Value != nil {
					return nil, fmt.Errorf("GUI runtime action effect %d update %d toggle must not provide a value", effectIndex, updateIndex)
				}
				prepared.value = guiRuntimeValue{kind: guiRuntimeKindBoolean}
			default:
				return nil, fmt.Errorf("GUI runtime action effect %d update %d operation %q is invalid; expected set or toggle", effectIndex, updateIndex, inputUpdate.Operation)
			}
			effect.updates = append(effect.updates, prepared)
		}
		result[key] = effect
	}
	return result, nil
}

func (compiler *guiRuntimeCompiler) providedAction(expression string, effect *preparedGUIRuntimeActionEffect) (int, error) {
	normalized := normalizeGUIRuntimeExpression(expression)
	actionKey := "provided:" + guiRuntimeFactKey(normalized)
	if index, exists := compiler.actionIndex[actionKey]; exists {
		effect.used = true
		return index, nil
	}
	plan := GUIRuntimeActionPlan{
		ID: len(compiler.actions), Expression: strings.TrimSpace(expression), Operation: "provided_effect",
		Fact: -1, Source: "provided",
	}
	for updateIndex, prepared := range effect.updates {
		factNode := compiler.fact(prepared.expression, prepared.value.kind)
		fact := compiler.facts[factNode.fact]
		if fact.Kind == guiRuntimeKindMixed {
			return 0, fmt.Errorf("GUI runtime action effect %q update %d conflicts with fact kind for %q", effect.expression, updateIndex, prepared.expression)
		}
		plan.Updates = append(plan.Updates, GUIRuntimeActionUpdate{
			Fact: factNode.fact, Expression: prepared.expression, Operation: prepared.operation, Value: prepared.publicValue,
		})
	}
	compiler.actionIndex[actionKey] = plan.ID
	compiler.actions = append(compiler.actions, plan)
	effect.used = true
	return plan.ID, nil
}

func bindGUIRuntimeResult(binding *GUIRuntimeBinding, plans []GUIRuntimePlan) {
	if binding == nil || binding.PlanID < 0 || binding.PlanID >= len(plans) {
		return
	}
	plan := plans[binding.PlanID]
	binding.Status = plan.status
	if plan.Result != nil {
		result := *plan.Result
		binding.Result = &result
	}
}

func bindGUIRuntimeNumberResult(binding *GUIRuntimeNumberBinding, plans []GUIRuntimePlan) {
	if binding == nil || binding.PlanID < 0 || binding.PlanID >= len(plans) {
		return
	}
	plan := plans[binding.PlanID]
	binding.Status = plan.status
	if plan.Number != nil {
		result := *plan.Number
		binding.Result = &result
	}
}

func bindGUIRuntimeColorResult(binding *GUIRuntimeColorBinding, plans []GUIRuntimePlan) {
	if binding == nil || binding.PlanID < 0 || binding.PlanID >= len(plans) {
		return
	}
	plan := plans[binding.PlanID]
	binding.Status = plan.status
	if plan.Color != nil {
		result := *plan.Color
		binding.Result = &result
	}
}

func (compiler *guiRuntimeCompiler) plan(expression string) int {
	return compiler.planKind(expression, guiRuntimeKindBoolean)
}

func (compiler *guiRuntimeCompiler) numberPlan(expression string) int {
	return compiler.planKind(expression, guiRuntimeKindNumber)
}

func (compiler *guiRuntimeCompiler) colorPlan(expression string) int {
	return compiler.planKind(expression, guiRuntimeKindColor)
}

func (compiler *guiRuntimeCompiler) planKind(expression, expectedKind string) int {
	normalized := normalizeGUIRuntimeExpression(expression)
	key := expectedKind + "\x00" + normalized
	if index, exists := compiler.planIndex[key]; exists {
		return index
	}
	plan := GUIRuntimePlan{ID: len(compiler.plans), Expression: strings.TrimSpace(expression), Kind: expectedKind, Supported: true}
	if len(compiler.plans) >= guiRuntimeMaxPlans {
		plan.Supported = false
		plan.Error = fmt.Sprintf("runtime plan limit %d exceeded", guiRuntimeMaxPlans)
	} else {
		compileKind := expectedKind
		var node *guiExprNode
		var err error
		if expectedKind == guiRuntimeKindColor {
			compileKind = guiRuntimeKindString
			if color, ok := normalizeGUIRuntimeColor(normalized); ok {
				node = &guiExprNode{op: "literal", value: guiRuntimeValue{kind: guiRuntimeKindString, known: true, text: color}}
			} else {
				node, err = compiler.compile(normalized, compileKind)
			}
		} else {
			node, err = compiler.compile(normalized, compileKind)
		}
		if err != nil {
			plan.Supported = false
			plan.Error = err.Error()
			compiler.unsupported[plan.Expression] = plan.Error
		} else {
			appendGUIRuntimeTokens(node, &plan.Tokens)
		}
	}
	compiler.planIndex[key] = plan.ID
	compiler.plans = append(compiler.plans, plan)
	return plan.ID
}

func (compiler *guiRuntimeCompiler) referencePlan(planID int) {
	if planID < 0 || planID >= len(compiler.plans) {
		return
	}
	seen := map[int]bool{}
	referenceGUIRuntimeTokens(compiler.plans[planID].Tokens, seen)
	for factIndex := range seen {
		if factIndex >= 0 && factIndex < len(compiler.facts) {
			compiler.facts[factIndex].References++
		}
	}
}

func (compiler *guiRuntimeCompiler) compile(expression, expectedKind string) (*guiExprNode, error) {
	return compiler.compileDepth(expression, expectedKind, 0)
}

func (compiler *guiRuntimeCompiler) compileDepth(expression, expectedKind string, depth int) (*guiExprNode, error) {
	if depth > guiRuntimeMaxExpressionDepth {
		return nil, fmt.Errorf("runtime expression nesting exceeds %d", guiRuntimeMaxExpressionDepth)
	}
	expression = normalizeGUIRuntimeExpression(expression)
	if expression == "" {
		return nil, fmt.Errorf("empty expression")
	}
	if literal, ok := parseGUIRuntimeLiteral(expression); ok {
		return &guiExprNode{op: "literal", value: literal}, nil
	}
	name, args, isCall, err := splitGUIRuntimeCall(expression)
	if err != nil {
		return nil, err
	}
	if isCall {
		if strings.EqualFold(strings.TrimSpace(name), "Select_float") {
			if expectedKind != guiRuntimeKindNumber {
				return nil, fmt.Errorf("%s is only supported for numeric properties", name)
			}
			if len(args) != 3 {
				return nil, fmt.Errorf("%s requires three arguments", name)
			}
			condition, err := compiler.compileDepth(args[0], guiRuntimeKindBoolean, depth+1)
			if err != nil {
				return nil, err
			}
			whenTrue, err := compiler.compileDepth(args[1], guiRuntimeKindNumber, depth+1)
			if err != nil {
				return nil, err
			}
			whenFalse, err := compiler.compileDepth(args[2], guiRuntimeKindNumber, depth+1)
			if err != nil {
				return nil, err
			}
			return &guiExprNode{op: "select", args: []*guiExprNode{condition, whenTrue, whenFalse}}, nil
		}
		if strings.EqualFold(strings.TrimSpace(name), "GetVariableSystem.HasValue") {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s requires two arguments", name)
			}
			variable, ok := parseGUIRuntimeVariableName(args[0])
			if !ok {
				return nil, fmt.Errorf("%s requires a static non-empty variable name", name)
			}
			expected, ok := parseGUIRuntimeLiteral(args[1])
			if !ok {
				return nil, fmt.Errorf("%s requires a literal comparison value", name)
			}
			if expected.kind == guiRuntimeKindString && len([]rune(expected.text)) > guiScenarioMaxValue {
				return nil, fmt.Errorf("%s comparison value exceeds %d characters", name, guiScenarioMaxValue)
			}
			exists := compiler.fact(guiRuntimeVariableExistsExpression(variable), guiRuntimeKindBoolean)
			value := compiler.fact(guiRuntimeVariableValueExpression(variable), expected.kind)
			return &guiExprNode{
				op: "and",
				args: []*guiExprNode{
					exists,
					{op: "eq", args: []*guiExprNode{
						value,
						{op: "literal", value: expected},
					}},
				},
			}, nil
		}
		operation := guiRuntimeOperation(name)
		switch operation {
		case "and", "or":
			if len(args) < 2 {
				return nil, fmt.Errorf("%s requires at least two arguments", name)
			}
			node := &guiExprNode{op: operation}
			for _, argument := range args {
				child, err := compiler.compileDepth(argument, guiRuntimeKindBoolean, depth+1)
				if err != nil {
					return nil, err
				}
				node.args = append(node.args, child)
			}
			return node, nil
		case "not":
			if len(args) != 1 {
				return nil, fmt.Errorf("%s requires one argument", name)
			}
			child, err := compiler.compileDepth(args[0], guiRuntimeKindBoolean, depth+1)
			if err != nil {
				return nil, err
			}
			return &guiExprNode{op: operation, args: []*guiExprNode{child}}, nil
		case "eq", "ne", "lt", "le", "gt", "ge":
			if len(args) != 2 {
				return nil, fmt.Errorf("%s requires two arguments", name)
			}
			leftKind, rightKind := guiRuntimeKindUnknown, guiRuntimeKindUnknown
			if literal, ok := parseGUIRuntimeLiteral(args[0]); ok {
				leftKind = literal.kind
			}
			if literal, ok := parseGUIRuntimeLiteral(args[1]); ok {
				rightKind = literal.kind
			}
			if leftKind == guiRuntimeKindUnknown {
				leftKind = rightKind
			}
			if rightKind == guiRuntimeKindUnknown {
				rightKind = leftKind
			}
			left, err := compiler.compileDepth(args[0], leftKind, depth+1)
			if err != nil {
				return nil, err
			}
			right, err := compiler.compileDepth(args[1], rightKind, depth+1)
			if err != nil {
				return nil, err
			}
			return &guiExprNode{op: operation, args: []*guiExprNode{left, right}}, nil
		}
	}
	return compiler.fact(expression, expectedKind), nil
}

func (compiler *guiRuntimeCompiler) fact(expression, expectedKind string) *guiExprNode {
	expression = normalizeGUIRuntimeExpression(expression)
	key := guiRuntimeFactKey(expression)
	index, exists := compiler.factIndex[key]
	if !exists {
		index = len(compiler.facts)
		compiler.factIndex[key] = index
		compiler.facts = append(compiler.facts, GUIRuntimeFact{
			Index: index, Expression: expression, Kind: normalizeGUIRuntimeKind(expectedKind),
		})
	} else {
		fact := &compiler.facts[index]
		fact.Kind = mergeGUIRuntimeKinds(fact.Kind, expectedKind)
	}
	return &guiExprNode{op: "fact", fact: index}
}

func (compiler *guiRuntimeCompiler) action(expression string) (int, bool) {
	normalized := normalizeGUIRuntimeExpression(expression)
	if index, exists := compiler.actionIndex[normalized]; exists {
		return index, true
	}
	name, args, isCall, err := splitGUIRuntimeCall(normalized)
	if err != nil || !isCall {
		return 0, false
	}
	operation := ""
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "opengameview":
		operation = "open_game_view"
	case "closegameview":
		operation = "close_game_view"
	case "togglegameview":
		operation = "toggle_game_view"
	case "togglegameviewdata":
		operation = "toggle_game_view_data"
	case "setmapmode":
		operation = "set_map_mode"
	case "getvariablesystem.toggle":
		operation = "toggle_variable"
	case "getvariablesystem.clear":
		operation = "clear_variable"
	case "getvariablesystem.set":
		operation = "set_variable"
	default:
		return 0, false
	}
	expectedArgs := 1
	if operation == "toggle_game_view_data" || operation == "set_variable" {
		expectedArgs = 2
	}
	if len(args) != expectedArgs {
		return 0, false
	}
	variableOrArgument, ok := parseGUIRuntimeVariableName(args[0])
	if !ok {
		return 0, false
	}
	argument := guiRuntimeValue{kind: guiRuntimeKindString, known: true, text: variableOrArgument}
	dataExpression := ""
	var setValue guiRuntimeValue
	if operation == "toggle_game_view_data" {
		dataExpression = normalizeGUIRuntimeExpression(args[1])
		if dataExpression == "" || len([]rune(dataExpression)) > 512 || strings.ContainsAny(dataExpression, "\r\n") {
			return 0, false
		}
	} else if operation == "set_variable" {
		setValue, ok = parseGUIRuntimeLiteral(args[1])
		if !ok {
			return 0, false
		}
		dataExpression = strings.TrimSpace(args[1])
		if len([]rune(dataExpression)) > 512 || strings.ContainsAny(dataExpression, "\r\n") {
			return 0, false
		}
	}
	factExpression := "IsGameViewOpen('" + argument.text + "')"
	if operation == "set_map_mode" {
		factExpression = "IsMapMode('" + argument.text + "')"
	} else if operation == "toggle_variable" || operation == "clear_variable" || operation == "set_variable" {
		factExpression = guiRuntimeVariableExistsExpression(argument.text)
	}
	factNode := compiler.fact(factExpression, guiRuntimeKindBoolean)
	plan := GUIRuntimeActionPlan{
		ID: len(compiler.actions), Expression: strings.TrimSpace(expression), Operation: operation,
		Fact: factNode.fact, Argument: argument.text, DataExpression: dataExpression,
	}
	if operation == "set_variable" {
		valueNode := compiler.fact(guiRuntimeVariableValueExpression(argument.text), setValue.kind)
		plan.Updates = []GUIRuntimeActionUpdate{
			{
				Fact: factNode.fact, Expression: factExpression,
				Operation: guiRuntimeActionUpdateSet, Value: true,
			},
			{
				Fact: valueNode.fact, Expression: guiRuntimeVariableValueExpression(argument.text),
				Operation: guiRuntimeActionUpdateSet, Value: publicGUIRuntimeValue(setValue),
			},
		}
	} else if operation == "clear_variable" {
		valueNode := compiler.fact(guiRuntimeVariableValueExpression(argument.text), guiRuntimeKindUnknown)
		plan.Updates = []GUIRuntimeActionUpdate{
			{
				Fact: factNode.fact, Expression: factExpression,
				Operation: guiRuntimeActionUpdateSet, Value: false,
			},
			{
				Fact: valueNode.fact, Expression: guiRuntimeVariableValueExpression(argument.text),
				Operation: guiRuntimeActionUpdateUnset,
			},
		}
	}
	compiler.actionIndex[normalized] = plan.ID
	compiler.actions = append(compiler.actions, plan)
	return plan.ID, true
}

func parseGUIRuntimeVariableName(expression string) (string, bool) {
	value, ok := parseGUIRuntimeLiteral(expression)
	if !ok || value.kind != guiRuntimeKindString {
		return "", false
	}
	name := strings.TrimSpace(value.text)
	if name == "" || len([]rune(name)) > 128 || strings.ContainsAny(name, "\r\n'") {
		return "", false
	}
	return name, true
}

func guiRuntimeVariableExistsExpression(name string) string {
	return "GetVariableSystem.Exists('" + name + "')"
}

func guiRuntimeVariableValueExpression(name string) string {
	return "GetVariableSystem.Get('" + name + "')"
}

func publicGUIRuntimeValue(value guiRuntimeValue) any {
	switch value.kind {
	case guiRuntimeKindBoolean:
		return value.boolV
	case guiRuntimeKindNumber:
		return value.number
	case guiRuntimeKindString:
		return value.text
	default:
		return nil
	}
}

func appendGUIRuntimeTokens(node *guiExprNode, tokens *[]GUIRuntimeToken) {
	if node == nil {
		return
	}
	switch node.op {
	case "fact":
		index := node.fact
		*tokens = append(*tokens, GUIRuntimeToken{Op: "f", Fact: &index})
	case "literal":
		token := GUIRuntimeToken{}
		switch node.value.kind {
		case guiRuntimeKindBoolean:
			value := node.value.boolV
			token.Op, token.Bool = "b", &value
		case guiRuntimeKindNumber:
			value := node.value.number
			token.Op, token.Number = "n", &value
		case guiRuntimeKindString:
			value := node.value.text
			token.Op, token.Text = "s", &value
		}
		*tokens = append(*tokens, token)
	case "select":
		if len(node.args) != 3 {
			return
		}
		token := GUIRuntimeToken{Op: "select"}
		appendGUIRuntimeTokens(node.args[0], &token.Condition)
		appendGUIRuntimeTokens(node.args[1], &token.WhenTrue)
		appendGUIRuntimeTokens(node.args[2], &token.WhenFalse)
		*tokens = append(*tokens, token)
	default:
		for _, child := range node.args {
			appendGUIRuntimeTokens(child, tokens)
		}
		token := GUIRuntimeToken{Op: node.op}
		if node.op == "and" || node.op == "or" {
			token.Arity = len(node.args)
		}
		*tokens = append(*tokens, token)
	}
}

func evaluateGUIRuntimeTokens(tokens []GUIRuntimeToken, facts []GUIRuntimeFact) (guiRuntimeValue, []int) {
	stack := make([]guiRuntimeValue, 0, 8)
	missingSet := map[int]bool{}
	pop := func() guiRuntimeValue {
		if len(stack) == 0 {
			return guiRuntimeValue{kind: guiRuntimeKindUnknown}
		}
		value := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return value
	}
	for _, token := range tokens {
		switch token.Op {
		case "select":
			condition, conditionMissing := evaluateGUIRuntimeTokens(token.Condition, facts)
			for _, index := range conditionMissing {
				missingSet[index] = true
			}
			if !condition.known || condition.kind != guiRuntimeKindBoolean {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindUnknown})
				continue
			}
			branch := token.WhenFalse
			if condition.boolV {
				branch = token.WhenTrue
			}
			value, branchMissing := evaluateGUIRuntimeTokens(branch, facts)
			for _, index := range branchMissing {
				missingSet[index] = true
			}
			stack = append(stack, value)
		case "f":
			if token.Fact == nil || *token.Fact < 0 || *token.Fact >= len(facts) {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindUnknown})
				continue
			}
			fact := facts[*token.Fact]
			if !fact.value.known {
				missingSet[*token.Fact] = true
			}
			stack = append(stack, fact.value)
		case "b":
			if token.Bool != nil {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: *token.Bool})
			}
		case "n":
			if token.Number != nil {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: *token.Number})
			}
		case "s":
			if token.Text != nil {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindString, known: true, text: *token.Text})
			}
		case "not":
			stack = append(stack, guiRuntimeNot(pop()))
		case "and", "or":
			arity := token.Arity
			if arity < 1 || arity > len(stack) {
				stack = append(stack, guiRuntimeValue{kind: guiRuntimeKindBoolean})
				continue
			}
			values := append([]guiRuntimeValue(nil), stack[len(stack)-arity:]...)
			stack = stack[:len(stack)-arity]
			stack = append(stack, guiRuntimeBooleanGroup(token.Op, values))
		case "eq", "ne", "lt", "le", "gt", "ge":
			right, left := pop(), pop()
			stack = append(stack, guiRuntimeCompare(token.Op, left, right))
		}
	}
	missing := make([]int, 0, len(missingSet))
	for index := range missingSet {
		missing = append(missing, index)
	}
	sort.Ints(missing)
	if len(stack) != 1 {
		return guiRuntimeValue{kind: guiRuntimeKindUnknown}, missing
	}
	return stack[0], missing
}

func referenceGUIRuntimeTokens(tokens []GUIRuntimeToken, seen map[int]bool) {
	for _, token := range tokens {
		if token.Op == "f" && token.Fact != nil {
			seen[*token.Fact] = true
		}
		referenceGUIRuntimeTokens(token.Condition, seen)
		referenceGUIRuntimeTokens(token.WhenTrue, seen)
		referenceGUIRuntimeTokens(token.WhenFalse, seen)
	}
}

func guiRuntimeNot(value guiRuntimeValue) guiRuntimeValue {
	if !value.known || value.kind != guiRuntimeKindBoolean {
		return guiRuntimeValue{kind: guiRuntimeKindBoolean}
	}
	return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: !value.boolV}
}

func guiRuntimeBooleanGroup(operation string, values []guiRuntimeValue) guiRuntimeValue {
	unknown := false
	for _, value := range values {
		if !value.known || value.kind != guiRuntimeKindBoolean {
			unknown = true
			continue
		}
		if operation == "and" && !value.boolV {
			return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: false}
		}
		if operation == "or" && value.boolV {
			return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: true}
		}
	}
	if unknown {
		return guiRuntimeValue{kind: guiRuntimeKindBoolean}
	}
	return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: operation == "and"}
}

func guiRuntimeCompare(operation string, left, right guiRuntimeValue) guiRuntimeValue {
	result := guiRuntimeValue{kind: guiRuntimeKindBoolean}
	if !left.known || !right.known || left.kind != right.kind {
		return result
	}
	var value bool
	switch left.kind {
	case guiRuntimeKindNumber:
		switch operation {
		case "eq":
			value = left.number == right.number
		case "ne":
			value = left.number != right.number
		case "lt":
			value = left.number < right.number
		case "le":
			value = left.number <= right.number
		case "gt":
			value = left.number > right.number
		case "ge":
			value = left.number >= right.number
		}
	case guiRuntimeKindString:
		switch operation {
		case "eq":
			value = left.text == right.text
		case "ne":
			value = left.text != right.text
		case "lt":
			value = left.text < right.text
		case "le":
			value = left.text <= right.text
		case "gt":
			value = left.text > right.text
		case "ge":
			value = left.text >= right.text
		}
	case guiRuntimeKindBoolean:
		if operation == "eq" {
			value = left.boolV == right.boolV
		}
		if operation == "ne" {
			value = left.boolV != right.boolV
		}
	default:
		return result
	}
	result.known, result.boolV = true, value
	return result
}

func guiRuntimeOperation(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "and", "or", "not":
		return name
	}
	for prefix, operation := range map[string]string{
		"lessthanorequalto": "le", "greaterthanorequalto": "ge",
		"notequalto": "ne", "lessthan": "lt", "greaterthan": "gt", "equalto": "eq",
	} {
		if name == prefix || strings.HasPrefix(name, prefix+"_") {
			return operation
		}
	}
	return ""
}

func splitGUIRuntimeCall(expression string) (string, []string, bool, error) {
	expression = strings.TrimSpace(expression)
	open := strings.IndexByte(expression, '(')
	if open <= 0 {
		return "", nil, false, nil
	}
	name := strings.TrimSpace(expression[:open])
	for _, segment := range strings.Split(name, ".") {
		if segment == "" {
			return "", nil, false, nil
		}
		for index, char := range segment {
			if !unicode.IsLetter(char) && char != '_' && (index == 0 || !unicode.IsDigit(char)) {
				return "", nil, false, nil
			}
		}
	}
	depth, quote, escaped, close := 0, rune(0), false, -1
	start := open + 1
	var args []string
	for index, char := range expression[open:] {
		absolute := open + index
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		switch char {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return "", nil, true, fmt.Errorf("unbalanced expression %q", expression)
			}
			if depth == 0 {
				close = absolute
				argument := strings.TrimSpace(expression[start:absolute])
				if argument != "" {
					args = append(args, argument)
				}
				break
			}
		case ',':
			if depth == 1 {
				argument := strings.TrimSpace(expression[start:absolute])
				if argument == "" {
					return "", nil, true, fmt.Errorf("empty argument in %q", expression)
				}
				args = append(args, argument)
				start = absolute + 1
			}
		}
		if close >= 0 {
			break
		}
	}
	if quote != 0 || depth != 0 || close < 0 {
		return "", nil, true, fmt.Errorf("unbalanced expression %q", expression)
	}
	if strings.TrimSpace(expression[close+1:]) != "" {
		return "", nil, false, nil
	}
	return name, args, true, nil
}

func parseGUIRuntimeLiteral(expression string) (guiRuntimeValue, bool) {
	value := strings.TrimSpace(expression)
	switch strings.ToLower(value) {
	case "yes", "true":
		return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: true}, true
	case "no", "false":
		return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: false}, true
	}
	quoted := false
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		quoted = true
		value = value[1 : len(value)-1]
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "(") {
		if end := strings.IndexByte(trimmed, ')'); end > 1 {
			typeName := strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			if strings.Contains(typeName, "int") || strings.Contains(typeName, "fixed") || strings.Contains(typeName, "float") || strings.Contains(typeName, "double") {
				if number, err := strconv.ParseFloat(strings.TrimSpace(trimmed[end+1:]), 64); err == nil {
					return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: number}, true
				}
			}
		}
	}
	if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: number}, true
	}
	if quoted {
		return guiRuntimeValue{kind: guiRuntimeKindString, known: true, text: value}, true
	}
	return guiRuntimeValue{}, false
}

func normalizeGUIRuntimeFactValue(value any) (guiRuntimeValue, any, error) {
	switch typed := value.(type) {
	case bool:
		return guiRuntimeValue{kind: guiRuntimeKindBoolean, known: true, boolV: typed}, typed, nil
	case float64:
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: typed}, typed, nil
	case float32:
		value := float64(typed)
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: value}, value, nil
	case int:
		value := float64(typed)
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: value}, value, nil
	case int64:
		value := float64(typed)
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: value}, value, nil
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			return guiRuntimeValue{}, nil, fmt.Errorf("value is not a valid number")
		}
		return guiRuntimeValue{kind: guiRuntimeKindNumber, known: true, number: value}, value, nil
	case string:
		if len([]rune(typed)) > guiScenarioMaxValue {
			return guiRuntimeValue{}, nil, fmt.Errorf("value exceeds %d characters", guiScenarioMaxValue)
		}
		return guiRuntimeValue{kind: guiRuntimeKindString, known: true, text: typed}, typed, nil
	default:
		return guiRuntimeValue{}, nil, fmt.Errorf("value must be a JSON boolean, number, or string")
	}
}

func normalizeGUIRuntimeExpression(expression string) string {
	expression = strings.TrimSpace(expression)
	if len(expression) >= 2 && expression[0] == '[' && expression[len(expression)-1] == ']' {
		expression = strings.TrimSpace(expression[1 : len(expression)-1])
	}
	return expression
}

func guiRuntimeFactKey(expression string) string {
	expression = normalizeGUIRuntimeExpression(expression)
	var output strings.Builder
	output.Grow(len(expression))
	quote, escaped := rune(0), false
	for _, char := range expression {
		if quote != 0 {
			output.WriteRune(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			output.WriteRune(char)
			continue
		}
		if unicode.IsSpace(char) {
			continue
		}
		output.WriteRune(char)
	}
	return output.String()
}

func normalizeGUIRuntimeKind(kind string) string {
	switch kind {
	case guiRuntimeKindBoolean, guiRuntimeKindNumber, guiRuntimeKindString, guiRuntimeKindMixed:
		return kind
	default:
		return guiRuntimeKindUnknown
	}
}

func mergeGUIRuntimeKinds(first, second string) string {
	first, second = normalizeGUIRuntimeKind(first), normalizeGUIRuntimeKind(second)
	if first == guiRuntimeKindMixed || second == guiRuntimeKindMixed {
		return guiRuntimeKindMixed
	}
	if first == guiRuntimeKindUnknown {
		return second
	}
	if second == guiRuntimeKindUnknown || first == second {
		return first
	}
	return guiRuntimeKindMixed
}

func equalGUIRuntimeValue(first, second guiRuntimeValue) bool {
	if first.kind != second.kind || first.known != second.known {
		return false
	}
	switch first.kind {
	case guiRuntimeKindBoolean:
		return first.boolV == second.boolV
	case guiRuntimeKindNumber:
		return first.number == second.number
	case guiRuntimeKindString:
		return first.text == second.text
	default:
		return true
	}
}
