package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrConflictingGeneration is returned when a staged full scan was built from
// a publication base that is no longer current. Callers may retry from a fresh
// base; the live index is left untouched.
var ErrConflictingGeneration = errors.New("the published index generation changed during staged refresh")

type PublicationBase struct {
	Generation int64
	Revision   string
	Status     string
	// DatabasePath pins the immutable generation from which the staged rebuild
	// started. The numeric generation can repeat after a clean rebuild, so the
	// path participates in the publication compare-and-swap when available.
	DatabasePath string
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
//
// The reported text keeps the cause with host paths redacted rather than
// dropping it. Callers driving this through MCP see only the tool result: a
// bare "did not complete" leaves them unable to distinguish an unreadable
// asset from a disk error, with no log to fall back on.
type stagedFullScanFailure struct {
	cause error
	// detail is the cause text after host paths were replaced by their
	// trailing segments. Empty when redaction could not be applied.
	detail string
}

func (e *stagedFullScanFailure) Error() string {
	if e.detail == "" {
		return "the staged full scan did not complete"
	}
	return "the staged full scan did not complete: " + e.detail
}

func (e *stagedFullScanFailure) Unwrap() error {
	return e.cause
}

func sanitizeStagedFullScanFailure(err error, hostPaths []string) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &stagedFullScanFailure{cause: err, detail: redactHostPaths(err.Error(), hostPaths)}
}

// scanRedactionPaths lists the host locations that must not appear in a
// user-visible scan error: the live cache, its staging sibling, and every
// configured source root.
func scanRedactionPaths(cfg Config, dbPath, stagePath string) []string {
	paths := []string{stagePath, dbPath}
	if dbPath != "" {
		paths = append(paths, filepath.Dir(dbPath))
	}
	for _, source := range cfg.Sources {
		paths = append(paths, source.Path)
	}
	// Redact longer paths first so a parent directory cannot partially rewrite
	// a longer child path and leave a mangled fragment behind.
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	return paths
}

// redactHostPaths replaces known host paths with their trailing segments,
// preserving the diagnostic sentence around them. Substituting known prefixes
// rather than pattern-matching anything path-shaped keeps ordinary message
// text (ids, relative filenames, engine terms) intact.
func redactHostPaths(text string, hostPaths []string) string {
	for _, hostPath := range hostPaths {
		hostPath = strings.TrimSpace(hostPath)
		if len(hostPath) < 4 {
			continue
		}
		replacement := displayPath(hostPath)
		for _, variant := range []string{filepath.ToSlash(hostPath), filepath.FromSlash(hostPath)} {
			if variant == "" || variant == replacement {
				continue
			}
			text = strings.ReplaceAll(text, variant, replacement)
		}
	}
	return text
}

