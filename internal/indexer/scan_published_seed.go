package indexer

import (
	"context"
	"strconv"
)

// A current project snapshot can seed a staged rebuild just as an upstream
// base can. It is only admitted for the exact source layout and rules that
// produced it. The ordinary scanner reconciles additions, deletions and
// override transitions, and verifyContent disables the binary mtime shortcut.
// Publication still happens only after finalization of the isolated copy.
func seedStagedScanFromPublished(ctx context.Context, cfg Config, published, stage, engineFingerprint string) bool {
	if !publishedSeedCompatible(ctx, cfg, published, engineFingerprint) {
		return false
	}
	if err := onlineBackupDatabaseWithOptions(ctx, published, stage, cfg.SQLiteReadOptions()); err != nil {
		removeDatabaseSnapshot(stage)
		return false
	}
	// Inspect the copy too: a compatibility decision about a live connection
	// must not substitute for validation of the actual snapshot we will use.
	if !publishedSeedCompatible(ctx, cfg, stage, engineFingerprint) {
		removeDatabaseSnapshot(stage)
		return false
	}
	return true
}

func publishedSeedCompatible(ctx context.Context, cfg Config, path, engineFingerprint string) bool {
	db, err := OpenReadOnlyWithOptions(path, cfg.SQLiteReadOptions())
	if err != nil {
		return false
	}
	defer db.Close()
	state, err := db.IndexState(ctx)
	if err != nil || !state.Ready() {
		return false
	}
	for key, expected := range map[string]string{
		"index_rule_version":           indexRuleVersion,
		"lint_rule_version":            lintRuleVersion,
		"engine_data_fingerprint":      engineFingerprint,
		indexedInputFingerprintMetaKey: IndexedInputFingerprint(cfg),
	} {
		value, err := db.metaValue(ctx, key)
		if err != nil || value != expected {
			return false
		}
	}
	for _, table := range semanticIndexTableCatalog {
		if !db.tableExists(ctx, table) {
			return false
		}
	}
	// Do not perpetuate an obviously damaged semantic cache. FTS and map
	// completeness are also checked/repaired by the scanner in the copied DB.
	var counts ScanStats
	for _, count := range scanStatsCountFields(&counts) {
		raw, err := db.metaValue(ctx, count.key)
		if err != nil {
			return false
		}
		expected, err := strconv.Atoi(raw)
		if err != nil || expected < 0 {
			return false
		}
		if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+count.table).Scan(count.value); err != nil || *count.value != expected {
			return false
		}
	}
	return true
}
