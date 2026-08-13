package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"ck3-index/internal/indexer"
)

func TestCanonicalToolArgsNormalizesKeyOrder(t *testing.T) {
	first := canonicalToolArgs(json.RawMessage(`{"b":2,"a":1}`))
	second := canonicalToolArgs(json.RawMessage(`{"a":1,"b":2}`))
	if string(first) != string(second) {
		t.Fatalf("canonical args differ for equivalent inputs: %s vs %s", first, second)
	}
	empty := canonicalToolArgs(json.RawMessage(`{}`))
	if string(empty) != `{}` {
		t.Fatalf("empty args canonicalized to %s", empty)
	}
	blank := canonicalToolArgs(nil)
	if string(blank) != `{}` {
		t.Fatalf("nil args canonicalized to %s", blank)
	}
}

func TestToolCacheKeySeparatesGenerationsAndArgs(t *testing.T) {
	a := toolCacheKey("ck3_search", "db-a", 1, 42, "rev-a", json.RawMessage(`{"query":"x"}`))
	b := toolCacheKey("ck3_search", "db-a", 1, 43, "rev-a", json.RawMessage(`{"query":"x"}`))
	c := toolCacheKey("ck3_search", "db-b", 2, 42, "rev-a", json.RawMessage(`{"query":"x"}`))
	d := toolCacheKey("ck3_search", "db-a", 1, 42, "rev-a", json.RawMessage(`{"query":"y"}`))
	e := toolCacheKey("ck3_inspect", "db-a", 1, 42, "rev-a", json.RawMessage(`{"query":"x"}`))
	keys := []string{a, b, c, d, e}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("duplicate key %q", key)
		}
		seen[key] = true
	}
}

func TestReadToolCacheBudgetAndEviction(t *testing.T) {
	cache := newReadToolCache(300, 200)
	cache.put("a", []byte("aaaa"))
	cache.put("b", []byte("bbbb"))
	cache.put("c", []byte("cccc"))
	// 12 bytes total, under budget: all present.
	for _, key := range []string{"a", "b", "c"} {
		if _, ok := cache.get(key); !ok {
			t.Fatalf("expected %s to be cached", key)
		}
	}
	// 150-byte entries: 3 x 150 = 450 > 300, evicts oldest until under budget.
	cache.put("x", make([]byte, 150))
	cache.put("y", make([]byte, 150))
	cache.put("z", make([]byte, 150))
	stats := cache.stats()
	if stats.CurBytes > 300 {
		t.Fatalf("cache over budget: %d > 300", stats.CurBytes)
	}
	// Oversized single entries are rejected outright.
	big := make([]byte, 250)
	cache.put("big", big)
	if _, ok := cache.get("big"); ok {
		t.Fatalf("entry above maxEntry should not be cached")
	}
}

func TestReadToolCacheLRUTouch(t *testing.T) {
	cache := newReadToolCache(512, 512)
	cache.put("a", make([]byte, 100))
	cache.put("b", make([]byte, 100))
	cache.put("c", make([]byte, 100))
	// Touch a so it becomes most recent, then force eviction with two more
	// 150-byte entries: 100+100+100+150+150 = 600 > 512, so the least
	// recently used entry (b, untouched) must go while a survives.
	if _, ok := cache.get("a"); !ok {
		t.Fatal("a should be cached")
	}
	cache.put("x", make([]byte, 150))
	cache.put("y", make([]byte, 150))
	if _, ok := cache.get("a"); !ok {
		t.Fatalf("recently touched entry a was evicted")
	}
	if _, ok := cache.get("b"); ok {
		t.Fatalf("oldest entry b should have been evicted")
	}
}

func TestReadToolCacheConcurrentAccess(t *testing.T) {
	cache := newReadToolCache(1<<20, 1<<16)
	done := make(chan struct{})
	for worker := 0; worker < 8; worker++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("k%d", i%16)
				cache.put(key, []byte(fmt.Sprintf("value-%d-%d", id, i)))
				cache.get(key)
			}
		}(worker)
	}
	for worker := 0; worker < 8; worker++ {
		<-done
	}
	if stats := cache.stats(); stats.CurBytes > 1<<20 {
		t.Fatalf("concurrent access exceeded budget: %d", stats.CurBytes)
	}
}