// ScanFullStaged performs a full rebuild without exposing a partial cache to
// readers. It scans into a sibling temporary SQLite database, verifies that
// snapshot reached ready, then promotes that complete file as a new immutable
// generation with one atomic pointer replacement. A failure or cancellation
// before the pointer commit preserves the last published generation.
func ScanFullStaged(ctx context.Context, cfg Config) (ScanStats, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	anchorPath, err := ConfiguredDatabaseAnchorPath(normalized)
	if err != nil {
		return ScanStats{}, err
	}
	lock, err := acquirePublicationLock(ctx, anchorPath)
	if err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	defer lock.Close()
	// Swept under the publication lock, before a new stage is created: at this
	// point no other full refresh can be holding one.
	removeOrphanedStagedDatabases(anchorPath)
	removeOrphanedPublishedDatabaseGenerations(anchorPath)
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	dbPath, err := ConfiguredDatabasePath(normalized)
	if err != nil {
		return ScanStats{}, err
	}
	base, err := readPublicationBase(ctx, dbPath, normalized.SQLiteReadOptions())
	if err != nil {
		return ScanStats{}, err
	}
	stagePath, err := stagedFullScanPath(anchorPath)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err, scanRedactionPaths(normalized, dbPath, ""))
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	defer func() {
		if cleanupErr := removeStagedDatabase(stagePath); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "[scan] could not remove staging cache %s: %v\n", filepath.Base(stagePath), cleanupErr)
		}
	}()

	engineLoadStart := time.Now()
	engineBundle, err := LoadEngineBundle(ctx, normalized.EngineLogs)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err, scanRedactionPaths(normalized, dbPath, stagePath))
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	engineLoadElapsed := time.Since(engineLoadStart)
	seedStart := time.Now()
	reused := false
	if !normalized.ForceClean {
		reused = seedStagedScanFromPublished(ctx, normalized, dbPath, stagePath, engineBundle.Fingerprint)
	}
	seeded, seedRejection := false, ""
	if !normalized.ForceClean && !reused {
		seeded, seedRejection = seedStagedScanFromBase(ctx, normalized, stagePath, engineBundle.Fingerprint)
	}
	seedElapsed := time.Since(seedStart)
	if reused {
		seedRejection = "reused the compatible published generation instead"
	} else if normalized.ForceClean {
		seedRejection = "clean rebuild requested"
	}
	seedResult := &BaseSeedResult{
		Configured: strings.TrimSpace(normalized.BaseDatabase) != "",
		Used:       seeded,
		Reason:     seedRejection,
	}

	stageConfig := normalized
	stageConfig.Database = stagePath
	stageConfig.disposableStage = true
	stageConfig.afterScanCommit = nil
	// A seeded stage already holds every upstream row, so it must be scanned
	// incrementally: a clean rebuild would discard exactly the work the base was
	// there to supply. Without a base the stage starts empty and a clean scan is
	// both correct and marginally cheaper.
	stageConfig.ForceClean = !seeded && !reused
	stageConfig.verifyContent = true
	// Reuse the exact bundle whose fingerprint admitted the optional base.
	// Reloading here doubles log parsing and creates a TOCTOU window where the
	// stage can be seeded against one bundle and scanned against another.
	scanStart := time.Now().Add(-engineLoadElapsed - seedElapsed)
	stats, err := scanWithPreparedEngineBundle(ctx, stageConfig, stageConfig.ForceClean, false, engineBundle, engineLoadElapsed, scanStart)
	if err != nil {
		err = sanitizeStagedFullScanFailure(err, scanRedactionPaths(normalized, dbPath, stagePath))
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	if err := ctx.Err(); err != nil {
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	// Validate and prepare the necessary engine snapshot before the pointer commit.
	stageDB, err := OpenReadOnlyWithOptions(stagePath, normalized.SQLiteReadOptions())
	if err != nil {
		return ScanStats{}, err
	}
	rules, rulesErr := engineRulesMatchingDB(ctx, stageDB, engineBundle)
	closeErr := stageDB.Close()
	if rulesErr != nil {
		return ScanStats{}, rulesErr
	}
	if closeErr != nil {
		return ScanStats{}, closeErr
	}
	publishStart := time.Now()
	var publishedState IndexState
	if err := publishStagedFullScan(ctx, normalized, stagePath, base, &publishedState); err != nil {
		err = sanitizeStagedFullScanFailure(err, scanRedactionPaths(normalized, dbPath, stagePath))
		recordStagedFullScanFailure(normalized, err)
		return ScanStats{}, err
	}
	stats.Committed = true
	if normalized.afterScanCommit != nil {
		normalized.afterScanCommit()
	}
	if rules != nil {
		rules.Generation = publishedState.Generation
		rules.Revision = publishedState.Revision
	}
	publishEngineRuleSnapshot(rules)
	if stats.TimingsMillis == nil {
		stats.TimingsMillis = map[string]int64{}
	}
	stats.TimingsMillis["publish_staged"] = time.Since(publishStart).Milliseconds()
	stats.TimingsMillis["seed_staged"] = seedElapsed.Milliseconds()
	stats.ReusedGeneration = reused
	stats.ElapsedMillis = time.Since(scanStart).Milliseconds()
	if seedResult.Configured {
		stats.BaseSeed = seedResult
	}
	// The staging location is an implementation detail and must never become
	// the apparent published database in refresh output.
	stats.Database = anchorPath
	return stats, nil
}

func readPublicationBase(ctx context.Context, dbPath string, options SQLiteReadOptions) (PublicationBase, error) {
	base := PublicationBase{DatabasePath: filepath.Clean(dbPath)}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return base, nil
	} else if err != nil {
		return PublicationBase{}, err
	}
	db, err := OpenReadOnlyWithOptions(dbPath, options)
	if err != nil {
		return PublicationBase{}, err
	}
	defer db.Close()
	state, err := db.IndexState(ctx)
	if err != nil {
		return PublicationBase{}, err
	}
	base = publicationBaseFromState(state)
	base.DatabasePath = filepath.Clean(dbPath)
	return base, nil
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

func removeStagedDatabase(path string) error {
	var removalErrors []error
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		candidate := path + suffix
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			removalErrors = append(removalErrors, fmt.Errorf("remove %s: %w", filepath.Base(candidate), err))
		}
	}
	return errors.Join(removalErrors...)
}

