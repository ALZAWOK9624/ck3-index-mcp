package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedSeedReconcilesAllLayersLikeClean(t *testing.T) {
	ctx := context.Background()
	f := writeBaseSeedFixture(t)
	cfg := f.config(t, f.project, "cache/reused.sqlite")
	binary := filepath.Join(f.project, "gfx", "test.dds")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("aaaa"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("bbbb"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Revive an upstream file hidden by the old project, remove replace_path,
	// change a dependency, and add a missing referenced definition.
	if err := os.Remove(filepath.Join(f.project, "common/traits/seed_replaced.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.project, "descriptor.mod"), []byte("name=\"seed project\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.mod, "common/traits/seed_mod_only.txt"), []byte("seed_mod_trait = { stewardship = 9 }\nseed_missing_trait = {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanFullStaged(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.ReusedGeneration || stats.FilesHashed != int64(stats.Files) {
		t.Fatalf("full content verification not used: %+v", stats)
	}
	clean := cfg
	clean.Database = filepath.Join(f.dir, "cache/clean.sqlite")
	clean.ForceClean = true
	if _, err := ScanFullStaged(ctx, clean); err != nil {
		t.Fatal(err)
	}
	p, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := ConfiguredDatabasePath(clean)
	if err != nil {
		t.Fatal(err)
	}
	got, want := indexProjection(t, p), indexProjection(t, cp)
	for table := range want {
		diffProjections(t, table, want[table], got[table])
	}
	// Explicit --clean must remain a real rebuild even with a ready snapshot.
	cfg.ForceClean = true
	stats, err = ScanFullStaged(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReusedGeneration || stats.FilesParsed == 0 {
		t.Fatalf("clean reused derived rows: %+v", stats)
	}
}

func TestPublishedSeedRejectsDamagedOrForeignSnapshot(t *testing.T) {
	ctx := context.Background()
	for _, damage := range []string{
		`DELETE FROM objects`,
		`UPDATE meta SET value='foreign' WHERE key='indexed_input_fingerprint'`,
		`UPDATE meta SET value='old' WHERE key='index_rule_version'`,
		`UPDATE meta SET value='old' WHERE key='lint_rule_version'`,
		`DROP TABLE map_titles`,
	} {
		t.Run(damage, func(t *testing.T) {
			cfg, _, _, _ := stagedFullRefreshFixture(t)
			db := openConfiguredDatabase(t, cfg)
			if _, err := db.sql.Exec(damage); err != nil {
				t.Fatal(err)
			}
			stats, err := ScanFullStaged(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stats.ReusedGeneration || stats.FilesParsed == 0 {
				t.Fatalf("damaged seed admitted: %+v", stats)
			}
		})
	}
}
