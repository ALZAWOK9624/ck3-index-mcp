package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setupEventTopologyDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/events/chain.txt", `chain.a = {
	type = character_event
	title = chain.a.t
	immediate = {
		trigger_event = chain.b
		trigger_event = missing.1
	}
}
chain.b = { type = character_event immediate = { trigger_event = chain.c } }
chain.c = { type = province_event immediate = { trigger_event = chain.b } }
chain.caller = { type = character_event immediate = { trigger_event = chain.a } }
chain.self = { type = character_event immediate = { trigger_event = chain.self } }
`)
	write("project/common/on_action/chain.txt", `root_action = {
	events = { chain.a }
	on_actions = { child_action }
}
child_action = { events = { chain.c } }
`)
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	write("ck3-index.toml", `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEventChainCalleesExposeCyclesUnresolvedAndShortestPaths(t *testing.T) {
	db := setupEventTopologyDB(t)
	got, err := db.LLMEventChain(context.Background(), "event:chain.a", EventChainOptions{
		LLMOptions: LLMOptions{AllowProject: true, Depth: 3, Limit: 8}, Direction: "callees", IncludeOnActions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Topology == nil {
		t.Fatal("expected topology payload")
	}
	wantNodes := map[string]bool{"event:chain.a": true, "event:chain.b": true, "event:chain.c": true}
	for _, node := range got.Topology.Nodes {
		delete(wantNodes, node.ID)
		if node.ID == "event:chain.a" && (node.EventType != "character_event" || node.Title != "chain.a.t") {
			t.Fatalf("event profile was not retained on topology node: %+v", node)
		}
	}
	if len(wantNodes) != 0 {
		t.Fatalf("missing resolved callee nodes: %+v in %+v", wantNodes, got.Topology.Nodes)
	}
	if got.Counts["unresolved"] != 1 {
		t.Fatalf("expected one unresolved call, got %+v", got.Counts)
	}
	foundMissingEdge := false
	for _, edge := range got.Topology.Edges {
		if edge.To == "event:missing.1" && edge.Resolution == "unresolved" {
			foundMissingEdge = true
		}
	}
	if !foundMissingEdge {
		t.Fatalf("unresolved call evidence missing: %+v", got.Topology.Edges)
	}
	for _, node := range got.Topology.Nodes {
		if node.ID == "event:missing.1" {
			t.Fatalf("unresolved target was invented as a node: %+v", node)
		}
	}
	if len(got.Topology.Cycles) != 1 || len(got.Topology.Cycles[0]) != 2 {
		t.Fatalf("expected chain.b/chain.c cycle, got %+v", got.Topology.Cycles)
	}
	foundPath := false
	for _, path := range got.Topology.PathsFromCenter {
		if path.To == "event:chain.c" && len(path.Nodes) == 3 && path.Nodes[0] == "event:chain.a" && path.Nodes[2] == "event:chain.c" {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("shortest path to chain.c missing: %+v", got.Topology.PathsFromCenter)
	}
}

func TestEventChainCallersAndOnActionFilter(t *testing.T) {
	db := setupEventTopologyDB(t)
	withActions, err := db.LLMEventChain(context.Background(), "chain.a", EventChainOptions{
		LLMOptions: LLMOptions{AllowProject: true, Depth: 2, Limit: 8}, Direction: "callers", IncludeOnActions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, node := range withActions.Topology.Nodes {
		seen[node.ID] = true
	}
	if !seen["event:chain.caller"] || !seen["on_action:root_action"] {
		t.Fatalf("caller hierarchy missing event or on_action caller: %+v", withActions.Topology.Nodes)
	}

	withoutActions, err := db.LLMEventChain(context.Background(), "chain.a", EventChainOptions{
		LLMOptions: LLMOptions{AllowProject: true, Depth: 2, Limit: 8}, Direction: "callers", IncludeOnActions: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range withoutActions.Topology.Nodes {
		if node.Type == "on_action" {
			t.Fatalf("include_on_actions=false retained %+v", node)
		}
	}
}

func TestEventChainDetectsSelfLoopAndPublicModeRedactsProject(t *testing.T) {
	db := setupEventTopologyDB(t)
	self, err := db.LLMEventChain(context.Background(), "event:chain.self", EventChainOptions{
		LLMOptions: LLMOptions{AllowProject: true, Depth: 1}, Direction: "callees", IncludeOnActions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(self.Topology.Cycles) != 1 || len(self.Topology.Cycles[0]) != 1 || self.Topology.Cycles[0][0] != "event:chain.self" {
		t.Fatalf("self loop not reported: %+v", self.Topology.Cycles)
	}

	public, err := db.LLMEventChain(context.Background(), "event:chain.a", EventChainOptions{
		LLMOptions: LLMOptions{Mode: "public", Depth: 2}, Direction: "both", IncludeOnActions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if public.Redacted == 0 {
		t.Fatalf("expected project edges to be redacted: %+v", public)
	}
	for _, edge := range public.Topology.Edges {
		if edge.Source == "project" || edge.Path != "" {
			t.Fatalf("public topology leaked project evidence: %+v", edge)
		}
	}
}
