package mcpserver

import (
	"encoding/json"
	"sort"
	"strings"
)

const searchTablePrefix = `{"format":"ck3-search-table-v1",`

func searchTextFormatProperty() map[string]any {
	property := stringProperty("Text presentation: compact (default) uses columns/rows tables. Merge shared fields into each row; null cells mean absent fields; integer path cells index the zero-based paths dictionary. json mirrors structuredContent for legacy text parsers. structuredContent is unchanged in either mode.", "compact", "json")
	property["default"] = "compact"
	return property
}

// Only the text presentation changes. In particular, no ranking, pagination,
// confidence, privacy, or machine-readable output contract is changed here.
// Work on the already-redacted map, and never mutate it: it is also the cached
// structuredContent payload. JSON quoting keeps script text and delimiters inert.
func searchTextContent(structured map[string]any) []map[string]any {
	plain, _ := json.Marshal(structured) // The caller has already validated JSON.
	compact := make(map[string]any, len(structured))
	for key, value := range structured {
		compact[key] = value
	}
	for _, field := range []string{"evidence", "suggestions", "batch"} {
		if rows, ok := structured[field].([]any); ok {
			compact[field] = compactSearchTable(rows)
		}
	}
	encoded, err := json.Marshal(compact)
	text := string(plain)
	if err == nil && len(encoded) > 2 {
		candidate := searchTablePrefix + string(encoded[1:])
		// A single short hit or a miss needs no table. Never inflate these
		// common cases just to force a uniform presentation.
		if len(candidate) < len(plain) {
			text = candidate
		}
	}
	return []map[string]any{{"type": "text", "text": text}}
}

// The version marker lives in the text itself, so it survives cache JSON
// round-trips without adding private Go values or custom MCP envelope fields.
func hasCompactSearchText(result map[string]any) bool {
	var block map[string]any
	switch items := result["content"].(type) {
	case []map[string]any:
		if len(items) > 0 {
			block = items[0]
		}
	case []any:
		if len(items) > 0 {
			block, _ = items[0].(map[string]any)
		}
	}
	text, _ := block["text"].(string)
	return strings.HasPrefix(text, searchTablePrefix)
}

// A table is lossless: merge shared into each row mapped to columns, omitting
// null cells (absent properties). If paths is present, integer path cells index
// that dictionary. Collections and their row order remain separate; suggestions
// never become evidence and batch rows still report unmatched terms.
func compactSearchTable(items []any) any {
	if len(items) < 2 {
		return items
	}
	rows := make([]map[string]any, len(items))
	keys := map[string]bool{}
	for i, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return items
		}
		for key, value := range row {
			// Explicit null and an absent property must not be conflated.
			// Current search rows have only non-null scalars. Future shapes
			// fall back to the ordinary JSON instead of losing information.
			switch value.(type) {
			case string, float64, bool, int:
			default:
				return items
			}
			keys[key] = true
		}
		rows[i] = row
	}
	shared := map[string]any{}
	for key, first := range rows[0] {
		common := true
		for _, row := range rows[1:] {
			value, present := row[key]
			if !present || value != first {
				common = false
				break
			}
		}
		if common {
			shared[key] = first
			delete(keys, key)
		}
	}
	columns := make([]string, 0, len(keys))
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
	values := make([][]any, len(rows))
	pathColumn := -1
	for i, row := range rows {
		values[i] = make([]any, len(columns))
		for j, key := range columns {
			values[i][j] = row[key]
			if key == "path" {
				pathColumn = j
			}
		}
	}
	table := map[string]any{"columns": columns, "rows": values}
	if len(shared) > 0 {
		table["shared"] = shared
	}
	plain, _ := json.Marshal(items)
	encoded, _ := json.Marshal(table)
	var best any = items
	bestSize := len(plain)
	if len(encoded) < bestSize {
		best, bestSize = table, len(encoded)
	}
	if pathColumn < 0 {
		return best
	}
	paths := []string{}
	pathIndex := map[string]int{}
	dictionaryRows := make([][]any, len(values))
	for i, row := range values {
		dictionaryRows[i] = append([]any(nil), row...)
		if row[pathColumn] == nil {
			continue
		}
		path, ok := row[pathColumn].(string)
		if !ok {
			return best
		}
		index, found := pathIndex[path]
		if !found {
			index = len(paths)
			pathIndex[path] = index
			paths = append(paths, path)
		}
		dictionaryRows[i][pathColumn] = index
	}
	dictionary := map[string]any{"columns": columns, "rows": dictionaryRows, "paths": paths}
	if len(shared) > 0 {
		dictionary["shared"] = shared
	}
	encoded, _ = json.Marshal(dictionary)
	if len(encoded) < bestSize {
		return dictionary
	}
	return best
}
