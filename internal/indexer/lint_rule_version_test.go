package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStaleLintRuleVersionRelintsWithoutDroppingTheCache pins the separation
// between the two rule versions.
//
// A diagnostic rule that changes what it reports leaves every stored row
// structurally valid, so it must not cost what indexRuleVersion costs: that one
// resets the cache and marks the map database stale, taking every map tool
// offline. But it must still cost something, because nothing else dislodges a
// diagnostic attached to a file that never changed — which is exactly how a
// corrected rule kept serving the warnings it no longer raises.
func TestStaleLintRuleVersionRelintsWithoutDroppingTheCache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	path := filepath.Join(project, "common", "traits", "probe.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("lint_version_probe_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/probe.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Stand in for an index built by a binary whose rules have since been
	// corrected: a diagnostic the current rules never raise, on a file that
	// will not change, plus a marker only a cache reset could remove.
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE meta SET value='stale-lint-version' WHERE key='lint_rule_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path)
		SELECT 'compiler','warning','superseded_rule_probe','raised by a rule that has since been corrected',id,path
		FROM files WHERE rel_path=?`, "common/traits/probe.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('cache_survival_probe','present')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	var superseded int
	if err := reopened.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostics WHERE code='superseded_rule_probe'`).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != 0 {
		t.Errorf("superseded diagnostic survived the rescan: %d rows; the file was not re-linted", superseded)
	}

	marker, err := reopened.metaValue(ctx, "cache_survival_probe")
	if err != nil {
		t.Fatal(err)
	}
	if marker != "present" {
		t.Errorf("cache_survival_probe = %q, want %q; a lint change must not reset the cache the way an index change does", marker, "present")
	}

	recorded, err := reopened.metaValue(ctx, "lint_rule_version")
	if err != nil {
		t.Fatal(err)
	}
	if recorded != lintRuleVersion {
		t.Errorf("lint_rule_version = %q, want %q", recorded, lintRuleVersion)
	}
}
