package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// Runtime-contract checks cover syntax that the generic PDX parser can read but
// CK3's object loader will reject or silently ignore. They are intentionally
// narrow: a missing entry here is safer than treating the empirical field
// table as an exhaustive engine schema.
func checkRuntimeContracts(nodes []*script.Node, relPath string) []ctxDiag {
	rel := strings.ToLower(filepathSlash(relPath))
	var out []ctxDiag
	if isEventPath(rel) {
		out = append(out, checkUnsupportedEventFields(nodes)...)
		out = append(out, checkEventOptionContracts(nodes)...)
	}
	if strings.Contains(rel, "common/on_action/") {
		out = append(out, checkOnActionRuntimeFields(nodes)...)
	}
	if strings.Contains(rel, "common/governments/") {
		out = append(out, checkGovernmentRuntimeFields(nodes)...)
	}
	if strings.Contains(rel, "common/laws/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkLawSuccessionContracts(nodes)...)
	}
	if strings.Contains(rel, "common/council_tasks/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkCouncilTaskContracts(nodes)...)
	}
	if strings.Contains(rel, "common/house_relation_types/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkHouseRelationContracts(nodes)...)
	}
	if strings.Contains(rel, "common/lease_contracts/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkLeaseContractContracts(nodes)...)
	}
	if strings.Contains(rel, "common/subject_contracts/contracts/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkSubjectContractContracts(nodes)...)
	}
	if strings.Contains(rel, "common/flavorization/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkFlavorizationContracts(nodes)...)
	}
	if strings.Contains(rel, "common/casus_belli_types/") {
		out = append(out, checkCasusBelliScriptValueFields(nodes)...)
	}
	if strings.Contains(rel, "common/modifier_definition_formats/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkModifierDefinitionFields(nodes)...)
	}
	if strings.Contains(rel, "common/modifiers/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkCharacterModifierDefinitions(nodes)...)
	}
	if strings.Contains(rel, "common/opinion_modifiers/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkOpinionModifierContracts(nodes)...)
	}
	if strings.Contains(rel, "common/scripted_relations/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkScriptedRelationContracts(nodes)...)
	}
	if strings.Contains(rel, "common/religion/religion_types/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkReligionDoctrineOrder(nodes)...)
	}
	if strings.Contains(rel, "common/culture/name_lists/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkNameListProbabilityContracts(nodes)...)
	}
	if strings.Contains(rel, "common/activities/activity_types/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkActivityTypeContracts(nodes)...)
		out = append(out, checkActivityCatalogContracts(nodes)...)
	}
	if strings.Contains(rel, "common/accolade_names/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkAccoladeNameContracts(nodes)...)
	}
	if strings.Contains(rel, "common/culture/eras/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkCultureEraContracts(nodes)...)
	}
	if strings.Contains(rel, "common/ai_war_stances/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkAIWarStanceContracts(nodes)...)
	}
	if strings.Contains(rel, "common/house_unities/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkHouseUnityContracts(nodes)...)
	}
	if strings.Contains(rel, "common/story_cycles/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkStoryCycleContracts(nodes)...)
	}
	if strings.Contains(rel, "common/decisions/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkDecisionContracts(nodes)...)
	}
	if strings.Contains(rel, "common/character_interactions/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkInteractionCatalogContracts(nodes)...)
	}
	if strings.Contains(rel, "common/great_projects/types/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkGreatProjectCatalogContracts(nodes)...)
	}
	if strings.Contains(rel, "common/situation/situations/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkSituationTakeoverContracts(nodes)...)
	}
	if strings.Contains(rel, "common/traits/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkTraitRuntimeContracts(nodes)...)
	}
	if strings.Contains(rel, "common/culture/innovations/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkInnovationAssetContracts(nodes)...)
	}
	if strings.Contains(rel, "common/event_transitions/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkEventTransitionContracts(nodes)...)
	}
	if strings.Contains(rel, "common/event_2d_effects/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkEvent2DEffectContracts(nodes)...)
	}
	if strings.Contains(rel, "common/event_themes/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkEventThemeContracts(nodes)...)
	}
	if strings.Contains(rel, "common/house_aspirations/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkHouseAspirationContracts(nodes)...)
	}
	if strings.Contains(rel, "common/dynasty_perks/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkDynastyPerkContracts(nodes)...)
	}
	if strings.Contains(rel, "common/struggle/struggles/") && !strings.HasSuffix(rel, ".info") {
		out = append(out, checkStruggleContracts(nodes)...)
	}
	out = append(out, checkModifierContainerFields(nodes, rel)...)
	return out
}

