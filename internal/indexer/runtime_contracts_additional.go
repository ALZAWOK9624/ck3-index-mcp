package indexer

import (
	"fmt"
	"strconv"

	"ck3-index/internal/script"
)

var nameListMaleProbabilityFields = []string{
	"pat_grf_name_chance",
	"mat_grf_name_chance",
	"father_name_chance",
}

var nameListFemaleProbabilityFields = []string{
	"pat_grm_name_chance",
	"mat_grm_name_chance",
	"mother_name_chance",
}

// Name-list ancestry probabilities are independent choices but their total
// must not exceed 100. Only literal numbers are checked; scripted or define
// driven values need runtime evaluation and are intentionally left alone.
func checkNameListProbabilityContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, definition := range nodes {
		if definition.Kind != "block" || definition.Key == "" {
			continue
		}
		for _, group := range []struct {
			name   string
			fields []string
		}{
			{name: "male", fields: nameListMaleProbabilityFields},
			{name: "female", fields: nameListFemaleProbabilityFields},
		} {
			sum := 0.0
			last := (*script.Node)(nil)
			seen := false
			for _, field := range definition.Children {
				if !containsString(group.fields, field.Key) {
					continue
				}
				value, err := strconv.ParseFloat(field.Value, 64)
				if err != nil {
					continue
				}
				sum += value
				last = field
				seen = true
			}
			if !seen || sum <= 100 {
				continue
			}
			line, col := definition.Line, definition.Col
			if last != nil {
				line, col = last.Line, last.Col
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "name_list_probability_sum",
				msg:      fmt.Sprintf("name list %q %s ancestry-name probabilities sum to %.2f; the CK3 contract requires a total of at most 100", definition.Key, group.name, sum),
				line:     line,
				col:      col,
			})
		}
	}
	return out
}

// Activity types use named option categories, options, and phases. Duplicate
// names within one definition are loader ambiguity, while an activity without
// any phase has no executable activity flow.
func checkActivityTypeContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, activity := range nodes {
		if activity.Kind != "block" || activity.Key == "" {
			continue
		}
		for _, child := range activity.Children {
			if child.Key == "options" && child.Kind == "block" {
				for _, category := range child.Children {
					if category.Kind != "block" {
						continue
					}
					out = append(out, duplicateNamedChildren(category.Children, "activity_duplicate_option", fmt.Sprintf("activity %q option category", activity.Key))...)
				}
				out = append(out, duplicateNamedChildren(child.Children, "activity_duplicate_category", fmt.Sprintf("activity %q option", activity.Key))...)
			}
			if child.Key != "phases" || child.Kind != "block" {
				continue
			}
			phaseCount := 0
			for _, phase := range child.Children {
				if phase.Kind == "block" {
					phaseCount++
				}
			}
			if phaseCount == 0 {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "activity_missing_phase",
					msg:      fmt.Sprintf("activity type %q defines an empty phases block; CK3 requires at least one active phase", activity.Key),
					line:     child.Line,
					col:      child.Col,
				})
			}
			out = append(out, duplicateNamedChildren(child.Children, "activity_duplicate_phase", fmt.Sprintf("activity %q phase", activity.Key))...)
		}
	}
	return out
}

func duplicateNamedChildren(children []*script.Node, code, context string) []ctxDiag {
	counts := map[string]int{}
	for _, child := range children {
		if child.Kind == "block" && child.Key != "" {
			counts[child.Key]++
		}
	}
	var out []ctxDiag
	for _, child := range children {
		if child.Kind != "block" || counts[child.Key] < 2 {
			continue
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     code,
			msg:      fmt.Sprintf("%s repeats named block %q %d times; CK3 requires unique names in this collection", context, child.Key, counts[child.Key]),
			line:     child.Line,
			col:      child.Col,
		})
	}
	return out
}

// Situation future phases choose either catalyst points or a takeover
// duration. The two fields are explicitly mutually exclusive in the vanilla
// contract and live on the same future-phase block.
func checkSituationTakeoverContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, situation := range nodes {
		if situation.Kind != "block" || situation.Key == "" {
			continue
		}
		phases := directBlock(situation, "phases")
		if phases == nil || countBlocks(phases.Children) == 0 {
			line, col := situation.Line, situation.Col
			if phases != nil {
				line, col = phases.Line, phases.Col
			}
			out = append(out, ctxDiag{
				severity: "error",
				code:     "situation_missing_phase",
				msg:      fmt.Sprintf("situation type %q has no phase in phases; CK3 requires at least one active phase", situation.Key),
				line:     line,
				col:      col,
			})
		}
	}
	walk(nodes, func(node *script.Node) {
		if node.Kind != "block" || !hasDirectChild(node, "takeover_points") || !hasDirectChild(node, "takeover_duration") {
			return
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "situation_takeover_conflict",
			msg:      fmt.Sprintf("situation future phase %q cannot combine takeover_points with takeover_duration", node.Key),
			line:     node.Line,
			col:      node.Col,
		})
	})
	return out
}
