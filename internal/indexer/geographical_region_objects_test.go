package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGeographicalRegionsAreFirstClassSemanticObjects(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMapContextFixture(t, dir)
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(dir, "project", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("common/scripted_triggers/region_refs.txt", `region_reference_test = {
	geographical_region = test_region
	culture_overlaps_geographical_region = test_region
}`)
	write("common/situation/situations/region_refs.txt", `region_situation_test = {
	sub_regions = {
		fixture = { geographical_regions = { test_region } }
	}
}`)
	write("common/buildings/region_refs.txt", `region_building_test = {
	asset = { graphical_regions = { test_region } }
}`)

	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	obj, err := db.QueryObject(context.Background(), "geographical_region:test_region")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 1 || obj.Definitions[0].Type != "geographical_region" {
		t.Fatalf("expected a typed geographical region definition, got %+v", obj)
	}

	patterns, err := db.QueryPatterns(context.Background(), "geographical_region")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, field := range patterns.Fields {
		fields[field.Field] = true
	}
	if !fields["provinces"] || !fields["regions"] {
		t.Fatalf("expected region membership fields in empirical patterns, got %+v", patterns.Fields)
	}

	parentRefs, err := db.QueryRefs(context.Background(), "geographical_region:parent_region")
	if err != nil {
		t.Fatal(err)
	}
	if parentRefs.OutgoingTotal != 1 || len(parentRefs.Outgoing) != 1 || parentRefs.Outgoing[0].Kind != "geographical_region" || parentRefs.Outgoing[0].Name != "test_region" || !parentRefs.Outgoing[0].Resolved {
		t.Fatalf("expected resolved parent-to-child region dependency, got %+v", parentRefs)
	}

	refs, err := db.QueryRefs(context.Background(), "geographical_region:test_region")
	if err != nil {
		t.Fatal(err)
	}
	if refs.IncomingTotal != 5 {
		t.Fatalf("expected parent, trigger, situation, and graphical-region references, got %+v", refs)
	}
	for _, ref := range refs.Incoming {
		if ref.Kind != "geographical_region" || !ref.Resolved {
			t.Fatalf("expected exact resolved geographical-region reference, got %+v", ref)
		}
	}

	search, err := db.LLMSearch(context.Background(), SearchOptions{Query: "test_region", LLMOptions: LLMOptions{AllowProject: true, Limit: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Evidence) == 0 || search.Evidence[0].Kind != "object" || search.Evidence[0].Type != "geographical_region" || search.Evidence[0].Name != "test_region" {
		t.Fatalf("expected exact region object to rank first, got %+v", search.Evidence)
	}

	inspect, err := db.LLMInspectObject(context.Background(), "geographical_region:test_region", LLMOptions{AllowProject: true, Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Counts["definitions"] != 1 || inspect.Counts["incoming_refs"] != 5 {
		t.Fatalf("expected region definition and references in inspect result, got %+v", inspect.Counts)
	}

	graph, err := db.LLMDependencyGraph(context.Background(), "geographical_region:test_region", LLMOptions{AllowProject: true, Limit: 8, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if graph.Counts["definitions"] != 1 || graph.Counts["incoming_refs"] != 5 || graph.Counts["unresolved"] != 0 {
		t.Fatalf("expected the dependency graph to reuse typed region refs, got %+v", graph.Counts)
	}
}

func TestGeographicalRegionDefinitionPathsStayInSync(t *testing.T) {
	paths := []string{
		"map_data/geographical_regions.txt",
		"map_data/geographical_region.txt",
		"map_data/geographical_regions/nested.txt",
		"map_data/island_region.txt",
		"common/geographical_region/test.txt",
		"common/geographical_regions/test.txt",
	}
	for _, path := range paths {
		if !isMapContextRel(path) {
			t.Errorf("map context rejected region path %q", path)
		}
		if got := objectTypeForPath(path); got != "geographical_region" {
			t.Errorf("objectTypeForPath(%q)=%q, want geographical_region", path, got)
		}
	}
	if isGeographicalRegionDefinitionsPath("common/decisions/not_a_region.txt") {
		t.Fatal("ordinary script path was misclassified as a geographical-region definition")
	}
}
