package mcpserver

import (
	"container/list"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
)

// cacheableReadTools is the allowlist for the in-process result cache. Every
// listed tool is a pure index read: deterministic given its arguments, no
// external-file or artifact state, and no side effects. Tools that read files
// from disk (ck3_save), mutate state (ck3_refresh, ck3_database, map authoring
// tools), accept large file payloads (ck3_review/preflight/impact), or return
// heavy media (map_render, ck3_gui previews) are deliberately excluded.
//
// Two more are excluded for the same reason even though they are read-only:
// ck3_workspace operation=on_action_evidence hashes engine logs and vanilla
// files off disk, and map_province_mapping decodes province rasters off disk.
// The cache key can only see the index generation, which does not move when a
// file outside the index changes, so caching either one would keep answering
// from a superseded on-disk state.
var cacheableReadTools = map[string]bool{
	"ck3_search":              true,
	"ck3_inspect":             true,
	"ck3_dependencies":        true,
	"ck3_diagnostics":         true,
	"ck3_script_reference":    true,
	"map_province_info":       true,
	"map_title_context":       true,
	"map_physical_context":    true,
	"map_neighbors":           true,
	"map_spatial_relation":    true,
	"map_strategic_passages":  true,
	"map_recipe_catalog":      true,
	"map_building_candidates": true,
	"map_assignment_plan":     true,
	"map_build_metric":        true,
	"map_route":               true,
}

type toolCacheEntry struct {
	key  string
	data []byte
}

// readToolCache is a byte-budgeted LRU over the final tool responses of
// cacheable read tools. Entries are keyed by tool name, database epoch and
// scan generation, so a refresh, a database switch, or any argument change
// (including visibility) produces a new key and the stale entry simply ages
// out under the byte budget.
type readToolCache struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	lru      *list.List
	curBytes int64
	maxBytes int64
	maxEntry int64
	hits     atomic.Int64
	misses   atomic.Int64
}

var mcpReadToolCache = newReadToolCache(64<<20, 1<<20)

func newReadToolCache(maxBytes, maxEntry int64) *readToolCache {
	return &readToolCache{
		entries:  map[string]*list.Element{},
		lru:      list.New(),
		maxBytes: maxBytes,
		maxEntry: maxEntry,
	}
}

func (c *readToolCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.lru.MoveToFront(element)
	c.hits.Add(1)
	return element.Value.(*toolCacheEntry).data, true
}

func (c *readToolCache) put(key string, data []byte) {
	if int64(len(data)) > c.maxEntry {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*toolCacheEntry)
		c.curBytes += int64(len(data)) - int64(len(entry.data))
		entry.data = data
		c.lru.MoveToFront(element)
	} else {
		entry := &toolCacheEntry{key: key, data: data}
		c.entries[key] = c.lru.PushFront(entry)
		c.curBytes += int64(len(data))
	}
	for c.curBytes > c.maxBytes && c.lru.Len() > 0 {
		back := c.lru.Back()
		entry := back.Value.(*toolCacheEntry)
		c.lru.Remove(back)
		delete(c.entries, entry.key)
		c.curBytes -= int64(len(entry.data))
	}
}

// Stats reports hit/miss counters and the current byte occupancy.
type readToolCacheStats struct {
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
	Entries  int   `json:"entries"`
	CurBytes int64 `json:"cur_bytes"`
	MaxBytes int64 `json:"max_bytes"`
}

func (c *readToolCache) stats() readToolCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return readToolCacheStats{
		Hits: c.hits.Load(), Misses: c.misses.Load(),
		Entries: len(c.entries), CurBytes: c.curBytes, MaxBytes: c.maxBytes,
	}
}

// toolCacheKey builds the cache key. databasePath is the full unredacted
// database path: keys are internal-only (the redacted form appears in tool
// responses, never in keys), and the full path separates two databases whose
// redacted tails would collide (two test fixtures both named
// cache/test.sqlite, for example). The epoch covers database switches and
// the generation covers refresh commits. Arguments are canonicalized by
// re-marshaling through a generic value so key order differences between two
// equivalent calls collapse to one key.
func toolCacheKey(name, databasePath string, epoch uint64, generation int64, args json.RawMessage) string {
	return name + "\x00" +
		databasePath + "\x00" +
		strconv.FormatUint(epoch, 10) + "\x00" +
		strconv.FormatInt(generation, 10) + "\x00" +
		string(canonicalToolArgs(args))
}

func canonicalToolArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}
