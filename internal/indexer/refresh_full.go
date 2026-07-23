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

const stagedFullScanSchema = "staged_full_scan"

// ErrConflictingGeneration is returned when a staged full scan was built from
// a publication base that is no longer current. Callers may retry from a fresh
// base; the live index is left untouched.
var ErrConflictingGeneration = errors.New("the published index generation changed during staged refresh")

type PublicationBase struct {
	Generation int64
	Revision   string
	Status     string
}

type PublicationConflictError struct {
	Base    PublicationBase
	Current PublicationBase
}

func (e *PublicationConflictError) Error() string {
	return ErrConflictingGeneration.Error()
}

func (e *PublicationConflictError) Unwrap() error {
	return ErrConflictingGeneration
}

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
	lock, err := acquirePublicationLock(ctx, dbPath)
	if err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	base, err := readPublicationBase(ctx, dbPath)
	if err != nil {
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
	stats, err := scanWithMode(ctx, stageConfig, true)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err)
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	publishStart := time.Now()
	if err := publishStagedFullScan(ctx, normalized, stagePath, base); err != nil {
		err = sanitizeStagedFullScanFailure(err)
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	if stats.TimingsMillis == nil {
		stats.TimingsMillis = map[string]int64{}
	}
	stats.TimingsMillis["publish_staged"] = time.Since(publishStart).Milliseconds()
	// The staging location is an implementation detail and must never become
	// the apparent published database in refresh output.
	stats.Database = dbPath
	return stats, nil
}

func readPublicationBase(ctx context.Context, dbPath string) (PublicationBase, error) {
	db, err := Open(dbPath)
	if err != nil {
		return PublicationBase{}, err
	}
	defer db.Close()
	if err := db.ensureSchema(ctx); err != nil {
		return PublicationBase{}, err
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return PublicationBase{}, err
	}
	return publicationBaseFromState(state), nil
}

func publicationBaseFromState(state IndexState) PublicationBase {
	return PublicationBase{Generation: state.Generation, Revision: state.Revision, Status: state.Status}
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

func publishStagedFullScan(ctx context.Context, cfg Config, stagePath string, base PublicationBase) error {
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

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	currentState, err := readIndexState(ctx, conn)
	if err != nil {
		return err
	}
	current := publicationBaseFromState(currentState)
	if current != base {
		return &PublicationConflictError{Base: base, Current: current}
	}
	for _, table := range publishedIndexTables {
		if err := verifyStagedTable(ctx, conn, table); err != nil {
			return err
		}
	}
	for _, table := range publishedIndexTables {
		if _, err := conn.ExecContext(ctx, `DELETE FROM `+qualifiedSQLiteIdentifier("main", table)); err != nil {
			return fmt.Errorf("clear published table %s: %w", table, err)
		}
	}
	for _, table := range publishedIndexTables {
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+qualifiedSQLiteIdentifier("main", table)+` SELECT * FROM `+qualifiedSQLiteIdentifier(stagedFullScanSchema, table)); err != nil {
			return fmt.Errorf("copy staged table %s: %w", table, err)
		}
	}

	nextGeneration := current.Generation + 1
	if nextGeneration < 1 {
		nextGeneration = 1
	}
	if err := ensureScanRevision(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(nextGeneration, 10)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_committed_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('last_scan_error_code','last_scan_error_at')`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

type stagedTableQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func verifyStagedTable(ctx context.Context, queryer stagedTableQueryer, table string) error {
	var count int
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+stagedFullScanSchema+`.sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
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