// CK3 1.19's event reader does not support the old is_triggered_only event
// field. Triggered events should be controlled by their callers, event
// triggers, or an on_action. Restrict the check to direct children of a
// namespaced event so an unrelated helper block cannot produce a false hit.
func checkUnsupportedEventFields(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, event := range nodes {
		if event.Kind != "block" || !strings.Contains(event.Key, ".") {
			continue
		}
		for _, child := range event.Children {
			if child.Key != "is_triggered_only" {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "unsupported_event_field",
				msg:      fmt.Sprintf("event %q uses unsupported field is_triggered_only; CK3 1.19 stops loading this event at that field", event.Key),
				line:     child.Line,
				col:      child.Col,
			})
		}
	}
	return out
}

// On-action fields are a closed engine contract. events/on_actions/random
// blocks are appendable, while trigger and effect are single blocks and cannot
// be declared twice for one named on_action. random_on_action is retained
// because it is present in the current vanilla 1.19 files even though older
// _on_actions.info text documents random_on_actions instead.
var onActionRuntimeFields = map[string]bool{
	"trigger":               true,
	"weight_multiplier":     true,
	"events":                true,
	"random_events":         true,
	"first_valid":           true,
	"on_actions":            true,
	"random_on_actions":     true,
	"random_on_action":      true,
	"first_valid_on_action": true,
	"effect":                true,
	"fallback":              true,
}

func checkOnActionRuntimeFields(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, action := range nodes {
		if action.Kind != "block" || action.Key == "" {
			continue
		}
		counts := map[string]int{}
		for _, child := range action.Children {
			if child.Key == "" {
				continue
			}
			counts[child.Key]++
			if !onActionRuntimeFields[child.Key] {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "illegal_field_context",
					msg:      fmt.Sprintf("on_action %q has unknown top-level field %q; valid CK3 on_action fields are trigger, weight_multiplier, events, random_events, first_valid, on_actions, random_on_action(s), first_valid_on_action, effect, and fallback", action.Key, child.Key),
					line:     child.Line,
					col:      child.Col,
				})
			}
		}
		for _, field := range []string{"trigger", "effect"} {
			if counts[field] > 1 {
				for _, child := range action.Children {
					if child.Key != field {
						continue
					}
					out = append(out, ctxDiag{
						severity: "error",
						code:     "duplicate_on_action_field",
						msg:      fmt.Sprintf("on_action %q declares %q %d times; CK3 permits only one %q block per named on_action", action.Key, field, counts[field], field),
						line:     child.Line,
						col:      child.Col,
					})
				}
			}
		}
	}
	return out
}

// A few government fields are valid only on the enclosing government object.
// In particular, court_generate_commanders is not one of the enum-bitmask
// members inside government_rules. The rule is explicit because the .info
// document is intentionally not exhaustive for government extensions.
var governmentTopLevelOnlyFields = map[string]bool{
	"court_generate_commanders": true,
}

func checkGovernmentRuntimeFields(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, government := range nodes {
		if government.Kind != "block" || government.Key == "" {
			continue
		}
		for _, child := range government.Children {
			if child.Key != "government_rules" || child.Kind != "block" {
				continue
			}
			for _, rule := range child.Children {
				if !governmentTopLevelOnlyFields[rule.Key] {
					continue
				}
				out = append(out, ctxDiag{
					severity: "error",
					code:     "invalid_government_rule_context",
					msg:      fmt.Sprintf("government field %q must be placed directly in government %q, not inside government_rules", rule.Key, government.Key),
					line:     rule.Line,
					col:      rule.Col,
				})
			}
		}
	}
	return out
}

