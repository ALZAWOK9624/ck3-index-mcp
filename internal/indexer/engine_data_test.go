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
	bundle, err := LoadEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	ConfigureEngineRulesFromBundle(bundle)
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
	if err := rebuildEngineDataFromBundle(ctx, tx, bundle); err != nil {
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

func TestCachedEngineBundleReusesAndInvalidatesManifest(t *testing.T) {
	ctx := context.Background()
	logs := makeEngineLogs(t, "")
	effects := filepath.Join(logs, "effects.log")
	if err := os.WriteFile(effects, []byte("first_effect - fixture\nSupported Scopes: character\n"), 0644); err != nil {
		t.Fatal(err)
	}
	first, err := loadCachedEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadCachedEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("unchanged manifest did not reuse the cached engine bundle")
	}
	if err := os.WriteFile(effects, []byte("second_effect - changed fixture\nSupported Scopes: province\n"), 0644); err != nil {
		t.Fatal(err)
	}
	third, err := loadCachedEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	if third == second || third.Fingerprint == second.Fingerprint {
		t.Fatal("changed engine input did not invalidate the cached bundle")
	}
}

func TestStrictEngineBundleDoesNotTrustPreservedMTimeAndSize(t *testing.T) {
	ctx := context.Background()
	logs := makeEngineLogs(t, "")
	effects := filepath.Join(logs, "effects.log")
	beforeText := []byte("alpha_effect - fixture\nSupported Scopes: character\n")
	afterText := []byte("omega_effect - fixture\nSupported Scopes: province_\n")
	if len(beforeText) != len(afterText) {
		t.Fatalf("fixture lengths differ: %d != %d", len(beforeText), len(afterText))
	}
	if err := os.WriteFile(effects, beforeText, 0644); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(effects)
	if err != nil {
		t.Fatal(err)
	}
	before, err := loadCachedEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(effects, afterText, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(effects, beforeInfo.ModTime(), beforeInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(effects)
	if err != nil {
		t.Fatal(err)
	}
	if afterInfo.Size() != beforeInfo.Size() || afterInfo.ModTime().UnixNano() != beforeInfo.ModTime().UnixNano() {
		t.Skip("filesystem could not preserve the test manifest")
	}
	strict, err := LoadEngineBundle(ctx, logs)
	if err != nil {
		t.Fatal(err)
	}
	if strict.Fingerprint == before.Fingerprint {
		t.Fatal("strict refresh trusted mtime/size and missed changed engine bytes")
	}
	if _, ok := strict.ScopeRules["omega_effect"]; !ok {
		t.Fatalf("strict bundle did not parse changed engine rule: %+v", strict.ScopeRules)
	}
}

func TestEngineLogsSupplementStaticRulesWithScopesTargetsAndModifiers(t *testing.T) {
	logs := makeEngineLogs(t, "")
	for path, text := range map[string]string{
		"triggers.log":      "log_only_trigger - fixture\nSupported Scopes: story\n",
		"effects.log":       "log_only_effect - fixture\nSupported Scopes: court_position\nSupported Targets: character\n",
		"event_targets.log": "log_only_target - fixture\nInput Scopes: character\nOutput Scopes: title_and_vassal_change\n",
		"modifiers.log":     "Tag: log_only_modifier\nExtra info: fixture detail\nUse areas: character， province，以及 county\n",
	} {
		if err := os.WriteFile(filepath.Join(logs, path), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _ = ConfigureEngineRules("") }()
	if err := ConfigureEngineRules(logs); err != nil {
		t.Fatal(err)
	}

	if got, ok := engineRuleScope("log_only_trigger", "trigger"); !ok || got != ScopeStoryCycle {
		t.Fatalf("live story trigger scope = %#v, %v; want ScopeStoryCycle", got, ok)
	}
	if got, ok := engineRuleScope("log_only_effect", "effect"); !ok || got != ScopeCourtPosition {
		t.Fatalf("live court-position effect scope = %#v, %v; want ScopeCourtPosition", got, ok)
	}
	if got, ok := engineRuleOutputScope("log_only_effect", "effect"); !ok || got != ScopeCharacter {
		t.Fatalf("live effect target scope = %#v, %v; want ScopeCharacter", got, ok)
	}
	if got, ok := engineTargetOutputScope("log_only_target"); !ok || got != ScopeTitleAndVassalChange {
		t.Fatalf("live target output = %#v, %v; want ScopeTitleAndVassalChange", got, ok)
	}
	if got := LookupScope("log_only_trigger"); got == nil || !got.IsTrigger || len(got.ScopeNames) != 1 || got.ScopeNames[0] != "story_cycle" {
		t.Fatalf("log-only trigger was not exposed by LookupScope: %+v", got)
	}
	if got := LookupScope("has_cultural_parameter"); got != nil {
		t.Fatalf("static-only scope rule remained visible despite a complete live log bundle: %+v", got)
	}
	if got := LookupModifier("log_only_modifier"); !got.Found || len(got.UseAreas) != 3 || got.UseAreas[0] != "character" || got.UseAreas[1] != "province" || got.UseAreas[2] != "county" || got.Source != "engine_log" {
		t.Fatalf("log-only modifier was not exposed: %+v", got)
	}
}

func TestParseModifierUseAreasSplitsEnglishConjunction(t *testing.T) {
	for input, want := range map[string][]string{
		"character and province":          {"character", "province"},
		"character, province, and county": {"character", "province", "county"},
		"character and terrain":           {"character", "terrain"},
	} {
		got := parseModifierUseAreas(input)
		if len(got) != len(want) {
			t.Fatalf("parseModifierUseAreas(%q) = %#v, want %#v", input, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("parseModifierUseAreas(%q) = %#v, want %#v", input, got, want)
			}
		}
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
