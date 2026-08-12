package mcpserver

import (
	"encoding/json"
	"fmt"
	"testing"
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
	a := toolCacheKey("ck3_search", "db-a", 1, 42, json.RawMessage(`{"query":"x"}`))
	b := toolCacheKey("ck3_search", "db-a", 1, 43, json.RawMessage(`{"query":"x"}`))
	c := toolCacheKey("ck3_search", "db-b", 2, 42, json.RawMessage(`{"query":"x"}`))
	d := toolCacheKey("ck3_search", "db-a", 1, 42, json.RawMessage(`{"query":"y"}`))
	e := toolCacheKey("ck3_inspect", "db-a", 1, 42, json.RawMessage(`{"query":"x"}`))
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
