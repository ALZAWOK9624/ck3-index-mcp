package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func lintTestDiagnostics(t *testing.T, source, path string) []ctxDiag {
	t.Helper()
	parsed := script.Parse(source)
	if len(parsed.Errors) != 0 {
		t.Fatalf("parse errors: %+v", parsed.Errors)
	}
	return checkScriptLint(parsed.Nodes, path, "test")
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