// ai_score and ai_score_mult are inline script values. The current CK3
// formula contract initializes them with value = ..., then applies operations
// such as add/multiply. base is a scripted-modifier field, not a script-value
// operation, and is therefore an illegal child in these CB blocks.
func checkCasusBelliScriptValueFields(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, cb := range nodes {
		if cb.Kind != "block" || cb.Key == "" {
			continue
		}
		for _, child := range cb.Children {
			if (child.Key != "ai_score" && child.Key != "ai_score_mult") || child.Kind != "block" {
				continue
			}
			for _, field := range child.Children {
				if field.Key != "base" {
					continue
				}
				out = append(out, ctxDiag{
					severity: "error",
					code:     "invalid_script_value_field",
					msg:      fmt.Sprintf("casus belli %q %s uses illegal script-value field base; use value = ... to initialize the score, then add/multiply operations", cb.Key, child.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
		}
	}
	return out
}

var modifierDefinitionFields = map[string]bool{
	"decimals":           true,
	"color":              true,
	"prefix":             true,
	"suffix":             true,
	"negative_suffix":    true,
	"percent":            true,
	"already_percent":    true,
	"hidden":             true,
	"no_difference_sign": true,
	"dlc_feature":        true,
}

func checkModifierDefinitionFields(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		if !LookupModifier(definition.Key).Found {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "unknown_modifier_definition",
				msg:      fmt.Sprintf("modifier definition %q is not a current CK3 modifier tag; common/modifier_definition_formats cannot create new modifier types", definition.Key),
				line:     definition.Line,
				col:      definition.Col,
			})
		}
		for _, field := range definition.Children {
			if modifierDefinitionFields[field.Key] {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "illegal_field_context",
				msg:      fmt.Sprintf("modifier definition %q has illegal field %q; CK3 accepts decimals, color, prefix, suffix, negative_suffix, percent, already_percent, hidden, no_difference_sign, and dlc_feature here", definition.Key, field.Key),
				line:     field.Line,
				col:      field.Col,
			})
		}
	}
	return out
}

// These are modifier containers observed in CK3 1.19 object definitions. The
// area is the object that receives the modifier, not necessarily the condition
// used to select it (for example, province_culture_modifier still applies to a
// province). Selector/display metadata is handled separately below.
var modifierContainerAreas = map[string][]string{
	"character_modifier":                      {"character"},
	"top_liege_character_modifier":            {"character"},
	"doctrine_character_modifier":             {"character"},
	"province_modifier":                       {"province"},
	"county_modifier":                         {"county"},
	"culture_modifier":                        {"culture"},
	"religion_modifier":                       {"religion"},
	"travel_plan_modifier":                    {"travel_plan"},
	"scheme_modifier":                         {"scheme"},
	"character_culture_modifier":              {"character"},
	"character_faith_modifier":                {"character"},
	"character_dynasty_modifier":              {"character"},
	"character_situation_modifier":            {"character"},
	"character_government_modifier":           {"character"},
	"province_culture_modifier":               {"province"},
	"province_faith_modifier":                 {"province"},
	"province_terrain_modifier":               {"province"},
	"province_dynasty_modifier":               {"province"},
	"province_situation_modifier":             {"province"},
	"province_government_modifier":            {"province"},
	"county_culture_modifier":                 {"county"},
	"county_faith_modifier":                   {"county"},
	"county_dynasty_modifier":                 {"county"},
	"county_situation_modifier":               {"county"},
	"county_holding_modifier":                 {"county"},
	"county_holder_character_modifier":        {"character"},
	"duchy_capital_county_modifier":           {"county"},
	"duchy_capital_county_situation_modifier": {"county"},
	"duchy_capital_county_culture_modifier":   {"county"},
	"duchy_capital_county_faith_modifier":     {"county"},
}

