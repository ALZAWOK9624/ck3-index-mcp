package indexer

import (
	"testing"

	"ck3-index/internal/script"
)

func TestOnActionLintAcceptsPublishedEngineOnlyHook(t *testing.T) {
	logs := makeEngineLogs(t, `On Action Documentation:

--------------------

on_engine_only_fixture:
Expected Scope: none
`)
	defer func() { _ = ConfigureEngineRules("") }()
	if err := ConfigureEngineRules(logs); err != nil {
		t.Fatal(err)
	}
	parsed := script.Parse(`pulse = { on_actions = { on_engine_only_fixture unknown_fixture } }`)
	if len(parsed.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", parsed.Errors)
	}
	diagnostics := checkOnActionRefs(parsed.Nodes, "common/on_action/fixture.txt")
	if len(diagnostics) != 1 || diagnostics[0].code != "unknown_on_action" || diagnostics[0].msg == "" {
		t.Fatalf("live engine hook was not accepted while unknown hook retained its warning: %+v", diagnostics)
	}
	if diagnostics[0].line != 1 {
		t.Fatalf("unknown hook lost source location: %+v", diagnostics[0])
	}
}
