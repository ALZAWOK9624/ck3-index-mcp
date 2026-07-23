package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// Council task clones may only redefine the position. The clone target must
// be a filled task; the cross-object reference is handled by the normal
// resolver, while this function enforces the local field grammar.
func checkCouncilTaskContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, task := range nodes {
		if task.Kind != "block" || task.Key == "" {
			continue
		}
		clone := directAtom(task, "clone")
		if clone != nil {
			if directAtom(task, "position") == nil {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "council_task_clone_context",
					msg:      fmt.Sprintf("council task %q clones %q but does not redefine position; CK3 clone tasks must redefine the court position", task.Key, strings.TrimSpace(clone.Value)),
					line:     clone.Line,
					col:      clone.Col,
				})
			}
			for _, field := range task.Children {
				if field.Key == "clone" || field.Key == "position" {
					continue
				}
				out = append(out, ctxDiag{
					severity: "error",
					code:     "council_task_clone_context",
					msg:      fmt.Sprintf("council task %q clones %q but redefines %s; a clone may redefine only position", task.Key, strings.TrimSpace(clone.Value), field.Key),
					line:     field.Line,
					col:      field.Col,
				})
			}
			continue
		}

		taskType := atomValue(directAtom(task, "task_type"))
		if field := directAtom(task, "county_target"); field != nil && taskType != "" && taskType != "task_type_county" {
			out = append(out, councilTaskFieldDiag(task, field, fmt.Sprintf("council task %q uses county_target with task_type = %s; county_target is only valid for task_type_county", task.Key, taskType)))
		}
		if field := directAtom(task, "ai_county_target"); field != nil && taskType != "" && taskType != "task_type_county" {
			out = append(out, councilTaskFieldDiag(task, field, fmt.Sprintf("council task %q uses ai_county_target with task_type = %s; ai_county_target is only valid for task_type_county", task.Key, taskType)))
		}
		progress := atomValue(directAtom(task, "task_progress"))
		if progress != "" && progress != "task_progress_value" {
			for _, field := range []*script.Node{directAtom(task, "task_current_value"), directAtom(task, "task_max_value")} {
				if field == nil {
					continue
				}
				out = append(out, councilTaskFieldDiag(task, field, fmt.Sprintf("council task %q defines %s with task_progress = %s; current/max value fields require task_progress_value", task.Key, field.Key, progress)))
			}
		}
		if defaultTask := directAtom(task, "default_task"); defaultTask != nil && strings.EqualFold(atomValue(defaultTask), "yes") {
			if taskType != "" && taskType != "task_type_general" {
				out = append(out, councilTaskFieldDiag(task, defaultTask, fmt.Sprintf("council task %q sets default_task = yes with task_type = %s; default_task is only applicable to task_type_general", task.Key, taskType)))
			}
			if progress != "" && progress != "task_progress_infinite" {
				out = append(out, councilTaskFieldDiag(task, defaultTask, fmt.Sprintf("council task %q sets default_task = yes with task_progress = %s; default_task requires task_progress_infinite", task.Key, progress)))
			}
		}
	}
	return out
}

func councilTaskFieldDiag(task, field *script.Node, message string) ctxDiag {
	return ctxDiag{severity: "error", code: "council_task_field_context", msg: message, line: field.Line, col: field.Col}
}

func checkHouseRelationContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, relation := range nodes {
		if relation.Kind != "block" || relation.Key == "" {
			continue
		}
		levels := directBlock(relation, "levels")
		if levels != nil && countBlocks(levels.Children) > 0 {
			continue
		}
		line, col := relation.Line, relation.Col
		if levels != nil {
			line, col = levels.Line, levels.Col
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "house_relation_missing_level",
			msg:      fmt.Sprintf("house relation %q has no level in levels; CK3 requires at least one relationship level", relation.Key),
			line:     line,
			col:      col,
		})
	}
	return out
}

// A domicile flavourization must identify which domicile database entry it
// matches. Other conditional flavourization fields are intentionally not
// rejected here because vanilla title-holder files use some documented
// "only applies" fields in broader contexts.
func checkFlavorizationContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, flavourization := range nodes {
		if flavourization.Kind != "block" || flavourization.Key == "" {
			continue
		}
		if atomValue(directAtom(flavourization, "type")) != "domicile" || hasDirectChild(flavourization, "domicile_type") {
			continue
		}
		out = append(out, ctxDiag{
			severity: "error",
			code:     "flavorization_missing_domicile_type",
			msg:      fmt.Sprintf("flavourization %q uses type = domicile without domicile_type; the CK3 contract requires a domicile database key", flavourization.Key),
			line:     flavourization.Line,
			col:      flavourization.Col,
		})
	}
	return out
}
