package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanFilesOverrideTakeoverReconcilesFormerActiveLayer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	write := func(root, rel, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}

	const sharedRel = "common/traits/shared.txt"
	write(game, sharedRel, "game_trait = {}\n")
	write(project, "common/decisions/consumer.txt", `consumer_decision = {
	is_shown = { has_trait = game_trait }
}`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "project", Path: project, Rank: 1},
			{Name: "game", Path: game, Rank: 2},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	assertConsumerResolution := func(want int) {
		t.Helper()
		var got int
		err := db.sql.QueryRowContext(ctx, `SELECT r.resolved
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE f.source_name='project' AND f.rel_path='common/decisions/consumer.txt'
				AND r.ref_kind='trait' AND r.ref_name='game_trait'`).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("consumer game_trait resolution=%d, want %d", got, want)
		}
	}
	assertFTSObjectCount := func(name string, want int) {
		t.Helper()
		var got int
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts WHERE kind='object' AND name=?`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("semantic FTS object rows for %q=%d, want %d", name, got, want)
		}
	}
	assertConsumerResolution(1)
	assertFTSObjectCount("game_trait", 1)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	write(project, sharedRel, "project_trait = {}\n")
	stats, err := ScanFiles(ctx, cfg, []string{sharedRel})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats.TimingsMillis["semantic_fts"]; !ok {
		t.Fatalf("scan --files did not refresh semantic FTS: %+v", stats.TimingsMillis)
	}

	db, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertConsumerResolution(0)
	assertFTSObjectCount("game_trait", 0)
	assertFTSObjectCount("project_trait", 1)

	var overridden, missing int
	if err := db.sql.QueryRowContext(ctx, `SELECT overridden FROM files WHERE source_name='game' AND rel_path=?`, sharedRel).Scan(&overridden); err != nil {
		t.Fatal(err)
	}
	if overridden != 1 {
		t.Fatalf("former game winner was not marked overridden: %d", overridden)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostics d JOIN files f ON f.id=d.file_id
		WHERE d.source='validator' AND d.code='missing_object_reference'
			AND f.source_name='project' AND f.rel_path='common/decisions/consumer.txt'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing == 0 {
		t.Fatal("consumer did not gain missing-object diagnostic after its provider was overridden")
	}
}