// orphanedStagedDatabaseAge keeps the sweep away from a staging cache that a
// live refresh is still filling. Holding the publication lock already implies
// no other full scan is running, but a lock reclaimed from a process declared
// dead leaves a narrow window, and these files are large enough that erring
// toward one extra hour of disk is the cheaper mistake.
const orphanedStagedDatabaseAge = time.Hour

// removeOrphanedStagedDatabases deletes staging caches abandoned by a refresh
// that died before its deferred cleanup could run — a killed process, a power
// loss. Nothing else ever reclaimed them, so each interrupted full refresh
// silently cost multiple gigabytes until someone went looking.
//
// Deletion is the lock test. On Windows an open SQLite file cannot be removed;
// on Unix the unlink is safe by construction because a reader keeps its inode.
// Cleanup failure does not interrupt the next scan, but it is reported instead
// of silently turning every killed refresh into another multi-gigabyte leak.
func removeOrphanedStagedDatabases(dbPath string) {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	matches, err := filepath.Glob(filepath.Join(dir, "."+base+".staging-*.sqlite*"))
	if err != nil {
		return
	}
	roots := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		name := filepath.Base(match)
		marker := strings.LastIndex(name, ".sqlite")
		if marker < 0 {
			continue
		}
		roots[filepath.Join(dir, name[:marker+len(".sqlite")])] = struct{}{}
	}
	var reclaimed int64
	for path := range roots {
		var size int64
		var newest time.Time
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			info, statErr := os.Stat(path + suffix)
			if statErr != nil {
				continue
			}
			size += info.Size()
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if newest.IsZero() || time.Since(newest) < orphanedStagedDatabaseAge {
			continue
		}
		if err := removeStagedDatabase(path); err != nil {
			fmt.Fprintf(os.Stderr, "[scan] could not remove orphaned staging cache %s: %v\n", filepath.Base(path), err)
			continue
		}
		stillPresent := false
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if _, statErr := os.Stat(path + suffix); statErr == nil {
				stillPresent = true
				break
			}
		}
		if stillPresent {
			continue
		}
		reclaimed += size
	}
	if reclaimed > 0 {
		fmt.Fprintf(os.Stderr, "[scan] removed orphaned staging caches, reclaimed %dMB\n", reclaimed/(1<<20))
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
	db, err := OpenWithOptions(dbPath, cfg.SQLiteReadOptions())
	if err != nil {
		return
	}
	defer db.Close()
	if db.ensureSchema(context.Background()) != nil {
		return
	}
	db.recordScanFailure(context.Background(), scanErr)
}

