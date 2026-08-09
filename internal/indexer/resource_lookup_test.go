package indexer

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// legacyResolved is the pre-index implementation, with both linear scans over
// every resource path. It is the oracle for the sorted-tail version.
func legacyResolved(lookup resourceLookup, name string) bool {
	normalized := normalizeResourceLookupPath(name)
	if normalized == "" {
		return false
	}
	if lookup.exact[normalized] {
		return true
	}
	ext := filepath.Ext(normalized)
	if ext != "" {
		if !strings.Contains(normalized, "/") && lookup.basenames[normalized] {
			return true
		}
		suffix := "/" + normalized
		for _, path := range lookup.sorted {
			if strings.HasSuffix(path, suffix) {
				return true
			}
		}
		return false
	}
	if !strings.Contains(normalized, "/") && lookup.stems[normalized] {
		return true
	}
	index := sort.SearchStrings(lookup.sorted, normalized)
	if index < len(lookup.sorted) && strings.HasPrefix(lookup.sorted[index], normalized) {
		return true
	}
	suffixPrefix := "/" + normalized
	for _, path := range lookup.sorted {
		if at := strings.LastIndex(path, suffixPrefix); at >= 0 && strings.HasPrefix(path[at+1:], normalized) {
			return true
		}
	}
	return false
}

func TestResourceLookupMatchesTheLinearImplementation(t *testing.T) {
	random := rand.New(rand.NewSource(20260809))
	segments := []string{"gfx", "interface", "icons", "portraits", "coat_of_arms", "models", "map", "flags", "traits", "buildings", "common", "shared"}
	extensions := []string{".dds", ".png", ".mesh", ".yml", ".txt", ".asset", ".gui"}

	paths := map[string]bool{}
	for i := 0; i < 400; i++ {
		depth := 1 + random.Intn(4)
		parts := make([]string, 0, depth+1)
		for d := 0; d < depth; d++ {
			parts = append(parts, segments[random.Intn(len(segments))])
		}
		parts = append(parts, fmt.Sprintf("%s_%02d%s",
			segments[random.Intn(len(segments))], random.Intn(20), extensions[random.Intn(len(extensions))]))
		paths[strings.Join(parts, "/")] = true
	}
	// Shapes that specifically stress the fallbacks: a repeated segment name so
	// LastIndex has more than one candidate, and a path whose tail equals
	// another path in full.
	paths["gfx/icons/gfx/icons/duplicate_segment.dds"] = true
	paths["common/traits/traits.txt"] = true
	paths["shared/common/traits/traits.txt"] = true

	lookup := newResourceLookup(paths)

	queries := []string{}
	for path := range paths {
		queries = append(queries,
			path,
			strings.ToUpper(path),
			"./"+path,
			`"`+path+`"`,
			filepath.Base(path),
			strings.TrimSuffix(path, filepath.Ext(path)),
		)
		if at := strings.Index(path, "/"); at >= 0 {
			queries = append(queries, path[at+1:], strings.TrimSuffix(path[at+1:], filepath.Ext(path)))
		}
	}
	// Names that should mostly miss, including near-misses on real prefixes.
	for i := 0; i < 300; i++ {
		queries = append(queries,
			fmt.Sprintf("%s/%s_%d", segments[random.Intn(len(segments))], segments[random.Intn(len(segments))], random.Intn(50)),
			fmt.Sprintf("%s_%d%s", segments[random.Intn(len(segments))], random.Intn(50), extensions[random.Intn(len(extensions))]),
			fmt.Sprintf("%s", segments[random.Intn(len(segments))]),
		)
	}
	queries = append(queries, "", "   ", "/", "//", "gfx", "gfx/", "traits.txt", "traits")

	agreements, hits := 0, 0
	for _, query := range queries {
		want := legacyResolved(lookup, query)
		got := lookup.resolved(query)
		if got != want {
			t.Fatalf("resolved(%q) = %v, linear implementation says %v", query, got, want)
		}
		agreements++
		if want {
			hits++
		}
	}
	if hits == 0 {
		t.Fatal("no query resolved; the comparison proved nothing")
	}
	if hits == agreements {
		t.Fatal("every query resolved; the comparison proved nothing")
	}
	t.Logf("compared %d queries, %d resolved", agreements, hits)
}

func TestResourceLookupTailIndexCoversEverySegmentBoundary(t *testing.T) {
	lookup := newResourceLookup(map[string]bool{"gfx/interface/icons/faith.dds": true})
	for _, tail := range []string{"interface/icons/faith.dds", "icons/faith.dds", "faith.dds"} {
		if !lookup.hasTail(tail) {
			t.Errorf("tail index missing %q", tail)
		}
		if !lookup.hasTailPrefix(strings.TrimSuffix(tail, ".dds")) {
			t.Errorf("tail prefix index missing %q", tail)
		}
	}
	// The full path is not a tail: it has no leading slash, and lookup.exact
	// already answers for it.
	if lookup.hasTail("gfx/interface/icons/faith.dds") {
		t.Error("the whole path was indexed as a tail")
	}
	if lookup.hasTail("nterface/icons/faith.dds") {
		t.Error("a mid-segment suffix was treated as a tail")
	}
}

func benchmarkResourceLookup(b *testing.B, resolve func(resourceLookup, string) bool) {
	paths := map[string]bool{}
	for i := 0; i < 20000; i++ {
		paths[fmt.Sprintf("gfx/interface/icons/group_%03d/icon_%05d.dds", i%200, i)] = true
	}
	lookup := newResourceLookup(paths)
	queries := []string{
		"icons/group_137/icon_09137.dds", // suffix fallback, hit
		"icons/group_137/absent_icon.dds", // suffix fallback, miss
		"interface/icons/group_042",       // extensionless fallback, hit
		"interface/icons/group_999",       // extensionless fallback, miss
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		resolve(lookup, queries[i%len(queries)])
		i++
	}
}

func BenchmarkResourceLookupResolved(b *testing.B) {
	benchmarkResourceLookup(b, resourceLookup.resolved)
}

func BenchmarkResourceLookupResolvedLegacy(b *testing.B) {
	benchmarkResourceLookup(b, legacyResolved)
}
