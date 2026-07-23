package indexer

import (
	"fmt"
	"math"
	"strings"

	"ck3-index/internal/script"
)

var ck3RuntimeTitleTiers = []string{
	"barony",
	"county",
	"duchy",
	"kingdom",
	"empire",
	"hegemony",
}

// These validators cover catalog modules whose _*.info files describe a
// loader invariant rather than a general-purpose script grammar. They only
// judge literal values when a scripted value could legitimately be resolved
// by the engine at runtime.

func checkAccoladeNameContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		optionCount := 0
		for _, child := range definition.Children {
			if child.Kind == "block" && child.Key == "option" {
				optionCount++
			}
		}
		numOptions := directAtom(definition, "num_options")
		if numOptions == nil {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "accolade_name_option_count",
				msg:      fmt.Sprintf("accolade name %q must define num_options equal to its %d option block(s)", definition.Key, optionCount),
				line:     definition.Line,
				col:      definition.Col,
			})
			continue
		}
		expected, ok := literalNumber(numOptions)
		if !ok {
			continue
		}
		if expected < 0 || math.Trunc(expected) != expected || int(expected) != optionCount {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "accolade_name_option_count",
				msg:      fmt.Sprintf("accolade name %q declares num_options = %s but contains %d option block(s); CK3 requires an exact match", definition.Key, atomValue(numOptions), optionCount),
				line:     numOptions.Line,
				col:      numOptions.Col,
			})
		}
	}
	return out
}

func checkCultureEraContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		year := directAtom(definition, "year")
		if year == nil {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "culture_era_year",
				msg:      fmt.Sprintf("culture era %q has no year; CK3 requires a year of 0 or greater", definition.Key),
				line:     definition.Line,
				col:      definition.Col,
			})
			continue
		}
		if value, ok := literalNumber(year); ok && value < 0 {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "culture_era_year",
				msg:      fmt.Sprintf("culture era %q has year = %s; CK3 requires a year of 0 or greater", definition.Key, atomValue(year)),
				line:     year.Line,
				col:      year.Col,
			})
		}
	}
	return out
}

var aiWarStanceBehaviourFields = map[string]bool{
	"stronger":  true,
	"weaker":    true,
	"desperate": true,
}

var aiWarStanceObjectiveNames = map[string]bool{
	"wargoal_province":        true,
	"enemy_unit_province":     true,
	"enemy_capital_province":  true,
	"capital_province":        true,
	"enemy_province":          true,
	"enemy_ally_province":     true,
	"province":                true,
	"defend_wargoal_province": true,
}

var aiWarStanceObjectiveAreas = map[string]bool{
	"wargoal":               true,
	"primary_attacker":      true,
	"primary_attacker_ally": true,
	"primary_defender":      true,
	"primary_defender_ally": true,
}

func checkAIWarStanceContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, stance := range nodes {
		if stance.Kind != "block" || stance.Key == "" {
			continue
		}
		side := directAtom(stance, "side")
		if side == nil || !containsString([]string{"attacker", "defender"}, atomValue(side)) {
			line, col := stance.Line, stance.Col
			if side != nil {
				line, col = side.Line, side.Col
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "ai_war_stance_side",
				msg:      fmt.Sprintf("war stance %q must set side to attacker or defender", stance.Key),
				line:     line,
				col:      col,
			})
		}

		behaviour := directBlock(stance, "behaviour_attributes")
		if behaviour == nil {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "ai_war_stance_behaviour_attribute",
				msg:      fmt.Sprintf("war stance %q must define behaviour_attributes with at least one of stronger, weaker, or desperate = yes", stance.Key),
				line:     stance.Line,
				col:      stance.Col,
			})
		} else {
			hasYes := false
			hasDynamic := false
			for _, field := range behaviour.Children {
				if !aiWarStanceBehaviourFields[field.Key] {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "ai_war_stance_behaviour_field",
						msg:      fmt.Sprintf("war stance %q has illegal behaviour_attributes field %q; valid fields are stronger, weaker, and desperate", stance.Key, field.Key),
						line:     field.Line,
						col:      field.Col,
					})
					continue
				}
				value := atomValue(field)
				if value == "yes" {
					hasYes = true
				} else if value != "no" {
					hasDynamic = true
				}
			}
			if !hasYes && !hasDynamic {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "ai_war_stance_behaviour_attribute",
					msg:      fmt.Sprintf("war stance %q has no behaviour attribute set to yes; at least one of stronger, weaker, or desperate must be enabled", stance.Key),
					line:     behaviour.Line,
					col:      behaviour.Col,
				})
			}
		}

		usedAreas := map[string]*script.Node{}
		for _, field := range stance.Children {
			if field.Key == "enemy_unit_priority" {
				checkAIWarStancePriority(&out, stance, field, "enemy unit priority")
				continue
			}
			if field.Key != "objectives" || field.Kind != "block" {
				continue
			}
			for _, objective := range field.Children {
				if !aiWarStanceObjectiveNames[objective.Key] {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "ai_war_stance_objective_field",
						msg:      fmt.Sprintf("war stance %q has illegal objective %q; use a CK3 war-coordinator objective name", stance.Key, objective.Key),
						line:     objective.Line,
						col:      objective.Col,
					})
					continue
				}
				if objective.Kind != "block" {
					if objective.Kind == "atom" {
						checkAIWarStancePriority(&out, stance, objective, "objective")
					}
					continue
				}
				if objective.Key != "enemy_unit_province" {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "ai_war_stance_objective_context",
						msg:      fmt.Sprintf("war stance %q uses object-style objective %q; only enemy_unit_province accepts a block with priority and area", stance.Key, objective.Key),
						line:     objective.Line,
						col:      objective.Col,
					})
					continue
				}
				hasPriority := false
				for _, child := range objective.Children {
					switch child.Key {
					case "priority":
						hasPriority = true
						checkAIWarStancePriority(&out, stance, child, "enemy unit objective")
					case "area":
						area := atomValue(child)
						if !aiWarStanceObjectiveAreas[area] {
							out = append(out, ctxDiag{
								severity: "error",
								code:     "ai_war_stance_area_enum",
								msg:      fmt.Sprintf("war stance %q uses invalid enemy_unit_province area %q", stance.Key, area),
								line:     child.Line,
								col:      child.Col,
							})
							continue
						}
						if previous := usedAreas[area]; previous != nil {
							out = append(out, ctxDiag{
								severity: "error",
								code:     "ai_war_stance_area_overlap",
								msg:      fmt.Sprintf("war stance %q repeats enemy_unit_province area %q; war-stances cannot overlap areas across objectives blocks", stance.Key, area),
								line:     child.Line,
								col:      child.Col,
							})
						} else {
							usedAreas[area] = child
						}
					default:
						out = append(out, ctxDiag{
							severity: "error",
							code:     "ai_war_stance_objective_field",
							msg:      fmt.Sprintf("war stance %q has illegal field %q inside enemy_unit_province; valid fields are priority and area", stance.Key, child.Key),
							line:     child.Line,
							col:      child.Col,
						})
					}
				}
				if !hasPriority {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "ai_war_stance_objective_priority",
						msg:      fmt.Sprintf("war stance %q has an enemy_unit_province objective without priority", stance.Key),
						line:     objective.Line,
						col:      objective.Col,
					})
				}
			}
		}
	}
	return out
}

