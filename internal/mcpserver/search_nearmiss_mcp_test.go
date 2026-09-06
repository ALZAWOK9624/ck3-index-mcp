package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

// newSearchFixture builds a one-source index whose object names use the
// lower_snake_case convention every CK3 script identifier follows. That
// convention is what made the audited misses recoverable: the caller typed the
// name as prose, and only the separator and the case differed.
func newSearchFixture(t *testing.T) (*indexer.DB, indexer.Config) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	write := func(root, rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	game := filepath.Join(dir, "game")
	project := filepath.Join(dir, "project")
	write(game, "common/traits/traits.txt", "nearmiss_target = { value = 1 }\npale_knight = { value = 2 }\nmechanic_candidate = { value = 3 }\n")
	write(project, "common/traits/project_traits.txt", "project_only_trait = { value = 3 }\n")
	cfg := indexer.Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []indexer.Source{
			{Name: "public-game", Path: game, Rank: 1, Role: indexer.SourceRoleGame, Private: false},
			{Name: "project", Path: project, Rank: 2, Role: indexer.SourceRoleProject, Private: false},
		},
	}
	if _, err := indexer.Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, cfg
}

// "pale knight", "Pale Knight" and "pale_knight" were three consecutive
// searches in one audited session; only the third could ever have matched.
func TestSearchRecoversFromSeparatorAndCaseSpelling(t *testing.T) {
	db, cfg := newSearchFixture(t)

	for _, query := range []string{"pale knight", "Pale Knight", "PALE_KNIGHT"} {
		t.Run(query, func(t *testing.T) {
			result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": query})
			if result["isError"] == true {
				t.Fatalf("search failed: %+v", result)
			}
			body := result["structuredContent"].(map[string]any)
			evidence := searchRowsForTest(t, body, "evidence")
			if len(evidence) == 0 {
				t.Fatalf("query %q recovered no evidence: %+v", query, body)
			}
			found := false
			for _, raw := range evidence {
				if raw.(map[string]any)["name"] == "pale_knight" {
					found = true
				}
			}
			if !found {
				t.Fatalf("query %q did not recover pale_knight: %+v", query, evidence)
			}
		})
	}
}

// ck3_inspect addresses objects as type:name and the audit shows that form
// being carried into ck3_search, where the colon matches nothing.
func TestSearchStripsInspectStyleIDPrefix(t *testing.T) {
	db, cfg := newSearchFixture(t)
	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": "trait:nearmiss_target"})
	body := result["structuredContent"].(map[string]any)
	evidence := searchRowsForTest(t, body, "evidence")
	if len(evidence) == 0 {
		t.Fatalf("type-prefixed query recovered nothing: %+v", body)
	}
}

func TestSearchLongestTokenCandidatesStaySuggestions(t *testing.T) {
	db, cfg := newSearchFixture(t)
	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": "special pale mechanic"})
	if result["isError"] == true {
		t.Fatalf("search failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	if evidence := searchRowsForTest(t, body, "evidence"); len(evidence) != 0 {
		t.Fatalf("longest-token candidates were promoted to evidence: %+v", evidence)
	}
	suggestions := searchRowsForTest(t, body, "suggestions")
	if len(suggestions) == 0 || body["recovered_query"] != "mechanic" || body["recovery_confidence"] != "low" {
		t.Fatalf("low-confidence recovery contract missing: %+v", body)
	}
	if _, exists := body["next_actions"]; exists {
		t.Fatalf("a low-confidence suggestion must not produce an inspect action: %+v", body["next_actions"])
	}
	definition, ok := findCanonicalTool("ck3_search")
	if !ok {
		t.Fatal("ck3_search is not registered")
	}
	assertDecodedValueMatchesSchema(t, "low-confidence search result", body, definition.OutputSchema)
}

// A genuine absence still has to end the search rather than invite another
// rephrasing: the audit's longest chain was 42 consecutive queries.
func TestSearchMissExplainsWhatWasTried(t *testing.T) {
	db, cfg := newSearchFixture(t)
	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{"query": "外乡人军团"})
	if result["isError"] == true {
		t.Fatalf("miss returned an error instead of guidance: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	if evidence := searchRowsForTest(t, body, "evidence"); len(evidence) != 0 {
		t.Fatalf("expected a miss, got %+v", evidence)
	}
	guidance := strings.Join(stringsOf(body["guidance"]), " ")
	for _, want := range []string{"No indexed identifier", "Rephrasing the same concept rarely helps"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("miss guidance %q does not mention %q", guidance, want)
		}
	}
}

// A restricted search that misses must say the restriction is a candidate
// cause, otherwise the caller concludes the term is absent from the corpus.
func TestSearchMissNamesTheActiveRestriction(t *testing.T) {
	db, cfg := newSearchFixture(t)
	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{
		"query": "pale_knight",
		"kind":  "localization",
	})
	body := result["structuredContent"].(map[string]any)
	guidance := strings.Join(stringsOf(body["guidance"]), " ")
	if !strings.Contains(guidance, `kind="localization" restricted this search`) {
		t.Fatalf("restricted miss did not name the restriction: %q", guidance)
	}
}

func stringsOf(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
