package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// Succession fields are accepted by the generic parser but several of them
// are conditional on order_of_succession. These relations are explicit in
// common/laws/_laws.info and are stable across the vanilla law examples.
func checkLawSuccessionContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, law := range nodes {
		if law.Kind != "block" || law.Key == "" {
			continue
		}
		succession := directBlock(law, "succession")
		if succession == nil {
			continue
		}
		order := directAtom(succession, "order_of_succession")
		orderValue := atomValue(order)
		titleDivision := directAtom(succession, "title_division")
		titleDivisionValue := atomValue(titleDivision)
		traversal := directAtom(succession, "traversal_order")
		traversalValue := atomValue(traversal)

		if titleDivision != nil && order != nil && !containsString([]string{"inheritance", "noble_family"}, orderValue) {
			out = append(out, lawSuccessionDiag(law, titleDivision, fmt.Sprintf("law %q uses title_division with order_of_succession = %s; title_division requires inheritance or noble_family", law.Key, orderValue)))
		}
		if titleDivisionValue == "partition" && traversal != nil && traversalValue != "children" {
			out = append(out, lawSuccessionDiag(law, traversal, fmt.Sprintf("law %q uses title_division = partition with traversal_order = %s; partition requires traversal_order = children", law.Key, traversalValue)))
		}
		if traversalValue == "children" && titleDivision != nil && titleDivisionValue != "partition" {
			out = append(out, lawSuccessionDiag(law, titleDivision, fmt.Sprintf("law %q uses traversal_order = children with title_division = %s; children requires title_division = partition", law.Key, titleDivisionValue)))
		}
		if titleDivisionValue == "single_heir" && order != nil && !containsString([]string{"inheritance", "noble_family"}, orderValue) {
			out = append(out, lawSuccessionDiag(law, titleDivision, fmt.Sprintf("law %q uses title_division = single_heir with order_of_succession = %s; single_heir requires inheritance or noble_family", law.Key, orderValue)))
		}
		if orderValue == "appointment" {
			for _, field := range []*script.Node{traversal, titleDivision, directAtom(succession, "rank")} {
				if field == nil {
					continue
				}
				out = append(out, lawSuccessionDiag(law, field, fmt.Sprintf("law %q uses order_of_succession = appointment with %s; appointment succession requires traversal, division, and rank to be undefined", law.Key, field.Key)))
			}
		}
		if field := directAtom(succession, "pool_character_config"); field != nil && order != nil && !containsString([]string{"theocratic", "company", "generate"}, orderValue) {
			out = append(out, lawSuccessionDiag(law, field, fmt.Sprintf("law %q uses pool_character_config with order_of_succession = %s; this field is only valid for theocratic, company, or generate", law.Key, orderValue)))
		}
		if field := directAtom(succession, "election_type"); field != nil && order != nil && orderValue != "election" {
			out = append(out, lawSuccessionDiag(law, field, fmt.Sprintf("law %q uses election_type with order_of_succession = %s; election_type requires election", law.Key, orderValue)))
		}
		if field := directAtom(succession, "appointment_type"); field != nil && order != nil && orderValue != "appointment" {
			out = append(out, lawSuccessionDiag(law, field, fmt.Sprintf("law %q uses appointment_type with order_of_succession = %s; appointment_type requires appointment", law.Key, orderValue)))
		}
	}
	return out
}

func atomValue(node *script.Node) string {
	if node == nil || node.Kind != "atom" {
		return ""
	}
	return strings.Trim(strings.TrimSpace(node.Value), `"`)
}

func lawSuccessionDiag(law, field *script.Node, message string) ctxDiag {
	return ctxDiag{
		severity: "error",
		code:     "law_succession_field_context",
		msg:      message,
		line:     field.Line,
		col:      field.Col,
	}
}
