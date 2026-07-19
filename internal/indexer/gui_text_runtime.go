package indexer

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	guiRuntimeTextMaxRunes         = 2048
	guiRuntimeTextMaxTokens        = 128
	guiRuntimeTextConditionalDepth = 4
)

var guiRuntimeTextFormatTag = regexp.MustCompile(`#[[:alnum:]_.-]+(?:;[[:alnum:]_.-]+)*|#!`)

type GUIRuntimeTextPlan struct {
	ID           int                   `json:"id"`
	Template     string                `json:"template"`
	Localized    bool                  `json:"localized,omitempty"`
	Supported    bool                  `json:"supported"`
	Tokens       []GUIRuntimeTextToken `json:"tokens,omitempty"`
	Status       string                `json:"status"`
	Result       string                `json:"result,omitempty"`
	MissingFacts []int                 `json:"missing_facts,omitempty"`
	Error        string                `json:"error,omitempty"`
}

type GUIRuntimeTextToken struct {
	Literal   string                `json:"l,omitempty"`
	Fact      *int                  `json:"f,omitempty"`
	Format    string                `json:"x,omitempty"`
	Condition []GUIRuntimeToken     `json:"c,omitempty"`
	WhenTrue  []GUIRuntimeTextToken `json:"t,omitempty"`
	WhenFalse []GUIRuntimeTextToken `json:"e,omitempty"`
}

type GUIRuntimeTextBindingSet struct {
	Raw         *GUIRuntimeTextBinding `json:"raw,omitempty"`
	English     *GUIRuntimeTextBinding `json:"english,omitempty"`
	SimpChinese *GUIRuntimeTextBinding `json:"simp_chinese,omitempty"`
}

type GUIRuntimeTextBinding struct {
	PlanID       int    `json:"plan_id"`
	Status       string `json:"status"`
	Result       string `json:"result,omitempty"`
	MissingFacts []int  `json:"missing_facts,omitempty"`
}

func (compiler *guiRuntimeCompiler) bindNodeRuntimeText(node *GUIPreviewNode) {
	if node == nil {
		return
	}
	text := &GUIRuntimeTextBindingSet{}
	rawText := node.Text
	if node.Semantics != nil && strings.TrimSpace(node.Semantics.RawText) != "" {
		rawText = node.Semantics.RawText
	}
	text.Raw = compiler.runtimeTextBinding(rawText, false, "", nil)
	if node.TextLocalization != nil {
		if node.TextLocalization.English != nil && node.TextLocalization.English.Dynamic {
			text.English = compiler.runtimeLocalizedTextBinding(node.TextLocalization.English)
		}
		if node.TextLocalization.SimpChinese != nil && node.TextLocalization.SimpChinese.Dynamic {
			text.SimpChinese = compiler.runtimeLocalizedTextBinding(node.TextLocalization.SimpChinese)
		}
	} else {
		text.English = compiler.runtimeConditionalLocalizedTextBinding(rawText, GUIPreviewLanguageEnglish)
		text.SimpChinese = compiler.runtimeConditionalLocalizedTextBinding(rawText, GUIPreviewLanguageSimpChinese)
	}
	if text.Raw != nil || text.English != nil || text.SimpChinese != nil {
		if node.Runtime == nil {
			node.Runtime = &GUINodeRuntime{}
		}
		node.Runtime.Text = text
	}

	tooltip := &GUIRuntimeTextBindingSet{}
	if node.Semantics != nil {
		tooltip.Raw = compiler.runtimeTextBinding(node.Semantics.Tooltip, false, "", nil)
	}
	if node.TooltipLocalization != nil {
		if node.TooltipLocalization.English != nil && node.TooltipLocalization.English.Dynamic {
			tooltip.English = compiler.runtimeLocalizedTextBinding(node.TooltipLocalization.English)
		}
		if node.TooltipLocalization.SimpChinese != nil && node.TooltipLocalization.SimpChinese.Dynamic {
			tooltip.SimpChinese = compiler.runtimeLocalizedTextBinding(node.TooltipLocalization.SimpChinese)
		}
	} else if node.Semantics != nil {
		tooltip.English = compiler.runtimeConditionalLocalizedTextBinding(node.Semantics.Tooltip, GUIPreviewLanguageEnglish)
		tooltip.SimpChinese = compiler.runtimeConditionalLocalizedTextBinding(node.Semantics.Tooltip, GUIPreviewLanguageSimpChinese)
	}
	if tooltip.Raw != nil || tooltip.English != nil || tooltip.SimpChinese != nil {
		if node.Runtime == nil {
			node.Runtime = &GUINodeRuntime{}
		}
		node.Runtime.Tooltip = tooltip
	}
}

