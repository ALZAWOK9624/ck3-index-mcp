package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/script"
)

func TestEventSemanticRefsCoverCurrentOnActionForms(t *testing.T) {
	parsed := script.Parse(`root_action = {
	events = { always.1 delay = { days = 1 } always.2 }
	random_events = { chance_to_happen = 25 100 = random.1 50 = 0 }
	first_valid = { first.1 first.2 }
	on_actions = { child_action }
	random_on_actions = { 100 = random_child 25 = 0 }
	first_valid_on_action = { first_child }
	fallback = fallback_child
	effect = {
		set_variable = { name = tracked_value value = 1 }
		has_variable = tracked_value
		add_character_flag = { flag = tracked_flag days = 5 }
		has_character_flag = tracked_flag
		has_innovation = innovation_test
		trigger_event = direct.1
		trigger_event = { id = delayed.1 days = 2 }
		trigger_event = { on_action = triggered_child }
	}
}`)
	rec := fileRecord{ID: 1, RelPath: "common/on_action/test.txt", SourceName: "project", SourceRank: 1}
	objects := extractObjects(rec, parsed.Nodes)
	refs := extractRefs(rec, parsed.Nodes, objects)

	type expected struct{ kind, name, relation, phase string }
	want := []expected{
		{"event", "always.1", "events", "events"},
		{"event", "always.2", "events", "events"},
		{"event", "random.1", "random_events", "random_events"},
		{"event", "first.1", "first_valid", "first_valid"},
		{"on_action", "child_action", "on_actions", "on_actions"},
		{"on_action", "random_child", "random_on_actions", "random_on_actions"},
		{"on_action", "first_child", "first_valid_on_action", "first_valid_on_action"},
		{"on_action", "fallback_child", "fallback", ""},
		{"event", "direct.1", "trigger_event", "effect"},
		{"event", "delayed.1", "trigger_event", "effect"},
		{"on_action", "triggered_child", "trigger_on_action", "effect"},
		{"variable", "tracked_value", "set_variable", "effect"},
		{"variable", "tracked_value", "read_variable", "effect"},
		{"character_flag", "tracked_flag", "add_character_flag", "effect"},
		{"character_flag", "tracked_flag", "read_character_flag", "effect"},
		{"innovation", "innovation_test", "has_innovation", "effect"},
	}
	for _, item := range want {
		found := false
		for _, ref := range refs {
			if ref.Kind == item.kind && ref.Name == item.name && ref.Relation == item.relation && ref.Phase == item.phase && ref.Confidence == "exact" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing semantic ref %+v in %+v", item, refs)
		}
	}
	for _, ref := range refs {
		if ref.Name == "0" || ref.Name == "25" || ref.Name == "50" || ref.Name == "100" {
			t.Errorf("weight/control value became a semantic target: %+v", ref)
		}
	}
}

func TestDefinitionResolutionOverrideEvidenceAndRefReasons(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/traits/project.txt", "shared_trait = { desc = shared_trait_desc }\nambiguous_trait = {}\n")
	write("project/common/traits/project_2.txt", "ambiguous_trait = {}\n")
	write("game/common/traits/game.txt", "shared_trait = {}\n")
	write("project/common/traits/00_same.txt", "same_path_trait = {}\n")
	write("game/common/traits/00_same.txt", "same_path_trait = {}\nhidden_trait = {}\n")
	write("project/common/on_action/project.txt", `merged_action = {
	events = { existing.1 missing.1 }
	effect = { exists = scope:runtime_target scope:runtime_target = { trigger_event = missing.2 } }
}`)
	write("game/common/on_action/game.txt", "merged_action = { events = { existing.1 } }\n")
	write("game/events/existing.txt", "existing.1 = { type = character_event title = existing.1.t desc = existing.1.desc triggered_only = yes }\n")

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	cfgText := `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
[[source]]
name = "game"
path = "game"
rank = 3
`
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0644); err != nil {
		t.Fatal(err)
	}
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
	defer db.Close()

	shared, err := db.QueryObject(context.Background(), "trait:shared_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.Resolution) != 1 || shared.Resolution[0].Status != "source_priority" || shared.Resolution[0].ActiveCount != 1 {
		t.Fatalf("expected source-priority resolution, got %+v", shared)
	}
	if len(shared.Definitions) != 2 || shared.Definitions[0].Status != "active" || shared.Definitions[1].Status != "shadowed_by_source_priority" {
		t.Fatalf("definition statuses missing: %+v", shared.Definitions)
	}
	if shared.Definitions[0].EndLine < shared.Definitions[0].Line {
		t.Fatalf("definition source range missing: %+v", shared.Definitions[0])
	}

	ambiguous, err := db.QueryObject(context.Background(), "trait:ambiguous_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(ambiguous.Resolution) != 1 || ambiguous.Resolution[0].Status != "ambiguous" || ambiguous.Resolution[0].ActiveCount != 0 {
		t.Fatalf("same-rank ambiguity was hidden: %+v", ambiguous)
	}

	merged, err := db.QueryObject(context.Background(), "on_action:merged_action")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Resolution) != 1 || merged.Resolution[0].Mode != "merge" || merged.Resolution[0].ActiveCount != 2 {
		t.Fatalf("on_action merge semantics missing: %+v", merged)
	}
	event, err := db.QueryObject(context.Background(), "event:existing.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(event.EventProfiles) != 1 || len(event.EventProfiles[0].Fields) != 4 {
		t.Fatalf("event field profile missing: %+v", event.EventProfiles)
	}
	foundType := false
	for _, field := range event.EventProfiles[0].Fields {
		if field.Field == "type" && field.Raw == "type = character_event" {
			foundType = true
		}
	}
	if !foundType {
		t.Fatalf("event type evidence missing: %+v", event.EventProfiles[0].Fields)
	}

	same, err := db.QueryObject(context.Background(), "trait:same_path_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(same.FileOverrides) != 2 || same.FileOverrides[1].OverrideReason != "same_relative_path" || same.FileOverrides[1].OverrideBySource != "project" {
		t.Fatalf("same-path file override reason missing: %+v", same.FileOverrides)
	}

	missing, err := db.QueryRefs(context.Background(), "event:missing.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Incoming) != 1 || missing.Incoming[0].Resolution != "unresolved" || missing.Incoming[0].ResolutionReason != "missing_definition" || missing.Incoming[0].Relation != "events" {
		t.Fatalf("missing event reason/relationship missing: %+v", missing.Incoming)
	}
	runtime, err := db.QueryRefs(context.Background(), "scope:runtime_target")
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Incoming) != 1 || runtime.Incoming[0].Resolution != "dynamic" || runtime.Incoming[0].ResolutionReason != "runtime_scope" {
		t.Fatalf("runtime scope was presented as an unexplained unresolved ref: %+v", runtime.Incoming)
	}
}
