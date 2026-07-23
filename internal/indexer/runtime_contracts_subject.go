package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

var subjectContractContributionFields = map[string]bool{
	"levies": true, "tax": true, "herd": true, "barter_goods": true,
	"min_levies": true, "min_tax": true, "min_herd": true, "min_barter_goods": true,
}

// Subject-contract contribution fields are percentages represented as fixed
// point values in [0,1]. Script math blocks are intentionally left to runtime
// evaluation; only literal values are statically rejected here.
func checkSubjectContractContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, contract := range nodes {
		if contract.Kind != "block" || contract.Key == "" {
			continue
		}
		if display := directAtom(contract, "display_mode"); display != nil {
			value := strings.Trim(strings.TrimSpace(display.Value), `"`)
			if !containsString([]string{"tree", "radiobutton", "checkbox", "hidden"}, value) {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "subject_contract_enum",
					msg:      fmt.Sprintf("subject contract %q display_mode must be tree, radiobutton, checkbox, or hidden, got %s", contract.Key, value),
					line:     display.Line,
					col:      display.Col,
				})
			}
		}
		levels := directBlock(contract, "obligation_levels")
		if levels == nil {
			continue
		}
		for _, level := range levels.Children {
			if level.Kind != "block" {
				continue
			}
			for _, field := range level.Children {
				if !subjectContractContributionFields[field.Key] {
					continue
				}
				value, ok := literalNumber(field)
				if !ok || (value >= 0 && value <= 1) {
					continue
				}
				out = append(out, ctxDiag{
					severity: "error",
					code:     "subject_contract_contribution_range",
					msg:      fmt.Sprintf("subject contract %q obligation level %q field %s must be between 0 and 1, got %s", contract.Key, level.Key, field.Key, strings.TrimSpace(field.Value)),
					line:     field.Line,
					col:      field.Col,
				})
			}
		}
	}
	return out
}