func publishStagedFullScan(ctx context.Context, cfg Config, stagePath string, base PublicationBase, publishedState ...*IndexState) error {
	anchorPath, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		return err
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return err
	}
	stage, err := OpenReadOnlyWithOptions(stagePath, cfg.SQLiteReadOptions())
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

	// Validate both sides before touching the staged file. The previous
	// implementation attached the stage to the live database, deleted every
	// published row, and copied the whole cache back inside one WAL transaction.
	// On a multi-gigabyte index that necessarily created a multi-gigabyte live
	// WAL and could make the machine appear frozen. A completed stage already is
	// the replacement database, so publication only needs a schema/CAS check and
	// an atomic pointer move.
	current := PublicationBase{DatabasePath: filepath.Clean(dbPath)}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		live, openErr := OpenReadOnlyWithOptions(dbPath, cfg.SQLiteReadOptions())
		if openErr != nil {
			return openErr
		}
		currentState, stateErr := live.IndexState(ctx)
		closeErr := live.Close()
		if stateErr != nil {
			return stateErr
		}
		if closeErr != nil {
			return closeErr
		}
		current = publicationBaseFromState(currentState)
		current.DatabasePath = filepath.Clean(dbPath)
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if !samePublicationBase(current, base) {
		return &PublicationConflictError{Base: base, Current: current}
	}
	stage, err = OpenReadOnlyWithOptions(stagePath, cfg.SQLiteReadOptions())
	if err != nil {
		return err
	}
	defer stage.Close()
	for _, table := range semanticIndexTableCatalog {
		if err := verifyImmutablePublicationTable(ctx, stage, table); err != nil {
			return err
		}
	}
	if err := stage.Close(); err != nil {
		return err
	}

	nextGeneration := current.Generation + 1
	if nextGeneration < 1 {
		nextGeneration = 1
	}
	stageWriter, err := OpenWithOptions(stagePath, cfg.SQLiteReadOptions())
	if err != nil {
		return err
	}
	defer stageWriter.Close()
	conn, err := stageWriter.sql.Conn(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		_ = conn.Close()
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
		_ = conn.Close()
	}()
	if err := copyPreservedPublicationTables(ctx, conn, dbPath); err != nil {
		return fmt.Errorf("preserve diagnostic baselines: %w", err)
	}
	// A staged publication is a new physical snapshot even when its contents
	// were seeded from a previous generation. Do not inherit that snapshot's
	// revision identity (used by pinned readers and stale-generation checks).
	if _, err := conn.ExecContext(ctx, `DELETE FROM meta WHERE key='scan_revision'`); err != nil {
		return err
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
	if _, err := conn.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('last_scan_error_code','last_scan_error_at','last_scan_error_detail')`); err != nil {
		return err
	}
	if err := clearIndexStaleMarkers(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	if err := conn.Close(); err != nil {
		return err
	}
	state, err := stageWriter.IndexState(ctx)
	if err != nil {
		return err
	}
	checkpoint, err := stageWriter.CheckpointWAL(ctx, "TRUNCATE")
	if err != nil {
		return err
	}
	if !checkpoint.FullyCheckpointed() {
		return fmt.Errorf("staged database WAL could not be fully checkpointed before publication")
	}
	if err := stageWriter.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(stagePath + suffix); err == nil {
			return fmt.Errorf("staged database retained a SQLite sidecar after checkpoint")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	generationPath := publishedDatabaseGenerationPath(anchorPath, nextGeneration, state.Revision)
	if _, err := os.Stat(generationPath); err == nil {
		return fmt.Errorf("published database generation already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stagePath, generationPath); err != nil {
		return fmt.Errorf("promote staged database generation: %w", err)
	}
	if err := publishDatabasePointer(anchorPath, generationPath); err != nil {
		return fmt.Errorf("publish database generation pointer: %w", err)
	}
	if len(publishedState) > 0 {
		*publishedState[0] = state
	}
	// The pointer commit is the publication boundary. Reclaiming the retired
	// generation is best-effort: an MCP lease may still hold it on Windows, and
	// the database manager retries after the last such lease is released.
	if cleanupErr := RemoveRetiredDatabaseGeneration(cfg, dbPath); cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "[scan] retired database generation cleanup deferred: %v\n", cleanupErr)
	}
	return nil
}

func samePublicationBase(current, base PublicationBase) bool {
	if current.Generation != base.Generation || current.Revision != base.Revision || current.Status != base.Status {
		return false
	}
	if strings.TrimSpace(base.DatabasePath) == "" {
		return true
	}
	return canonicalConfigPath(current.DatabasePath) == canonicalConfigPath(base.DatabasePath)
}

func verifyImmutablePublicationTable(ctx context.Context, stage *DB, table string) error {
	stageColumns, err := sqliteTableColumns(ctx, stage.sql, "main", table)
	if err != nil {
		return fmt.Errorf("inspect staged table %s columns: %w", table, err)
	}
	if len(stageColumns) == 0 {
		return fmt.Errorf("staged full scan is missing table %q", table)
	}
	return nil
}

type stagedTableColumnQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func sqliteTableColumns(ctx context.Context, queryer stagedTableColumnQueryer, schema, table string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `PRAGMA `+quoteSQLiteIdentifier(schema)+`.table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