// A clean reset rebuilds meta and can restart numbering, so the same
// (path, epoch, generation) triple can name two different published databases.
// Without the revision the second one is answered out of the first one's cache.
func TestToolCacheKeySeparatesRevisionsAtTheSameGeneration(t *testing.T) {
	args := json.RawMessage(`{"query":"x"}`)
	first := toolCacheKey("ck3_search", "db-a", 1, 1, "revision-a", args)
	afterReset := toolCacheKey("ck3_search", "db-a", 1, 1, "revision-b", args)
	if first == afterReset {
		t.Fatal("generation 1 before and after a clean reset produced one key")
	}
	laterGeneration := toolCacheKey("ck3_search", "db-a", 1, 2, "revision-a", args)
	if first == laterGeneration {
		t.Fatal("two generations of one revision produced one key")
	}
	cache := newReadToolCache(1<<20, 1<<16)
	cache.put(first, []byte(`{"evidence":"old"}`))
	if _, ok := cache.get(afterReset); ok {
		t.Fatal("a rebuilt database was served from the previous database's entry")
	}
	if _, ok := cache.get(first); !ok {
		t.Fatal("the original entry should still be cached")
	}
}

func TestCacheWriteRequiresOneVerifiedPublishedState(t *testing.T) {
	ready := indexer.IndexState{Generation: 7, Revision: "revision-seven", Status: indexer.IndexStatusReady}
	changed := ready
	changed.Generation++
	missingRevision := ready
	missingRevision.Revision = ""
	initializing := ready
	initializing.Status = indexer.IndexStatusInitializing
	readErr := errors.New("state unavailable")

	tests := []struct {
		name                string
		before, after       indexer.IndexState
		beforeErr, afterErr error
		want                bool
	}{
		{name: "stable ready generation", before: ready, after: ready, want: true},
		{name: "before read failed", before: ready, after: ready, beforeErr: readErr},
		{name: "after read failed", before: ready, after: ready, afterErr: readErr},
		{name: "generation changed", before: ready, after: changed},
		{name: "missing revision", before: missingRevision, after: missingRevision},
		{name: "not published", before: initializing, after: initializing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cacheablePublishedTransition(test.before, test.beforeErr, test.after, test.afterErr); got != test.want {
				t.Fatalf("cacheablePublishedTransition()=%v, want %v", got, test.want)
			}
		})
	}
}

