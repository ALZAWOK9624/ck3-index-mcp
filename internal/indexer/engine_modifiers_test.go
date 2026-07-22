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
}
