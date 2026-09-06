package mcpserver

import (
	"fmt"
	"sort"
)

// Search has one wire contract: structuredContent with columns/rows tables.
// Paths and source names remain literal strings. There are no presentation
// options, dictionaries, shared defaults, or size-dependent shape changes.
func compactSearchResult(body map[string]any) error {
	for _, field := range []string{"evidence", "suggestions", "batch"} {
		value, present := body[field]
		if !present && field != "evidence" {
			continue
		}
		var items []any
		if present {
			var ok bool
			items, ok = value.([]any)
			if !ok {
				return fmt.Errorf("search %s is not an object collection", field)
			}
		}
		table, err := searchTable(items)
		if err != nil {
			return fmt.Errorf("search %s: %w", field, err)
		}
		body[field] = table
	}
	return nil
}

func searchTable(items []any) (map[string]any, error) {
	keys := map[string]bool{}
	rows := make([]map[string]any, len(items))
	for i, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("row %d is not an object", i)
		}
		for key, value := range row {
			// null cells encode absence. Reject an unsupported internal shape
			// instead of silently switching representations or losing data.
			switch value.(type) {
			case string, float64, bool, int:
			default:
				return nil, fmt.Errorf("row %d field %s is not a non-null scalar", i, key)
			}
			keys[key] = true
		}
		rows[i] = row
	}
	columns := []string{}
	for _, key := range []string{"query", "kind", "type", "name", "source", "path", "line", "column", "detail", "snippet", "edge_type", "suggestion", "rule_source", "returned", "available", "emitted", "has_more", "suggested", "recovered_spelling"} {
		if keys[key] {
			columns = append(columns, key)
			delete(keys, key)
		}
	}
	other := make([]string, 0, len(keys))
	for key := range keys {
		other = append(other, key)
	}
	sort.Strings(other)
	columns = append(columns, other...)
	values := make([]any, len(rows))
	for i, row := range rows {
		cells := make([]any, len(columns))
		for j, key := range columns {
			cells[j] = row[key]
		}
		values[i] = cells
	}
	return map[string]any{"columns": columns, "rows": values}, nil
}

func searchResultOutputSchema() map[string]any {
	schema := llmResultOutputSchema()
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"evidence", "suggestions", "batch"} {
		properties[key] = objectSchema(map[string]any{
			"columns": arrayProperty("Row field names in order.", stringProperty("")),
			"rows": arrayProperty("Ranked rows. null means an absent field; source and path are literal strings.",
				arrayProperty("Cells aligned with columns.", map[string]any{"type": []string{"string", "number", "boolean", "null"}})),
		}, "columns", "rows")
	}
	schema["required"] = append(schema["required"].([]string), "evidence")
	return schema
}

// Other tools expose ordered object arrays; search exposes ordered table rows.
// This is internal collection handling, not another public search format.
func responseRows(value any) []any {
	if table, ok := value.(map[string]any); ok {
		value = table["rows"]
	}
	items, _ := value.([]any)
	return items
}

func setResponseRows(body map[string]any, field string, rows []any) {
	if table, ok := body[field].(map[string]any); ok {
		table["rows"] = rows
	} else {
		body[field] = rows
	}
}
