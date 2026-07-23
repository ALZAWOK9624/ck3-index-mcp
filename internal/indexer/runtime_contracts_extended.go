package indexer

import (
	"fmt"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// CK3 event options have two different AI selection grammars. The engine's
// event contract documents them as mutually exclusive; when both are present
// CK3 currently prefers ai_will_select, which is a silent semantic override.
func checkEventOptionContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, event := range nodes {
		if !isNumericEventID(event) {
			continue
		}
		for _, option := range event.Children {
			if option.Key != "option" || option.Kind != "block" {
				continue
			}
			var aiChance, aiWillSelect *script.Node
			for _, field := range option.Children {
				switch field.Key {
				case "ai_chance":
					aiChance = field
				case "ai_will_select":
					aiWillSelect = field
				}
			}
			if aiChance == nil || aiWillSelect == nil {
				continue
			}
			out = append(out, ctxDiag{
				severity: "warning",
				code:     "event_option_selection_conflict",
				msg:      fmt.Sprintf("event %q option declares both ai_chance and ai_will_select; CK3 1.19 treats them as mutually exclusive and prefers ai_will_select", event.Key),
				line:     aiWillSelect.Line,
				col:      aiWillSelect.Col,
			})
		}
	}
	return out
}

var characterModifierDefinitionFields = map[string]bool{
	"icon":         true,
	"scale":        true,
	"stacking":     true,
	"hide_effects": true,
}

var characterModifierScaleFields = map[string]bool{
	"value":        true,
	"desc":         true,
	"display_mode": true,
}

// common/modifiers defines reusable character modifiers. It is not the same
// thing as a modifier container: the direct children are still engine static
// modifier tags, with the documented icon/scale metadata and the additional
// engine-supported stacking and hide_effects flags observed in vanilla.
func checkCharacterModifierDefinitions(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		for _, field := range definition.Children {
			if field.Key == "icon" {
				continue
			}
			if field.Key == "scale" {
				if field.Kind != "block" {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "illegal_field_context",
						msg:      fmt.Sprintf("character modifier %q uses scale as a scalar; common/modifiers requires scale = { value = ... desc = ... }", definition.Key),
						line:     field.Line,
						col:      field.Col,
					})
					continue
				}
				for _, scaleField := range field.Children {
					if characterModifierScaleFields[scaleField.Key] {
						continue
					}
					out = append(out, ctxDiag{
						severity: "error",
						code:     "illegal_field_context",
						msg:      fmt.Sprintf("character modifier %q scale block has illegal field %q; valid fields are value and desc", definition.Key, scaleField.Key),
						line:     scaleField.Line,
						col:      scaleField.Col,
					})
				}
				continue
			}
			if characterModifierDefinitionFields[field.Key] {
				continue
			}
			modifier := LookupModifier(field.Key)
			if !modifier.Found {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "unknown_modifier_field",
					msg:      fmt.Sprintf("character modifier %q uses unknown CK3 modifier field %q; common/modifiers accepts current engine modifier tags only", definition.Key, field.Key),
					line:     field.Line,
					col:      field.Col,
				})
				continue
			}
		}
	}
	return out
}

var opinionModifierFields = map[string]bool{
	"days": true, "months": true, "years": true,
	"decaying": true, "delay_days": true, "delay_months": true, "delay_years": true,
	"growing": true, "monthly_change": true, "stacking": true,
	"min": true, "max": true,
	"opinion":             true,
	"imprisonment_reason": true, "banish_reason": true, "execute_reason": true,
	"revoke_title_reason": true, "divorce_reason": true, "obedient": true,
}

var opinionDurationFields = map[string]bool{
	"days": true, "months": true, "years": true,
}

var opinionDelayFields = map[string]bool{
	"delay_days": true, "delay_months": true, "delay_years": true,
}

func checkOpinionModifierContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		hasMonthlyChange := false
		hasDuration := false
		hasDecaying := hasDirectYesChild(definition, "decaying")
		hasGrowing := hasDirectYesChild(definition, "growing")
		delayField := ""
		monthlyChange := ""
		for _, field := range definition.Children {
			if !opinionModifierFields[field.Key] {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "illegal_field_context",
					msg:      fmt.Sprintf("opinion modifier %q has illegal field %q; check common/opinion_modifiers/_opinions.info", definition.Key, field.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
			if opinionDurationFields[field.Key] {
				hasDuration = true
			}
			if field.Key == "monthly_change" {
				hasMonthlyChange = true
				monthlyChange = strings.TrimSpace(field.Value)
			}
			if opinionDelayFields[field.Key] {
				delayField = field.Key
			}
		}
		if hasMonthlyChange && hasDuration {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "opinion_modifier_time_conflict",
				msg:      fmt.Sprintf("opinion modifier %q cannot combine monthly_change with days/months/years; the CK3 contract requires one duration grammar", definition.Key),
				line:     definition.Line,
				col:      definition.Col,
			})
		}
		if hasDecaying && hasGrowing {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "opinion_modifier_mode_conflict",
				msg:      fmt.Sprintf("opinion modifier %q cannot set both decaying = yes and growing = yes", definition.Key),
				line:     definition.Line,
				col:      definition.Col,
			})
		}
		if delayField != "" && !hasDecaying {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "opinion_modifier_invalid_delay",
				msg:      fmt.Sprintf("opinion modifier %q uses %s without decaying = yes", definition.Key, delayField),
				line:     definition.Line,
				col:      definition.Col,
			})
		}
		if (hasDecaying || hasGrowing) && !hasMonthlyChange && !hasDuration {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "opinion_modifier_missing_duration",
				msg:      fmt.Sprintf("opinion modifier %q uses decaying/growing but defines neither monthly_change nor days/months/years", definition.Key),
				line:     definition.Line,
				col:      definition.Col,
			})
		}
		if monthlyChange != "" {
			if value, err := strconv.ParseFloat(monthlyChange, 64); err == nil && value < 0 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "opinion_modifier_invalid_value",
					msg:      fmt.Sprintf("opinion modifier %q monthly_change must be non-negative, got %s", definition.Key, monthlyChange),
					line:     definition.Line,
					col:      definition.Col,
				})
			}
		}
	}
	return out
}

func checkScriptedRelationContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, relation := range nodes {
		if relation.Kind != "block" || relation.Key == "" {
			continue
		}
		for _, field := range relation.Children {
			if field.Key == "flags" && field.Kind == "block" && len(field.Children) > 32 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "scripted_relation_flag_limit",
					msg:      fmt.Sprintf("scripted relation %q declares %d flags; CK3 supports at most 32 flags per relation", relation.Key, len(field.Children)),
					line:     field.Line,
					col:      field.Col,
				})
			}
			if field.Key != "modifier" || field.Kind != "block" {
				continue
			}
			for _, modifierField := range field.Children {
				if modifierField.Key == "name" {
					continue
				}
				modifier := LookupModifier(modifierField.Key)
				if !modifier.Found {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "unknown_scripted_relation_modifier",
						msg:      fmt.Sprintf("scripted relation %q uses modifier %q, which is not a current static CK3 modifier; generated modifiers from schemes/lifestyles cannot be used here", relation.Key, modifierField.Key),
						line:     modifierField.Line,
						col:      modifierField.Col,
					})
					continue
				}
				if len(modifier.UseAreas) == 0 || modifierUseAreaAllowed(modifier.UseAreas, []string{"character"}) {
					continue
				}
				out = append(out, ctxDiag{
					severity: "error",
					code:     "invalid_modifier_context",
					msg:      fmt.Sprintf("scripted relation %q applies modifier %q to characters, but the modifier is valid for %s", relation.Key, modifierField.Key, strings.Join(modifier.UseAreas, "/")),
					line:     modifierField.Line,
					col:      modifierField.Col,
				})
			}
		}
	}
	return out
}

func checkReligionDoctrineOrder(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, religion := range nodes {
		if religion.Kind != "block" || religion.Key == "" {
			continue
		}
		faithsSeen := false
		for _, field := range religion.Children {
			if field.Key == "faiths" && field.Kind == "block" {
				faithsSeen = true
				continue
			}
			if faithsSeen && field.Key == "doctrine" {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "religion_doctrine_order",
					msg:      fmt.Sprintf("religion %q declares doctrine after faiths; CK3 requires religion-level doctrines before the faiths block", religion.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
		}
	}
	return out
}
