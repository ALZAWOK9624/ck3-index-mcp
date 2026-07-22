package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// publishedIndexTables is the complete semantic snapshot published by a full
// scan. Keep this alongside DB.reset: staged publication copies these tables
// inside one transaction instead of replacing the SQLite file underneath
// long-lived MCP read-only connections.
var publishedIndexTables = []string{
	"meta",
	"source_layers",
	"files",
	"nodes",
	"objects",
	"object_defs",
	"refs",
	"localization",
	"resources",
	"schema_fields",
	"object_fields",
	"diagnostics",
	"saved_scopes",
	"variables",
	"map_provinces",
	"map_province_geometry",
	"map_physical_rasters",
	"map_province_physical",
	"map_physical_water_body_provinces",
	"map_physical_water_bodies",
	"map_major_river_edges",
	"map_surface_rasters",
	"map_surface_materials",
	"map_province_materials",
	"map_object_instances",
	"map_adjacencies",
	"map_strategic_adjacencies",
	"map_water_body_shores",
	"map_water_body_provinces",
	"map_water_bodies",
	"map_title_adjacencies",
	"map_titles",
	"map_title_provinces",
	"map_integrity_issues",
	"map_province_history",
	"map_title_history",
	"map_characters",
	"map_character_history",
	"map_holy_sites",
	"map_holy_site_faiths",
	"map_province_regions",
	"engine_datatypes",
	"engine_scope_rules",
	"search_fts",
}

const stagedFullScanSchema = "staged_full_scan"

// stagedFullScanFailure keeps ephemeral staging paths out of the MCP-facing
// error text while retaining the original cause for cancellation and durable
// failure-code classification.
type stagedFullScanFailure struct {
	cause error
}

func (e *stagedFullScanFailure) Error() string {
	return "the staged full scan did not complete"
}

func (e *stagedFullScanFailure) Unwrap() error {
	return e.cause
}

func sanitizeStagedFullScanFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &stagedFullScanFailure{cause: err}
}

// ScanFullStaged performs a full rebuild without exposing a partial cache to
// readers. It scans into a sibling temporary SQLite database, verifies that
// snapshot reached ready, then publishes its complete table set to the live
// database in one transaction. A failure or cancellation before commit only
// removes the staging database, preserving the last published generation.
func ScanFullStaged(ctx context.Context, cfg Config) (ScanStats, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	dbPath, err := ConfiguredDatabasePath(normalized)
	if err != nil {
		return ScanStats{}, err
	}
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	stagePath, err := stagedFullScanPath(dbPath)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err)
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	defer removeStagedDatabase(stagePath)

	stageConfig := normalized
	stageConfig.Database = stagePath
	stageConfig.ForceClean = true
	stats, err := Scan(ctx, stageConfig)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err)
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	if err := publishStagedFullScan(ctx, normalized, stagePath); err != nil {
		err = sanitizeStagedFullScanFailure(err)
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	// The staging location is an implementation detail and must never become
	// the apparent published database in refresh output.
	stats.Database = dbPath
	return stats, nil
}

func stagedFullScanPath(dbPath string) (string, error) {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "."+base+".staging-*.sqlite")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	// SQLite creates the file itself. Removing the empty placeholder prevents
	// a stale zero-byte file from looking like a partially initialized cache.
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func removeStagedDatabase(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

// recordStagedFullScanFailure deliberately touches only durable failure
// metadata in the live cache. It never resets or marks the prior ready
// generation as finalizing, so status can report the failure while ordinary
// readers continue to use the last successful snapshot.
func recordStagedFullScanFailure(cfg Config, scanErr error) {
	if scanErr == nil {
		return
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return
	}
	db, err := Open(dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	if db.ensureSchema(context.Background()) != nil {
		return
	}
	db.recordScanFailure(context.Background(), scanErr)
}

func publishStagedFullScan(ctx context.Context, cfg Config, stagePath string) error {
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return err
	}
	stage, err := OpenReadOnly(stagePath)
	if err != nil {
		return err
	}
	stageState, stageErr := stage.IndexState(ctx)
	_ = stage.Close()
	if stageErr != nil {
		return stageErr
	}
	if !stageState.Ready() {
		return fmt.Errorf("staged full scan did not publish a ready generation")
	}

	db, err := Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ensureSchema(ctx); err != nil {
		return err
	}
	previous, err := db.IndexState(ctx)
	if err != nil {
		return err
	}

	// ATTACH is connection-local in SQLite. Pin both attachment and transaction
	// to one connection so a pooled database/sql connection cannot lose sight
	// of the staged schema between DELETE and INSERT.
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=60000`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS `+stagedFullScanSchema, stagePath); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `DETACH DATABASE `+stagedFullScanSchema)
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range publishedIndexTables {
		if err := verifyStagedTable(ctx, tx, table); err != nil {
			return err
		}
	}
	for _, table := range publishedIndexTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+qualifiedSQLiteIdentifier("main", table)); err != nil {
			return fmt.Errorf("clear published table %s: %w", table, err)
		}
	}
	for _, table := range publishedIndexTables {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+qualifiedSQLiteIdentifier("main", table)+` SELECT * FROM `+qualifiedSQLiteIdentifier(stagedFullScanSchema, table)); err != nil {
			return fmt.Errorf("copy staged table %s: %w", table, err)
		}
	}

	nextGeneration := previous.Generation + 1
	if nextGeneration < 1 {
		nextGeneration = 1
	}
	if err := ensureScanRevision(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(nextGeneration, 10)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_committed_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('last_scan_error_code','last_scan_error_at')`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func verifyStagedTable(ctx context.Context, tx *sql.Tx, table string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+stagedFullScanSchema+`.sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("staged full scan is missing table %q", table)
	}
	return nil
}

func qualifiedSQLiteIdentifier(schema, table string) string {
	return quoteSQLiteIdentifier(schema) + "." + quoteSQLiteIdentifier(table)
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
