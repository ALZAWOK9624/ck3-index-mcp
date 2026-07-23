package indexer

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"ck3-index/internal/script"
)

// Traits have two incompatible inheritance grammars. A genetic trait uses
// CK3's active/inactive inheritance model, while inherit_chance and
// both_parent_has_trait_inherit_chance belong to the manual model.
func checkTraitRuntimeContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, trait := range nodes {
		if trait.Kind != "block" || trait.Key == "" {
			continue
		}
		genetic := hasDirectYesChild(trait, "genetic")
		for _, field := range trait.Children {
			if genetic && (field.Key == "inherit_chance" || field.Key == "both_parent_has_trait_inherit_chance") {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "trait_genetic_inheritance_conflict",
					msg:      fmt.Sprintf("trait %q sets genetic = yes together with manual inheritance field %q; CK3 requires one inheritance grammar", trait.Key, field.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
			if field.Kind != "block" {
				continue
			}
			switch field.Key {
			case "triggered_opinion":
				if hasDirectYesChild(field, "male_only") && hasDirectYesChild(field, "female_only") {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "trait_opinion_gender_conflict",
						msg:      fmt.Sprintf("trait %q triggered_opinion cannot set both male_only = yes and female_only = yes", trait.Key),
						line:     field.Line,
						col:      field.Col,
					})
				}
			case "tracks":
				out = append(out, duplicateNamedChildren(field.Children, "trait_track_duplicate_name", fmt.Sprintf("trait %q tracks", trait.Key))...)
				for _, track := range field.Children {
					if track.Kind == "block" {
						out = append(out, checkTraitTrackThresholds(trait.Key, track.Key, track)...)
					}
				}
			case "track":
				out = append(out, checkTraitTrackThresholds(trait.Key, trait.Key, field)...)
			}
		}
	}
	return out
}

func checkTraitTrackThresholds(traitKey, trackKey string, track *script.Node) []ctxDiag {
	var out []ctxDiag
	previous := 0.0
	havePrevious := false
	for _, threshold := range track.Children {
		if threshold.Kind != "block" {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(threshold.Key), 64)
		if err != nil {
			// CK3 also accepts named track levels in a few vanilla traits; the
			// numeric range/order contract cannot safely judge those names.
			continue
		}
		if value < 0 || value > 100 {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "trait_track_xp_range",
				msg:      fmt.Sprintf("trait %q track %q uses XP threshold %.2f; CK3 requires thresholds from 0 through 100", traitKey, trackKey, value),
				line:     threshold.Line,
				col:      threshold.Col,
			})
		}
		if havePrevious && value <= previous {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "trait_track_xp_order",
				msg:      fmt.Sprintf("trait %q track %q has XP threshold %.2f after %.2f; CK3 requires ascending thresholds", traitKey, trackKey, value, previous),
				line:     threshold.Line,
				col:      threshold.Col,
			})
		}
		previous = value
		havePrevious = true
	}
	return out
}

// Innovation assets are optional, but every declared asset needs at least one
// display identity. Without name and icon the engine cannot style the asset.
func checkInnovationAssetContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, innovation := range nodes {
		if innovation.Kind != "block" || innovation.Key == "" {
			continue
		}
		for _, field := range innovation.Children {
			if field.Key != "asset" || field.Kind != "block" {
				continue
			}
			if hasDirectChild(field, "name") || hasDirectChild(field, "icon") {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "innovation_asset_display_missing",
				msg:      fmt.Sprintf("innovation %q asset has neither name nor icon; CK3 requires at least one asset display identity", innovation.Key),
				line:     field.Line,
				col:      field.Col,
			})
		}
	}
	return out
}

func checkEventTransitionContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		for _, field := range definition.Children {
			if field.Key != "transition" || field.Kind != "block" {
				continue
			}
			if duration := directAtom(field, "duration"); duration != nil {
				if value, ok := literalNumber(duration); ok && value <= 0 {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "event_transition_invalid_duration",
						msg:      fmt.Sprintf("event transition %q duration must be greater than 0, got %s", definition.Key, strings.TrimSpace(duration.Value)),
						line:     duration.Line,
						col:      duration.Col,
					})
				}
			}
		}
	}
	return out
}

