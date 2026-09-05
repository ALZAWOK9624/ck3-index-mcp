package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func algebraDiagnostics(t *testing.T, source string) []ctxDiag {
	t.Helper()
	parsed := script.Parse(source)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors: %+v", parsed.Errors)
	}
	return checkTriggerAlgebra(parsed.Nodes, "common/scripted_triggers/test.txt")
}

func assertAlgebraCodes(t *testing.T, source string, wantCodes map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, diagnostic := range algebraDiagnostics(t, source) {
		got[diagnostic.code]++
	}
	for code, want := range wantCodes {
		if got[code] != want {
			t.Fatalf("code %q fired %d times, want %d\nsource:\n%s\nall: %+v", code, got[code], want, source, got)
		}
	}
	for code, count := range got {
		if _, expected := wantCodes[code]; !expected {
			t.Fatalf("unexpected code %q fired %d times\nsource:\n%s", code, count, source)
		}
	}
}

func TestTriggerAlgebraFlattensNestedSameOperator(t *testing.T) {
	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		AND = {
			is_adult = yes
			is_ruler = yes
		}
	}
}`, map[string]int{"trigger_nested_same_operator": 1})

	// An AND inside an OR is a real branch and must survive untouched.
	assertAlgebraCodes(t, `rule = {
	OR = {
		has_trait = brave
		AND = {
			is_adult = yes
			is_ruler = yes
		}
	}
}`, map[string]int{})
}

func TestTriggerAlgebraFindsRepeatedAndContradictoryConditions(t *testing.T) {
	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		is_adult = yes
		has_trait = brave
	}
}`, map[string]int{"trigger_duplicate_condition": 1})

	// Same key, different value: two different questions.
	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		has_trait = ambitious
	}
}`, map[string]int{})

	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		NOT = { has_trait = brave }
	}
}`, map[string]int{"trigger_always_false": 1})

	assertAlgebraCodes(t, `rule = {
	OR = {
		has_trait = brave
		NOT = { has_trait = brave }
	}
}`, map[string]int{"trigger_always_true": 1})

	// The negation of a different value is an ordinary pair of conditions.
	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		NOT = { has_trait = craven }
	}
}`, map[string]int{})
}

func TestTriggerAlgebraCollapsesDoubleNegation(t *testing.T) {
	assertAlgebraCodes(t, `rule = {
	NOT = { NOT = { has_trait = brave } }
}`, map[string]int{"trigger_double_negation": 1})

	assertAlgebraCodes(t, `rule = {
	NOT = {
		NOR = {
			has_trait = brave
			has_trait = ambitious
		}
	}
}`, map[string]int{"trigger_double_negation": 1})

	// NOT with several members is a NOR, not a double negation.
	assertAlgebraCodes(t, `rule = {
	NOT = {
		has_trait = brave
		has_trait = ambitious
	}
}`, map[string]int{})
}

func TestTriggerAlgebraLiftsConditionCommonToEveryBranch(t *testing.T) {
	assertAlgebraCodes(t, `rule = {
	OR = {
		AND = {
			is_ruler = yes
			has_trait = brave
		}
		AND = {
			is_ruler = yes
			has_trait = ambitious
		}
	}
}`, map[string]int{"trigger_common_condition": 1})

	// One branch without the shared condition means it is not shared.
	assertAlgebraCodes(t, `rule = {
	OR = {
		AND = {
			is_ruler = yes
			has_trait = brave
		}
		AND = {
			is_adult = yes
			has_trait = ambitious
		}
	}
}`, map[string]int{})
}

func TestTriggerAlgebraFindsAbsorbedBranches(t *testing.T) {
	// X OR (X AND Y) is just X.
	assertAlgebraCodes(t, `rule = {
	OR = {
		has_trait = brave
		AND = {
			has_trait = brave
			is_adult = yes
		}
	}
}`, map[string]int{"trigger_absorbed_branch": 1})

	// X AND (X OR Y) is just X.
	assertAlgebraCodes(t, `rule = {
	AND = {
		has_trait = brave
		OR = {
			has_trait = brave
			is_adult = yes
		}
	}
}`, map[string]int{"trigger_absorbed_branch": 1})

	// A branch that shares nothing with the plain conditions decides something.
	assertAlgebraCodes(t, `rule = {
	OR = {
		has_trait = brave
		AND = {
			has_trait = ambitious
			is_adult = yes
		}
	}
}`, map[string]int{})
}

// The rules must never leave an explicit connective. A repeated line in an
// effect block is a second effect, not a redundant check.
func TestTriggerAlgebraIgnoresNonBooleanContainers(t *testing.T) {
	assertAlgebraCodes(t, `some_effect = {
	effect = {
		add_gold = 5
		add_gold = 5
	}
	limit = {
		has_trait = brave
		has_trait = brave
	}
}`, map[string]int{})
}

func hiddenScopeDiagnostics(t *testing.T, source, relPath string) []ctxDiag {
	t.Helper()
	parsed := script.Parse(source)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors: %+v", parsed.Errors)
	}
	return checkHiddenScopeDependency(parsed.Nodes, relPath)
}

func TestHiddenScopeDependencyFlagsTopLevelPrev(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh_check_effect = {
	prev = { add_gold = 100 }
}`, "common/scripted_effects/gh_rewards.txt")
	if lintCodeCount(found, "hidden_scope_dependency") != 1 {
		t.Fatalf("top-level prev was not reported: %+v", found)
	}
}

// root inside a scripted effect is CK3's own house style, used thousands of
// times in vanilla. Reporting it would bury every real finding.
func TestHiddenScopeDependencyLeavesRootAlone(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh_give_reward_effect = {
	root = { add_gold = 100 }
	scope:recipient = { add_gold = root.gold }
}`, "common/scripted_effects/gh_rewards.txt")
	if len(found) != 0 {
		t.Fatalf("root inside a scripted effect was reported: %+v", found)
	}
}

// prev below a scope the macro entered itself refers to that scope, which is
// exactly the pattern the root finding tells people to use.
func TestHiddenScopeDependencyAcceptsPrevBelowAnEnteredScope(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh_reward_liege_effect = {
	liege = {
		prev = { add_gold = 100 }
	}
}`, "common/scripted_effects/gh_rewards.txt")
	if len(found) != 0 {
		t.Fatalf("prev under an entered scope was reported: %+v", found)
	}
}

// A macro whose name states the requirement has documented it where every
// caller already looks.
func TestHiddenScopeDependencyRespectsNamedRequirement(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh_prev_gains_gold_effect = {
	prev = { add_gold = 100 }
}`, "common/scripted_effects/gh_rewards.txt")
	if len(found) != 0 {
		t.Fatalf("a macro naming its prev requirement was still reported: %+v", found)
	}
}

// Events and decisions run in a scope the file itself establishes, so prev
// there resolves against a scope the same file opened. The check belongs to
// reusable macros only.
func TestHiddenScopeDependencyStaysOutOfEventFiles(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh.0001 = {
	immediate = {
		prev = { add_gold = 100 }
	}
}`, "events/gh_events.txt")
	if len(found) != 0 {
		t.Fatalf("prev in an event file was reported: %+v", found)
	}
}

// prevailing_wind is a name, not the prev scope.
func TestHiddenScopeDependencyDoesNotMatchSubstrings(t *testing.T) {
	found := hiddenScopeDiagnostics(t, `gh_trait_effect = {
	add_trait = prevailing_wind
}`, "common/scripted_effects/gh_rewards.txt")
	if len(found) != 0 {
		t.Fatalf("an identifier merely starting with prev was reported: %+v", found)
	}
}
