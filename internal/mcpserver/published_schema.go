package mcpserver

import "strings"

// publishedInputSchema returns the wire form of a tool input schema.
//
// A pure-required anyOf/oneOf branch names which argument to send, not which
// shape to send it in: it carries a required list and nothing else. Strict
// function-declaration validators read that branch as a schema in its own
// right and reject it twice over — once because required is only allowed on an
// object type, and once because the property it names is not declared beside
// it. Gemini refuses the entire tool list on that ground, so five branches
// spread across ck3_search, ck3_gui, ck3_package, map_province_migration, and
// map_render took every other tool down with them.
//
// The constraint those branches express is real, so it survives here as the
// same sentence the runtime error already uses, and validateArguments keeps
// enforcing it against ToolDefinition.InputSchema, which this does not touch.
func publishedInputSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	return foldRequiredAlternatives(cloneSchema(schema))
}

// foldRequiredAlternatives rewrites one schema node in place and descends into
// every child a tool schema can carry. Composition that says something about
// shape rather than about which argument to send is left alone; only branches
// pureRequiredAlternatives accepts are folded into prose.
func foldRequiredAlternatives(node map[string]any) map[string]any {
	if node == nil {
		return nil
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		alternatives, ok := node[keyword].([]any)
		if !ok || !pureRequiredAlternatives(alternatives) {
			continue
		}
		delete(node, keyword)
		node["description"] = describeRequiredChoice(node["description"], alternatives)
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, child := range properties {
			if nested, ok := child.(map[string]any); ok {
				properties[name] = foldRequiredAlternatives(nested)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		node["items"] = foldRequiredAlternatives(items)
	}
	if additional, ok := node["additionalProperties"].(map[string]any); ok {
		node["additionalProperties"] = foldRequiredAlternatives(additional)
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		alternatives, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for index, item := range alternatives {
			if nested, ok := item.(map[string]any); ok {
				alternatives[index] = foldRequiredAlternatives(nested)
			}
		}
	}
	return node
}

// describeRequiredChoice states the folded constraint in the words a caller
// gets back from missingOneOfArguments, so the schema a model reads and the
// error it receives for ignoring it cannot drift apart.
func describeRequiredChoice(existing any, alternatives []any) string {
	choices := make([]string, 0, len(alternatives))
	for _, item := range alternatives {
		alternative, _ := item.(map[string]any)
		choices = append(choices, strings.Join(schemaStrings(alternative["required"]), "+"))
	}
	sentence := "Provide one of: " + strings.Join(choices, ", ") + "."
	description, _ := existing.(string)
	if description = strings.TrimSpace(description); description == "" {
		return sentence
	}
	return description + " " + sentence
}
