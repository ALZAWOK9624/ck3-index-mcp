package mcpserver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Session audit of ~2100 real bot calls found that 53% of every ck3-index tool
// error was one of two argument-bound rejections: a limit above the documented
// maximum, or a max_response_bytes below the documented minimum. Both fields
// are tuning knobs, not semantic inputs -- indexer.LLMOptions.normalizedLimit
// already clamps limit to the same ceiling once the call reaches the handler.
// Rejecting them only cost the caller a round trip and, in the observed logs,
// usually a run of four to seven identical retries before it guessed the bound.
//
// clampableArguments are therefore repaired to the nearest legal value and
// reported back as a notice instead of failing. Every other out-of-range
// argument still errors: clamping something like page would silently answer a
// different question than the one that was asked.
var clampableArguments = map[string]bool{
	"limit":              true,
	"max_response_bytes": true,
}

// argumentAliases are deliberately scoped by tool. A canonical field such as
// id can mean a province, artifact, event, save character, or script object;
// accepting province_id or subject everywhere merely because a schema happens
// to contain id silently changes unrelated requests. Each alias below names
// the exact tool whose semantics make the rewrite equivalent.
var argumentAliases = map[string]map[string]string{
	"map_province_info": {
		"province_id":  "id",
		"subject":      "id",
		"history_year": "year",
	},
	"map_neighbors":           {"history_year": "year"},
	"map_spatial_relation":    {"history_year": "year"},
	"map_title_context":       {"history_year": "year"},
	"map_assignment_plan":     {"history_year": "year"},
	"map_building_candidates": {"history_year": "year"},
	"map_build_metric":        {"history_year": "year"},
	"map_route":               {"history_year": "year"},
	"map_render":              {"history_year": "year"},
}

// repairToolArguments rewrites a tools/call argument object so that recoverable
// caller mistakes become notices rather than errors. It runs before schema
// validation and returns the notices to attach to the successful result, so the
// caller still learns the documented bound and can send it correctly next time.
func repairToolArguments(tool string, schema map[string]any, raw json.RawMessage) (json.RawMessage, []string) {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return raw, nil
	}
	var notices []string
	changed := false
	for alias, canonical := range argumentAliases[tool] {
		value, aliased := fields[alias]
		if !aliased {
			continue
		}
		if _, documented := properties[canonical]; !documented {
			continue
		}
		if _, occupied := fields[canonical]; occupied {
			continue
		}
		if _, aliasIsDocumented := properties[alias]; aliasIsDocumented {
			continue
		}
		fields[canonical] = value
		delete(fields, alias)
		changed = true
		notices = append(notices, fmt.Sprintf("argument %q is not documented; it was read as %q", alias, canonical))
	}
	for name, value := range fields {
		if !clampableArguments[name] {
			continue
		}
		property, ok := properties[name].(map[string]any)
		if !ok {
			// max_response_bytes is only advertised on the tools whose result can
			// approach the budget; it stays legal everywhere through the
			// compatibility list, and splitResponseControl applies the same bounds.
			if name != "max_response_bytes" {
				continue
			}
			property = responseBudgetProperty()
		}
		clamped, notice, ok := clampNumericArgument(name, value, property)
		if !ok {
			continue
		}
		fields[name] = clamped
		notices = append(notices, notice)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return raw, nil
	}
	sort.Strings(notices)
	return data, notices
}

// clampNumericArgument reports the repaired encoding only when the value is a
// finite number outside the documented range. Anything else -- a string, a
// float where an integer is required, a value already in range -- is left for
// validateArguments to judge, so no type error is masked by the clamp.
func clampNumericArgument(name string, raw json.RawMessage, property map[string]any) (json.RawMessage, string, bool) {
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return nil, "", false
	}
	requested, err := number.Int64()
	if err != nil {
		return nil, "", false
	}
	minimum, hasMinimum := schemaNumber(property["minimum"])
	maximum, hasMaximum := schemaNumber(property["maximum"])
	switch {
	case hasMaximum && float64(requested) > maximum:
		bound := int64(maximum)
		return json.RawMessage(fmt.Sprint(bound)),
			fmt.Sprintf("%s %d is above the maximum %d; the request used %d", name, requested, bound, bound), true
	case hasMinimum && float64(requested) < minimum:
		bound := int64(minimum)
		return json.RawMessage(fmt.Sprint(bound)),
			fmt.Sprintf("%s %d is below the minimum %d; the request used %d", name, requested, bound, bound), true
	default:
		return nil, "", false
	}
}

