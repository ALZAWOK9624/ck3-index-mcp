package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyRelOnlyIndexesCK3LoadRoots(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"common/decisions/test.txt", "script"},
		{"events/test_events.txt", "script"},
		{"localization/english/test_l_english.yml", "localization"},
		{"gfx/interface/icons/test.dds", "resource"},
		{".map-editor-backups/20260705/history/provinces/test.txt", ""},
		{"tools/generated/common/decisions/test.txt", ""},
		{"docs/examples/events/test.txt", ""},
		{"tmp/history/characters/test.txt", ""},
	}
	for _, tt := range tests {
		if got := classifyRel(tt.path); got != tt.want {
			t.Errorf("classifyRel(%q)=%q want=%q", tt.path, got, tt.want)
		}
	}
	if !shouldPruneSourceDir(".map-editor-backups") || !shouldPruneSourceDir("tools") || shouldPruneSourceDir("common") {
		t.Fatal("source-root pruning did not preserve only CK3 load roots")
	}
}

func TestQueryRefsReportsExactTotalsWhenEvidenceIsCapped(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, "common", "culture", "cultures"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "history", "characters"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "common", "culture", "cultures", "test.txt"), []byte("test_culture = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var history strings.Builder
	for i := 0; i < 510; i++ {
		fmt.Fprintf(&history, "char%d = { culture = test_culture }\n", i)
	}
	if err := os.WriteFile(filepath.Join(project, "history", "characters", "test.txt"), []byte(history.String()), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
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
	refs, err := db.QueryRefs(context.Background(), "test_culture")
	if err != nil {
		t.Fatal(err)
	}
	if refs.IncomingTotal != 510 || len(refs.Incoming) != 500 || !refs.IncomingTruncated {
		t.Fatalf("unexpected capped refs: total=%d returned=%d truncated=%v", refs.IncomingTotal, len(refs.Incoming), refs.IncomingTruncated)
	}
	got, err := db.LLMFindRefs(context.Background(), "test_culture", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["incoming"] != 510 || got.Counts["incoming_returned"] != 500 {
		t.Fatalf("LLM totals are not exact: %+v", got.Counts)
	}
}

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

func TestScanFilesRefusesToAdvanceStaleRuleVersion(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "traits", "test.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test_trait = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0644); err != nil {
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
	if _, err := db.sql.Exec(`UPDATE meta SET value='stale-test-version' WHERE key='index_rule_version'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	if err := os.WriteFile(path, []byte("test_trait = { prowess = 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = ScanFiles(context.Background(), cfg, []string{"common/traits/test.txt"})
	var fullRequired *FullScanRequiredError
	if !errors.As(err, &fullRequired) || fullRequired.Reason != "the index rule version changed" {
		t.Fatalf("expected typed stale-rule full-scan requirement, got %v", err)
	}
	db, err = Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version, err := db.metaValue(context.Background(), "index_rule_version")
	if err != nil {
		t.Fatal(err)
	}
	if version != "stale-test-version" {
		t.Fatalf("incremental scan advanced stale rule version to %q", version)
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

func TestScanHonorsModReplacePaths(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("mod/godherja.mod", `name="Godherja"
replace_path="common/culture/cultures"
replace_path = "history/provinces"
`)
	write("mod/common/culture/cultures/godherja.txt", `gallicads = { ethos = ethos_bellicose }`)
	write("mod/history/provinces/godherja.txt", `1 = { culture = gallicads }`)
	write("game/common/culture/cultures/vanilla.txt", `armenian = { ethos = ethos_stoic }`)
	write("game/history/provinces/vanilla.txt", `1 = { culture = armenian }`)
	write("game/common/traits/vanilla.txt", `vanilla_trait = { }`)

	cfgPath := filepath.Join(dir, "ck3-index.toml")
	cfgText := `database = "cache/test.sqlite"
[[source]]
name = "godherja"
path = "mod"
rank = 2
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

	armenian, err := db.QueryObject(context.Background(), "armenian")
	if err != nil {
		t.Fatal(err)
	}
	if len(armenian.Definitions) != 0 {
		t.Fatalf("expected replace_path to hide vanilla culture, got %+v", armenian.Definitions)
	}
	var overrideReason, overrideBySource, overrideRule string
	if err := db.sql.QueryRowContext(context.Background(), `SELECT override_reason,override_by_source,override_rule
		FROM files WHERE rel_path='common/culture/cultures/vanilla.txt' AND source_name='game'`).Scan(&overrideReason, &overrideBySource, &overrideRule); err != nil {
		t.Fatal(err)
	}
	if overrideReason != "descriptor_replace_path" || overrideBySource != "godherja" || overrideRule != "common/culture/cultures" {
		t.Fatalf("replace_path provenance missing: reason=%q by=%q rule=%q", overrideReason, overrideBySource, overrideRule)
	}
	trait, err := db.QueryObject(context.Background(), "vanilla_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(trait.Definitions) != 1 || trait.Definitions[0].Source != "game" {
		t.Fatalf("expected unrelated vanilla path to remain active, got %+v", trait.Definitions)
	}
	active, err := collectActiveMapFiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := active["history/provinces/vanilla.txt"]; ok {
		t.Fatal("expected map cache collection to honor history/provinces replace_path")
	}
	if _, ok := active["history/provinces/godherja.txt"]; !ok {
		t.Fatal("expected replacing source history to remain active")
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

func TestPreflightDecisionLocAndRuntimeFlags(t *testing.T) {
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
	write("project/common/decisions/test_decisions.txt", `test_coronation_decision = {
	title = test_coronation_decision.t
	desc = test_coronation_decision.desc
	is_shown = {
		is_target_in_global_variable_list = {
			name = test_global_list
			target = flag:test_runtime_flag
		}
	}
	effect = {
		add_to_global_variable_list = {
			name = test_global_list
			target = flag:test_runtime_flag
		}
	}
}`)
	write("project/localization/english/test_l_english.yml", `l_english:
 test_coronation_decision.t:0 "Coronation"
 test_coronation_decision.desc:0 "A test decision."
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

	refs, err := db.QueryRefs(context.Background(), "test_coronation_decision")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range refs.Outgoing {
		if h.Kind == "flag" && !h.Resolved {
			t.Fatalf("runtime flag refs should be treated as resolved, got %+v", h)
		}
	}
	preflight, err := db.LLMPreflight(context.Background(), "test_coronation_decision", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Counts["localization"] == 0 {
		t.Fatalf("expected decision .t/.desc localization candidates, got %+v", preflight)
	}
	if preflight.Counts["unresolved_refs"] != 0 {
		t.Fatalf("expected runtime flags to be skipped from unresolved preflight refs, got %+v", preflight)
	}
}

func TestCultureTraditionLayersResolveLayerRelativeDDS(t *testing.T) {
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
	write("project/common/culture/traditions/test_traditions.txt", `tradition_test_layer_icon = {
	layers = {
		0 = steward
		1 = indian
		4 = test_layer_icon.dds
	}
}`)
	write("project/gfx/interface/icons/culture_tradition/4-items/test_layer_icon.dds", "fake")

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

	refs, err := db.QueryRefs(context.Background(), "tradition_test_layer_icon")
	if err != nil {
		t.Fatal(err)
	}
	want := "gfx/interface/icons/culture_tradition/4-items/test_layer_icon.dds"
	found := false
	for _, h := range refs.Outgoing {
		if h.Kind == "resource" && h.Name == want && h.Resolved {
			found = true
		}
		if h.Kind == "resource" && h.Name == "test_layer_icon.dds" {
			t.Fatalf("layer-relative DDS should not be indexed as bare resource: %+v", h)
		}
	}
	if !found {
		t.Fatalf("expected resolved layer resource %q, got %+v", want, refs.Outgoing)
	}
	diags, err := db.ExplainDiagnostic(context.Background(), "missing_resource")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "test_layer_icon.dds") {
			t.Fatalf("expected no missing_resource for layer-relative icon, got %+v", d)
		}
	}
}

func TestReligionFaithReferenceClosure(t *testing.T) {
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
	write("project/common/religion/religions/test_religion.txt", `test_religion = {
	family = rf_test
	custom_faith_icons = {
		missing_custom_icon
	}
	faiths = {
		test_faith = {
			icon = missing_faith_icon
			reformed_icon = existing_faith_icon
			holy_site = test_holy_site
			holy_site = missing_holy_site
		}
	}
}`)
	write("project/common/religion/holy_sites/test_holy_sites.txt", `test_holy_site = {
	county = c_missing_county
	barony = b_existing_barony
}`)
	write("project/common/landed_titles/test_titles.txt", `b_existing_barony = {}`)
	write("project/gfx/interface/icons/faith/existing_faith_icon.dds", "fake")

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

	refs, err := db.QueryRefs(context.Background(), "test_faith")
	if err != nil {
		t.Fatal(err)
	}
	var foundIcon, foundHolySite bool
	for _, h := range refs.Outgoing {
		if h.Kind == "resource" && h.Name == "gfx/interface/icons/faith/missing_faith_icon.dds" && !h.Resolved {
			foundIcon = true
		}
		if h.Kind == "holy_site" && h.Name == "test_holy_site" && h.Resolved {
			foundHolySite = true
		}
	}
	if !foundIcon {
		t.Fatalf("expected unresolved faith icon resource, got %+v", refs.Outgoing)
	}
	if !foundHolySite {
		t.Fatalf("expected resolved holy_site ref, got %+v", refs.Outgoing)
	}

	holyRefs, err := db.QueryRefs(context.Background(), "test_holy_site")
	if err != nil {
		t.Fatal(err)
	}
	var foundCounty bool
	for _, h := range holyRefs.Outgoing {
		if h.Kind == "title" && h.Name == "c_missing_county" && !h.Resolved {
			foundCounty = true
		}
	}
	if !foundCounty {
		t.Fatalf("expected unresolved holy site county title, got %+v", holyRefs.Outgoing)
	}

	diags, err := db.CachedValidation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := []string{
		`resource "gfx/interface/icons/faith/missing_faith_icon.dds"`,
		`resource "gfx/interface/icons/faith/missing_custom_icon.dds"`,
		`holy_site "missing_holy_site"`,
		`title "c_missing_county"`,
	}
	for _, want := range wantMessages {
		found := false
		for _, d := range diags.Diagnostics {
			if strings.Contains(d.Message, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected diagnostic containing %q, got %+v", want, diags.Diagnostics)
		}
	}
}

func TestMenAtArmsCanRecruitCultureScopeMismatch(t *testing.T) {
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
	write("project/common/men_at_arms_types/test_maa.txt", `bad_maa = {
	type = heavy_infantry
	can_recruit = {
		has_cultural_parameter = unlock_bad_maa
	}
}
wrapped_maa = {
	type = heavy_infantry
	can_recruit = {
		culture = { has_cultural_parameter = unlock_wrapped_maa }
	}
}
vanilla_style_maa = {
	type = heavy_infantry
	can_recruit = {
		valid_for_maa_trigger = { PARAMETER = unlock_maa_vanilla_style }
	}
}
accolade_style_maa = {
	type = heavy_infantry
	can_recruit = {
		any_active_accolade = {
			accolade_rank >= 3
		}
	}
}`)

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

	diags, err := db.ExplainDiagnostic(context.Background(), "scope_mismatch")
	if err != nil {
		t.Fatal(err)
	}
	badFound := false
	for _, d := range diags {
		if strings.Contains(d.Message, "bad_maa") && strings.Contains(d.Message, "culture = { ... }") && strings.Contains(d.Message, "valid_for_maa_trigger") {
			badFound = true
		}
		if strings.Contains(d.Message, "wrapped_maa") || strings.Contains(d.Message, "vanilla_style_maa") || strings.Contains(d.Message, "accolade_style_maa") {
			t.Fatalf("expected no MAA can_recruit diagnostic for valid styles, got %+v", d)
		}
	}
	if !badFound {
		t.Fatalf("expected scope_mismatch for direct culture trigger in can_recruit, got %+v", diags)
	}

	preflight, err := db.LLMPreflight(context.Background(), "bad_maa", LLMOptions{AllowProject: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Counts["nonblocking_risks"] == 0 || preflight.Counts["diagnostics"] == 0 {
		t.Fatalf("expected preflight to include can_recruit scope diagnostic, got %+v", preflight)
	}
	preflightText := ""
	for _, ev := range preflight.Evidence {
		preflightText += ev.Detail + "\n"
	}
	if !strings.Contains(preflightText, "can_recruit scope mismatch") {
		t.Fatalf("expected preflight evidence to mention can_recruit scope mismatch, got %+v", preflight.Evidence)
	}
}

func TestScanFilesRefreshesCurrentProjectFiles(t *testing.T) {
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
	write("project/common/decisions/test_incremental.txt", `test_incremental_decision = {
	desc = test_incremental_decision.desc
	is_shown = { always = yes }
	effect = { add_prestige = 10 }
}`)
	write("project/localization/english/test_incremental_l_english.yml", `l_english:
 test_incremental_decision.desc:0 "Old desc"
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
	write("project/common/decisions/test_incremental.txt", `test_incremental_decision = {
	desc = test_incremental_decision.desc
	is_shown = { always = yes }
	effect = { add_prestige = 20 }
}
test_incremental_decision_two = {
	desc = test_incremental_decision_two.desc
	is_shown = { always = yes }
	effect = { add_prestige = 5 }
}`)
	write("project/localization/english/test_incremental_l_english.yml", `l_english:
 test_incremental_decision.desc:0 "New desc"
 test_incremental_decision_two.desc:0 "Second desc"
`)
	stats, err := ScanFiles(context.Background(), cfg, []string{
		"common/decisions/test_incremental.txt",
		"localization/english/test_incremental_l_english.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 {
		t.Fatalf("expected 2 refreshed files, got %+v", stats)
	}
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	obj, err := db.QueryObject(context.Background(), "test_incremental_decision_two")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.Definitions) != 1 {
		t.Fatalf("expected refreshed second decision definition, got %+v", obj)
	}
	loc, err := db.QueryLocalization(context.Background(), "test_incremental_decision_two.desc")
	if err != nil {
		t.Fatal(err)
	}
	if len(loc.Values) != 1 || !strings.Contains(loc.Values[0].Value, "Second desc") {
		t.Fatalf("expected refreshed localization, got %+v", loc)
	}
	pf, err := db.LLMPreflight(context.Background(), "test_incremental_decision_two", LLMOptions{AllowProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if pf.Counts["unresolved_refs"] != 0 {
		t.Fatalf("expected scoped resolver to resolve refreshed refs, got %+v", pf)
	}
}