func checkAIWarStancePriority(out *[]ctxDiag, stance, field *script.Node, label string) {
	if value, ok := literalNumber(field); ok && (value < 0 || value > 1000 || math.Trunc(value) != value) {
		*out = append(*out, ctxDiag{
			severity: "error",
			code:     "ai_war_stance_objective_priority",
			msg:      fmt.Sprintf("war stance %q has %s priority = %s; CK3 requires an integer between 0 and 1000", stance.Key, label, atomValue(field)),
			line:     field.Line,
			col:      field.Col,
		})
	}
}

func checkHouseUnityContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, unity := range nodes {
		if unity.Kind != "block" || unity.Key == "" {
			continue
		}
		for _, stage := range unity.Children {
			if stage.Kind != "block" {
				continue
			}
			points := directAtom(stage, "points")
			if points == nil {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "house_unity_stage_points",
					msg:      fmt.Sprintf("house unity %q stage %q has no points; CK3 ignores stages whose points are not greater than zero", unity.Key, stage.Key),
					line:     stage.Line,
					col:      stage.Col,
				})
				continue
			}
			if value, ok := literalNumber(points); ok && (value <= 0 || math.Trunc(value) != value) {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "house_unity_stage_points",
					msg:      fmt.Sprintf("house unity %q stage %q has points = %s; CK3 requires a positive integer", unity.Key, stage.Key, atomValue(points)),
					line:     points.Line,
					col:      points.Col,
				})
			}
		}
	}
	return out
}

var storyCycleDurationFields = []string{"days", "weeks", "months", "years"}

func checkStoryCycleContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, story := range nodes {
		if story.Kind != "block" || story.Key == "" {
			continue
		}
		for _, group := range story.Children {
			if group.Kind != "block" || group.Key != "effect_group" {
				continue
			}
			durationCount := 0
			for _, key := range storyCycleDurationFields {
				if hasDirectChild(group, key) {
					durationCount++
				}
			}
			if durationCount == 0 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "story_cycle_duration_missing",
					msg:      fmt.Sprintf("story cycle %q effect_group has no days, weeks, months, or years duration", story.Key),
					line:     group.Line,
					col:      group.Col,
				})
			} else if durationCount > 1 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "story_cycle_duration_conflict",
					msg:      fmt.Sprintf("story cycle %q effect_group defines more than one duration unit; use only one of days, weeks, months, or years", story.Key),
					line:     group.Line,
					col:      group.Col,
				})
			}
			if chance := directAtom(group, "chance"); chance != nil {
				if value, ok := literalNumber(chance); ok && (value < 0 || value > 100) {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "story_cycle_chance_range",
						msg:      fmt.Sprintf("story cycle %q effect_group has chance = %s; CK3 requires a value in the inclusive range 0..100", story.Key, atomValue(chance)),
						line:     chance.Line,
						col:      chance.Col,
					})
				}
			}
			walk([]*script.Node{group}, func(node *script.Node) {
				if node.Kind != "block" || node.Key != "triggered_effect" {
					return
				}
				if !hasDirectChild(node, "effect") {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "story_cycle_triggered_effect_shape",
						msg:      fmt.Sprintf("story cycle %q triggered_effect must contain an effect block; trigger is optional and may inherit the effect_group trigger", story.Key),
						line:     node.Line,
						col:      node.Col,
					})
				}
			})
		}
	}
	return out
}

