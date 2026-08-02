package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

// A cache that describes a project tree which has since been replaced is worse
// than an unavailable one: the rows still look complete. MarkIndexStale makes
// every readiness check fail while keeping the rows for forensics, and it must
// carry the reason and the required recovery to the caller.
func TestMarkIndexStaleInvalidatesAPublishedGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	publishReadyIndexState(t, ctx, db)

	before, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Ready() {
		t.Fatalf("fixture did not publish a ready generation: %+v", before)
	}
	if err := db.MarkIndexStale(ctx, "project_tree_replaced", "full_refresh"); err != nil {
		t.Fatal(err)
	}
	after, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Ready() || after.Status != IndexStatusStale {
		t.Fatalf("index still reports %q after invalidation", after.Status)
	}
	if after.StaleReason != "project_tree_replaced" || after.RequiredAction != "full_refresh" {
		t.Fatalf("invalidation is not explained: reason=%q action=%q", after.StaleReason, after.RequiredAction)
	}
	if after.Generation != before.Generation {
		t.Fatalf("invalidation changed the generation: %d -> %d", before.Generation, after.Generation)
	}
}

// The stale explanation belongs to one invalidation. A later publication is
// exactly the recovery it asked for, so it must not inherit the old reason.
func TestPublishingClearsIndexStaleMarkers(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	publishReadyIndexState(t, ctx, db)
	if err := db.MarkIndexStale(ctx, "project_tree_replaced", "full_refresh"); err != nil {
		t.Fatal(err)
	}
	publishReadyIndexState(t, ctx, db)

	state, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Ready() || state.StaleReason != "" || state.RequiredAction != "" {
		t.Fatalf("republished index still carries an invalidation: %+v", state)
	}
}

// MarkIndexStale runs on paths that may not have an index yet; a database with
// no meta table has never published anything that could mislead a caller.
func TestMarkIndexStaleIsSafeWithoutAPublishedIndex(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.MarkIndexStale(ctx, "project_tree_replaced", "full_refresh"); err != nil {
		t.Fatalf("marking an empty cache stale failed: %v", err)
	}
}

func publishReadyIndexState(t *testing.T, ctx context.Context, db *DB) {
	t.Helper()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bumpScanGeneration(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := clearIndexStaleMarkers(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A display identity is deliberately shortened to its trailing path segments,
// which two unrelated roots can share. Drift detection must use the internal
// fingerprint over full canonical paths instead.
func TestScanConfigFingerprintSeparatesRootsWithMatchingTrailingSegments(t *testing.T) {
	left := Config{
		ConfigPath: filepath.Join("D:", "workspace", "ck3-index.toml"),
		Database:   filepath.Join("D:", "workspace", "cache", "index.sqlite"),
		Sources:    []Source{{Name: "project", Path: filepath.Join("D:", "workspace", "release", "my-mod"), Rank: 1, Role: SourceRoleProject}},
	}
	right := left
	right.Sources = []Source{{Name: "project", Path: filepath.Join("E:", "backup", "release", "my-mod"), Rank: 1, Role: SourceRoleProject}}

	leftIdentity, rightIdentity := SourceIdentities(left), SourceIdentities(right)
	if len(leftIdentity) != 1 || len(rightIdentity) != 1 {
		t.Fatalf("unexpected identity counts: %v / %v", leftIdentity, rightIdentity)
	}
	if leftIdentity[0].Root != rightIdentity[0].Root {
		t.Skipf("display identities already differ on this platform (%q vs %q); the fingerprint check below is what matters", leftIdentity[0].Root, rightIdentity[0].Root)
	}
	if ScanConfigFingerprint(left) == ScanConfigFingerprint(right) {
		t.Fatal("two different source roots produced the same scan config fingerprint")
	}
}

func TestScanConfigFingerprintCoversMoreThanSources(t *testing.T) {
	base := Config{
		ConfigPath: filepath.Join("D:", "workspace", "ck3-index.toml"),
		Database:   filepath.Join("D:", "workspace", "cache", "index.sqlite"),
		EngineLogs: filepath.Join("D:", "workspace", "logs"),
		Sources:    []Source{{Name: "project", Path: filepath.Join("D:", "workspace", "mod"), Rank: 1, Role: SourceRoleProject}},
	}
	for name, mutate := range map[string]func(Config) Config{
		"engine_logs": func(cfg Config) Config { cfg.EngineLogs = filepath.Join("D:", "workspace", "other-logs"); return cfg },
		"database": func(cfg Config) Config {
			cfg.Database = filepath.Join("D:", "workspace", "cache", "other.sqlite")
			return cfg
		},
		"base_database": func(cfg Config) Config {
			cfg.BaseDatabase = filepath.Join("D:", "workspace", "cache", "base.sqlite")
			return cfg
		},
		"rank": func(cfg Config) Config {
			cfg.Sources = []Source{{Name: "project", Path: cfg.Sources[0].Path, Rank: 9, Role: SourceRoleProject}}
			return cfg
		},
		"resource_only": func(cfg Config) Config {
			cfg.Sources = []Source{{Name: "project", Path: cfg.Sources[0].Path, Rank: 1, Role: SourceRoleProject, ResourceOnly: true}}
			return cfg
		},
	} {
		if ScanConfigFingerprint(base) == ScanConfigFingerprint(mutate(base)) {
			t.Fatalf("changing %s did not change the scan config fingerprint", name)
		}
	}
}
