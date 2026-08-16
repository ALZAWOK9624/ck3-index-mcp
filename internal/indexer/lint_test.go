package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"ck3-index/internal/script"
)

func lintTestDiagnostics(t *testing.T, source, path string) []ctxDiag {
	t.Helper()
	parsed := script.Parse(source)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors: %+v", parsed.Errors)
	}
	return checkScriptLint(parsed.Nodes, path, SourceRoleProject, nil)
}

func lintCodeCount(diagnostics []ctxDiag, code string) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.code == code {
			count++
		}
	}
	return count
}

func TestTriggerElseTerminatorOnlyChecksAdjacentElseIfChains(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{
			name: "independent trigger if blocks are not a chain",
			source: `rule = {
	trigger_if = { always = yes }
	trigger_if = { always = no }
}`,
			want: 0,
		},
		{
			name: "complete chain is valid",
			source: `rule = {
	trigger_if = { always = yes }
	trigger_else_if = { always = no }
	trigger_else = { always = yes }
}`,
			want: 0,
		},
		{
			name: "else if chain without else is reported once",
			source: `rule = {
	trigger_if = { always = yes }
	trigger_else_if = { always = no }
}`,
			want: 1,
		},
		{
			name: "distant else does not close a chain",
			source: `rule = {
	trigger_if = { always = yes }
	trigger_else_if = { always = no }
	custom_tooltip = { always = yes }
	trigger_else = { always = yes }
}`,
			want: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := lintTestDiagnostics(t, test.source, "common/test.txt")
			if got := lintCodeCount(diagnostics, "missing_trigger_else"); got != test.want {
				t.Fatalf("missing_trigger_else = %d, want %d; diagnostics=%+v", got, test.want, diagnostics)
			}
		})
	}
}

func TestEventHasOptionOnlyChecksVisibleNumericEventDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{
			name: "helper named event is not an event definition",
			source: `coronation_events_0200_available_chaplain_trigger = {
	always = yes
}`,
			want: 0,
		},
		{
			name: "dotted helper is not a numeric event id",
			source: `cultural_festival.3002.add_hook = {
	always = yes
}`,
			want: 0,
		},
		{
			name: "hidden numeric event can omit option",
			source: `coronation_events.0050 = {
	type = activity_event
	hidden = yes
	immediate = { always = yes }
}`,
			want: 0,
		},
		{
			name: "visible numeric event without option is reported",
			source: `coronation_events.0051 = {
	type = activity_event
}`,
			want: 1,
		},
		{
			name: "visible numeric event with option is valid",
			source: `coronation_events.0052 = {
	type = activity_event
	option = { name = ok }
}`,
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := lintTestDiagnostics(t, test.source, "events/test.txt")
			if got := lintCodeCount(diagnostics, "event_no_option"); got != test.want {
				t.Fatalf("event_no_option = %d, want %d; diagnostics=%+v", got, test.want, diagnostics)
			}
		})
	}
}

func TestEventHasOptionIgnoresNonEventLoadRoots(t *testing.T) {
	diagnostics := lintTestDiagnostics(t, `example.1 = { type = character_event }`, "common/custom_events/test.txt")
	if got := lintCodeCount(diagnostics, "event_no_option"); got != 0 {
		t.Fatalf("event_no_option = %d, want 0 outside events root; diagnostics=%+v", got, diagnostics)
	}
}

func TestNumericEventIDRequiresOrdinaryAssignment(t *testing.T) {
	if isNumericEventID(&script.Node{Key: "helper.0001", Kind: "block", Operator: "scripted_effect"}) {
		t.Fatal("scripted-effect declaration must not be treated as an event definition")
	}
}

func TestLintDoesNotGuessSavedScopeLifetime(t *testing.T) {
	source := `example.1 = {
	type = character_event
	option = {
		hidden_effect = {
			save_scope_as = local_scope
			switch = {
				trigger = yes
				scope:weapon = { always = yes }
			}
			scope:player = { always = yes }
		}
	}
}`
	diagnostics := lintTestDiagnostics(t, source, "events/test.txt")
	if got := lintCodeCount(diagnostics, "scope_never_saved"); got != 0 {
		t.Fatalf("scope_never_saved = %d, want 0; diagnostics=%+v", got, diagnostics)
	}
}

