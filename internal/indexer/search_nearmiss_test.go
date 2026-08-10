package indexer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNearMissQueryVariantsCoverTheAuditedSpellings(t *testing.T) {
	for _, testCase := range []struct {
		query string
		want  string
	}{
		{"Pale Knight", "pale_knight"},
		{"pale knight", "pale_knight"},
		{"PALE_KNIGHT", "pale_knight"},
		{"pale-knight", "pale_knight"},
		{"pale_knight", "pale knight"},
		// ck3_inspect's type:name addressing, carried into ck3_search.
		{"province:3602", "3602"},
		{"trait:pale_knight", "pale_knight"},
	} {
		variants := nearMissQueryVariants(testCase.query)
		if !slices.Contains(variants, testCase.want) {
			t.Errorf("variants of %q = %v, missing %q", testCase.query, variants, testCase.want)
		}
		if slices.Contains(variants, testCase.query) {
			t.Errorf("variants of %q repeat the query that already missed: %v", testCase.query, variants)
		}
	}
}

func TestNearMissQueryVariantsAreBounded(t *testing.T) {
	if variants := nearMissQueryVariants("a:b c-d_e F"); len(variants) > nearMissVariantLimit {
		t.Fatalf("variant fan-out is unbounded: %v", variants)
	}
	if variants := nearMissQueryVariants("   "); len(variants) != 0 {
		t.Fatalf("blank query produced variants: %v", variants)
	}
}

// "Djinn-Kings of Sarradon" indexes under sarradon, not under the phrase.
func TestLongestQueryTokenSkipsFillerWords(t *testing.T) {
	if token := longestQueryToken("Djinn-Kings of Sarradon"); token != "sarradon" {
		t.Fatalf("longestQueryToken = %q, want sarradon", token)
	}
	if token := longestQueryToken("of a the"); token != "" {
		t.Fatalf("filler-only phrase produced a token: %q", token)
	}
	if token := longestQueryToken("ab cd"); token != "" {
		t.Fatalf("tokens under the minimum length must be rejected, got %q", token)
	}
}

func newNearMissDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "traits", "traits.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pale_knight = { value = 1 }\nmechanic_candidate = { value = 2 }\nmechanic_candidate_two = { value = 3 }\nmechanic_candidate_three = { value = 4 }\nmechanic_candidate_four = { value = 5 }\nmechanic_candidate_five = { value = 6 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Recovery has to come from the indexed prefix range, not a substring scan: on
// the production index a substring pass costs about 1.5 s per column.
func TestSearchIndexedSpellingUsesThePrefixRange(t *testing.T) {
	db := newNearMissDB(t)
	ctx := context.Background()
	evidence, err := db.searchIndexedSpelling(ctx, "pale_knight", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 {
		t.Fatal("indexed spelling lookup found nothing for pale_knight")
	}
	// A mid-name substring is deliberately not recovered: serving it would mean
	// reintroducing the unindexed scan this function exists to avoid.
	interior, err := db.searchIndexedSpelling(ctx, "knight", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(interior) != 0 {
		t.Fatalf("interior substring was served from the prefix path: %v", interior)
	}
}

// The recovering spelling has to come back with the evidence, or the caller's
// follow-up ck3_inspect repeats the spelling that just missed.
func TestSearchNearMissReportsTheRecoveringSpelling(t *testing.T) {
	db := newNearMissDB(t)
	evidence, spelling, confidence, err := db.searchNearMiss(context.Background(), "Pale Knight", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 {
		t.Fatal("near-miss recovered nothing for \"Pale Knight\"")
	}
	if spelling != "pale_knight" {
		t.Fatalf("recovering spelling = %q, want pale_knight", spelling)
	}
	if confidence != "high" {
		t.Fatalf("mechanical recovery confidence = %q, want high", confidence)
	}
}

func TestSearchNearMissMarksLongestTokenRecoveryLowConfidence(t *testing.T) {
	db := newNearMissDB(t)
	evidence, spelling, confidence, err := db.searchNearMiss(context.Background(), "special pale mechanic", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 || spelling != "mechanic" || confidence != "low" {
		t.Fatalf("token recovery = evidence=%v spelling=%q confidence=%q", evidence, spelling, confidence)
	}

	result, err := db.LLMSearch(context.Background(), SearchOptions{Query: "special pale mechanic"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 0 || len(result.Suggestions) == 0 {
		t.Fatalf("lossy token result promoted suggestions to evidence: %+v", result)
	}
	if result.RecoveredQuery != "mechanic" || result.RecoveryConfidence != "low" {
		t.Fatalf("recovery metadata = query=%q confidence=%q", result.RecoveredQuery, result.RecoveryConfidence)
	}
}

func TestSearchNearMissPrefixOnlyMechanicalRewriteIsNotEvidence(t *testing.T) {
	db := newNearMissDB(t)
	result, err := db.LLMSearch(context.Background(), SearchOptions{Query: "Mechanic Can"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 0 || len(result.Suggestions) == 0 {
		t.Fatalf("prefix-only normalized spelling was promoted to evidence: %+v", result)
	}
	if result.RecoveredQuery != "mechanic" || result.RecoveryConfidence != "low" {
		t.Fatalf("prefix-only recovery metadata = query=%q confidence=%q", result.RecoveredQuery, result.RecoveryConfidence)
	}
}

func TestSearchNearMissSuggestionsPaginateWithoutRepeatingPageOne(t *testing.T) {
	db := newNearMissDB(t)
	first, err := db.LLMSearch(context.Background(), SearchOptions{Query: "special pale mechanic", Page: 1, LLMOptions: LLMOptions{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.LLMSearch(context.Background(), SearchOptions{Query: "special pale mechanic", Page: 2, LLMOptions: LLMOptions{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Suggestions) != 2 || len(second.Suggestions) != 2 {
		t.Fatalf("suggestion pages have wrong sizes: first=%d second=%d", len(first.Suggestions), len(second.Suggestions))
	}
	if first.Pagination != nil || second.Pagination != nil || first.SuggestionPagination == nil || second.SuggestionPagination == nil || first.SuggestionPagination.Returned != 2 || second.SuggestionPagination.Returned != 2 || !first.SuggestionPagination.HasMore {
		t.Fatalf("suggestion pagination metadata is inconsistent: evidence=(%+v,%+v) suggestions=(%+v,%+v)", first.Pagination, second.Pagination, first.SuggestionPagination, second.SuggestionPagination)
	}
	firstNames := map[string]bool{}
	for _, item := range first.Suggestions {
		firstNames[item.Name] = true
	}
	for _, item := range second.Suggestions {
		if firstNames[item.Name] {
			t.Fatalf("page 2 repeated page-1 suggestion %q", item.Name)
		}
	}
}

func TestSearchNearMissStaysEmptyForAGenuineAbsence(t *testing.T) {
	db := newNearMissDB(t)
	evidence, spelling, confidence, err := db.searchNearMiss(context.Background(), "外乡人军团", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 || spelling != "" || confidence != "" {
		t.Fatalf("absent term produced evidence %v via %q confidence=%q", evidence, spelling, confidence)
	}
}

func TestNoMatchGuidanceListsWhatWasAttempted(t *testing.T) {
	joined := strings.Join(noMatchGuidance("Pale Knight", SearchOptions{Kind: "localization"}), " ")
	for _, want := range []string{"pale_knight", "Pale Knight", `kind="localization" restricted this search`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("guidance %q does not mention %q", joined, want)
		}
	}
}

// The prefix range only reaches ids that start with the token, and a
// distinctive word usually sits in the middle of one. The ordinary path tries
// FTS on the whole phrase, which ANDs every term and matches nothing; the
// token has to reach FTS too or the caller is sent off to guess again.
func TestNearMissTokenFallbackReachesInteriorMatchesThroughFTS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "landed_titles", "titles.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("d_deep_ancientmoot_halls = { color = { 1 2 3 } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The prefix path alone cannot see it: the id does not start with the token.
	prefix, err := db.searchIndexedSpelling(ctx, "ancientmoot", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefix) != 0 {
		t.Fatalf("prefix lookup unexpectedly matched an interior token: %v", prefix)
	}

	// FTS indexes the interior, so the token reaches what the prefix cannot.
	fts, err := db.searchFTS(ctx, "ancientmoot", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(fts) == 0 {
		t.Fatal("FTS cannot see the interior token either; this fixture proves nothing")
	}

	result, err := db.LLMSearch(ctx, SearchOptions{
		Query:      "Halls of the Ancientmoot",
		LLMOptions: LLMOptions{Limit: 8, AllowProject: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A single token of a phrase is a lossy recovery, so it stays a suggestion
	// rather than becoming evidence. Before the FTS fallback there was nothing
	// here at all: the caller was told to stop and had no candidate to inspect.
	if len(result.Evidence) != 0 {
		t.Fatalf("a lossy token recovery was promoted to evidence: %+v", result.Evidence)
	}
	if len(result.Suggestions) == 0 {
		t.Fatalf("a phrase whose distinctive token is indexed produced no candidate: %+v", result.Guidance)
	}
	if result.RecoveredQuery != "ancientmoot" || result.RecoveryConfidence != "low" {
		t.Fatalf("recovery metadata = query=%q confidence=%q", result.RecoveredQuery, result.RecoveryConfidence)
	}
	found := false
	for _, item := range result.Suggestions {
		if strings.Contains(item.Name, "ancientmoot") {
			found = true
		}
	}
	if !found {
		t.Fatalf("suggestions do not contain the interior token match: %+v", result.Suggestions)
	}
}

// A CK3 identifier names its family first and discriminates last. Recovering
// innovation_burstbolt as "innovation" matched every innovation in the game
// and told the caller nothing, which is what the production probe returned.
func TestIdentifierTokenPrefersTheDiscriminatingTail(t *testing.T) {
	for _, testCase := range []struct{ query, want string }{
		{"innovation_burstbolt", "burstbolt"},
		{"innovation_night_vision_potion", "potion"},
		{"scripted_effect_lichify", "lichify"},
		{"pale-knight", "knight"},
		// Prose keeps the longest-word rule: the distinctive word can sit
		// anywhere, and "Sarradon" is what recovers this one.
		{"Djinn-Kings of Sarradon", "sarradon"},
		{"Halls of the Ancientmoot", "ancientmoot"},
	} {
		if got := longestQueryToken(testCase.query); got != testCase.want {
			t.Errorf("longestQueryToken(%q) = %q, want %q", testCase.query, got, testCase.want)
		}
	}
}

func TestIdentifierLikeQueryOnlyMatchesSingleTokenIdentifiers(t *testing.T) {
	for query, want := range map[string]bool{
		"innovation_burstbolt":   true,
		"pale-knight":            true,
		"Djinn-Kings of Sarrado": false,
		"plain":                  false,
		"":                       false,
	} {
		if got := identifierLikeQuery(query); got != want {
			t.Errorf("identifierLikeQuery(%q) = %v, want %v", query, got, want)
		}
	}
}
