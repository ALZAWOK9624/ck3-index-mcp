package indexer

import "testing"

func TestLookupShapeUsesCurrentEngineDocumentation(t *testing.T) {
	if engineShapeTableVersion != "1.19.0-engine-log" {
		t.Fatalf("shape snapshot version = %q", engineShapeTableVersion)
	}
	got := LookupShape(" ALWAYS ")
	if got == nil {
		t.Fatal("always documentation was not found")
	}
	if got.Key != "always" || got.EvidenceKind != "documented_usage" {
		t.Fatalf("always lookup = %+v", got)
	}
	if len(got.Documentation) != 1 {
		t.Fatalf("always documentation count = %d, want 1", len(got.Documentation))
	}
	doc := got.Documentation[0]
	if doc.RuleKind != "trigger" || doc.Source != "engine_logs/triggers.log" {
		t.Fatalf("always documentation provenance = %+v", doc)
	}
	if len(doc.Examples) < 3 || doc.Examples[0] != "always = yes # always succeeds" {
		t.Fatalf("always examples = %#v", doc.Examples)
	}
}

func TestLookupShapeKeepsTriggerAndEffectDocumentationSeparate(t *testing.T) {
	got := LookupShape("add_to_temporary_list")
	if got == nil {
		t.Fatal("add_to_temporary_list documentation was not found")
	}
	var trigger, effect *ShapeDocumentation
	for i := range got.Documentation {
		doc := &got.Documentation[i]
		switch doc.RuleKind {
		case "trigger":
			trigger = doc
		case "effect":
			effect = doc
		}
	}
	if trigger == nil || effect == nil {
		t.Fatalf("separate trigger/effect documentation missing: %+v", got.Documentation)
	}
	if trigger.Source != "engine_logs/triggers.log" || effect.Source != "engine_logs/effects.log" {
		t.Fatalf("unexpected documentation sources: trigger=%+v effect=%+v", trigger, effect)
	}
	if len(effect.Examples) == 0 {
		t.Fatalf("effect examples were not retained: %+v", effect)
	}
}

func TestLookupShapeDoesNotUseLegacyRemovedRule(t *testing.T) {
	if got := LookupShape("is_accolade_active"); got != nil {
		t.Fatalf("legacy-only shape data leaked into current lookup: %+v", got)
	}
}
