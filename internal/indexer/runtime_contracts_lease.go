package indexer

import (
	"fmt"
	"strings"

	"ck3-index/internal/script"
)

// Lease tax/levy shares are percentages. The parser accepts any number, but
// the lease loader expects literal shares and UI maxima in the inclusive
// range 0..100. Dynamic weights remain runtime-only.
func checkLeaseContractContracts(nodes []*script.Node) []ctxDiag {
	var out []ctxDiag
	for _, lease := range nodes {
		if lease.Kind != "block" || lease.Key == "" {
			continue
		}
		for _, field := range lease.Children {
			if field.Key != "tax" && field.Key != "levy" || field.Kind != "block" {
				continue
			}
			if share := directAtom(field, "lease_liege"); share != nil {
				if value, ok := literalNumber(share); ok && (value < 0 || value > 100) {
					out = append(out, leaseRangeDiag(lease, share, fmt.Sprintf("lease %q %s.lease_liege must be between 0 and 100, got %s", lease.Key, field.Key, strings.TrimSpace(share.Value))))
				}
				if directBlock(lease, "hierarchy") == nil {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "lease_contract_hierarchy_context",
						msg:      fmt.Sprintf("lease %q defines %s.lease_liege without hierarchy; lease_liege requires a hierarchy definition", lease.Key, field.Key),
						line:     share.Line,
						col:      share.Col,
					})
				}
			}
			rest := directBlock(field, "rest")
			if rest == nil {
				if restChoice := directAtom(field, "rest"); restChoice != nil {
					choice := strings.Trim(strings.TrimSpace(restChoice.Value), `"`)
					if choice != "ruler" && choice != "lessee" {
						out = append(out, ctxDiag{
							severity: "error",
							code:     "lease_contract_enum",
							msg:      fmt.Sprintf("lease %q %s.rest must be ruler or lessee, got %s", lease.Key, field.Key, choice),
							line:     restChoice.Line,
							col:      restChoice.Col,
						})
					}
				}
				continue
			}
			if maximum := directAtom(rest, "max"); maximum != nil {
				if value, ok := literalNumber(maximum); ok && (value < 0 || value > 100) {
					out = append(out, leaseRangeDiag(lease, maximum, fmt.Sprintf("lease %q %s.rest.max must be between 0 and 100, got %s", lease.Key, field.Key, strings.TrimSpace(maximum.Value))))
				}
			}
			for _, key := range []string{"beneficiary", "rest"} {
				value := directAtom(rest, key)
				if value == nil {
					continue
				}
				choice := strings.Trim(strings.TrimSpace(value.Value), `"`)
				if choice != "ruler" && choice != "lessee" {
					out = append(out, ctxDiag{
						severity: "error",
						code:     "lease_contract_enum",
						msg:      fmt.Sprintf("lease %q %s.rest.%s must be ruler or lessee, got %s", lease.Key, field.Key, key, choice),
						line:     value.Line,
						col:      value.Col,
					})
				}
			}
		}
		if opinion := directAtom(lease, "hook_strength_max_opinion"); opinion != nil {
			value := strings.Trim(strings.TrimSpace(opinion.Value), `"`)
			if value != "none" && value != "any" && value != "strong" {
				out = append(out, ctxDiag{
					severity: "error",
					code:     "lease_contract_enum",
					msg:      fmt.Sprintf("lease %q hook_strength_max_opinion must be none, any, or strong, got %s", lease.Key, value),
					line:     opinion.Line,
					col:      opinion.Col,
				})
			}
		}
	}
	return out
}

func leaseRangeDiag(lease, field *script.Node, message string) ctxDiag {
	return ctxDiag{severity: "error", code: "lease_contract_value_range", msg: message, line: field.Line, col: field.Col}
}
