package indexer

import "testing"

func TestEngineModifiersUseCurrentLogAndVanillaFormats(t *testing.T) {
	if err := ConfigureEngineRules(""); err != nil {
		t.Fatal(err)
	}
	if got := LookupModifier("subject_salary_income_gold_add"); !got.Found || len(got.UseAreas) != 0 || got.Source != "vanilla_modifier_format" {
		t.Fatalf("template-derived concrete modifier should not inherit an unproven use area: %+v", got)
	}
	if got := LookupModifier("prowess_no_portrait"); !got.Found || len(got.UseAreas) != 1 || got.UseAreas[0] != "character" || got.Source != "engine_log+vanilla_modifier_format" {
		t.Fatalf("current modifier with intervening Extra info was not parsed from the 1.19 log: %+v", got)
	}
	if got := LookupModifier("afar_opinion"); !got.Found || len(got.UseAreas) != 0 || got.Source != "vanilla_modifier_format" {
		t.Fatalf("ambiguous current modifier template should not inherit a guessed use area: %+v", got)
	}
	if got := LookupModifier("ck3_index_not_a_real_modifier"); got.Found {
		t.Fatalf("unknown modifier was accepted: %+v", got)
	}
	if got := LookupModifier("magic_focused_ai_rationality"); !got.Found || got.Source != "engine_log_template" {
		t.Fatalf("generated vassal-stance modifier was not matched: %+v", got)
	}
	if got := LookupModifier("world_ga_aironoi_development_growth"); !got.Found {
		t.Fatalf("generated geographical-region modifier was not matched: %+v", got)
	}
	if got := LookupModifier("k10_archers_damage_add"); !got.Found {
		t.Fatalf("generated men-at-arms modifier was not matched: %+v", got)
	}
	if got := LookupModifier("garrison_size_mult"); got.Found {
		t.Fatalf("obsolete static modifier was accepted as a generated template: %+v", got)
	}
}
