package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBatchFixture(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "culture", "innovations", "innovations.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "innovation_alpha = { group = culture_group_military }\n" +
		"innovation_beta = { group = culture_group_military }\n" +
		"innovation_gamma = { group = culture_group_civic }\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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

// Walking an id family one term per call was 163 calls in the audited
// sessions. One call has to answer for all of them, and every term has to be
// attributable afterwards.
func TestBatchSearchAnswersEveryTermAndTagsItsEvidence(t *testing.T) {
	db := newBatchFixture(t)
	terms := []string{"innovation_alpha", "innovation_beta", "innovation_gamma"}
	result, err := db.LLMSearchBatch(context.Background(), terms, SearchOptions{
		LLMOptions: LLMOptions{Limit: 8, AllowProject: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batch) != len(terms) {
		t.Fatalf("batch reported %d rows for %d terms", len(result.Batch), len(terms))
	}
	for i, row := range result.Batch {
		if row.Query != terms[i] {
			t.Fatalf("batch row %d is %q, want %q", i, row.Query, terms[i])
		}
		if row.Returned == 0 {
			t.Errorf("term %q returned nothing", row.Query)
		}
	}
	if len(result.Evidence) == 0 {
		t.Fatal("batch returned no evidence at all")
	}
	seen := map[string]bool{}
	for _, item := range result.Evidence {
		if item.Query == "" {
			t.Fatalf("evidence item %+v is not attributed to a term", item)
		}
		seen[item.Query] = true
	}
	for _, term := range terms {
		if !seen[term] {
			t.Errorf("no evidence carries the tag %q", term)
		}
	}
}

// The reason to ask for several at once is usually to learn which are absent,
// and an absent term cannot be read off an evidence list that simply lacks it.
func TestBatchSearchReportsTermsThatMatchedNothing(t *testing.T) {
	db := newBatchFixture(t)
	result, err := db.LLMSearchBatch(context.Background(),
		[]string{"innovation_alpha", "innovation_does_not_exist_at_all"},
		SearchOptions{LLMOptions: LLMOptions{Limit: 8, AllowProject: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batch) != 2 {
		t.Fatalf("batch rows = %d, want 2", len(result.Batch))
	}
	absent := result.Batch[1]
	if absent.Query != "innovation_does_not_exist_at_all" || absent.Returned != 0 {
		t.Fatalf("absent term row = %+v", absent)
	}
	joined := strings.Join(result.Guidance, " ")
	if !strings.Contains(joined, "returned=0") {
		t.Fatalf("guidance does not explain an empty term: %q", joined)
	}
}

// A batch is meant to be reasoned about as the calls it replaces, so each term
// must return what asking for it alone would have returned.
func TestBatchSearchMatchesTheSingleCallItReplaces(t *testing.T) {
	db := newBatchFixture(t)
	ctx := context.Background()
	opts := SearchOptions{LLMOptions: LLMOptions{Limit: 8, AllowProject: true}}

	single, err := db.LLMSearch(ctx, SearchOptions{Query: "innovation_beta", LLMOptions: opts.LLMOptions})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := db.LLMSearchBatch(ctx, []string{"innovation_beta"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Evidence) != len(single.Evidence) {
		t.Fatalf("batch returned %d items, single call returned %d", len(batch.Evidence), len(single.Evidence))
	}
	for i := range single.Evidence {
		if batch.Evidence[i].Name != single.Evidence[i].Name || batch.Evidence[i].Kind != single.Evidence[i].Kind {
			t.Fatalf("item %d differs: batch %+v single %+v", i, batch.Evidence[i], single.Evidence[i])
		}
	}
}

func TestBatchSearchBoundsItsInput(t *testing.T) {
	db := newBatchFixture(t)
	ctx := context.Background()
	opts := SearchOptions{LLMOptions: LLMOptions{Limit: 8, AllowProject: true}}

	many := make([]string, maxBatchSearchQueries+1)
	for i := range many {
		many[i] = "innovation_alpha_" + strings.Repeat("x", i+1)
	}
	if _, err := db.LLMSearchBatch(ctx, many, opts); err == nil {
		t.Fatal("an over-long batch was accepted")
	}
	if _, err := db.LLMSearchBatch(ctx, []string{"   ", ""}, opts); err == nil {
		t.Fatal("a batch of blank terms was accepted")
	}
	// Duplicates collapse rather than paying for the same search twice.
	result, err := db.LLMSearchBatch(ctx, []string{"innovation_alpha", "innovation_alpha"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Batch) != 1 {
		t.Fatalf("duplicate terms produced %d rows", len(result.Batch))
	}
}

// Public visibility filters each term's result before it is merged, so the
// batch must not reintroduce aggregate counts the single-call path removes.
func TestBatchSearchKeepsPublicVisibilityQuiet(t *testing.T) {
	db := newBatchFixture(t)
	result, err := db.LLMSearchBatch(context.Background(), []string{"innovation_alpha"},
		SearchOptions{LLMOptions: LLMOptions{Limit: 8, Mode: "public"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts != nil {
		t.Fatalf("public batch retained aggregate counts: %+v", result.Counts)
	}
	if len(result.Batch) != 1 {
		t.Fatalf("public batch lost its per-term rows: %+v", result.Batch)
	}
}
