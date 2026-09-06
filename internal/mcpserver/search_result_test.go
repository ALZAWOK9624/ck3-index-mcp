package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Independent client decoder: the public contract has exactly one table shape.
func searchRowsForTest(t *testing.T, body map[string]any, field string) []any {
	t.Helper()
	value, exists := cloneStructured(body)[field]
	if !exists {
		return nil
	}
	table, ok := value.(map[string]any)
	if !ok || len(table) != 2 {
		t.Fatalf("%s is not exclusively columns/rows: %v", field, value)
	}
	columns, ok := table["columns"].([]any)
	if !ok {
		t.Fatalf("%s columns is not an array", field)
	}
	rows, ok := table["rows"].([]any)
	if !ok {
		t.Fatalf("%s rows is not an array", field)
	}
	expanded := make([]any, len(rows))
	for i, raw := range rows {
		cells := raw.([]any)
		if len(cells) != len(columns) {
			t.Fatalf("%s has %d columns and %d cells", field, len(columns), len(cells))
		}
		row := map[string]any{}
		for j, value := range cells {
			if value != nil {
				row[columns[j].(string)] = value
			}
		}
		expanded[i] = row
	}
	return expanded
}

func assertSearchResultContract(t *testing.T, result map[string]any) {
	t.Helper()
	normalized := cloneStructured(result)
	if normalized["isError"] == true {
		t.Fatalf("search failed: %v", normalized)
	}
	content, ok := normalized["content"].([]any)
	if !ok || len(content) != 0 {
		t.Fatalf("search still emits a text compatibility copy: %v", content)
	}
	body := normalized["structuredContent"].(map[string]any)
	if _, ok := body["evidence"]; !ok {
		t.Fatal("every search must carry an evidence table, including no-match")
	}
	for _, field := range []string{"evidence", "suggestions", "batch"} {
		searchRowsForTest(t, body, field)
	}
	definition, _ := findCanonicalTool("ck3_search")
	assertDecodedValueMatchesSchema(t, "canonical search", body, definition.OutputSchema)
}

func TestSearchResultSingleContractAndCache(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	definition, _ := findCanonicalTool("ck3_search")
	for _, args := range []map[string]any{
		{"query": "size_probe"}, {"query": "size_probe", "limit": 20},
		{"query": "size_probe", "limit": 1}, {"query": "size_probe", "page": 2},
		{"query": "size_probe", "visibility": "public"},
		{"query": "size_probe_trait_alpha", "kind": "reference"},
		{"query": "size_probe_trait_alpha", "kind": "script_text"},
		{"query": "no_such_name_123456"},
		{"queries": []string{"size_probe", "size_probe_trait_alpha", "no_such_name_123456"}},
		{"query": "size_probe", "limit": 100},
	} {
		t.Run(mustJSON(t, args), func(t *testing.T) {
			raw, notices := repairToolArguments("ck3_search", definition.InputSchema, json.RawMessage(mustJSON(t, args)))
			output, err := definition.Handler(context.Background(), &Runtime{DB: db, Config: cfg}, definition, raw)
			if err != nil {
				t.Fatal(err)
			}
			_, expected, err := encodeStructuredValue(output.Value)
			if err != nil {
				t.Fatal(err)
			}
			if len(notices) > 0 {
				expected["argument_notices"] = notices
			}
			for attempt := 0; attempt < 2; attempt++ {
				result := callToolForTest(t, db, cfg, "ck3_search", args)
				assertSearchResultContract(t, result)
				body := result["structuredContent"].(map[string]any)
				expanded := cloneStructured(body)
				for _, field := range []string{"evidence", "suggestions", "batch"} {
					if _, present := expected[field]; present {
						expanded[field] = searchRowsForTest(t, body, field)
					} else {
						delete(expanded, field)
					}
				}
				if !reflect.DeepEqual(expanded, cloneStructured(expected)) {
					t.Fatalf("search changed evidence or metadata:\ngot %s\nwant %s", mustJSON(t, expanded), mustJSON(t, expected))
				}
			}
		})
	}
	for _, format := range []string{"json", "compact"} {
		result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": "size_probe", "format": format})
		if result["isError"] != true {
			t.Fatal("removed format option is still accepted")
		}
	}
}

func TestSearchTableSparseQuotedAndEmptyRows(t *testing.T) {
	for _, count := range []int{0, 1, 24} {
		items := make([]any, count)
		for i := range items {
			row := map[string]any{"kind": "reference", "type": "trait", "source": "game",
				"name": fmt.Sprintf("example_%d", i), "path": "common/traits/example.txt", "line": i + 1,
				"detail": "中文\n\t\"quoted\" | } $TOKEN$ 🍎", "future_flag": false}
			if i%2 == 0 {
				row["snippet"] = "line 1\nline 2"
			}
			items[i] = row
		}
		table, err := searchTable(items)
		if err != nil {
			t.Fatal(err)
		}
		got := searchRowsForTest(t, map[string]any{"evidence": table}, "evidence")
		if mustJSON(t, got) != mustJSON(t, items) {
			t.Fatalf("table lost data: %s", mustJSON(t, got))
		}
		for i := 0; i < 5; i++ {
			again, _ := searchTable(items)
			if mustJSON(t, again) != mustJSON(t, table) {
				t.Fatal("map iteration changed row or column order")
			}
		}
	}
	for _, bad := range []any{nil, []any{"x"}, map[string]any{"x": nil}} {
		if _, err := searchTable([]any{bad}); err == nil {
			t.Fatal("unsupported internal shape silently accepted")
		}
	}
}

func TestSearchResultTrimmingAndNotices(t *testing.T) {
	result := textResultWithEvidence(80)
	body := result["structuredContent"].(map[string]any)
	body["intent"] = "ck3_search"
	body["pagination"] = map[string]any{"page": 1, "limit": 80, "returned": 80, "has_more": true, "next_page": 2}
	if err := compactSearchResult(body); err != nil {
		t.Fatal(err)
	}
	result["content"] = []any{}
	result = attachArgumentNotices(cloneStructured(result), []string{"limit repaired"})
	budget := encodedResultBytes(t, result) / 2
	trimmed, err := enforceResponseBudget(result, budget, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	assertSearchResultContract(t, trimmed)
	body = trimmed["structuredContent"].(map[string]any)
	kept := len(searchRowsForTest(t, body, "evidence"))
	if body["truncated"] != true || kept >= 80 || encodedResultBytes(t, trimmed) > budget {
		t.Fatal("search budget did not trim and report its evidence")
	}
	if !strings.Contains(mustJSON(t, body), "limit repaired") {
		t.Fatal("notice lost after trimming")
	}
	page := body["pagination"].(map[string]any)
	if page["returned"] != kept || page["next_page"] != float64(2) {
		t.Fatalf("trim changed page semantics: %v", page)
	}
}

func BenchmarkSearchResultEncoding(b *testing.B) {
	for _, count := range []int{8, 24} {
		rows := make([]any, count)
		for i := range rows {
			rows[i] = map[string]any{"kind": "object", "type": "trait", "source": "game", "name": fmt.Sprintf("example_%d", i), "path": "common/traits/example.txt", "line": i + 1}
		}
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				table, _ := searchTable(rows)
				_, _ = json.Marshal(table)
			}
		})
	}
}
