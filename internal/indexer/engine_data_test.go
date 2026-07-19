package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOnActionEngineLogPreservesExpectedNoneScope(t *testing.T) {
	ctx := context.Background()
	logs := makeEngineLogs(t, `On Action Documentation:

--------------------

on_character_fixture:
Expected Scope: character

--------------------

on_global_fixture:
Expected Scope: none
`)
	defer func() { _ = ConfigureEngineRules("") }()
	if err := ConfigureEngineRules(logs); err != nil {
		t.Fatal(err)
	}
	if !IsOnAction("on_character_fixture") || !IsOnAction("on_global_fixture") {
		t.Fatal("live on_action log was not exposed through IsOnAction")
	}

	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildEngineData(ctx, tx, logs); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	global, err := db.LookupOnActionEvidence(ctx, "on_global_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(global) != 1 || global[0].RuleKind != "on_action" || len(global[0].InputScopes) != 1 || global[0].InputScopes[0] != "none" || global[0].RuleSource != "engine_logs/on_actions.log" {
		t.Fatalf("Expected Scope: none was not preserved: %+v", global)
	}
	character, err := db.LookupOnActionEvidence(ctx, "on_character_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(character) != 1 || len(character[0].InputScopes) != 1 || character[0].InputScopes[0] != "character" {
		t.Fatalf("Expected Scope: character was not ingested: %+v", character)
	}
}

func TestLogicalEngineEvidenceSourceRemovesMachineRoots(t *testing.T) {
	for input, want := range map[string]string{
		`C:\private\logs\on_actions.log`:                    "engine_logs/on_actions.log",
		`/private/logs/data_types/data_types_character.log`: "engine_logs/data_types/data_types_character.log",
		"on_actions.log": "engine_logs/on_actions.log",
		"":               "engine_logs",
	} {
		if got := logicalEngineEvidenceSource(input); got != want {
			t.Fatalf("logicalEngineEvidenceSource(%q) = %q, want %q", input, got, want)
		}
	}
}

func makeEngineLogs(t *testing.T, onActions string) string {
	t.Helper()
	logs := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(filepath.Join(logs, "data_types"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"effects.log", "triggers.log", "event_targets.log", "event_scopes.log"} {
		if err := os.WriteFile(filepath.Join(logs, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(logs, "on_actions.log"), []byte(onActions), 0644); err != nil {
		t.Fatal(err)
	}
	return logs
}
