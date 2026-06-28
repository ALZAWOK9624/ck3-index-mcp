package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanAndQueryFixture(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/traits/test_traits.txt", `test_trait = { desc = test_trait_desc }`)
	write("project/events/test_events.txt", `character_event = {
	id = test.0001
	desc = test_event_desc
	trigger = { add_gold = 5 }
	immediate = { has_trait = test_trait }
}
direct_event.1 = {
	desc = direct_event_desc
}`)
	write("project/localization/english/test_l_english.yml", `l_english:
 test_trait_desc:0 "Trait"
`)
	write("project/gfx/interface/icons/test.dds", "fake")
	write("game/common/traits/00_traits.txt", `vanilla_trait = { desc = vanilla_trait_desc }`)
	write("game/events/example_events.txt", `vanilla.0001 = {
	option = {
		name = vanilla.0001.a
		add_character_modifier = { modifier = vanilla_trait years = 1 }
	}
}`)

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
	stats, err := Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Objects < 2 || stats.Localization != 1 || stats.Resources != 1 || stats.ObjectFields == 0 {
		t.Fatalf("bad stats: %+v", stats)
	}
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	obj, err := db.QueryObject(context.Background(), "test_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 1 || obj.Definitions[0].Type != "trait" {
		t.Fatalf("bad object query: %+v", obj)
	}
	eventObj, err := db.QueryObject(context.Background(), "direct_event.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(eventObj.Definitions) != 1 || eventObj.Definitions[0].Type != "event" {
		t.Fatalf("bad direct event query: %+v", eventObj)
	}
	examples, err := db.QueryExamples(context.Background(), "trait", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(examples.Examples) == 0 || examples.Examples[0].Source != "game" {
		t.Fatalf("expected vanilla-first examples, got %+v", examples)
	}
	eventExamples, err := db.QueryExamples(context.Background(), "event", "add_character_modifier", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventExamples.Examples) == 0 || eventExamples.Examples[0].MatchLine == 0 || !strings.Contains(eventExamples.Examples[0].Snippet, "add_character_modifier") {
		t.Fatalf("expected body-matched event example, got %+v", eventExamples)
	}
	patterns, err := db.QueryPatterns(context.Background(), "trait")
	if err != nil {
		t.Fatal(err)
	}
	foundDescPattern := false
	for _, p := range patterns.Fields {
		if p.Field == "desc" && p.Count > 0 {
			foundDescPattern = true
			break
		}
	}
	if !foundDescPattern {
		t.Fatalf("expected empirical desc pattern for trait, got %+v", patterns)
	}
	report, err := db.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["error"] == 0 || report.Counts["warning"] == 0 {
		t.Fatalf("expected compiler diagnostics, got %+v", report.Counts)
	}
	inspect, err := db.LLMInspectObject(context.Background(), "test_trait", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Counts["definitions"] != 1 || inspect.Counts["incoming_refs"] == 0 || inspect.Counts["localization"] != 0 {
		t.Fatalf("bad llm inspect summary: %+v", inspect)
	}
	public, err := db.LLMInspectObject(context.Background(), "test_trait", LLMOptions{Mode: "public", AllowProject: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range public.Evidence {
		if ev.Source == "project" {
			t.Fatalf("public result leaked project evidence: %+v", public)
		}
	}
	if strings.Contains(strings.ToLower(public.Summary), "project") {
		t.Fatalf("public summary leaked project source: %+v", public.Summary)
	}
	prep, err := db.LLMPrepareEdit(context.Background(), "trait:test", LLMOptions{AllowProject: true, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Counts["examples"] == 0 || prep.Counts["patterns"] == 0 {
		t.Fatalf("expected edit prep examples: %+v", prep)
	}
}

func TestScanSkipsOverriddenObjectsButKeepsLIOSValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/traits/00_traits.txt", `test_trait = { desc = test_trait_desc }`)
	write("game/common/traits/00_traits.txt", `test_trait = { desc = vanilla_trait_desc }
vanilla_only_trait = { desc = vanilla_only_trait_desc }`)

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
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	obj, err := db.QueryObject(context.Background(), "test_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 1 || obj.Definitions[0].Source != "project" {
		t.Fatalf("expected one active project definition, got %+v", obj.Definitions)
	}
	if len(obj.Overrides) != 1 || obj.Overrides[0].Source != "project" {
		t.Fatalf("expected active-only override evidence, got %+v", obj.Overrides)
	}
	hidden, err := db.QueryObject(context.Background(), "vanilla_only_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Definitions) != 0 || len(hidden.Overrides) != 0 {
		t.Fatalf("expected overridden-only object to be excluded, got %+v", hidden)
	}

	rep, err := db.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundLIOS := false
	for _, d := range rep.Diagnostics {
		if d.Code == "lios_partial_override" {
			foundLIOS = true
			break
		}
	}
	if !foundLIOS {
		t.Fatalf("expected lios_partial_override diagnostic, got %+v", rep.Counts)
	}
}

func TestScanDoesNotTreatTriggerEventIDAsDefinition(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/events/test_events.txt", `namespace = test_chain

test_chain.0001 = {
	option = {
		name = test_chain.0001.a
		trigger_event = { id = test_chain.0002 days = 7 }
	}
}

test_chain.0002 = {
	desc = test_chain.0002.desc
}
`)

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	cfgText := `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
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
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	obj, err := db.QueryObject(context.Background(), "test_chain.0002")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 1 {
		t.Fatalf("expected one real event definition, got %+v", obj.Definitions)
	}
	if obj.Definitions[0].Line != 10 {
		t.Fatalf("expected definition at top-level event line 10, got line %d", obj.Definitions[0].Line)
	}
}

func TestScanValidatesSoundRefs(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/decisions/sound_decisions.txt", `known_sound_decision = {
	desc = known_sound_decision_desc
	soundeffect = "event:/DLC/BP1/MUSIC/moodtrack/mx_BP1Mood_Generic"
}

bad_sound_decision = {
	desc = bad_sound_decision_desc
	soundeffect = "event:/CK3_INDEX/UNKNOWN_SOUND"
}
`)

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	cfgText := `database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
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
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	refs, err := db.QueryRefs(context.Background(), "known_sound_decision")
	if err != nil {
		t.Fatal(err)
	}
	foundKnown := false
	for _, h := range refs.Outgoing {
		if h.Kind == "sound" && h.Name == "event:/DLC/BP1/MUSIC/moodtrack/mx_BP1Mood_Generic" && h.Resolved {
			foundKnown = true
			break
		}
	}
	if !foundKnown {
		t.Fatalf("expected known sound ref to be resolved, got %+v", refs.Outgoing)
	}

	diags, err := db.ExplainDiagnostic(context.Background(), "missing_sound")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "UNKNOWN_SOUND") {
		t.Fatalf("expected one missing_sound diagnostic for fake sound, got %+v", diags)
	}
	preflight, err := db.LLMPreflight(context.Background(), "bad_sound_decision", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Counts["blocking_risks"] == 0 || preflight.Counts["unresolved_refs"] == 0 {
		t.Fatalf("expected preflight to flag missing sound as blocking, got %+v", preflight)
	}
}
