package indexer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareObjectAgainstBaseMatchesAcrossFilesAndSummarizesFields(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project_traits.txt", `
brave = {
    changed = source_value
    same = { nested = one }
    source_only = yes
}

calm = { value = 1 }
`)
	writeObjectCompareFixture(t, base, "common/traits/vanilla_traits.txt", `
# File name and formatting intentionally differ from the project layer.
brave = {
    changed = base_value
    same = {
        nested = one
    }
    base_only = yes
}

calm = {
    value = 1 # a comment must not affect the canonical hash
}
`)
	cfg := objectCompareConfig(project, base)

	brave, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if brave.Status != "matched" || brave.ComparedDir != "common/traits" || brave.AST == nil || brave.AST.Equal || brave.AST.Status != "changed" {
		t.Fatalf("unexpected cross-file result: %+v", brave)
	}
	if len(brave.SourceCandidates) != 1 || len(brave.BaseCandidates) != 1 {
		t.Fatalf("expected one candidate on each side: %+v", brave)
	}
	if brave.SourceCandidates[0].Path != "common/traits/project_traits.txt" || brave.BaseCandidates[0].Path != "common/traits/vanilla_traits.txt" {
		t.Fatalf("cross-file candidate paths were not retained: %+v", brave)
	}
	changes := map[string]ObjectCompareFieldChange{}
	for _, change := range brave.FieldChanges {
		changes[change.Field] = change
	}
	for field, want := range map[string]string{
		"changed":     "changed",
		"source_only": "added",
		"base_only":   "removed",
	} {
		if got := changes[field].Classification; got != want {
			t.Fatalf("field %s = %+v, want %s", field, changes[field], want)
		}
	}
	if _, exists := changes["same"]; exists {
		t.Fatalf("unchanged direct field was emitted: %+v", brave.FieldChanges)
	}
	encoded, err := json.Marshal(brave)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{project, base} {
		if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), strings.ReplaceAll(root, `\`, `\\`)) {
			t.Fatalf("object compare leaked a physical source root: %s", encoded)
		}
	}

	calm, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:calm", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if calm.Status != "matched" || calm.AST == nil || !calm.AST.Equal || calm.AST.Status != "identical" || len(calm.FieldChanges) != 0 {
		t.Fatalf("comments/formatting changed canonical comparison: %+v", calm)
	}
}

func TestCompareObjectAgainstBaseSupportsEventsAndReportsPresenceOrAmbiguity(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "events/project_events.txt", `
demo.1 = { type = character_event title = demo_title }
source_only.1 = { type = character_event }
duplicate_trait = { marker = one }
`)
	writeObjectCompareFixture(t, project, "common/traits/one.txt", `
duplicate_trait = { marker = one }
`)
	writeObjectCompareFixture(t, project, "common/traits/two.txt", `
duplicate_trait = { marker = two }
`)
	writeObjectCompareFixture(t, base, "events/vanilla_events.txt", `
demo.1 = {
    type = character_event
    title = changed_title
}
base_only.1 = { type = character_event }
`)
	writeObjectCompareFixture(t, base, "common/traits/base.txt", `
duplicate_trait = { marker = base }
`)
	cfg := objectCompareConfig(project, base)

	matched, err := CompareObjectAgainstBase(context.Background(), cfg, "event:demo.1", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if matched.Status != "matched" || matched.ComparedDir != "events" || matched.AST == nil || matched.AST.Equal {
		t.Fatalf("event identity did not compare across different files: %+v", matched)
	}

	sourceOnly, err := CompareObjectAgainstBase(context.Background(), cfg, "event:source_only.1", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if sourceOnly.Status != "source_only" || len(sourceOnly.SourceCandidates) != 1 || len(sourceOnly.BaseCandidates) != 0 {
		t.Fatalf("source-only event status wrong: %+v", sourceOnly)
	}
	baseOnly, err := CompareObjectAgainstBase(context.Background(), cfg, "event:base_only.1", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if baseOnly.Status != "base_only" || len(baseOnly.SourceCandidates) != 0 || len(baseOnly.BaseCandidates) != 1 {
		t.Fatalf("base-only event status wrong: %+v", baseOnly)
	}

	ambiguous, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:duplicate_trait", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous.Status != "ambiguous" || len(ambiguous.SourceCandidates) != 2 || ambiguous.AST != nil {
		t.Fatalf("duplicate candidates were not kept ambiguous: %+v", ambiguous)
	}
}

func TestCompareObjectAgainstBaseRejectsMergeAndOutOfScopeTypes(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	cfg := objectCompareConfig(project, base)

	onAction, err := CompareObjectAgainstBase(context.Background(), cfg, "on_action:on_birth_child", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if onAction.Status != "unsupported" || !strings.Contains(onAction.Reason, "merge semantics") {
		t.Fatalf("on_action did not remain safely unsupported: %+v", onAction)
	}
	gui, err := CompareObjectAgainstBase(context.Background(), cfg, "gui:some_window", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if gui.Status != "unsupported" {
		t.Fatalf("out-of-scope type did not remain unsupported: %+v", gui)
	}
	if _, err := CompareObjectAgainstBase(context.Background(), cfg, "brave", ObjectCompareOptions{}); err == nil {
		t.Fatal("untyped id was accepted")
	}
}

func TestCompareObjectAgainstBaseBoundsDuplicateCandidates(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	var repeated strings.Builder
	for i := 0; i < objectCompareCandidateLimit+1; i++ {
		repeated.WriteString("many = { marker = yes }\n")
	}
	writeObjectCompareFixture(t, project, "common/traits/many.txt", repeated.String())
	writeObjectCompareFixture(t, base, "common/traits/base.txt", "many = { marker = no }\n")

	result, err := CompareObjectAgainstBase(context.Background(), objectCompareConfig(project, base), "trait:many", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ambiguous" || !result.SourceTruncated || len(result.SourceCandidates) != objectCompareCandidateLimit || result.AST != nil {
		t.Fatalf("candidate bound was not preserved: %+v", result)
	}
}

func TestCompareObjectAgainstBaseFallsThroughToNearestLowerDefinition(t *testing.T) {
	project := t.TempDir()
	godherja := t.TempDir()
	game := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project.txt", "brave = { value = project }\n")
	writeObjectCompareFixture(t, godherja, "common/traits/other.txt", "calm = { value = upstream }\n")
	writeObjectCompareFixture(t, game, "common/traits/game.txt", "brave = { value = vanilla }\n")
	cfg := Config{Sources: []Source{
		{Name: "project", Path: project, Rank: 1},
		{Name: "godherja", Path: godherja, Rank: 2},
		{Name: "game", Path: game, Rank: 3},
	}}

	fallback, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Status != "matched" || fallback.Base != "game" || fallback.BaseRank != 3 || fallback.BaseSelection != "nearest_lower_definition" {
		t.Fatalf("default comparison did not fall through to the nearest declaring base: %+v", fallback)
	}

	explicit, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project", Base: "godherja"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Status != "source_only" || explicit.Base != "godherja" || explicit.BaseSelection != "explicit" {
		t.Fatalf("explicit base unexpectedly fell through: %+v", explicit)
	}
}

func TestCompareObjectAgainstBaseScopesParseUncertaintyToRequestedIdentity(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project.txt", "source_only = { value = yes }\n")
	// This file is malformed but has no token for source_only. It must not
	// poison a deterministic source-only result from the same source scope.
	writeObjectCompareFixture(t, base, "common/traits/unrelated_bad.txt", "unrelated = { broken = ? }\n")
	cfg := objectCompareConfig(project, base)

	cleanResult, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:source_only", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if cleanResult.Status != "source_only" || cleanResult.BaseParseErrorFiles != 0 {
		t.Fatalf("unrelated parse failure made source-only evidence uncertain: %+v", cleanResult)
	}

	// The same exact identity is now mentioned in a malformed base file. Even
	// if partial extraction retains it, this is not safe source-only evidence.
	writeObjectCompareFixture(t, base, "common/traits/relevant_bad.txt", "source_only = { broken =\n")
	uncertain, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:source_only", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Status != "unsupported" || uncertain.BaseParseErrorFiles == 0 {
		t.Fatalf("relevant parse failure produced a deterministic result: %+v", uncertain)
	}
}

func TestCompareObjectAgainstBaseFindsScriptedVariablesInCommonAndEvents(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/defines/project.txt", "@cross_layer = 2\n")
	writeObjectCompareFixture(t, base, "events/base.txt", "@cross_layer = 1\n")

	result, err := CompareObjectAgainstBase(context.Background(), objectCompareConfig(project, base), "scripted_variable:@cross_layer", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ambiguous" || len(result.SourceCandidates) != 1 || len(result.BaseCandidates) != 1 {
		t.Fatalf("scripted variable was not found in both common and events: %+v", result)
	}
	if result.SourceCandidates[0].Path != "common/defines/project.txt" || result.BaseCandidates[0].Path != "events/base.txt" {
		t.Fatalf("scripted variable roots were not preserved: %+v", result)
	}
}

func TestCompareObjectAgainstBaseDoesNotFlagIdenticalRepeatedFields(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project.txt", `
brave = {
    repeated = same
    repeated = same
    changed = project
}
`)
	writeObjectCompareFixture(t, base, "common/traits/base.txt", `
brave = {
    repeated = same
    repeated = same
    changed = base
}
`)

	result, err := CompareObjectAgainstBase(context.Background(), objectCompareConfig(project, base), "trait:brave", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "matched" || len(result.FieldChanges) != 1 || result.FieldChanges[0].Field != "changed" || result.FieldChanges[0].Classification != "changed" {
		t.Fatalf("identical repeated field was emitted as ambiguous: %+v", result)
	}
}

func TestCompareObjectAgainstBaseAppliesExplicitLimitToCandidatesAndFields(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project.txt", `
field_limited = { first = project second = project }
candidate_limited = { value = one }
candidate_limited = { value = two }
`)
	writeObjectCompareFixture(t, base, "common/traits/base.txt", `
field_limited = { first = base second = base }
candidate_limited = { value = base }
`)
	cfg := objectCompareConfig(project, base)

	fields, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:field_limited", ObjectCompareOptions{Source: "project", Base: "game", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if fields.Status != "matched" || len(fields.FieldChanges) != 1 || !fields.FieldsTruncated {
		t.Fatalf("explicit limit did not cap field changes: %+v", fields)
	}

	candidates, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:candidate_limited", ObjectCompareOptions{Source: "project", Base: "game", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if candidates.Status != "ambiguous" || len(candidates.SourceCandidates) != 1 || !candidates.SourceTruncated {
		t.Fatalf("explicit limit did not cap source candidates: %+v", candidates)
	}
}

func TestCompareObjectAgainstBaseRefreshesChangedCachedSourceFile(t *testing.T) {
	project := t.TempDir()
	base := t.TempDir()
	writeObjectCompareFixture(t, project, "common/traits/project.txt", "brave = { value = first }\n")
	writeObjectCompareFixture(t, base, "common/traits/base.txt", "brave = { value = base }\n")
	cfg := objectCompareConfig(project, base)

	first, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if first.AST == nil {
		t.Fatalf("initial comparison did not produce an AST: %+v", first)
	}
	cache := objectCompareCacheFor(cfg.Sources[0], objectCompareRootsForType("trait"))
	cache.mu.Lock()
	initialIndexedFiles := cache.indexedFiles
	cache.mu.Unlock()
	if _, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project", Base: "game"}); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	unchangedIndexedFiles := cache.indexedFiles
	cache.mu.Unlock()
	if unchangedIndexedFiles != initialIndexedFiles {
		t.Fatalf("unchanged interactive compare reparsed the source tree: before=%d after=%d", initialIndexedFiles, unchangedIndexedFiles)
	}
	writeObjectCompareFixture(t, project, "common/traits/project.txt", "brave = { value = deliberately_longer_second_value }\n")
	second, err := CompareObjectAgainstBase(context.Background(), cfg, "trait:brave", ObjectCompareOptions{Source: "project", Base: "game"})
	if err != nil {
		t.Fatal(err)
	}
	if second.AST == nil || second.AST.SourceHash == first.AST.SourceHash {
		t.Fatalf("cached compare did not refresh changed source content: first=%+v second=%+v", first, second)
	}
	cache.mu.Lock()
	updatedIndexedFiles := cache.indexedFiles
	cache.mu.Unlock()
	if updatedIndexedFiles != unchangedIndexedFiles+1 {
		t.Fatalf("changed file did not selectively refresh one cached entry: unchanged=%d updated=%d", unchangedIndexedFiles, updatedIndexedFiles)
	}
}

func objectCompareConfig(project, base string) Config {
	return Config{Sources: []Source{
		{Name: "project", Path: project, Rank: 1},
		{Name: "game", Path: base, Rank: 3},
	}}
}

func writeObjectCompareFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