func TestScriptedEffectRecursionIsScopedToItsOwnDefinition(t *testing.T) {
	parsed := script.Parse(`first_effect = {
	second_effect = yes
}
second_effect = {
	first_effect = yes
}
self_recursive_effect = {
	if = {
		limit = { always = yes }
		self_recursive_effect = yes
	}
}`)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors: %+v", parsed.Errors)
	}
	counts := map[string]int{}
	for _, definition := range parsed.Nodes {
		if definition.Kind != "block" {
			continue
		}
		counts[definition.Key] = lintCodeCount(
			checkScriptEffectRecursion(definition.Children, "common/scripted_effects/test.txt", definition.Key),
			"scripted_effect_recursion",
		)
	}
	if counts["first_effect"] != 0 || counts["second_effect"] != 0 {
		t.Fatalf("cross-effect calls were reported as recursion: %+v", counts)
	}
	if counts["self_recursive_effect"] != 1 {
		t.Fatalf("self recursion count = %d, want 1", counts["self_recursive_effect"])
	}
	for _, diagnostic := range checkScriptEffectRecursion(parsed.Nodes[2].Children, "common/scripted_effects/test.txt", "self_recursive_effect") {
		if diagnostic.code == "scripted_effect_recursion" && diagnostic.severity != "warning" {
			t.Fatalf("finite recursion risk must not be a categorical error: %+v", diagnostic)
		}
	}
}

func TestOnActionAndIteratorLintRespectSourceRole(t *testing.T) {
	vanilla := vanillaOnActionFixture(t)
	onAction := script.Parse(`on_birth = { effect = { add_gold = 1 } }`)
	if got := lintCodeCount(checkScriptLint(onAction.Nodes, "common/on_action/test.txt", SourceRoleGame, vanilla), "on_action_direct_override"); got != 0 {
		t.Fatalf("vanilla source reported as overriding itself: %d", got)
	}
	if got := lintCodeCount(checkScriptLint(onAction.Nodes, "common/on_action/test.txt", SourceRoleDependency, vanilla), "on_action_direct_override"); got != 1 {
		t.Fatalf("dependency vanilla on_action override warning=%d, want 1", got)
	}
	custom := script.Parse(`custom_mod_on_action = { effect = { add_gold = 1 } }`)
	if got := lintCodeCount(checkScriptLint(custom.Nodes, "common/on_action/test.txt", SourceRoleDependency, vanilla), "on_action_direct_override"); got != 0 {
		t.Fatalf("custom on_action was mistaken for vanilla: %d", got)
	}

	nested := script.Parse(`every_vassal = { every_child = { add_gold = 1 } }`)
	if got := lintCodeCount(checkScriptLint(nested.Nodes, "events/test.txt", SourceRoleGame, nil), "nested_iterator"); got != 0 {
		t.Fatalf("vanilla iterator heuristic leaked into project diagnostics: %d", got)
	}
	if got := lintCodeCount(checkScriptLint(nested.Nodes, "events/test.txt", SourceRoleProject, nil), "nested_iterator"); got != 1 {
		t.Fatalf("project nested iterator advisory=%d, want 1", got)
	}
}

// vanillaOnActionFixture writes a game layer whose on_birth carries an effect,
// so the override check has an original to compare a mod's block against.
func vanillaOnActionFixture(t *testing.T) *vanillaOnActionIndex {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "common", "on_action")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	game := `on_birth = {
	effect = { add_gold = 5 }
}
`
	if err := os.WriteFile(filepath.Join(dir, "vanilla_on_actions.txt"), []byte(game), 0o644); err != nil {
		t.Fatal(err)
	}
	return newVanillaOnActionIndex(root)
}

// TestOnActionOverrideComparesAgainstTheGameBlock pins the reported false
// positive. Overriding a vanilla on_action file means copying it, so on a real
// mod tree four out of five flagged blocks were the vanilla block verbatim:
// the warning said they overwrite originals while they replaced the original
// with an identical original, and told the author to restructure code that was
// already correct.
func TestOnActionOverrideComparesAgainstTheGameBlock(t *testing.T) {
	vanilla := vanillaOnActionFixture(t)
	rewrapped := `on_birth = {
	effect = {
		add_gold = 5
	}
}`
	for _, testcase := range []struct {
		name    string
		source  string
		vanilla *vanillaOnActionIndex
		want    int
	}{
		{name: "changed effect is reported", source: `on_birth = { effect = { add_gold = 1 } }`, vanilla: vanilla, want: 1},
		{name: "verbatim copy of the game block is not", source: `on_birth = { effect = { add_gold = 5 } }`, vanilla: vanilla, want: 0},
		{name: "same shape written differently is not", source: rewrapped, vanilla: vanilla, want: 0},
		{name: "block the game does not declare is not", source: `on_birth = { trigger = { always = yes } }`, vanilla: vanilla, want: 0},
		{name: "no game layer means nothing to overwrite", source: `on_birth = { effect = { add_gold = 1 } }`, vanilla: nil, want: 0},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			parsed := script.Parse(testcase.source)
			if len(parsed.Errors) != 0 {
				t.Fatalf("parse errors: %+v", parsed.Errors)
			}
			got := lintCodeCount(checkScriptLint(parsed.Nodes, "common/on_action/test.txt", SourceRoleDependency, testcase.vanilla), "on_action_direct_override")
			if got != testcase.want {
				t.Fatalf("on_action_direct_override = %d, want %d", got, testcase.want)
			}
		})
	}
}
