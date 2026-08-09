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
	if err := os.WriteFile(path, []byte("pale_knight = { value = 1 }\n"), 0o644); err != nil {
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
	evidence, spelling, err := db.searchNearMiss(context.Background(), "Pale Knight", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 {
		t.Fatal("near-miss recovered nothing for \"Pale Knight\"")
	}
	if spelling != "pale_knight" {
		t.Fatalf("recovering spelling = %q, want pale_knight", spelling)
	}
}

func TestSearchNearMissStaysEmptyForAGenuineAbsence(t *testing.T) {
	db := newNearMissDB(t)
	evidence, spelling, err := db.searchNearMiss(context.Background(), "外乡人军团", SearchOptions{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 0 || spelling != "" {
		t.Fatalf("absent term produced evidence %v via %q", evidence, spelling)
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
