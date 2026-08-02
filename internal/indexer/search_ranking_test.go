package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Search used to order every prefix query by an unindexable
// "CASE WHEN name=? THEN 0 ELSE 1 END" ahead of the source rank, which forced
// SQLite to materialise and sort the entire prefix range before applying a
// LIMIT of nine. Removing it relies on two properties that were previously
// implicit, so they are asserted here rather than left to be rediscovered:
//
//   - inside the half-open range [query, query+￿) the query itself is the
//     shortest member and therefore sorts first, so index order alone leads
//     with the exact match;
//   - ordering by name before source rank keeps the layered winner first
//     within one name, which is what the rank term was actually protecting.

func TestSearchRanksExactMatchAheadOfPrefixMatches(t *testing.T) {
	db := writeSearchRankingFixture(t)
	result, err := db.LLMSearch(context.Background(), SearchOptions{
		Query:      "rank_probe_trait",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) == 0 {
		t.Fatal("search returned no evidence")
	}
	if got := result.Evidence[0].Name; got != "rank_probe_trait" {
		t.Fatalf("exact match did not rank first: got %q, evidence=%s", got, evidenceNames(result))
	}
}

// TestSearchPrefersHigherPriorityLayerForOneName covers the ordering the rank
// term protected: the same identifier defined in two layers must surface the
// winning one first.
func TestSearchPrefersHigherPriorityLayerForOneName(t *testing.T) {
	db := writeSearchRankingFixture(t)
	result, err := db.LLMSearch(context.Background(), SearchOptions{
		Query:      "rank_probe_shared",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range result.Evidence {
		if evidence.Name != "rank_probe_shared" || evidence.Kind != "object" {
			continue
		}
		if evidence.Source != "project" {
			t.Fatalf("layered name surfaced %q before the project layer: evidence=%s", evidence.Source, evidenceNames(result))
		}
		return
	}
	t.Fatalf("layered name produced no object evidence: %s", evidenceNames(result))
}

// TestSearchExactMatchSurvivesAWidePrefixRange is the regression that matters
// most for the removed CASE: with far more prefix matches than the limit, the
// exact hit must still be returned rather than sorted out of the window.
func TestSearchExactMatchSurvivesAWidePrefixRange(t *testing.T) {
	db := writeSearchRankingFixture(t)
	result, err := db.LLMSearch(context.Background(), SearchOptions{
		Query:      "rank_probe_wide",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) == 0 {
		t.Fatal("wide prefix search returned no evidence")
	}
	if got := result.Evidence[0].Name; got != "rank_probe_wide" {
		t.Fatalf("exact match lost to prefix matches in a wide range: got %q, evidence=%s", got, evidenceNames(result))
	}
}

func TestSearchReservesPrefixCapacityForProjectLayer(t *testing.T) {
	db := writeSearchRankingFixture(t)
	result, err := db.LLMSearch(context.Background(), SearchOptions{
		Query: "rank_probe_quota_", Kind: "object",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range result.Evidence {
		if evidence.Kind == "object" && evidence.Source == "project" && evidence.Name == "rank_probe_quota_zz_project" {
			return
		}
	}
	t.Fatalf("wide upstream prefix displaced the reserved project candidate: %s", evidenceNames(result))
}

func evidenceNames(result LLMResult) string {
	names := make([]string, 0, len(result.Evidence))
	for _, evidence := range result.Evidence {
		names = append(names, evidence.Kind+":"+evidence.Name+"@"+evidence.Source)
	}
	return strings.Join(names, ", ")
}

func writeSearchRankingFixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	write := func(root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var traits strings.Builder
	// The exact identifier plus siblings that share its prefix and sort after
	// it, so a query for the bare name has competition inside its own range.
	traits.WriteString("rank_probe_trait = { category = personality }\n")
	for _, suffix := range []string{"_alpha", "_beta", "_gamma", "_delta"} {
		traits.WriteString("rank_probe_trait" + suffix + " = { category = personality }\n")
	}
	// A range far wider than any limit, to prove the exact hit is not sorted
	// out of the fetch window.
	traits.WriteString("rank_probe_wide = { category = personality }\n")
	for index := 0; index < 200; index++ {
		traits.WriteString("rank_probe_wide_" + string(rune('a'+index%26)) + strconv.Itoa(index) + " = { category = personality }\n")
	}
	for index := 0; index < 40; index++ {
		traits.WriteString("rank_probe_quota_a" + strconv.Itoa(index) + " = { category = personality }\n")
	}
	// One name present in both layers. The game copy lives in a different file
	// so it stays active rather than being hidden by file-level override.
	traits.WriteString("rank_probe_shared = { category = personality martial = 1 }\n")
	write(game, "common/traits/00_rank_probe.txt", traits.String())
	write(project, "common/traits/10_rank_probe_project.txt", `rank_probe_shared = { category = personality martial = 9 }
rank_probe_quota_zz_project = { category = personality }
`)

	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/rank.sqlite",
		GISEnabled: false,
		Sources: []Source{
			{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject},
			{Name: "game", Path: game, Rank: 2, Role: SourceRoleGame},
		},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