func checkRequiredTierFields(nodes []*script.Node, field, code, context string) []ctxDiag {
	var out []ctxDiag
	walk(nodes, func(node *script.Node) {
		if node.Kind != "block" || node.Key != field {
			return
		}
		for _, tier := range ck3RuntimeTitleTiers {
			if hasDirectChild(node, tier) {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     code,
				msg:      fmt.Sprintf("%s %q omits required %s entry from %s; CK3 requires barony, county, duchy, kingdom, empire, and hegemony", context, node.Key, tier, field),
				line:     node.Line,
				col:      node.Col,
			})
		}
	})
	return out
}

func checkDecisionContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	out = append(out, checkRequiredTierFields(nodes, "ai_check_interval_by_tier", "decision_ai_tier_missing", "decision")...)
	for _, decision := range nodes {
		if decision.Kind != "block" || decision.Key == "" {
			continue
		}
		pictureFound := false
		for _, child := range decision.Children {
			if child.Key == "picture" && child.Kind == "block" && decisionPictureHasReference(child) {
				pictureFound = true
				break
			}
		}
		if !pictureFound {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "decision_picture_missing",
				msg:      fmt.Sprintf("decision %q has no picture entry with a reference; CK3 requires at least one decision picture", decision.Key),
				line:     decision.Line,
				col:      decision.Col,
			})
		}
		if hasDirectYesChild(decision, "ai_goal") {
			continue
		}
		if hasDirectChild(decision, "ai_check_interval") || hasDirectChild(decision, "ai_check_interval_by_tier") {
			continue
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "decision_ai_interval_missing",
			msg:      fmt.Sprintf("decision %q has neither ai_check_interval nor ai_check_interval_by_tier and is not ai_goal = yes", decision.Key),
			line:     decision.Line,
			col:      decision.Col,
		})
	}
	return out
}

func decisionPictureHasReference(picture *script.Node) bool {
	if picture == nil {
		return false
	}
	for _, child := range picture.Children {
		if child.Key == "reference" && child.Kind == "atom" && strings.TrimSpace(child.Value) != "" {
			return true
		}
		if decisionPictureHasReference(child) {
			return true
		}
	}
	return false
}

func checkActivityCatalogContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	out = append(out, checkRequiredTierFields(nodes, "ai_check_interval_by_tier", "activity_ai_tier_missing", "activity")...)
	for _, activity := range nodes {
		if activity.Kind != "block" || activity.Key == "" {
			continue
		}
		for _, role := range []string{"host_intents", "guest_intents"} {
			intentGroup := directBlock(activity, role)
			if intentGroup == nil {
				continue
			}
			intents := directBlock(intentGroup, "intents")
			allowed := map[string]bool{}
			if intents != nil {
				for _, intent := range intents.Children {
					if intent.Key != "" {
						allowed[runtimeContractListName(intent)] = true
					}
				}
			}
			if defaultNode := directAtom(intentGroup, "default"); defaultNode != nil && !allowed[atomValue(defaultNode)] {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "activity_intent_default_invalid",
					msg:      fmt.Sprintf("activity %q %s default %q is not listed in intents", activity.Key, role, atomValue(defaultNode)),
					line:     defaultNode.Line,
					col:      defaultNode.Col,
				})
			}
			if playerDefaults := directBlock(intentGroup, "player_defaults"); playerDefaults != nil {
				for _, defaultNode := range playerDefaults.Children {
					if defaultNode.Key == "" || allowed[runtimeContractListName(defaultNode)] {
						continue
					}
					out = append(out, ctxDiag{
						severity: "error",
						code:     "activity_intent_default_invalid",
						msg:      fmt.Sprintf("activity %q %s player_defaults contains %q not listed in intents", activity.Key, role, runtimeContractListName(defaultNode)),
						line:     defaultNode.Line,
						col:      defaultNode.Col,
					})
				}
			}
		}
	}
	return out
}

func runtimeContractListName(node *script.Node) string {
	if node == nil {
		return ""
	}
	if node.Kind == "atom" {
		return atomValue(node)
	}
	return strings.Trim(strings.TrimSpace(node.Key), `"`)
}

func checkInteractionCatalogContracts(nodes []*script.Node) []ctxDiag {
	return checkRequiredTierFields(nodes, "ai_frequency_by_tier", "interaction_ai_tier_missing", "character interaction")
}

func checkGreatProjectCatalogContracts(nodes []*script.Node) []ctxDiag {
	return checkRequiredTierFields(nodes, "ai_check_interval_by_tier", "great_project_ai_tier_missing", "great project")
}
