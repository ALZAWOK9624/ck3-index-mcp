package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// This decoder is deliberately independent of the encoder. It models a client
// following the documented table contract and proves semantic round trips.
func expandSearchText(t *testing.T, text string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatal(err)
	}
	if body["format"] != "ck3-search-table-v1" {
		return body
	}
	delete(body, "format")
	for _, key := range []string{"evidence", "suggestions", "batch"} {
		table, ok := body[key].(map[string]any)
		if !ok {
			continue
		}
		columns := table["columns"].([]any)
		rows := table["rows"].([]any)
		expanded := make([]any, len(rows))
		for i, raw := range rows {
			row := map[string]any{}
			if shared, ok := table["shared"].(map[string]any); ok {
				for name, value := range shared {
					row[name] = value
				}
			}
			cells := raw.([]any)
			if len(cells) != len(columns) {
				t.Fatalf("table has %d columns and %d cells", len(columns), len(cells))
			}
			for j, value := range cells {
				if value == nil {
					continue
				}
				name := columns[j].(string)
				if paths, ok := table["paths"].([]any); ok && name == "path" {
					value = paths[int(value.(float64))]
				}
				row[name] = value
			}
			expanded[i] = row
		}
		body[key] = expanded
	}
	return body
}

func searchTextForTest(t *testing.T, result map[string]any) string {
	t.Helper()
	// Normalize fresh and cache-hit content block representations.
	copy := cloneStructured(result)
	return copy["content"].([]any)[0].(map[string]any)["text"].(string)
}

func assertSearchTextRoundTrip(t *testing.T, result map[string]any) {
	t.Helper()
	got := expandSearchText(t, searchTextForTest(t, result))
	want := cloneStructured(result["structuredContent"].(map[string]any))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compact text changed evidence or metadata:\ngot %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
	}
}

func TestCompactSearchTextRoundTripAndCompatibility(t *testing.T) {
	db, cfg := openResponseSizeFixture(t)
	for _, args := range []map[string]any{
		{"query": "size_probe"},
		{"query": "size_probe", "limit": 20},
		{"query": "size_probe", "page": 2},
		{"query": "size_probe", "visibility": "public"},
		{"query": "size_probe_trait_alpha", "kind": "reference"},
		{"query": "size_probe_trait_alpha", "kind": "script_text"},
		{"query": "no_such_name_123456"},
		{"queries": []string{"size_probe", "size_probe_trait_alpha", "no_such_name_123456"}},
		{"query": "size_probe", "limit": 100}, // repaired arguments
	} {
		t.Run(mustJSON(t, args), func(t *testing.T) {
			plainArgs := make(map[string]any, len(args)+1)
			for key, value := range args {
				plainArgs[key] = value
			}
			plainArgs["format"] = "json"
			plain := callToolForTest(t, db, cfg, "ck3_search", plainArgs)
			compact := callToolForTest(t, db, cfg, "ck3_search", args)
			if compact["isError"] == true || plain["isError"] == true {
				t.Fatalf("search failed: %v / %v", compact, plain)
			}
			if mustJSON(t, plain["structuredContent"]) != mustJSON(t, compact["structuredContent"]) {
				t.Fatal("text format changed the machine-readable contract")
			}
			if searchTextForTest(t, plain) != mustJSON(t, plain["structuredContent"]) {
				t.Fatal("format=json no longer mirrors structuredContent")
			}
			cachedPlain := callToolForTest(t, db, cfg, "ck3_search", plainArgs)
			if searchTextForTest(t, cachedPlain) != mustJSON(t, cachedPlain["structuredContent"]) {
				t.Fatal("cached format=json text lost per-call argument notices")
			}
			assertSearchTextRoundTrip(t, compact)
			if len(searchTextForTest(t, compact)) > len(searchTextForTest(t, plain)) {
				t.Fatal("compact text inflated a search response")
			}
			// A cached payload is unmarshaled JSON, including []any content.
			cached := callToolForTest(t, db, cfg, "ck3_search", args)
			assertSearchTextRoundTrip(t, cached)
			if searchTextForTest(t, cached) != searchTextForTest(t, compact) {
				t.Fatal("cached presentation differs from the original")
			}
		})
	}
	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": "size_probe"})
	if !hasCompactSearchText(result) {
		t.Fatal("default wide search did not use compact tables")
	}
}