// These direct children are selector/display metadata, not numeric modifier
// tags. They are documented in vanilla _buildings.info and in the relevant
// doctrine/dynasty/scheme examples.
var modifierContainerMetadata = map[string]map[string]bool{
	"character_modifier":                      {"name": true},
	"top_liege_character_modifier":            {"name": true},
	"doctrine_character_modifier":             {"name": true, "doctrine": true},
	"province_modifier":                       {"name": true},
	"county_modifier":                         {"name": true},
	"culture_modifier":                        {"name": true},
	"religion_modifier":                       {"name": true},
	"scheme_modifier":                         {"object": true, "target": true},
	"character_culture_modifier":              {"name": true, "parameter": true},
	"character_faith_modifier":                {"name": true, "parameter": true},
	"character_dynasty_modifier":              {"name": true, "county_holder_dynasty_perk": true},
	"character_situation_modifier":            {"name": true, "parameter": true},
	"character_government_modifier":           {"name": true, "parameter": true},
	"province_culture_modifier":               {"name": true, "parameter": true},
	"province_faith_modifier":                 {"name": true, "parameter": true},
	"province_terrain_modifier":               {"name": true, "parameter": true, "terrain": true, "is_coastal": true, "is_riverside": true},
	"province_dynasty_modifier":               {"name": true, "county_holder_dynasty_perk": true},
	"province_situation_modifier":             {"name": true, "parameter": true},
	"province_government_modifier":            {"name": true, "parameter": true},
	"county_culture_modifier":                 {"name": true, "parameter": true},
	"county_faith_modifier":                   {"name": true, "parameter": true},
	"county_dynasty_modifier":                 {"name": true, "county_holder_dynasty_perk": true},
	"county_situation_modifier":               {"name": true, "parameter": true},
	"county_holding_modifier":                 {"name": true, "holding": true},
	"county_holder_character_modifier":        {"name": true},
	"duchy_capital_county_modifier":           {"name": true},
	"duchy_capital_county_situation_modifier": {"name": true, "parameter": true},
	"duchy_capital_county_culture_modifier":   {"name": true, "parameter": true},
	"duchy_capital_county_faith_modifier":     {"name": true, "parameter": true},
}

func checkModifierContainerFields(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	rel := strings.ToLower(filepathSlash(relPath))
	// common/game_concepts contains display concepts named
	// character_modifier/county_modifier. Their texture and parent fields are
	// game-concept metadata, not numeric modifier containers.
	if strings.HasPrefix(rel, "common/game_concepts/") {
		return nil
	}
	walk(nodes, func(node *script.Node) {
		areas, ok := modifierContainerAreas[node.Key]
		if !ok || node.Kind != "block" {
			return
		}
		if node.Key == "culture_modifier" &&
			(strings.HasPrefix(rel, "common/traits/") || strings.HasPrefix(rel, "common/court_positions/")) {
			// In these modules culture_modifier selects a culture parameter but
			// applies the numeric modifier entries to the owning character.
			areas = []string{"character"}
		}
		for _, field := range node.Children {
			if field.Key == "" || field.Kind == "block" {
				continue
			}
			if modifierContainerMetadata[node.Key][field.Key] {
				continue
			}
			if node.Key == "culture_modifier" && field.Key == "parameter" &&
				(strings.HasPrefix(rel, "common/traits/") || strings.HasPrefix(rel, "common/court_positions/")) {
				continue
			}
			if node.Key == "county_modifier" && field.Key == "scale" &&
				strings.HasPrefix(rel, "common/council_tasks/") {
				continue
			}
			modifier := LookupModifier(field.Key)
			if !modifier.Found {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "unknown_modifier_field",
					msg:      fmt.Sprintf("modifier field %q is not a current CK3 modifier tag in %s; check the CK3 1.19 modifiers contract", field.Key, node.Key),
					line:     field.Line,
					col:      field.Col,
				})
				continue
			}
			if len(modifier.UseAreas) == 0 || modifierUseAreaAllowed(modifier.UseAreas, areas) {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "invalid_modifier_context",
				msg:      fmt.Sprintf("modifier field %q is valid for %s but not for %s; move it to a compatible modifier container", field.Key, strings.Join(modifier.UseAreas, "/"), node.Key),
				line:     field.Line,
				col:      field.Col,
			})
		}
	})
	if strings.Contains(relPath, "common/buildings/") {
		out = append(out, checkIllegalBuildingModifierContainers(nodes)...)
	}
	return out
}

func modifierUseAreaAllowed(actual, allowed []string) bool {
	for _, want := range allowed {
		for _, got := range actual {
			if strings.EqualFold(got, want) {
				return true
			}
		}
	}
	return false
}

// country_modifier appears in older CK3 building examples but is not a CK3
// 1.19 building modifier container. Keep this path-specific so a mod-defined
// script key with the same name elsewhere is not treated as a field error.
func checkIllegalBuildingModifierContainers(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	walk(nodes, func(node *script.Node) {
		if node.Key != "country_modifier" || node.Kind != "block" {
			return
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "illegal_modifier_container",
			msg:      "country_modifier is not a valid CK3 1.19 building modifier container; use character_modifier, province_modifier, county_modifier, or a documented culture modifier container",
			line:     node.Line,
			col:      node.Col,
		})
	})
	return out
}