func guiRuntimeLocalizationTemplate(value *GUILocalizedValue) string {
	if value == nil {
		return ""
	}
	if value.ResolvedValue != "" {
		return value.ResolvedValue
	}
	return value.Value
}

func (compiler *guiRuntimeCompiler) runtimeLocalizedTextBinding(value *GUILocalizedValue) *GUIRuntimeTextBinding {
	if value == nil {
		return nil
	}
	return compiler.runtimeTextBinding(guiRuntimeLocalizationTemplate(value), true, value.Language, value.runtimeLookup)
}

func (compiler *guiRuntimeCompiler) runtimeConditionalLocalizedTextBinding(template, language string) *GUIRuntimeTextBinding {
	lookup := compiler.runtimeLocalizationLookups[language]
	if len(lookup) == 0 || len(guiConditionalLocalizationKeys(template)) == 0 {
		return nil
	}
	return compiler.runtimeTextBinding(template, true, language, lookup)
}

func (compiler *guiRuntimeCompiler) runtimeTextBinding(template string, localized bool, language string, localizationLookup map[string]string) *GUIRuntimeTextBinding {
	template = strings.TrimSpace(template)
	if template == "" || (!strings.Contains(template, "[") && !strings.Contains(template, "$")) {
		return nil
	}
	planID, dynamic := compiler.textPlan(template, localized, language, localizationLookup)
	if !dynamic {
		return nil
	}
	compiler.referenceTextPlan(planID)
	return &GUIRuntimeTextBinding{PlanID: planID}
}

func (compiler *guiRuntimeCompiler) textPlan(template string, localized bool, language string, localizationLookup map[string]string) (int, bool) {
	template = strings.TrimSpace(template)
	key := strconv.FormatBool(localized) + "\x00" + language + "\x00" + template
	if index, exists := compiler.textPlanIndex[key]; exists {
		return index, true
	}
	plan := GUIRuntimeTextPlan{ID: len(compiler.textPlans), Template: template, Localized: localized, Supported: true}
	tokens, dynamic, err := compiler.compileTextTemplate(template, localized, localizationLookup)
	if !dynamic {
		return 0, false
	}
	if err != nil {
		plan.Supported = false
		plan.Status = "unsupported"
		plan.Error = err.Error()
		compiler.unsupported[template] = plan.Error
	} else {
		plan.Tokens = tokens
	}
	compiler.textPlanIndex[key] = plan.ID
	compiler.textPlans = append(compiler.textPlans, plan)
	return plan.ID, true
}

func (compiler *guiRuntimeCompiler) compileTextTemplate(template string, localized bool, localizationLookup map[string]string) ([]GUIRuntimeTextToken, bool, error) {
	return compiler.compileTextTemplateDepth(template, localized, localizationLookup, 0, false)
}

func (compiler *guiRuntimeCompiler) compileTextTemplateDepth(template string, localized bool, localizationLookup map[string]string, depth int, preserveBoundaryWhitespace bool) ([]GUIRuntimeTextToken, bool, error) {
	if depth > guiRuntimeTextConditionalDepth {
		return nil, true, fmt.Errorf("runtime text conditional nesting exceeds %d", guiRuntimeTextConditionalDepth)
	}
	if len([]rune(template)) > guiRuntimeTextMaxRunes {
		return nil, true, fmt.Errorf("runtime text template exceeds %d characters", guiRuntimeTextMaxRunes)
	}
	template = normalizeGUIRuntimeTextTemplate(template, localized, preserveBoundaryWhitespace)
	var tokens []GUIRuntimeTextToken
	appendLiteral := func(value string) {
		if value == "" {
			return
		}
		if len(tokens) > 0 && isGUIRuntimeLiteralTextToken(tokens[len(tokens)-1]) {
			tokens[len(tokens)-1].Literal += value
			return
		}
		tokens = append(tokens, GUIRuntimeTextToken{Literal: value})
	}
	dynamic := false
	for offset := 0; offset < len(template); {
		nextBracket := strings.IndexByte(template[offset:], '[')
		nextMacro := strings.IndexByte(template[offset:], '$')
		start := -1
		kind := byte(0)
		if nextBracket >= 0 {
			start, kind = offset+nextBracket, '['
		}
		if nextMacro >= 0 && (start < 0 || offset+nextMacro < start) {
			start, kind = offset+nextMacro, '$'
		}
		if start < 0 {
			appendLiteral(template[offset:])
			break
		}
		appendLiteral(template[offset:start])
		end := -1
		if kind == '[' {
			end = strings.IndexByte(template[start+1:], ']')
		} else {
			end = strings.IndexByte(template[start+1:], '$')
		}
		if end < 0 {
			return nil, true, fmt.Errorf("unclosed runtime text marker")
		}
		end += start + 1
		expression := strings.TrimSpace(template[start+1 : end])
		if kind == '$' {
			expression = "$" + expression + "$"
		}
		if expression == "" {
			return nil, true, fmt.Errorf("empty runtime text marker")
		}
		format := ""
		if kind == '[' {
			expression, format = splitGUIRuntimeTextFormat(expression)
			if format == "" {
				conditional, ok, err := compiler.compileConditionalTextExpression(expression, localized, localizationLookup, depth)
				if err != nil {
					return nil, true, err
				}
				if ok {
					tokens = append(tokens, conditional)
					dynamic = true
					offset = end + 1
					if countGUIRuntimeTextTokens(tokens) > guiRuntimeTextMaxTokens {
						return nil, true, fmt.Errorf("runtime text template exceeds %d tokens", guiRuntimeTextMaxTokens)
					}
					continue
				}
			}
		}
		expected := guiRuntimeKindUnknown
		if format != "" {
			expected = guiRuntimeKindNumber
		}
		fact := compiler.fact(expression, expected)
		index := fact.fact
		tokens = append(tokens, GUIRuntimeTextToken{Fact: &index, Format: format})
		dynamic = true
		offset = end + 1
		if countGUIRuntimeTextTokens(tokens) > guiRuntimeTextMaxTokens {
			return nil, true, fmt.Errorf("runtime text template exceeds %d tokens", guiRuntimeTextMaxTokens)
		}
	}
	return tokens, dynamic, nil
}

