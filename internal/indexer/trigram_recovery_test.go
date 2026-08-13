package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The substring index is created by ensureSchema on any writable open, so a
// dropped or truncated trigram_loc comes back as a table that exists and is
// empty. Nothing about that state is an error: searches simply stop finding
// localization values. The scan has to notice and rebuild it.
func TestTrigramIndexRecoversFromDamage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeProjectFile(t, dir, "localization/english/zz_trigram_l_english.yml",
		"l_english:\n zz_trigram_key:0 \"The Pale Knight of Aversaria\"\n")
	writeProjectFile(t, dir, "common/decisions/zz_trigram.txt", `zz_trigram_decision = { is_shown = { always = yes } }`)
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")

	substringHits := func(t *testing.T) int {
		t.Helper()
		db, err := OpenReadOnly(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		evidence, err := db.searchLocalizationValues(ctx, "Pale Knight", SearchOptions{}, 20)
		if err != nil {
			t.Fatalf("localization substring search failed: %v", err)
		}
		return len(evidence)
	}
	if got := substringHits(t); got == 0 {
		t.Fatal("the freshly scanned index found no substring match to begin with")
	}

	for _, damage := range []struct {
		name string
		sql  string
	}{
		{"dropped", `DROP TABLE trigram_loc`},
		{"emptied", `DELETE FROM trigram_loc`},
		{"partial", `DELETE FROM trigram_loc WHERE rowid IN (SELECT id FROM localization LIMIT 1)`},
	} {
		t.Run(damage.name, func(t *testing.T) {
			raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.ExecContext(ctx, damage.sql); err != nil {
				raw.Close()
				t.Fatalf("could not damage the index: %v", err)
			}
			raw.Close()

			// A scan with no file changes at all: the only thing that can bring
			// the index back is noticing that trigram_loc no longer matches
			// localization.
			if _, err := Scan(ctx, cfg); err != nil {
				t.Fatalf("scan after %s: %v", damage.name, err)
			}
			if got := substringHits(t); got == 0 {
				t.Fatalf("after %s and a rescan the substring search still finds nothing", damage.name)
			}
		})
	}
}

func TestMissingTrigramUpdateTriggerRebuildsStaleTokens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeProjectFile(t, dir, "localization/english/zz_trigger_gap_l_english.yml",
		"l_english:\n zz_trigger_gap:0 \"Before Trigger Gap\"\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cache", "test.sqlite")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TRIGGER trigram_loc_au`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE localization SET value='After Trigger Gap' WHERE key='zz_trigger_gap'`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	quick, err := reader.HealthConfiguredDepth(ctx, cfg, HealthQuick)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	deep, err := reader.HealthConfiguredDepth(ctx, cfg, HealthDeep)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	before, err := reader.IndexState(ctx)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !quick.FTS5Available {
		t.Fatal("quick health rejected an available FTS implementation without doing a deep consistency scan")
	}
	if deep.FTS5Available || deep.Status != "error" {
		t.Fatalf("deep health accepted a trigram index with a missing update trigger: %+v", deep)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := db.ensureSchemaWithRepair(ctx)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if !repaired {
		db.Close()
		t.Fatal("ensureSchema did not report the missing trigram trigger repair")
	}
	after, err := db.IndexState(ctx)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	hits, err := db.searchLocalizationValues(ctx, "After Trigger Gap", SearchOptions{}, 20)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("trigram repair did not index the value updated while its trigger was absent")
	}
	if after.Generation <= before.Generation {
		t.Fatalf("trigram repair did not advance the published generation: before=%+v after=%+v", before, after)
	}
}

// reset() drops localization and with it the trigram triggers. A generation
// published without them would stop maintaining its own substring index on the
// next incremental edit.
func TestForceCleanScanRestoresTrigramTriggers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeProjectFile(t, dir, "localization/english/zz_trigger_l_english.yml",
		"l_english:\n zz_trigger_key:0 \"Trigger Restoration\"\n")
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: filepath.Join(dir, "project"), Rank: 1}},
		ForceClean: true,
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := OpenReadOnly(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range trigramLocTriggerNames {
		var found string
		err := db.sql.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&found)
		if err == sql.ErrNoRows {
			t.Fatalf("force-clean scan published a database without trigger %q", name)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func writeProjectFile(t *testing.T, dir, rel, text string) {
	t.Helper()
	path := filepath.Join(dir, "project", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
