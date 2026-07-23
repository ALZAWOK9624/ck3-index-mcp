package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const maxChangedSymbolsPerRefresh = 128

// NormalizeRefreshPath validates the narrow path surface accepted by an
// incremental refresh. It is exported for the MCP boundary so unsafe paths are
// reported as a stable argument error before any database mutation begins.
func NormalizeRefreshPath(raw string) (string, error) {
	rel, err := normalizePatchRelPath(raw)
	if err != nil {
		return "", err
	}
	if classifyVirtualPath(rel) == "" && !isMapContextRel(rel) {
		return "", fmt.Errorf("path is not an indexed CK3 source-root-relative input")
	}
	return rel, nil
}

// FullScanRequiredError makes an intentionally conservative incremental
// refusal recoverable without parsing an English error message.
type FullScanRequiredError struct {
	Reason string
	Paths  []string
}

func (e *FullScanRequiredError) Error() string {
	if len(e.Paths) == 0 {
		return "a full scan is required: " + e.Reason
	}
	return fmt.Sprintf("a full scan is required for %s: %s", strings.Join(e.Paths, ", "), e.Reason)
}

func ScanFiles(ctx context.Context, cfg Config, relPaths []string) (stats ScanStats, resultErr error) {
	start := time.Now()
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	cfg = normalized
	if err := validateSources(cfg.Sources); err != nil {
		return ScanStats{}, err
	}
	if err := validateSourceRoots(cfg.Sources); err != nil {
		return ScanStats{}, err
	}
	engineLoadStart := time.Now()
	engineBundle, err := LoadEngineBundle(ctx, cfg.EngineLogs)
	if err != nil {
		return ScanStats{}, err
	}
	engineLoadMillis := time.Since(engineLoadStart).Milliseconds()
	ConfigureEngineRulesFromBundle(engineBundle)
	if len(relPaths) == 0 {
		return ScanStats{}, fmt.Errorf("scan --files requires at least one source-root relative path")
	}
	src, err := ProjectSource(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	lock, err := acquirePublicationLock(ctx, dbPath)
	if err != nil {
		return ScanStats{}, err
	}
	defer lock.Close()
	db, err := Open(dbPath)
	if err != nil {
		return ScanStats{}, err
	}
	defer db.Close()
	defer func() {
		if resultErr != nil {
			db.recordScanFailure(context.Background(), resultErr)
			return
		}
		db.clearScanFailure(context.Background())
	}()
	if err := db.ensureSchema(ctx); err != nil {
		return ScanStats{}, err
	}
	version, err := db.metaValue(ctx, "index_rule_version")
	if err != nil {
		return ScanStats{}, err
	}
	if version != indexRuleVersion {
		return ScanStats{}, &FullScanRequiredError{Reason: "the index rule version changed"}
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return ScanStats{}, err
	}
	if !state.Ready() {
		return ScanStats{}, fmt.Errorf("incremental scan requires a ready published index; current scan status is %q, run or wait for a full ck3-index scan", state.Status)
	}
	engineFingerprint := engineBundle.Fingerprint
	cachedEngineFingerprint, err := db.metaValue(ctx, "engine_data_fingerprint")
	if err != nil {
		return ScanStats{}, err
	}
	if cachedEngineFingerprint != engineFingerprint {
		return ScanStats{}, &FullScanRequiredError{Reason: "engine log rules changed"}
	}
	stats = ScanStats{Database: dbPath, BySource: map[string]int{}, TimingsMillis: map[string]int64{}}
	stats.TimingsMillis["load_engine_bundle"] = engineLoadMillis

	existingLoadStart := time.Now()
	existing, err := db.fileRecordsByProjectRel(ctx, src.Rank)
	if err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["load_existing_index"] = time.Since(existingLoadStart).Milliseconds()
	jobs := make([]fileJob, 0, len(relPaths))
	removed := make([]fileRecord, 0, len(relPaths))
	mapRefresh := false
	oldFileIDs := map[int64]bool{}
	affected := map[string]bool{}
	requested := map[string]bool{}
	pathOutcomes := map[string]RefreshPathOutcome{}
	for _, raw := range relPaths {
		rel, err := NormalizeRefreshPath(raw)
		if err != nil {
			return ScanStats{}, err
		}
		if requested[rel] {
			continue
		}
		requested[rel] = true
		mapRel := isMapContextRel(rel)
		if mapRel {
			mapRefresh = true
		}
		kind := classifyVirtualPath(rel)
		prev := existing[rel]
		full, _, err := sourceRegularFileAt(src.Path, rel)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return ScanStats{}, fmt.Errorf("read refresh path %s: %w", rel, err)
			}
			if mapRel {
				return ScanStats{}, &FullScanRequiredError{Reason: "a map input was removed", Paths: []string{rel}}
			}
			if prev.ID == 0 {
				stats.MissingFiles = append(stats.MissingFiles, rel)
				pathOutcomes[rel] = RefreshPathOutcome{Path: rel, Status: "not_indexed"}
				continue
			}
			lower, lowerErr := db.hasLowerPrecedenceFile(ctx, rel, src.Rank)
			if lowerErr != nil {
				return ScanStats{}, lowerErr
			}
			if lower {
				return ScanStats{}, &FullScanRequiredError{Reason: "removal would expose a lower-precedence file that has not been parsed as active", Paths: []string{rel}}
			}
			oldFileIDs[prev.ID] = true
			removed = append(removed, prev)
			pathOutcomes[rel] = RefreshPathOutcome{Path: rel, Status: "removed"}
			continue
		}
		if kind == "" {
			if mapRel {
				pathOutcomes[rel] = RefreshPathOutcome{Path: rel, Status: "map_context_rebuilt"}
				continue
			}
			return ScanStats{}, fmt.Errorf("unsupported scan --files path %q", rel)
		}
		if prev.ID != 0 {
			oldFileIDs[prev.ID] = true
		}
		jobs = append(jobs, fileJob{src: src, path: full, rel: rel, kind: kind, prev: prev})
		pathOutcomes[rel] = RefreshPathOutcome{Path: rel, Status: "refreshed"}
	}
	sort.Strings(stats.MissingFiles)
	if len(jobs) == 0 && len(removed) == 0 && !mapRefresh {
		stats.PathOutcomes = sortedRefreshPathOutcomes(pathOutcomes)
		return stats, nil
	}
	beforeDiagnostics, err := db.diagnosticFingerprintSet(ctx)
	if err != nil {
		return ScanStats{}, err
	}
	if err := db.collectAffectedForFiles(ctx, oldFileIDs, affected); err != nil {
		return ScanStats{}, err
	}

	writerConn, err := db.scanWriteConnection(ctx)
	if err != nil {
		return ScanStats{}, err
	}
	defer writerConn.Close()
	tx, err := writerConn.BeginTx(ctx, nil)
	if err != nil {
		return ScanStats{}, err
	}
	defer tx.Rollback()
	if err := syncSourceLayers(ctx, tx, cfg.Sources); err != nil {
		return ScanStats{}, err
	}
	ftsCurrent, err := searchFTSCacheMatches(ctx, tx)
	if err != nil {
		return ScanStats{}, err
	}
	writer, closeWriter, err := prepareScanWriter(ctx, tx)
	if err != nil {
		return ScanStats{}, err
	}
	defer closeWriter()
	locKeys := map[string]bool{}
	resources := map[string]bool{}
	newFileIDs := map[int64]bool{}
	changedSymbols := map[string]bool{}
	var workTotals fileWorkTotals
	var sqliteWriteTotal time.Duration
	if len(removed) > 0 {
		writeStart := time.Now()
		for _, rec := range removed {
			if err := collectFileSymbolLabelsTx(ctx, tx, rec.ID, changedSymbols); err != nil {
				return ScanStats{}, err
			}
			if err := deleteFileRecords(ctx, tx, rec.ID); err != nil {
				return ScanStats{}, err
			}
			stats.ChangedFiles++
			stats.RemovedFiles++
		}
		sqliteWriteTotal += time.Since(writeStart)
	}
	for _, job := range jobs {
		res := parseOneFile(job)
		workTotals.add(&stats, res.work)
		stats.Files++
		stats.BySource[src.Name]++
		if res.err != nil {
			return ScanStats{}, fmt.Errorf("read source file %s: %w", job.rel, res.err)
		}
		if res.info == nil {
			return ScanStats{}, fmt.Errorf("could not read %s", job.rel)
		}
		if res.skip {
			pathOutcomes[job.rel] = RefreshPathOutcome{Path: job.rel, Status: "unchanged"}
			writeStart := time.Now()
			if err := refreshSkippedFileMetadata(ctx, tx, res); err != nil {
				return ScanStats{}, err
			}
			sqliteWriteTotal += time.Since(writeStart)
			if job.prev.ID != 0 {
				newFileIDs[job.prev.ID] = true
			}
			continue
		}
		stats.ChangedFiles++
		writeStart := time.Now()
		if job.prev.ID != 0 {
			if err := collectFileSymbolLabelsTx(ctx, tx, job.prev.ID, changedSymbols); err != nil {
				return ScanStats{}, err
			}
			if err := deleteFileRecords(ctx, tx, job.prev.ID); err != nil {
				return ScanStats{}, err
			}
		}
		// A newly added project file can take over a same-relative-path file
		// that was previously active in a lower-priority source. That hidden
		// file is not part of the project-source `existing` map above, so record its
		// exported symbols before flipping its override bit. Otherwise refs,
		// validator diagnostics, and semantic FTS rows can keep treating the
		// previous winner as active.
		if err := collectProjectOverrideVictims(ctx, tx, job.rel, src.Rank, oldFileIDs, affected); err != nil {
			return ScanStats{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET overridden=1,override_reason='same_relative_path',
			override_by_source=?,override_by_rank=?,override_rule=? WHERE rel_path=? AND source_rank>?`,
			src.Name, src.Rank, job.rel, job.rel, src.Rank); err != nil {
			return ScanStats{}, err
		}
		rec, err := writeFileResult(ctx, writer, res, &stats, locKeys, resources)
		if err != nil {
			return ScanStats{}, err
		}
		newFileIDs[rec.ID] = true
		stats.Objects += 0
		if err := collectAffectedForFileTx(ctx, tx, rec.ID, affected); err != nil {
			return ScanStats{}, err
		}
		if err := collectFileSymbolLabelsTx(ctx, tx, rec.ID, changedSymbols); err != nil {
			return ScanStats{}, err
		}
		sqliteWriteTotal += time.Since(writeStart)
	}
	if len(jobs) > 0 {
		stats.PeakQueuedResults = 1
	}
	workTotals.applyTimings(&stats)
	stats.TimingsMillis["sqlite_write"] = sqliteWriteTotal.Milliseconds()
	scopedFinalizer := len(newFileIDs) <= scopedFinalizerFileLimit && len(affected) <= scopedFinalizerSymbolLimit
	if scopedFinalizer {
		fits, err := scopedValidatorCandidatesFit(ctx, tx, src.Rank, newFileIDs, affected, scopedValidatorFileLimit)
		if err != nil {
			return ScanStats{}, err
		}
		scopedFinalizer = fits
	}
	stageStart := time.Now()
	if scopedFinalizer {
		if err := refreshRefsResolvedScoped(ctx, tx, newFileIDs, affected); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["resolve_refs"] = time.Since(stageStart).Milliseconds()
		stats.TimingsMillis["resolve_refs_scoped"] = stats.TimingsMillis["resolve_refs"]
		stageStart = time.Now()
		if err := refreshValidatorDiagnosticsScoped(ctx, tx, src.Rank, newFileIDs, affected); err != nil {
			return ScanStats{}, err
		}
		if err := refreshTitleIntegrityDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentRegistrationDiagnostics(ctx, tx, src.Rank); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentFallbackDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentMechanicDefaultDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshCourtTypeDefaultDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshErrorLogContractDiagnostics(ctx, tx, src.Rank); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()
		stats.TimingsMillis["validator_scoped"] = stats.TimingsMillis["validator"]
	} else {
		// `scan --files` is usually tiny, but a small provider can fan out to
		// hundreds of consumers. Retain correctness and SQL safety by using the
		// same global finalizer as a broad full scan in that case.
		stageStart = time.Now()
		objectNames, err := loadAllObjectNames(ctx, tx)
		if err != nil {
			return ScanStats{}, err
		}
		if err := loadAllLocKeys(ctx, tx, locKeys); err != nil {
			return ScanStats{}, err
		}
		if err := loadAllResources(ctx, tx, resources); err != nil {
			return ScanStats{}, err
		}
		evidence, err := loadReferenceResolutionEvidence(ctx, tx, locKeys, resources)
		if err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["load_symbols"] = time.Since(stageStart).Milliseconds()
		stageStart = time.Now()
		if err := refreshRefsResolvedGo(ctx, tx, objectNames, locKeys, evidence); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["resolve_refs"] = time.Since(stageStart).Milliseconds()
		stageStart = time.Now()
		if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='validator'`); err != nil {
			return ScanStats{}, err
		}
		if err := addValidationDiagnostics(ctx, tx, src.Rank, locKeys, objectNames, evidence); err != nil {
			return ScanStats{}, err
		}
		if err := refreshTitleIntegrityDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentRegistrationDiagnostics(ctx, tx, src.Rank); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentFallbackDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshGovernmentMechanicDefaultDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshCourtTypeDefaultDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		if err := refreshErrorLogContractDiagnostics(ctx, tx, src.Rank); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()
	}
	if err := db.RefreshArchitectureOverviewCache(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	if mapRefresh {
		if err := rebuildMapCache(ctx, tx, cfg); err != nil {
			return ScanStats{}, err
		}
	}
	stageStart = time.Now()
	if ftsCurrent {
		if err := refreshSearchFTSForFiles(ctx, tx, oldFileIDs, newFileIDs); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["semantic_fts_scoped"] = time.Since(stageStart).Milliseconds()
	} else {
		if err := rebuildSearchFTS(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["semantic_fts_rebuild"] = time.Since(stageStart).Milliseconds()
	}
	if err := storeSearchFTSRowCount(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["semantic_fts"] = time.Since(stageStart).Milliseconds()
	if err := bumpScanGeneration(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	if err := refreshScanStatsTotals(ctx, tx, &stats); err != nil {
		return ScanStats{}, err
	}
	if err := tx.Commit(); err != nil {
		return ScanStats{}, err
	}
	afterDiagnostics, err := db.diagnosticFingerprintSet(ctx)
	if err != nil {
		return ScanStats{}, err
	}
	stats.DiagnosticDelta = diffDiagnosticFingerprints(beforeDiagnostics, afterDiagnostics)
	stats.ChangedSymbols, stats.ChangedSymbolsTruncated = boundedChangedSymbols(changedSymbols, maxChangedSymbolsPerRefresh)
	stats.PathOutcomes = sortedRefreshPathOutcomes(pathOutcomes)
	checkpoint, checkpointErr := db.checkpointWALAfterScan(ctx)
	if checkpointErr != nil {
		fmt.Fprintf(os.Stderr, "[scan --files] WAL checkpoint deferred: %v\n", checkpointErr)
	} else {
		stats.WALCheckpoint = &checkpoint
		fmt.Fprintf(os.Stderr, "[scan --files] WAL checkpoint %s busy=%d frames=%d/%d\n", checkpoint.Mode, checkpoint.Busy, checkpoint.CheckpointedFrames, checkpoint.LogFrames)
	}
	stats.ElapsedMillis = time.Since(start).Milliseconds()
	return stats, nil
}

func (db *DB) hasLowerPrecedenceFile(ctx context.Context, rel string, projectRank int) (bool, error) {
	var one int
	err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM files WHERE rel_path=? AND source_rank>? LIMIT 1`, rel, projectRank).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func collectFileSymbolLabelsTx(ctx context.Context, tx *sql.Tx, fileID int64, out map[string]bool) error {
	queries := []struct {
		prefix string
		query  string
	}{
		{"", `SELECT object_type || ':' || name FROM objects WHERE file_id=?`},
		{"localization:", `SELECT key FROM localization WHERE file_id=?`},
		{"resource:", `SELECT resource_path FROM resources WHERE file_id=?`},
	}
	for _, item := range queries {
		rows, err := tx.QueryContext(ctx, item.query, fileID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				rows.Close()
				return err
			}
			if label != "" {
				out[item.prefix+label] = true
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) diagnosticFingerprintSet(ctx context.Context) (map[string]bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0),fingerprint FROM diagnostics`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var d Diagnostic
		if err := rows.Scan(&d.Source, &d.Severity, &d.Code, &d.Message, &d.Path, &d.Line, &d.Column, &d.Fingerprint); err != nil {
			return nil, err
		}
		fingerprint := d.Fingerprint
		if fingerprint == "" {
			fingerprint = diagnosticFingerprint(d)
		}
		result[d.Source+"\x00"+fingerprint] = true
	}
	return result, rows.Err()
}

func diffDiagnosticFingerprints(before, after map[string]bool) *DiagnosticDelta {
	delta := &DiagnosticDelta{Remaining: len(after)}
	for fingerprint := range after {
		if !before[fingerprint] {
			delta.Added++
		}
	}
	for fingerprint := range before {
		if !after[fingerprint] {
			delta.Resolved++
		}
	}
	return delta
}

func boundedChangedSymbols(symbols map[string]bool, limit int) ([]string, bool) {
	items := make([]string, 0, len(symbols))
	for symbol := range symbols {
		items = append(items, symbol)
	}
	sort.Strings(items)
	if limit > 0 && len(items) > limit {
		return items[:limit], true
	}
	return items, false
}

func sortedRefreshPathOutcomes(outcomes map[string]RefreshPathOutcome) []RefreshPathOutcome {
	items := make([]RefreshPathOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		items = append(items, outcome)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items
}

func (db *DB) fileRecordsByProjectRel(ctx context.Context, sourceRank int) (map[string]fileRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id,source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden,
		override_reason,override_by_source,override_by_rank,override_rule
		FROM files WHERE source_rank=?`, sourceRank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fileRecord{}
	for rows.Next() {
		var rec fileRecord
		var overridden int
		if err := rows.Scan(&rec.ID, &rec.SourceName, &rec.SourceRank, &rec.Path, &rec.RelPath, &rec.Kind, &rec.MTime, &rec.Size, &rec.SHA, &overridden,
			&rec.OverrideReason, &rec.OverrideBySource, &rec.OverrideByRank, &rec.OverrideRule); err != nil {
			return nil, err
		}
		rec.Overridden = overridden != 0
		out[rec.RelPath] = rec
	}
	return out, rows.Err()
}

func prepareScanWriter(ctx context.Context, tx *sql.Tx) (scanWriter, func(), error) {
	var stmts []*sql.Stmt
	prep := func(query string) (*sql.Stmt, error) {
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, stmt)
		return stmt, nil
	}
	closeFn := func() {
		for _, stmt := range stmts {
			_ = stmt.Close()
		}
	}
	fileStmt, err := prep(`INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden,
		override_reason,override_by_source,override_by_rank,override_rule) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	diagStmt, err := prep(`INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	resStmt, err := prep(`INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	schemaStmt, err := prep(`INSERT INTO schema_fields(object_type,field,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	return scanWriter{
		tx:         tx,
		fileStmt:   fileStmt,
		diagStmt:   diagStmt,
		resStmt:    resStmt,
		schemaStmt: schemaStmt,
	}, closeFn, nil
}

func (db *DB) collectAffectedForFiles(ctx context.Context, fileIDs map[int64]bool, affected map[string]bool) error {
	if len(fileIDs) == 0 {
		return nil
	}
	return db.withFileIDRows(ctx, fileIDs, func(rows *sql.Rows) error {
		for rows.Next() {
			var kind, name string
			if err := rows.Scan(&kind, &name); err != nil {
				return err
			}
			addAffectedSymbol(affected, kind, name)
		}
		return rows.Err()
	})
}

func collectAffectedForFileTx(ctx context.Context, tx *sql.Tx, fileID int64, affected map[string]bool) error {
	for _, query := range []string{
		`SELECT object_type,name FROM objects WHERE file_id=?`,
		`SELECT 'localization',key FROM localization WHERE file_id=?`,
		`SELECT 'resource',resource_path FROM resources WHERE file_id=?`,
		`SELECT ref_kind,ref_name FROM refs WHERE file_id=?`,
	} {
		rows, err := tx.QueryContext(ctx, query, fileID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, name string
			if err := rows.Scan(&kind, &name); err != nil {
				rows.Close()
				return err
			}
			addAffectedSymbol(affected, kind, name)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// collectProjectOverrideVictims finds currently active lower-priority files
// that a project-source scan --files update is about to hide. Their exported
// symbols must join the incremental invalidation set before UPDATE files marks
// them overridden; the source-file scan itself only knows its predecessor.
func collectProjectOverrideVictims(ctx context.Context, tx *sql.Tx, relPath string, projectRank int, oldFileIDs map[int64]bool, affected map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM files
		WHERE rel_path=? AND source_rank>? AND overridden=0`, relPath, projectRank)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if oldFileIDs[id] {
			continue
		}
		if err := collectAffectedForFileTx(ctx, tx, id, affected); err != nil {
			return err
		}
		oldFileIDs[id] = true
	}
	return nil
}

func addAffectedSymbol(affected map[string]bool, kind, name string) {
	if name == "" {
		return
	}
	affected[name] = true
	if kind != "" {
		// Keep bookkeeping identities disjoint from raw Paradox ids. Colons are
		// valid inside some object names, so `kind:name` cannot safely double as
		// an internal marker.
		affected[affectedTypedMarker+kind+"\x00"+name] = true
	}
}

func (db *DB) withFileIDRows(ctx context.Context, fileIDs map[int64]bool, fn func(*sql.Rows) error) error {
	ids := sortedIDs(fileIDs)
	if len(ids) == 0 {
		return nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT object_type,name FROM objects WHERE file_id IN (`+ph+`)
		UNION ALL SELECT 'localization',key FROM localization WHERE file_id IN (`+ph+`)
		UNION ALL SELECT 'resource',resource_path FROM resources WHERE file_id IN (`+ph+`)
		UNION ALL SELECT ref_kind,ref_name FROM refs WHERE file_id IN (`+ph+`)`, append(append(append(args, args...), args...), args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return fn(rows)
}

func sortedIDs(in map[int64]bool) []int64 {
	out := make([]int64, 0, len(in))
	for id := range in {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