func isGUIRuntimeLiteralTextToken(token GUIRuntimeTextToken) bool {
	return token.Fact == nil && len(token.Condition) == 0
}

func countGUIRuntimeTextTokens(tokens []GUIRuntimeTextToken) int {
	count := 0
	for _, token := range tokens {
		count++
		count += len(token.Condition)
		count += countGUIRuntimeTextTokens(token.WhenTrue)
		count += countGUIRuntimeTextTokens(token.WhenFalse)
	}
	return count
}

func (compiler *guiRuntimeCompiler) compileConditionalTextExpression(expression string, localized bool, localizationLookup map[string]string, depth int) (GUIRuntimeTextToken, bool, error) {
	name, args, isCall, err := splitGUIRuntimeCall(expression)
	if err != nil || !isCall {
		return GUIRuntimeTextToken{}, false, err
	}
	if depth >= guiRuntimeTextConditionalDepth {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "selectlocalization", "select_cstring", "addtextif", "addlocalizationif":
			return GUIRuntimeTextToken{}, true, fmt.Errorf("runtime text conditional nesting exceeds %d", guiRuntimeTextConditionalDepth)
		default:
			return GUIRuntimeTextToken{}, false, nil
		}
	}

	resolveLocalization := false
	hasFalseBranch := false
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "selectlocalization":
		if len(args) != 3 {
			return GUIRuntimeTextToken{}, true, fmt.Errorf("%s requires three arguments", name)
		}
		resolveLocalization = true
		hasFalseBranch = true
	case "select_cstring":
		if len(args) != 3 {
			return GUIRuntimeTextToken{}, true, fmt.Errorf("%s requires three arguments", name)
		}
		hasFalseBranch = true
	case "addtextif":
		if len(args) != 2 {
			return GUIRuntimeTextToken{}, true, fmt.Errorf("%s requires two arguments", name)
		}
	case "addlocalizationif":
		if len(args) != 2 {
			return GUIRuntimeTextToken{}, true, fmt.Errorf("%s requires two arguments", name)
		}
		resolveLocalization = true
	default:
		return GUIRuntimeTextToken{}, false, nil
	}

	if literal, ok := parseGUIRuntimeLiteral(args[0]); ok && literal.kind != guiRuntimeKindBoolean {
		return GUIRuntimeTextToken{}, true, fmt.Errorf("%s requires a boolean condition", name)
	}
	condition, err := compiler.compile(args[0], guiRuntimeKindBoolean)
	if err != nil {
		return GUIRuntimeTextToken{}, true, err
	}
	var conditionTokens []GUIRuntimeToken
	appendGUIRuntimeTokens(condition, &conditionTokens)
	whenTrue, err := compiler.compileConditionalTextBranch(args[1], localized, resolveLocalization, localizationLookup, depth+1)
	if err != nil {
		return GUIRuntimeTextToken{}, true, err
	}
	var whenFalse []GUIRuntimeTextToken
	if hasFalseBranch {
		whenFalse, err = compiler.compileConditionalTextBranch(args[2], localized, resolveLocalization, localizationLookup, depth+1)
		if err != nil {
			return GUIRuntimeTextToken{}, true, err
		}
	}
	return GUIRuntimeTextToken{Condition: conditionTokens, WhenTrue: whenTrue, WhenFalse: whenFalse}, true, nil
}