func TestCompactSearchTextPreservesSuggestions(t *testing.T) {
	db, cfg := newSearchFixture(t)
	for _, query := range []string{"special pale mechanic", "pale knight", "外乡人军团"} {
		result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": query})
		assertSearchTextRoundTrip(t, result)
	}
}

func TestCompactSearchTextSparseRowsAndQuotedValues(t *testing.T) {
	rows := make([]any, 24)
	for i := range rows {
		row := map[string]any{
			"kind": "reference", "type": "trait", "source": "game",
			"name": fmt.Sprintf("trait_%d", i), "line": i + 1, "column": 1,
			"path":   fmt.Sprintf("common/scripted_effects/a_very_long_repeated_path_%d.txt", i%2),
			"detail": "中文\n\t\"quoted\" | } $TOKEN$ 🍎", "future_flag": false,
		}
		if i%2 == 0 {
			row["snippet"] = "line 1\nline 2"
		} else {
			row["rule_source"] = "engine"
		}
		rows[i] = row
	}
	body := map[string]any{"intent": "ck3_search", "summary": "bounded", "evidence": rows,
		"suggestions": []any{map[string]any{"name": "maybe", "kind": "object"}}, "recovery_confidence": "low"}
	before := mustJSON(t, body)
	result := map[string]any{"structuredContent": body, "content": searchTextContent(body)}
	if !hasCompactSearchText(result) || !strings.Contains(searchTextForTest(t, result), `"paths":`) {
		t.Fatal("repeated paths did not use a dictionary")
	}
	assertSearchTextRoundTrip(t, result)
	if mustJSON(t, body) != before {
		t.Fatal("compaction mutated structuredContent")
	}
	for i := 0; i < 10; i++ {
		if searchTextContent(body)[0]["text"] != result["content"].([]map[string]any)[0]["text"] {
			t.Fatal("map iteration changed compact output")
		}
	}
	// Literal null, nested data, and a future non-string path must survive.
	for _, extra := range []map[string]any{{"nullable": nil}, {"nested": []any{"x"}}, {"path": 4}} {
		rows[0] = extra
		result["content"] = searchTextContent(body)
		assertSearchTextRoundTrip(t, result)
	}
}

func TestCompactSearchTextTrimmingAndCachedNotices(t *testing.T) {
	result := textResultWithEvidence(80)
	result["structuredContent"].(map[string]any)["pagination"] = map[string]any{
		"page": 1, "limit": 80, "returned": 80, "has_more": true, "next_page": 2,
	}
	result["content"] = searchTextContent(result["structuredContent"].(map[string]any))
	if !hasCompactSearchText(result) {
		t.Fatal("fixture must exercise compact content")
	}
	// This is the representation restored from the cache.
	result = cloneStructured(result)
	result = attachArgumentNotices(result, []string{"limit repaired"})
	assertSearchTextRoundTrip(t, result)
	budget := encodedResultBytes(t, result) / 2
	trimmed, err := enforceResponseBudget(result, budget, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	assertSearchTextRoundTrip(t, trimmed)
	body := trimmed["structuredContent"].(map[string]any)
	if body["truncated"] != true || len(body["evidence"].([]any)) >= 80 || encodedResultBytes(t, trimmed) > budget {
		t.Fatal("compact budget trimming did not keep bounded, honest evidence")
	}
	page := body["pagination"].(map[string]any)
	if page["returned"] != len(body["evidence"].([]any)) || page["next_page"] != float64(2) {
		t.Fatalf("trim changed page semantics: %v", page)
	}
}

func BenchmarkSearchTextEncoding(b *testing.B) {
	for _, count := range []int{8, 24} {
		rows := make([]any, count)
		for i := range rows {
			rows[i] = map[string]any{"kind": "object", "type": "trait", "source": "game",
				"name": fmt.Sprintf("example_trait_%d", i), "path": fmt.Sprintf("common/traits/example_%d.txt", i%3),
				"line": i + 1, "column": 1}
		}
		body := map[string]any{"intent": "ck3_search", "summary": "bounded", "evidence": rows}
		b.Run(fmt.Sprintf("json/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = json.Marshal(body)
			}
		})
		b.Run(fmt.Sprintf("compact/%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = searchTextContent(body)
			}
		})
	}
}
