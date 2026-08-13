package indexer

import (
	"context"
	"fmt"
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
	if !strings.Contains(joined, "available=0") {
		t.Fatalf("guidance does not explain an empty term: %q", joined)
	}
	if strings.Contains(joined, "returned=0") {
		t.Fatalf("guidance confuses response truncation with absence: %q", joined)
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

// The evidence ceiling is applied while merging terms, so the later terms in a
// wide batch can be cut off entirely. Their rows used to keep reporting what
// the search found rather than what the response carries, which reads as
// evidence the caller never received.
func TestBatchSearchRowsCountWhatTheResponseCarries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	path := filepath.Join(game, "common", "decisions", "wide.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	families := []string{"zzwidea", "zzwideb", "zzwidec", "zzwided", "zzwidee", "zzwidef", "zzwideg", "zzwideh"}
	for _, family := range families {
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&content, "%s_%02d = { is_shown = { always = yes } }\n", family, i)
		}
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: game, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := db.LLMSearchBatch(ctx, families, SearchOptions{LLMOptions: LLMOptions{Limit: 20, AllowProject: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) > batchSearchEvidenceCeil {
		t.Fatalf("evidence %d exceeded the ceiling %d", len(result.Evidence), batchSearchEvidenceCeil)
	}
	emitted := 0
	for _, row := range result.Batch {
		if row.Emitted > row.Available {
			t.Fatalf("term %q reports %d emitted of %d available", row.Query, row.Emitted, row.Available)
		}
		if row.Returned != row.Emitted {
			t.Fatalf("term %q: returned=%d but emitted=%d", row.Query, row.Returned, row.Emitted)
		}
		if row.Emitted < row.Available && !row.HasMore {
			t.Fatalf("term %q was cut off at %d of %d without has_more", row.Query, row.Emitted, row.Available)
		}
		if row.Available == 0 || row.Emitted == 0 {
			t.Fatalf("round-robin merge starved %q: available=%d emitted=%d", row.Query, row.Available, row.Emitted)
		}
		emitted += row.Emitted
	}
	if emitted != len(result.Evidence) {
		t.Fatalf("rows account for %d evidence items, response carries %d", emitted, len(result.Evidence))
	}
	if !strings.Contains(result.Summary, "evidence item(s) emitted") {
		t.Fatalf("summary does not report emitted items: %q", result.Summary)
	}
}

func TestBatchSearchConcurrencyLeavesOrdinaryPoolCapacity(t *testing.T) {
	for _, test := range []struct {
		connections int
		want        int
	}{
		{connections: 1, want: 1},
		{connections: 4, want: 2},
		{connections: 8, want: 4},
		{connections: 32, want: 4},
	} {
		if got := batchSearchConcurrency(SQLiteReadOptions{Connections: test.connections}); got > test.want || got < 1 {
			t.Fatalf("batchSearchConcurrency(%d)=%d, expected at most %d and at least 1", test.connections, got, test.want)
		}
	}
}