func (compiler *guiRuntimeCompiler) compileConditionalTextBranch(expression string, localized, resolveLocalization bool, localizationLookup map[string]string, depth int) ([]GUIRuntimeTextToken, error) {
	if literal, ok := parseGUIRuntimeLiteral(expression); ok {
		value := ""
		switch literal.kind {
		case guiRuntimeKindString:
			value = literal.text
			if resolveLocalization {
				if resolved, exists := localizationLookup[value]; exists {
					value = resolved
				}
			}
		case guiRuntimeKindBoolean:
			value = strconv.FormatBool(literal.boolV)
		case guiRuntimeKindNumber:
			value = strconv.FormatFloat(literal.number, 'f', -1, 64)
		default:
			return nil, fmt.Errorf("conditional text branch has unsupported literal type")
		}
		tokens, _, err := compiler.compileTextTemplateDepth(value, localized, localizationLookup, depth, true)
		return tokens, err
	}
	if conditional, ok, err := compiler.compileConditionalTextExpression(expression, localized, localizationLookup, depth); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return []GUIRuntimeTextToken{conditional}, nil
	}
	fact := compiler.fact(expression, guiRuntimeKindString)
	index := fact.fact
	return []GUIRuntimeTextToken{{Fact: &index}}, nil
}

func normalizeGUIRuntimeTextTemplate(template string, localized, preserveBoundaryWhitespace bool) string {
	if !preserveBoundaryWhitespace {
		template = strings.TrimSpace(template)
	}
	if len(template) >= 2 && ((template[0] == '"' && template[len(template)-1] == '"') || (template[0] == '\'' && template[len(template)-1] == '\'')) {
		template = template[1 : len(template)-1]
	}
	template = strings.ReplaceAll(template, `\n`, "\n")
	if localized {
		template = guiLocalizationSimpleLink.ReplaceAllStringFunc(template, func(match string) string {
			inner := strings.TrimSuffix(strings.TrimPrefix(match, "["), "]")
			separator := strings.LastIndexByte(inner, '|')
			if separator < 0 || validGUIRuntimeNumberFormat(strings.TrimSpace(inner[separator+1:])) {
				return match
			}
			return strings.TrimSpace(inner[:separator])
		})
	}
	template = guiLocalizationIcon.ReplaceAllString(template, "●")
	template = guiLocalizationColorTag.ReplaceAllString(template, "")
	template = guiRuntimeTextFormatTag.ReplaceAllString(template, "")
	if !preserveBoundaryWhitespace {
		template = strings.TrimSpace(template)
	}
	return template
}

func splitGUIRuntimeTextFormat(expression string) (string, string) {
	quote, depth, separator := rune(0), 0, -1
	for index, char := range expression {
		if quote != 0 {
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
			if depth > 0 {
				depth--
			}
		case '|':
			if depth == 0 {
				separator = index
			}
		}
	}
	if separator < 0 {
		return strings.TrimSpace(expression), ""
	}
	format := strings.TrimSpace(expression[separator+1:])
	if !validGUIRuntimeNumberFormat(format) {
		return strings.TrimSpace(expression), ""
	}
	return strings.TrimSpace(expression[:separator]), format
}

func validGUIRuntimeNumberFormat(format string) bool {
	if strings.HasPrefix(format, "+=") {
		format = strings.TrimPrefix(format, "+=")
	}
	if format == "" || len(format) > 1 || format[0] < '0' || format[0] > '4' {
		return false
	}
	return true
}

func (compiler *guiRuntimeCompiler) referenceTextPlan(planID int) {
	if planID < 0 || planID >= len(compiler.textPlans) {
		return
	}
	seen := map[int]bool{}
	var referenceTextTokens func([]GUIRuntimeTextToken)
	referenceTextTokens = func(tokens []GUIRuntimeTextToken) {
		for _, token := range tokens {
			referenceGUIRuntimeTokens(token.Condition, seen)
			if token.Fact != nil {
				seen[*token.Fact] = true
			}
			referenceTextTokens(token.WhenTrue)
			referenceTextTokens(token.WhenFalse)
		}
	}
	referenceTextTokens(compiler.textPlans[planID].Tokens)
	for factIndex := range seen {
		if factIndex >= 0 && factIndex < len(compiler.facts) {
			compiler.facts[factIndex].References++
		}
	}
}