// Two operations on otherwise cacheable tools read files the index generation
// does not cover, so they must never be served from a generation-keyed cache.
func TestCacheableReadRequestExcludesExternalFileOperations(t *testing.T) {
	for _, test := range []struct {
		name string
		tool string
		args string
		want bool
	}{
		{"inspect aggregate", "ck3_inspect", `{"id":"trait_brave"}`, true},
		{"inspect definition", "ck3_inspect", `{"id":"trait_brave","operation":"definition"}`, true},
		{"inspect compare reads files", "ck3_inspect", `{"id":"trait_brave","operation":"compare"}`, false},
		{"inspect compare mixed case", "ck3_inspect", `{"id":"trait_brave","operation":"COMPARE"}`, false},
		{"script reference trigger", "ck3_script_reference", `{"id":"add_gold","kind":"trigger"}`, true},
		{"script reference on_action reads files", "ck3_script_reference", `{"id":"on_birth","kind":"on_action"}`, false},
		{"search stays cacheable", "ck3_search", `{"query":"x"}`, true},
		{"workspace reads engine logs", "ck3_workspace", `{"operation":"overview"}`, false},
		{"province mapping decodes rasters", "map_province_mapping", `{}`, false},
		{"refresh is never cacheable", "ck3_refresh", `{"operation":"files"}`, false},
		{"diagnostics summary stays cacheable", "ck3_diagnostics", `{"operation":"summary"}`, true},
		{"baseline save writes", "ck3_diagnostic_baseline", `{"operation":"save"}`, false},
		{"baseline list is never cacheable", "ck3_diagnostic_baseline", `{"operation":"list"}`, false},
		{"baseline clear writes", "ck3_diagnostic_baseline", `{"operation":"clear"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cacheableReadRequest(test.tool, json.RawMessage(test.args)); got != test.want {
				t.Fatalf("cacheableReadRequest(%s, %s)=%v, want %v", test.tool, test.args, got, test.want)
			}
		})
	}
}

// A baseline write must never be answered out of the read cache. The cache key
// carries the published index identity, which a baseline write does not move,
// so an identical second save would have hit the first one's cached response,
// skipped the handler entirely, and reported a success that wrote nothing.
//
// Driven through callMCPTool rather than the indexer, because the cache sits in
// the runtime above the handler and is the whole subject of the test.
func TestDiagnosticBaselineWritesAreNotServedFromTheReadCache(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	call := func(t *testing.T, name string, arguments map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
		if err != nil {
			t.Fatal(err)
		}
		resultAny, err := callMCPTool(context.Background(), db, cfg, raw)
		if err != nil {
			t.Fatalf("%s returned a protocol error: %v", name, err)
		}
		result := resultAny.(map[string]any)
		if result["isError"] == true {
			t.Fatalf("%s returned a tool error: %+v", name, result)
		}
		structured, ok := result["structuredContent"].(map[string]any)
		if !ok {
			t.Fatalf("%s returned no structuredContent: %+v", name, result)
		}
		return structured
	}
	names := func(t *testing.T) []string {
		t.Helper()
		listed := call(t, "ck3_diagnostic_baseline", map[string]any{"operation": "list"})
		entries, ok := listed["baselines"].([]any)
		if !ok {
			t.Fatalf("list returned no baselines array: %+v", listed)
		}
		var out []string
		for _, entry := range entries {
			row, ok := entry.(map[string]any)
			if !ok {
				t.Fatalf("baseline entry is %T, want an object", entry)
			}
			name, _ := row["name"].(string)
			out = append(out, name)
		}
		return out
	}
	holds := func(t *testing.T, want string) bool {
		t.Helper()
		for _, name := range names(t) {
			if name == want {
				return true
			}
		}
		return false
	}

	// list -> save -> list. The first list is what would be cached and replayed.
	if holds(t, "foo") {
		t.Fatal("an unrecorded baseline was listed")
	}
	call(t, "ck3_diagnostic_baseline", map[string]any{"operation": "save", "baseline": "foo"})
	if !holds(t, "foo") {
		t.Fatal("a saved baseline was not listed; the list was answered from cache")
	}

	// save -> clear -> save. The second save repeats the first one's arguments
	// exactly, which is the call a cache hit would swallow.
	call(t, "ck3_diagnostic_baseline", map[string]any{"operation": "clear", "baseline": "foo"})
	if holds(t, "foo") {
		t.Fatal("a cleared baseline was still listed")
	}
	call(t, "ck3_diagnostic_baseline", map[string]any{"operation": "save", "baseline": "foo"})
	if !holds(t, "foo") {
		t.Fatal("re-saving a cleared baseline reported success without recording it")
	}

	// A recorded baseline changes what ck3_diagnostics reports without moving
	// the scan generation, so the write has to drop the cached diagnostics too.
	before := call(t, "ck3_diagnostics", map[string]any{"operation": "summary", "baseline": "foo"})
	if before["intent"] == nil {
		t.Fatalf("diagnostics summary against a baseline returned %+v", before)
	}
	call(t, "ck3_diagnostic_baseline", map[string]any{"operation": "clear", "baseline": "foo"})
	rawCall, err := json.Marshal(map[string]any{
		"name":      "ck3_diagnostics",
		"arguments": map[string]any{"operation": "summary", "baseline": "foo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	afterAny, err := callMCPTool(context.Background(), db, cfg, rawCall)
	if err != nil {
		t.Fatal(err)
	}
	if after := afterAny.(map[string]any); after["isError"] != true {
		t.Fatalf("a summary against a cleared baseline still answered: %+v", after)
	}
}