func checkEvent2DEffectContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		for _, field := range definition.Children {
			if field.Key != "effect_2d" || field.Kind != "block" {
				continue
			}
			if duration := directAtom(field, "duration"); duration != nil {
				if value, ok := literalNumber(duration); ok && value < 0 {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "event_2d_invalid_duration",
						msg:      fmt.Sprintf("event 2D effect %q duration must be at least 0, got %s", definition.Key, strings.TrimSpace(duration.Value)),
						line:     duration.Line,
						col:      duration.Col,
					})
				}
			}
		}
	}
	return out
}

// Event themes require the three core triggered resource groups. Optional
// header backgrounds and transitions are intentionally not required here.
func checkEventThemeContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, theme := range nodes {
		if theme.Kind != "block" || theme.Key == "" {
			continue
		}
		for _, required := range []string{"background", "icon", "sound"} {
			if hasDirectChild(theme, required) {
				continue
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "event_theme_missing_required_field",
				msg:      fmt.Sprintf("event theme %q is missing required field %q; background, icon, and sound are required by the CK3 event-theme contract", theme.Key, required),
				line:     theme.Line,
				col:      theme.Col,
			})
		}
	}
	return out
}

func checkHouseAspirationContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, aspiration := range nodes {
		if aspiration.Kind != "block" || aspiration.Key == "" {
			continue
		}
		if hasDirectChild(aspiration, "level") {
			continue
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "house_aspiration_missing_level",
			msg:      fmt.Sprintf("house aspiration %q has no level block; CK3 requires at least one power level", aspiration.Key),
			line:     aspiration.Line,
			col:      aspiration.Col,
		})
	}
	return out
}

func checkDynastyPerkContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, perk := range nodes {
		if perk.Kind != "block" || perk.Key == "" {
			continue
		}
		for _, field := range perk.Children {
			if field.Key != "traits" || field.Kind != "block" {
				continue
			}
			if len(field.Children) == 0 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "dynasty_perk_trait_chance",
					msg:      fmt.Sprintf("dynasty perk %q has an empty traits block; CK3 requires at least one non-zero trait AI chance", perk.Key),
					line:     field.Line,
					col:      field.Col,
				})
				continue
			}
			allLiteral := true
			hasNonZero := false
			for _, trait := range field.Children {
				value, ok := literalNumber(trait)
				if !ok {
					allLiteral = false
					break
				}
				if value != 0 {
					hasNonZero = true
				}
			}
			if allLiteral && !hasNonZero {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "dynasty_perk_trait_chance",
					msg:      fmt.Sprintf("dynasty perk %q traits block gives every trait a zero AI chance; at least one literal chance must be non-zero", perk.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
		}
	}
	return out
}

func checkStruggleContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, struggle := range nodes {
		if struggle.Kind != "block" || struggle.Key == "" {
			continue
		}
		phaseList := directBlock(struggle, "phase_list")
		if phaseList == nil || countBlocks(phaseList.Children) == 0 {
			line, col := struggle.Line, struggle.Col
			if phaseList != nil {
				line, col = phaseList.Line, phaseList.Col
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "struggle_missing_phase_list",
				msg:      fmt.Sprintf("struggle %q has no phase in phase_list; CK3 requires at least one struggle phase", struggle.Key),
				line:     line,
				col:      col,
			})
		}
		if !hasDirectChild(struggle, "start_phase") {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "struggle_missing_start_phase",
				msg:      fmt.Sprintf("struggle %q has no start_phase; CK3 requires an initial phase", struggle.Key),
				line:     struggle.Line,
				col:      struggle.Col,
			})
		}
		if phaseList == nil {
			continue
		}
		phaseNames := map[string]bool{}
		for _, phase := range phaseList.Children {
			if phase.Kind == "block" && phase.Key != "" {
				phaseNames[phase.Key] = true
			}
		}
		if start := directAtom(struggle, "start_phase"); start != nil {
			startName := atomValue(start)
			if startName != "" && !runtimeContractDynamicName(startName) && !phaseNames[startName] {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "struggle_phase_reference",
					msg:      fmt.Sprintf("struggle %q start_phase references unknown phase %q", struggle.Key, startName),
					line:     start.Line,
					col:      start.Col,
				})
			}
		}
		hasEndingDecision := false
		for _, phase := range phaseList.Children {
			if phase.Kind != "block" {
				continue
			}
			if endingDecisions := directBlock(phase, "ending_decisions"); endingDecisions != nil && len(endingDecisions.Children) > 0 {
				hasEndingDecision = true
			}
			endingPhase := strings.Contains(strings.ToLower(phase.Key), "ending_phase")
			if endingPhase {
				for _, field := range []string{"ending_decisions", "future_phases", "war_effects", "culture_effects", "faith_effects", "other_effects"} {
					if invalid := directBlock(phase, field); invalid != nil {
						out = append(out, ctxDiag{
							severity: "error",
							code:     "struggle_ending_phase_fields",
							msg:      fmt.Sprintf("struggle %q ending phase %q defines %s; ending phases cannot have ending decisions, future phases, or struggle modifiers", struggle.Key, phase.Key, field),
							line:     invalid.Line,
							col:      invalid.Col,
						})
					}
				}
			} else {
				future := directBlock(phase, "future_phases")
				if future == nil || countBlocks(future.Children) == 0 {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "struggle_missing_future_phase",
						msg:      fmt.Sprintf("struggle %q phase %q has no future_phases entry; non-ending phases require at least one next phase", struggle.Key, phase.Key),
						line:     phase.Line,
						col:      phase.Col,
					})
				} else {
					for _, next := range future.Children {
						if next.Kind == "block" && next.Key != "" && !phaseNames[next.Key] {
							out = append(out, ctxDiag{
								severity: "error",
								code:     "struggle_phase_reference",
								msg:      fmt.Sprintf("struggle %q phase %q future_phases references unknown phase %q", struggle.Key, phase.Key, next.Key),
								line:     next.Line,
								col:      next.Col,
							})
						}
					}
				}
			}
			if duration := directBlock(phase, "duration"); duration != nil {
				if points := directAtom(duration, "points"); points != nil {
					if value, ok := literalNumber(points); ok && (value < 1 || math.Trunc(value) != value) {
						out = append(out, ctxDiag{
							severity: "error",
							code:     "struggle_invalid_duration",
							msg:      fmt.Sprintf("struggle %q phase %q has duration points = %s; point-based phases require an integer of at least 1", struggle.Key, phase.Key, atomValue(points)),
							line:     points.Line,
							col:      points.Col,
						})
					}
				}
				for _, unit := range []string{"days", "weeks", "months", "years"} {
					if valueNode := directAtom(duration, unit); valueNode != nil {
						if value, ok := literalNumber(valueNode); ok && value <= 0 {
							out = append(out, ctxDiag{
								severity: "error",
								code:     "struggle_invalid_duration",
								msg:      fmt.Sprintf("struggle %q phase %q has %s = %s; time-based phase durations must be positive", struggle.Key, phase.Key, unit, atomValue(valueNode)),
								line:     valueNode.Line,
								col:      valueNode.Col,
							})
						}
					}
				}
			}
		}
		if countBlocks(phaseList.Children) > 0 && !hasEndingDecision {
			out = append(out, ctxDiag{
				severity: "error",
				code:     "struggle_missing_ending_decision",
				msg:      fmt.Sprintf("struggle %q has phases but no phase with ending_decisions; CK3 requires at least one ending decision", struggle.Key),
				line:     phaseList.Line,
				col:      phaseList.Col,
			})
		}
	}
	return out
}

func runtimeContractDynamicName(value string) bool {
	return strings.Contains(value, "@") || strings.Contains(value, "[") || strings.Contains(value, "scope:") || strings.Contains(value, "var:")
}

func directAtom(node *script.Node, key string) *script.Node {
	for _, child := range node.Children {
		if child.Key == key && child.Kind == "atom" {
			return child
		}
	}
	return nil
}

func directBlock(node *script.Node, key string) *script.Node {
	for _, child := range node.Children {
		if child.Key == key && child.Kind == "block" {
			return child
		}
	}
	return nil
}

func countBlocks(nodes []*script.Node) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == "block" {
			count++
		}
	}
	return count
}

func literalNumber(node *script.Node) (float64, bool) {
	if node == nil || node.Kind != "atom" {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(node.Value), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