func evaluateGUIRuntimeTextTokens(tokens []GUIRuntimeTextToken, facts []GUIRuntimeFact) (string, []int, bool) {
	var output strings.Builder
	missingSet := map[int]bool{}
	unresolved := false
	var evaluate func([]GUIRuntimeTextToken)
	evaluate = func(items []GUIRuntimeTextToken) {
		for _, token := range items {
			if len(token.Condition) > 0 {
				condition, missing := evaluateGUIRuntimeTokens(token.Condition, facts)
				for _, index := range missing {
					missingSet[index] = true
				}
				if !condition.known || condition.kind != guiRuntimeKindBoolean {
					unresolved = true
					output.WriteString("<unknown>")
					continue
				}
				if condition.boolV {
					evaluate(token.WhenTrue)
				} else {
					evaluate(token.WhenFalse)
				}
				continue
			}
			if token.Fact == nil {
				output.WriteString(token.Literal)
				continue
			}
			index := *token.Fact
			if index < 0 || index >= len(facts) || !facts[index].value.known {
				missingSet[index] = true
				unresolved = true
				output.WriteString("<unknown>")
				continue
			}
			value, ok := formatGUIRuntimeTextValue(facts[index].value, token.Format)
			if !ok {
				missingSet[index] = true
				unresolved = true
				output.WriteString("<unknown>")
				continue
			}
			output.WriteString(value)
		}
	}
	evaluate(tokens)
	missing := make([]int, 0, len(missingSet))
	for index := range missingSet {
		missing = append(missing, index)
	}
	sort.Ints(missing)
	return output.String(), missing, unresolved
}

func formatGUIRuntimeTextValue(value guiRuntimeValue, format string) (string, bool) {
	if !value.known {
		return "", false
	}
	switch value.kind {
	case guiRuntimeKindString:
		if format != "" {
			return "", false
		}
		return value.text, true
	case guiRuntimeKindBoolean:
		if format != "" {
			return "", false
		}
		return strconv.FormatBool(value.boolV), true
	case guiRuntimeKindNumber:
		if format == "" {
			return strconv.FormatFloat(value.number, 'f', -1, 64), true
		}
		signed := strings.HasPrefix(format, "+=")
		precision, err := strconv.Atoi(strings.TrimPrefix(format, "+="))
		if err != nil {
			return "", false
		}
		factor := math.Pow10(precision)
		rounded := math.Round(value.number*factor) / factor
		result := strconv.FormatFloat(rounded, 'f', precision, 64)
		if signed && value.number > 0 {
			result = "+" + result
		}
		return result, true
	default:
		return "", false
	}
}

func bindGUIRuntimeTextSet(set *GUIRuntimeTextBindingSet, plans []GUIRuntimeTextPlan) {
	if set == nil {
		return
	}
	bindGUIRuntimeText(set.Raw, plans)
	bindGUIRuntimeText(set.English, plans)
	bindGUIRuntimeText(set.SimpChinese, plans)
}

func bindGUIRuntimeText(binding *GUIRuntimeTextBinding, plans []GUIRuntimeTextPlan) {
	if binding == nil || binding.PlanID < 0 || binding.PlanID >= len(plans) {
		return
	}
	plan := plans[binding.PlanID]
	binding.Status = plan.Status
	binding.Result = plan.Result
	binding.MissingFacts = append([]int(nil), plan.MissingFacts...)
}

func selectGUIRuntimeTextBinding(set *GUIRuntimeTextBindingSet, language string) (*GUIRuntimeTextBinding, *GUIRuntimeTextBinding) {
	if set == nil {
		return nil, nil
	}
	switch language {
	case GUIPreviewLanguageEnglish:
		if set.English != nil {
			return set.English, nil
		}
	case GUIPreviewLanguageSimpChinese:
		if set.SimpChinese != nil {
			return set.SimpChinese, nil
		}
	case GUIPreviewLanguageBilingual:
		if set.SimpChinese != nil || set.English != nil {
			return set.SimpChinese, set.English
		}
	}
	return set.Raw, nil
}

func resolvedGUIRuntimeText(set *GUIRuntimeTextBindingSet, language string) (string, bool) {
	first, second := selectGUIRuntimeTextBinding(set, language)
	if first == nil && second == nil {
		return "", false
	}
	values := make([]string, 0, 2)
	if first != nil {
		values = append(values, first.Result)
	}
	if second != nil {
		values = append(values, second.Result)
	}
	return strings.Join(values, " / "), true
}
