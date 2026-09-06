package indexer

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestFullNoopPreservesPublishedFileAndIdentity(t *testing.T) {
	ctx := context.Background()
	cfg, db, _, _ := stagedFullRefreshFixture(t)
	path, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := fileDigest(t, path)
	stats, err := ScanFullStaged(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Noop || stats.Committed || !stats.ReusedGeneration || stats.FilesHashed != int64(stats.Files) || stats.FilesHashed == 0 || stats.FilesParsed != 0 {
		t.Fatalf("not a complete read-only no-op: %+v", stats)
	}
	afterPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterPath != path || before.Generation != after.Generation || before.Revision != after.Revision || digest != fileDigest(t, path) {
		t.Fatal("no-op rewrote or republished the live index")
	}
	if stats.TimingsMillis["seed_staged"] != 0 || stats.TimingsMillis["publish_staged"] != 0 {
		t.Fatalf("no-op paid for copying/publishing: %+v", stats.TimingsMillis)
	}
}

func TestFullNoopRejectsSameMetadataTextChange(t *testing.T) {
	cfg, _, source, _ := stagedFullRefreshFixture(t)
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), original...)
	changed[0] = 'x'
	if err := os.WriteFile(source, changed, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanFullStaged(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Noop || !stats.Committed || stats.FilesParsed == 0 || stats.FilesHashed <= int64(stats.Files) {
		t.Fatalf("same-metadata change escaped full verification/counters: %+v", stats)
	}
}

func TestFullNoopRepairsMissingSchemaAndFailureState(t *testing.T) {
	for _, damage := range []string{
		`DROP INDEX idx_objects_name`,
		`DROP TRIGGER trigram_loc_ai`,
		`DELETE FROM search_documents`,
		`INSERT OR REPLACE INTO meta(key,value) VALUES('last_scan_error_code','previous_failure')`,
	} {
		t.Run(damage, func(t *testing.T) {
			cfg, _, _, _ := stagedFullRefreshFixture(t)
			db := openConfiguredDatabase(t, cfg)
			if _, err := db.sql.Exec(damage); err != nil {
				t.Fatal(err)
			}
			stats, err := ScanFullStaged(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !stats.Committed {
				t.Fatalf("repair was incorrectly skipped: %+v", stats)
			}
			stats, err = ScanFullStaged(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !stats.Noop || stats.Committed {
				t.Fatalf("repaired cache not reusable: %+v", stats)
			}
		})
	}
}

func TestFullNoopCancellationAndConflictingPublication(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancellation", false: "conflict"}[cancelled], func(t *testing.T) {
			cfg, _, _, _ := stagedFullRefreshFixture(t)
			db := openConfiguredDatabase(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			cfg.afterFullNoopProbe = func() {
				called = true
				if cancelled {
					cancel()
					return
				}
				if _, err := db.sql.Exec(`UPDATE meta SET value='changed-outside-publication-lock' WHERE key='scan_revision'`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ScanFullStaged(ctx, cfg)
			want := ErrConflictingGeneration
			if cancelled {
				want = context.Canceled
			}
			if !called || !errors.Is(err, want) {
				t.Fatalf("hook called=%v error=%v expected=%v", called, err, want)
			}
		})
	}
}

func TestFullNoopMetadataOnlyChangeStillUpdatesFileRecord(t *testing.T) {
	cfg, _, source, _ := stagedFullRefreshFixture(t)
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	next := info.ModTime().Add(time.Second)
	if err := os.Chtimes(source, next, next); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanFullStaged(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Committed || stats.FilesParsed != 0 {
		t.Fatalf("metadata-only update not persisted: %+v", stats)
	}
	db := openConfiguredDatabase(t, cfg)
	var got int64
	if err := db.sql.QueryRow(`SELECT mtime FROM files WHERE path=?`, source).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != next.UnixNano() {
		t.Fatalf("metadata=%d want=%d", got, next.UnixNano())
	}
}