// attachArgumentNotices records the repairs on the successful result. The text
// content also carries the notices, so both representations have to be rewritten
// together while preserving the caller's selected text presentation.
func attachArgumentNotices(result map[string]any, notices []string) map[string]any {
	if len(notices) == 0 || result == nil {
		return result
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return result
	}
	structured["argument_notices"] = notices
	if hasCompactSearchText(result) {
		result["content"] = searchTextContent(structured)
		return result
	}
	data, err := json.Marshal(structured)
	if err != nil {
		return result
	}
	var items []map[string]any
	switch content := result["content"].(type) {
	case []map[string]any:
		items = content
	case []any:
		for _, raw := range content {
			if item, ok := raw.(map[string]any); ok {
				items = append(items, item)
			}
		}
	}
	for i, item := range items {
		if kind, _ := item["type"].(string); kind != "text" {
			continue
		}
		items[i]["text"] = string(data)
		break
	}
	return result
}

// privateOnlyVisibilityTools reject visibility=public outright: the map cache
// does not record source-level provenance, so there is no honest way to answer
// a public query from it. The audit shows the schema never said so, and a
// caller that sets visibility=public by habit -- 82% of observed calls do --
// discovers it one rejection at a time. map_title_context and
// map_province_info alone lost 14 calls that way, in runs of up to eight,
// because each rejection was a different id rather than a retry it could learn
// from. Declaring the constraint moves the correction to schema-reading time.
//
// TestPrivateOnlyVisibilityToolsMatchTheHandlers keeps this list equal to the
// set of handlers that actually call requireSourceTrackedMapVisibility.
var privateOnlyVisibilityTools = map[string]bool{
	"map_apply_split":         true,
	"map_artifact":            true,
	"map_assignment_plan":     true,
	"map_build_metric":        true,
	"map_building_candidates": true,
	"map_neighbors":           true,
	"map_physical_context":    true,
	"map_province_info":       true,
	"map_render":              true,
	"map_route":               true,
	"map_spatial_relation":    true,
	"map_split_province":      true,
	"map_strategic_passages":  true,
	"map_terrain_edit":        true,
	"map_title_context":       true,
}

// declarePrivateOnlyVisibility narrows the advertised visibility enum on the
// tools that cannot serve a public request. The handler check stays exactly as
// it was: this only means a caller reading the schema no longer has to learn
// the constraint from a failed call.
func declarePrivateOnlyVisibility(definitions []ToolDefinition) []ToolDefinition {
	for i := range definitions {
		if !privateOnlyVisibilityTools[definitions[i].Name] {
			continue
		}
		properties, ok := definitions[i].InputSchema["properties"].(map[string]any)
		if !ok || properties == nil {
			continue
		}
		if _, declared := properties["visibility"]; !declared {
			continue
		}
		// The description is kept to roughly the length of the shared one: it is
		// repeated on fifteen tools, and the catalog byte budget is measured.
		// The recovery pointer stays in the runtime error, which only a caller
		// that ignored the enum ever sees.
		properties["visibility"] = map[string]any{
			"type":        "string",
			"enum":        []string{"private"},
			"default":     "private",
			"description": "private only; the map cache records no per-source provenance.",
		}
	}
	return definitions
}

// describeUnknownArgument turns "unknown argument field X" into something the
// caller can act on in one step. The audit showed eight consecutive identical
// ck3_search calls rejected for an undocumented "domain" field: the error named
// the offending field but never said which fields the tool does accept, so the
// model had nothing to correct toward.
func describeUnknownArgument(name string, properties map[string]any, compatibility []string) string {
	accepted := make([]string, 0, len(properties)+len(compatibility))
	for field := range properties {
		accepted = append(accepted, field)
	}
	accepted = append(accepted, compatibility...)
	sort.Strings(accepted)
	message := fmt.Sprintf("unknown argument field %q; remove it or use the documented input schema", name)
	if nearest, ok := nearestArgumentName(name, accepted); ok {
		message += fmt.Sprintf("; did you mean %q?", nearest)
	}
	if len(accepted) > 0 {
		message += fmt.Sprintf(" This tool accepts: %s.", strings.Join(accepted, ", "))
	}
	return message
}

// nearestArgumentName returns the closest accepted field when the mistake is
// plausibly a spelling of it. The distance ceiling scales with the name so a
// short field cannot be "corrected" into an unrelated short field.
func nearestArgumentName(name string, accepted []string) (string, bool) {
	lowered := strings.ToLower(name)
	best := ""
	bestDistance := 0
	for _, candidate := range accepted {
		distance := editDistance(lowered, strings.ToLower(candidate))
		if best == "" || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	ceiling := len(lowered) / 3
	if ceiling < 1 {
		ceiling = 1
	}
	if best == "" || bestDistance > ceiling {
		return "", false
	}
	return best, true
}

func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			substitution := previous[j-1]
			if a[i-1] != b[j-1] {
				substitution++
			}
			current[j] = min(min(current[j-1]+1, previous[j]+1), substitution)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
