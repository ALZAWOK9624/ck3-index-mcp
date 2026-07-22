package indexer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ck3-index/internal/script"
)

type ScanStats struct {
	Database     string `json:"database"`
	Files        int    `json:"files"`
	Nodes        int    `json:"nodes"`
	Objects      int    `json:"objects"`
	References   int    `json:"references"`
	Localization int    `json:"localization"`
	Resources    int    `json:"resources"`
	SchemaFields int    `json:"schema_fields"`
	ObjectFields int    `json:"object_fields"`
	Diagnostics  int    `json:"diagnostics"`
	Overridden   int    `json:"overridden"`
	// Noop reports that the previously published semantic generation was
	// already current. File mtime metadata may have been refreshed, but no
	// global resolver, validator, map, engine, FTS, or overview work ran.
	Noop          bool                 `json:"no_op,omitempty"`
	ElapsedMillis int64                `json:"elapsed_ms"`
	TimingsMillis map[string]int64     `json:"timings_ms,omitempty"`
	BySource      map[string]int       `json:"by_source"`
	WALCheckpoint *WALCheckpointResult `json:"wal_checkpoint,omitempty"`
}

type scanWriter struct {
	fileStmt   *sql.Stmt
	objStmt    *sql.Stmt
	refStmt    *sql.Stmt
	diagStmt   *sql.Stmt
	locStmt    *sql.Stmt
	resStmt    *sql.Stmt
	schemaStmt *sql.Stmt
	fieldStmt  *sql.Stmt
	scopeStmt  *sql.Stmt
	varStmt    *sql.Stmt
}

type fileRecord struct {
	ID               int64
	SourceName       string
	SourceRank       int
	Path             string
	RelPath          string
	Kind             string
	MTime            int64
	Size             int64
	SHA              string
	Overridden       bool
	OverrideReason   string
	OverrideBySource string
	OverrideByRank   int
	OverrideRule     string
}

const indexRuleVersion = "2026-07-22-v0.2.31-ck3-1.19-static-audit"

// Keep ordinary full scans well below SQLite's variable limit when they take
// the scoped resolver/validator path. Larger edits remain correct by falling
// back to the established global finalizers.
const (
	scopedFinalizerFileLimit   = 128
	scopedFinalizerSymbolLimit = 512
	scopedValidatorFileLimit   = 500
)

func Scan(ctx context.Context, cfg Config) (ScanStats, error) {
	return scanWithMode(ctx, cfg, cfg.ForceClean)
}

func scanWithMode(ctx context.Context, cfg Config, forceClean bool) (ScanStats, error) {
	start := time.Now()
	if err := validateSources(cfg.Sources); err != nil {
		return ScanStats{}, err
	}
	if err := ConfigureEngineRules(cfg.EngineLogs); err != nil {
		return ScanStats{}, err
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	db, err := Open(dbPath)
	if err != nil {
		return ScanStats{}, err
	}
	defer db.Close()
	// This database is a rebuildable cache. Scans do large write batches, so
	// avoid growing a huge WAL file that can make commit/checkpoint look hung.
	fmt.Fprintln(os.Stderr, "[scan] preparing sqlite cache")
	for _, p := range []string{
		`PRAGMA busy_timeout=60000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=OFF`,
		`PRAGMA temp_store=MEMORY`,
		`PRAGMA cache_size=-200000`,
	} {
		if _, err := db.sql.ExecContext(ctx, p); err != nil {
			return ScanStats{}, err
		}
	}
	if _, err := db.CheckpointWAL(ctx, "PASSIVE"); err != nil {
		fmt.Fprintf(os.Stderr, "[scan] WAL checkpoint deferred before scan: %v\n", err)
	}
	// ensureSchema recreates a missing FTS table. Remember its pre-schema
	// presence so a repaired-but-empty table cannot be mistaken for a complete
	// published semantic index later in this scan.
	ftsPresentBeforeSchema := !forceClean && db.tableExists(ctx, "search_fts")
	if forceClean {
		if err := db.reset(ctx); err != nil {
			return ScanStats{}, err
		}
	} else {
		if err := db.ensureSchema(ctx); err != nil {
			return ScanStats{}, err
		}
		version, err := db.metaValue(ctx, "index_rule_version")
		if err != nil {
			return ScanStats{}, err
		}
		if version != indexRuleVersion {
			fmt.Fprintf(os.Stderr, "[scan] index rule version changed (%q -> %q), rebuilding sqlite cache\n", version, indexRuleVersion)
			if err := db.reset(ctx); err != nil {
				return ScanStats{}, err
			}
			forceClean = true
		}
	}
	stats := ScanStats{Database: dbPath, BySource: map[string]int{}, TimingsMillis: map[string]int64{}}

	existing := map[string]fileRecord{}
	if !forceClean {
		rows, err := db.sql.QueryContext(ctx, `SELECT id, source_name, source_rank, path, rel_path, kind, mtime, file_size, sha256, overridden,
			override_reason,override_by_source,override_by_rank,override_rule FROM files`)
		if err != nil {
			return ScanStats{}, err
		}
		for rows.Next() {
			var rec fileRecord
			var recOvr int
			if err := rows.Scan(&rec.ID, &rec.SourceName, &rec.SourceRank, &rec.Path, &rec.RelPath, &rec.Kind, &rec.MTime, &rec.Size, &rec.SHA, &recOvr,
				&rec.OverrideReason, &rec.OverrideBySource, &rec.OverrideByRank, &rec.OverrideRule); err != nil {
				rows.Close()
				return ScanStats{}, err
			}
			rec.Overridden = recOvr != 0
			existing[rec.Path] = rec
		}
		rows.Close()
		if needsPathCacheRebuild(existing) {
			fmt.Fprintln(os.Stderr, "[scan] old relative path cache detected, rebuilding sqlite cache")
			if err := db.reset(ctx); err != nil {
				return ScanStats{}, err
			}
			existing = map[string]fileRecord{}
			forceClean = true
		}
	}
	publishedState := IndexState{}
	cachedEngineFingerprint := ""
	cachedRuleVersion := ""
	if !forceClean {
		publishedState, err = db.IndexState(ctx)
		if err != nil {
			return ScanStats{}, err
		}
		if publishedState.Ready() {
			cachedEngineFingerprint, err = db.metaValue(ctx, "engine_data_fingerprint")
			if err != nil {
				return ScanStats{}, err
			}
			cachedRuleVersion, err = db.metaValue(ctx, "index_rule_version")
			if err != nil {
				return ScanStats{}, err
			}
		}
	}
	engineFingerprint, err := engineDataFingerprint(cfg.EngineLogs)
	if err != nil {
		return ScanStats{}, err
	}
	engineDataDirty := forceClean || !publishedState.Ready() || engineFingerprint != cachedEngineFingerprint

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return ScanStats{}, err
	}
	defer tx.Rollback()
	ftsCurrent := false
	if !forceClean && ftsPresentBeforeSchema && db.tableExists(ctx, "search_fts") {
		ftsCurrent, err = searchFTSCacheMatches(ctx, tx)
		if err != nil {
			return ScanStats{}, err
		}
	}

	locKeys := map[string]bool{}
	resources := map[string]bool{}
	tracked := map[string]bool{}
	oldFileIDs := map[int64]bool{}
	newFileIDs := map[int64]bool{}
	affected := map[string]bool{}
	fileChanges := forceClean
	scopedFinalizerCandidate := !forceClean && !engineDataDirty
	trackOldFile := func(fileID int64) error {
		if fileID == 0 {
			return nil
		}
		oldFileIDs[fileID] = true
		if !scopedFinalizerCandidate {
			return nil
		}
		if len(oldFileIDs)+len(newFileIDs) > scopedFinalizerFileLimit {
			scopedFinalizerCandidate = false
			return nil
		}
		if err := collectAffectedForFileTx(ctx, tx, fileID, affected); err != nil {
			return err
		}
		if len(affected) > scopedFinalizerSymbolLimit {
			scopedFinalizerCandidate = false
		}
		return nil
	}
	trackNewFile := func(fileID int64) error {
		if fileID == 0 {
			return nil
		}
		newFileIDs[fileID] = true
		if !scopedFinalizerCandidate {
			return nil
		}
		if len(oldFileIDs)+len(newFileIDs) > scopedFinalizerFileLimit {
			scopedFinalizerCandidate = false
			return nil
		}
		if err := collectAffectedForFileTx(ctx, tx, fileID, affected); err != nil {
			return err
		}
		if len(affected) > scopedFinalizerSymbolLimit {
			scopedFinalizerCandidate = false
		}
		return nil
	}

	// Collect file jobs first, then parse them concurrently.
	var jobs []fileJob
	for _, src := range cfg.Sources {
		if src.Name == "" || src.Path == "" {
			continue
		}
		if err := filepath.WalkDir(src.Path, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if shouldPruneSourceDir(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			kind := classifyRel(rel)
			if kind == "" {
				return nil
			}
			jobs = append(jobs, fileJob{
				src:        src,
				path:       path,
				rel:        rel,
				kind:       kind,
				prev:       existing[path],
				forceParse: engineDataDirty && kind == "script",
			})
			return nil
		}); err != nil {
			return ScanStats{}, fmt.Errorf("scan source %q: %w", src.Name, err)
		}
	}

	// Override pass: files with the same rel_path across sources.
	// The source with the lowest rank (highest priority) wins; others
	// are skipped entirely (only a file record is stored, no parsing).
	replacePaths, err := collectSourceReplacePaths(cfg.Sources)
	if err != nil {
		return ScanStats{}, err
	}
	overrideWinners := map[string]Source{} // rel_path -> highest-priority source
	for _, j := range jobs {
		if winner, ok := overrideWinners[j.rel]; !ok || j.src.Rank < winner.Rank {
			overrideWinners[j.rel] = j.src
		}
	}
	sourceNameByRank := map[int]string{}
	for _, source := range cfg.Sources {
		sourceNameByRank[source.Rank] = source.Name
	}
	overriddenCount := 0
	for i := range jobs {
		winner := overrideWinners[jobs[i].rel]
		if jobs[i].src.Rank > winner.Rank {
			jobs[i].overridden = true
			jobs[i].overrideReason = "same_relative_path"
			jobs[i].overrideBySource = winner.Name
			jobs[i].overrideByRank = winner.Rank
			jobs[i].overrideRule = jobs[i].rel
			overriddenCount++
		} else if rank, rule, ok := replacePathEvidence(jobs[i].rel, jobs[i].src.Rank, replacePaths); ok {
			jobs[i].overridden = true
			jobs[i].overrideReason = "descriptor_replace_path"
			jobs[i].overrideBySource = sourceNameByRank[rank]
			jobs[i].overrideByRank = rank
			jobs[i].overrideRule = rule
			overriddenCount++
		}
	}
	stats.Overridden = overriddenCount

	jobsCh := make(chan fileJob, 256)
	resCh := make(chan fileResult, 256)
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parseFileWorker(jobsCh, resCh)
		}()
	}
	go func() {
		for _, j := range jobs {
			jobsCh <- j
		}
		close(jobsCh)
	}()
	go func() {
		wg.Wait()
		close(resCh)
	}()

	progressEvery := 2000
	processed := 0

	// Prepared statements: avoid re-parsing the same SQL once per row.
	fileStmt, err := tx.PrepareContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden,
		override_reason,override_by_source,override_by_rank,override_rule) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer fileStmt.Close()
	objStmt, err := tx.PrepareContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer objStmt.Close()
	refStmt, err := tx.PrepareContext(ctx, `INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved,
		relation,phase,confidence,resolution_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer refStmt.Close()
	diagStmt, err := tx.PrepareContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer diagStmt.Close()
	locStmt, err := tx.PrepareContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer locStmt.Close()
	resStmt, err := tx.PrepareContext(ctx, `INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer resStmt.Close()
	schemaStmt, err := tx.PrepareContext(ctx, `INSERT INTO schema_fields(object_type,field,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer schemaStmt.Close()
	fieldStmt, err := tx.PrepareContext(ctx, `INSERT INTO object_fields(object_type,object_name,field,value_shape,date_key,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer fieldStmt.Close()

	scopeStmt, err := tx.PrepareContext(ctx, `INSERT INTO saved_scopes(file_id,scope_name) VALUES(?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer scopeStmt.Close()
	varStmt, err := tx.PrepareContext(ctx, `INSERT INTO variables(file_id,var_name) VALUES(?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer varStmt.Close()
	writer := scanWriter{
		fileStmt:   fileStmt,
		objStmt:    objStmt,
		refStmt:    refStmt,
		diagStmt:   diagStmt,
		locStmt:    locStmt,
		resStmt:    resStmt,
		schemaStmt: schemaStmt,
		fieldStmt:  fieldStmt,
		scopeStmt:  scopeStmt,
		varStmt:    varStmt,
	}

	for res := range resCh {
		processed++
		if processed%progressEvery == 0 {
			fmt.Fprintf(os.Stderr, "[scan] %d/%d files indexed\n", processed, len(jobs))
		}
		src := res.job.src
		tracked[res.job.path] = true
		stats.Files++
		stats.BySource[src.Name]++
		if res.skip {
			if err := refreshSkippedFileMetadata(ctx, tx, res); err != nil {
				return ScanStats{}, err
			}
			continue
		}
		fileChanges = true
		if res.info == nil {
			if res.job.prev.ID != 0 {
				if err := trackOldFile(res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
				if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
			}
			continue
		}
		if res.overridden {
			if res.job.prev.ID != 0 {
				if err := trackOldFile(res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
				if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
			}
			if _, err := fileStmt.ExecContext(ctx, src.Name, src.Rank, res.job.path, res.job.rel, res.job.kind, res.info.ModTime().UnixNano(), res.info.Size(), res.sum, 1,
				res.job.overrideReason, res.job.overrideBySource, res.job.overrideByRank, res.job.overrideRule); err != nil {
				return ScanStats{}, err
			}
			continue
		}
		if res.job.prev.ID != 0 {
			if err := trackOldFile(res.job.prev.ID); err != nil {
				return ScanStats{}, err
			}
			if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
				return ScanStats{}, err
			}
		}
		rec, err := writeFileResult(ctx, writer, res, &stats, locKeys, resources)
		if err != nil {
			return ScanStats{}, err
		}
		if err := trackNewFile(rec.ID); err != nil {
			return ScanStats{}, err
		}
	}
	fmt.Fprintf(os.Stderr, "[scan] all %d files indexed, finalizing\n", processed)

	for path, ex := range existing {
		if tracked[path] {
			continue
		}
		if err := trackOldFile(ex.ID); err != nil {
			return ScanStats{}, err
		}
		fileChanges = true
		if err := deleteFileRecords(ctx, tx, ex.ID); err != nil {
			return ScanStats{}, err
		}
	}

	// CWTools-style scan planning: a full filesystem walk is allowed to prove
	// that the semantic snapshot is still current. The proof has to include
	// inputs outside ordinary script jobs (map CSV/.map files and engine logs),
	// otherwise an apparently no-op scan could leave a derived cache stale.
	if !fileChanges && !engineDataDirty && publishedState.Ready() && cachedRuleVersion == indexRuleVersion && ftsCurrent {
		stageStart := time.Now()
		mapFingerprint, mapReusable, activeMapFiles, err := mapInputFingerprint(cfg)
		if err != nil {
			return ScanStats{}, err
		}
		mapCurrent, err := mapCacheMatchesInput(ctx, tx, mapFingerprint, mapReusable, activeMapFiles)
		if err != nil {
			return ScanStats{}, err
		}
		if mapCurrent {
			if err := refreshScanStatsTotals(ctx, tx, &stats); err != nil {
				return ScanStats{}, err
			}
			if err := tx.Commit(); err != nil {
				return ScanStats{}, err
			}
			stats.Noop = true
			stats.TimingsMillis["reuse_published_index"] = time.Since(stageStart).Milliseconds()
			stats.ElapsedMillis = time.Since(start).Milliseconds()
			fmt.Fprintln(os.Stderr, "[scan] no input changes; reused published index")
			return stats, nil
		}
	}
	scopedFinalizer := scopedFinalizerCandidate && len(oldFileIDs)+len(newFileIDs) <= scopedFinalizerFileLimit && len(affected) <= scopedFinalizerSymbolLimit
	if scopedFinalizer {
		// A tiny provider edit can fan out to many consumers. Keep the scoped
		// validator below its SQL batch limit; a broad fan-out is still correct,
		// but is better served by the established global finalizer.
		fits, err := scopedValidatorCandidatesFit(ctx, tx, newFileIDs, affected, scopedValidatorFileLimit)
		if err != nil {
			return ScanStats{}, err
		}
		scopedFinalizer = fits
	}

	// Build indexes before running the cross-table finalizer queries so they
	// can use the indexes instead of full table scans. During a clean scan no
	// indexes existed yet, which would make the ref resolution and validator
	// joins grind to a halt. We commit the bulk-insert tx first, build indexes
	// in a fresh connection, then run finalizers in a new tx.
	fmt.Fprintln(os.Stderr, "[scan] committing indexed rows")
	stageStart := time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','finalizing')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return ScanStats{}, err
	}
	if err := tx.Commit(); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["commit_indexed_rows"] = time.Since(stageStart).Milliseconds()
	if forceClean {
		fmt.Fprintln(os.Stderr, "[scan] building sqlite indexes")
		stageStart = time.Now()
		if err := db.CreateIndexes(ctx); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["build_indexes"] = time.Since(stageStart).Milliseconds()
		fmt.Fprintln(os.Stderr, "[scan] sqlite indexes ready")
	}
	stageStart = time.Now()
	tx2, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return ScanStats{}, err
	}
	defer tx2.Rollback()
	tx = tx2
	stats.TimingsMillis["begin_finalize_tx"] = time.Since(stageStart).Milliseconds()

	if scopedFinalizer {
		fmt.Fprintln(os.Stderr, "[scan] resolving changed references only")
		stageStart = time.Now()
		if err := refreshRefsResolvedScoped(ctx, tx, newFileIDs, affected); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["resolve_refs"] = time.Since(stageStart).Milliseconds()
		stats.TimingsMillis["resolve_refs_scoped"] = stats.TimingsMillis["resolve_refs"]

		fmt.Fprintln(os.Stderr, "[scan] writing changed validation diagnostics only")
		stageStart = time.Now()
		if err := refreshValidatorDiagnosticsScoped(ctx, tx, newFileIDs, affected); err != nil {
			return ScanStats{}, err
		}
		// Title/duplicate integrity is a graph-level invariant. Keep its proven
		// full refresh for semantic file changes, while map-only changes can skip
		// it because they do not alter the indexed object graph.
		if fileChanges {
			if err := refreshTitleIntegrityDiagnostics(ctx, tx); err != nil {
				return ScanStats{}, err
			}
		}
		stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()
		stats.TimingsMillis["validator_scoped"] = stats.TimingsMillis["validator"]
	} else {
		fmt.Fprintln(os.Stderr, "[scan] loading active symbol tables")
		stageStart = time.Now()
		// Re-resolve refs against the current state of active objects.
		objectNames, err := loadAllObjectNames(ctx, tx)
		if err != nil {
			return ScanStats{}, err
		}
		// Load ALL existing localization keys and resources from the database
		// BEFORE resolving refs, so unchanged files' keys are not treated as
		// unresolved just because they were not parsed in this incremental scan.
		if err := loadAllLocKeys(ctx, tx, locKeys); err != nil {
			return ScanStats{}, err
		}
		if err := loadAllResources(ctx, tx, resources); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["load_symbols"] = time.Since(stageStart).Milliseconds()
		fmt.Fprintln(os.Stderr, "[scan] resolving references")
		stageStart = time.Now()
		if err := refreshRefsResolvedGo(ctx, tx, objectNames, locKeys, resources); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["resolve_refs"] = time.Since(stageStart).Milliseconds()

		// Re-run validator cross-file integrity diagnostics.
		fmt.Fprintln(os.Stderr, "[scan] writing validation diagnostics")
		stageStart = time.Now()
		if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='validator'`); err != nil {
			return ScanStats{}, err
		}
		if err := addValidationDiagnostics(ctx, tx, locKeys, resources, objectNames); err != nil {
			return ScanStats{}, err
		}
		if err := refreshTitleIntegrityDiagnostics(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()
	}

	fmt.Fprintln(os.Stderr, "[scan] checking map context cache inputs")
	stageStart = time.Now()
	mapFingerprint, mapReusable, activeMapFiles, err := mapInputFingerprint(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	mapCurrent, err := mapCacheMatchesInput(ctx, tx, mapFingerprint, mapReusable, activeMapFiles)
	if err != nil {
		return ScanStats{}, err
	}
	if mapCurrent {
		fmt.Fprintln(os.Stderr, "[scan] reusing map context cache")
		stats.TimingsMillis["map_context_reused"] = time.Since(stageStart).Milliseconds()
	} else {
		fmt.Fprintln(os.Stderr, "[scan] rebuilding map context cache")
		if err := rebuildMapCache(ctx, tx, cfg); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["map_context_rebuild"] = time.Since(stageStart).Milliseconds()
	}
	stats.TimingsMillis["map_context"] = time.Since(stageStart).Milliseconds()

	fmt.Fprintln(os.Stderr, "[scan] refreshing engine data and semantic FTS")
	stageStart = time.Now()
	if engineDataDirty {
		fmt.Fprintln(os.Stderr, "[scan] ingesting changed engine logs")
		if err := rebuildEngineData(ctx, tx, cfg.EngineLogs); err != nil {
			return ScanStats{}, err
		}
		if err := storeEngineDataFingerprint(ctx, tx, engineFingerprint); err != nil {
			return ScanStats{}, err
		}
	}
	fullFTSRebuild := engineDataDirty || cachedRuleVersion != indexRuleVersion || !ftsPresentBeforeSchema || !ftsCurrent || !db.tableExists(ctx, "search_fts")
	ftsStart := time.Now()
	if fullFTSRebuild {
		fmt.Fprintln(os.Stderr, "[scan] rebuilding semantic FTS")
		if err := rebuildSearchFTS(ctx, tx); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["semantic_fts_rebuild"] = time.Since(ftsStart).Milliseconds()
	} else {
		fmt.Fprintf(os.Stderr, "[scan] refreshing semantic FTS for %d changed files\n", len(oldFileIDs)+len(newFileIDs))
		if err := refreshSearchFTSForFiles(ctx, tx, oldFileIDs, newFileIDs); err != nil {
			return ScanStats{}, err
		}
		stats.TimingsMillis["semantic_fts_scoped"] = time.Since(ftsStart).Milliseconds()
	}
	if err := storeSearchFTSRowCount(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["semantic_fts"] = time.Since(stageStart).Milliseconds()

	stageStart = time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('index_rule_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, indexRuleVersion); err != nil {
		return ScanStats{}, err
	}
	if err := bumpScanGeneration(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return ScanStats{}, err
	}
	// The overview is derived only from the semantic tables. A map-only input
	// change therefore has nothing new to publish here.
	if fileChanges {
		if err := db.RefreshArchitectureOverviewCache(ctx, tx); err != nil {
			return ScanStats{}, err
		}
	}
	if err := refreshScanStatsTotals(ctx, tx, &stats); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["count_diagnostics"] = time.Since(stageStart).Milliseconds()
	stageStart = time.Now()
	if err := tx.Commit(); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["commit_finalize"] = time.Since(stageStart).Milliseconds()
	stageStart = time.Now()
	checkpoint, checkpointErr := db.checkpointWALAfterScan(ctx)
	if checkpointErr != nil {
		fmt.Fprintf(os.Stderr, "[scan] WAL checkpoint deferred after scan: %v\n", checkpointErr)
	} else {
		stats.WALCheckpoint = &checkpoint
		fmt.Fprintf(os.Stderr, "[scan] WAL checkpoint %s busy=%d frames=%d/%d\n", checkpoint.Mode, checkpoint.Busy, checkpoint.CheckpointedFrames, checkpoint.LogFrames)
	}
	var freePages, totalPages int
	_ = db.sql.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages)
	_ = db.sql.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&totalPages)
	if totalPages > 0 && freePages*100/totalPages >= 5 {
		// VACUUM is intentionally not a normal scan-finalizer. It needs a much
		// stronger lock and can create another large write burst; leave space
		// recovery to an explicit maintenance operation instead.
		fmt.Fprintf(os.Stderr, "[scan] cache has %d free pages; deferred VACUUM to explicit maintenance\n", freePages)
	}
	stats.TimingsMillis["checkpoint_wal"] = time.Since(stageStart).Milliseconds()
	for _, key := range []string{"commit_indexed_rows", "build_indexes", "begin_finalize_tx", "load_symbols", "resolve_refs", "resolve_refs_scoped", "validator", "validator_scoped", "map_context", "map_context_rebuild", "map_context_reused", "semantic_fts", "semantic_fts_rebuild", "semantic_fts_scoped", "count_diagnostics", "commit_finalize", "checkpoint_wal"} {
		if ms, ok := stats.TimingsMillis[key]; ok {
			fmt.Fprintf(os.Stderr, "[scan] timing %s=%dms\n", key, ms)
		}
	}
	stats.ElapsedMillis = time.Since(start).Milliseconds()
	return stats, nil
}

func refreshScanStatsTotals(ctx context.Context, tx *sql.Tx, stats *ScanStats) error {
	counts := []struct {
		table string
		value *int
	}{
		{"nodes", &stats.Nodes},
		{"objects", &stats.Objects},
		{"refs", &stats.References},
		{"localization", &stats.Localization},
		{"resources", &stats.Resources},
		{"schema_fields", &stats.SchemaFields},
		{"object_fields", &stats.ObjectFields},
		{"diagnostics", &stats.Diagnostics},
	}
	for _, count := range counts {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+count.table).Scan(count.value); err != nil {
			return fmt.Errorf("count indexed %s: %w", count.table, err)
		}
	}
	return nil
}

func deleteFileRecords(ctx context.Context, tx *sql.Tx, fileID int64) error {
	// nodes/object_defs are no longer written, but current schemas always
	// contain them. Propagating deletion failures prevents a locked or corrupt
	// cache from being partially refreshed under the guise of compatibility.
	for _, table := range []string{"objects", "refs", "localization", "resources", "schema_fields", "object_fields", "diagnostics", "saved_scopes", "variables", "nodes", "object_defs"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE file_id=?`, fileID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id=?`, fileID); err != nil {
		return err
	}
	return nil
}

func refreshSkippedFileMetadata(ctx context.Context, tx *sql.Tx, result fileResult) error {
	if result.job.prev.ID == 0 || result.info == nil {
		return nil
	}
	mtime := result.info.ModTime().UnixNano()
	size := result.info.Size()
	if result.job.prev.MTime == mtime && result.job.prev.Size == size {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE files SET mtime=?,file_size=? WHERE id=?`,
		mtime, size, result.job.prev.ID)
	return err
}

func writeFileResult(ctx context.Context, w scanWriter, res fileResult, stats *ScanStats, locKeys, resources map[string]bool) (fileRecord, error) {
	src := res.job.src
	r2, err := w.fileStmt.ExecContext(ctx, src.Name, src.Rank, res.job.path, res.job.rel, res.job.kind, res.info.ModTime().UnixNano(), res.info.Size(), res.sum, 0, "", "", 0, "")
	if err != nil {
		return fileRecord{}, err
	}
	fid, err := r2.LastInsertId()
	if err != nil {
		return fileRecord{}, err
	}
	rec := fileRecord{ID: fid, SourceName: src.Name, SourceRank: src.Rank, Path: res.job.path, RelPath: res.job.rel, Kind: res.job.kind, MTime: res.info.ModTime().UnixNano(), Size: res.info.Size(), SHA: res.sum}
	switch res.job.kind {
	case "script":
		for _, pe := range res.parsed.Errors {
			if _, err := w.diagStmt.ExecContext(ctx, "parser", "error", "parse_error", pe.Message, rec.ID, rec.Path, pe.Line, pe.Col); err != nil {
				return fileRecord{}, err
			}
			stats.Diagnostics++
		}
		// Context checks now run during the parse pass (checkScriptContext)
		// so we no longer store the full node tree, saving ~12M rows.
		for _, d := range res.ctxDiags {
			if _, err := w.diagStmt.ExecContext(ctx, "compiler", d.severity, d.code, d.msg, rec.ID, rec.Path, d.line, d.col); err != nil {
				return fileRecord{}, err
			}
			stats.Diagnostics++
		}
		for _, s := range res.savedScopes {
			if _, err := w.scopeStmt.ExecContext(ctx, rec.ID, s); err != nil {
				return fileRecord{}, err
			}
		}
		for _, v := range res.variables {
			if _, err := w.varStmt.ExecContext(ctx, rec.ID, v); err != nil {
				return fileRecord{}, err
			}
		}
		objs := extractObjects(rec, res.parsed.Nodes)
		for _, obj := range objs {
			if _, err := w.objStmt.ExecContext(ctx, obj.Type, obj.Name, obj.Value, obj.FileID, obj.NodeID, obj.SourceName, obj.SourceRank, obj.Path, obj.Line, obj.Col, obj.EndLine, obj.EndCol); err != nil {
				return fileRecord{}, err
			}
			stats.Objects++
		}
		refs := extractRefs(rec, res.parsed.Nodes, objs)
		for _, ref := range refs {
			if _, err := w.refStmt.ExecContext(ctx, ref.FromType, ref.FromName, ref.Kind, ref.Name, ref.FileID, ref.NodeID, ref.Line, ref.Col, ref.Raw, ref.Resolved,
				ref.Relation, ref.Phase, ref.Confidence, ref.ResolutionReason); err != nil {
				return fileRecord{}, err
			}
			stats.References++
		}
		fields := extractObjectFields(rec, res.parsed.Nodes, objs)
		for _, field := range fields {
			if _, err := w.fieldStmt.ExecContext(ctx, field.Type, field.ObjectName, field.Field, field.Shape, field.DateKey, field.FileID, field.SourceName, field.SourceRank, field.Path, field.Line, field.Raw); err != nil {
				return fileRecord{}, err
			}
			stats.ObjectFields++
		}
	case "localization":
		for _, e := range res.locs {
			locKeys[e.key] = true
			if _, err := w.locStmt.ExecContext(ctx, e.key, e.lang, e.val, rec.ID, rec.SourceName, rec.SourceRank, rec.Path, e.line, e.replace); err != nil {
				return fileRecord{}, err
			}
			stats.Localization++
		}
	case "resource":
		rp := normalizeResource(rec.RelPath)
		if _, err := w.resStmt.ExecContext(ctx, rp, strings.TrimPrefix(strings.ToLower(filepath.Ext(rp)), "."), rec.ID, rec.SourceName, rec.SourceRank, rec.Path); err != nil {
			return fileRecord{}, err
		}
		resources[rp] = true
		stats.Resources++
	case "schema":
		for _, e := range res.schemaEntries {
			if _, err := w.schemaStmt.ExecContext(ctx, e.typ, e.field, rec.ID, rec.SourceName, rec.SourceRank, rec.Path, e.line, e.raw); err != nil {
				return fileRecord{}, err
			}
			stats.SchemaFields++
		}
	}
	return rec, nil
}

func needsPathCacheRebuild(existing map[string]fileRecord) bool {
	if len(existing) == 0 {
		return false
	}
	checked := 0
	bad := 0
	for path := range existing {
		checked++
		if !filepath.IsAbs(path) {
			bad++
		}
		if checked >= 200 {
			break
		}
	}
	return checked > 0 && bad*2 >= checked
}

func loadAllObjectNames(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	names := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.object_type, o.name
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			return nil, err
		}
		names[typ+":"+name] = true
		names[name] = true
	}
	return names, rows.Err()
}

func loadAllLocKeys(ctx context.Context, tx *sql.Tx, seen map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT l.key
		FROM localization l JOIN files f ON f.id=l.file_id
		WHERE f.overridden=0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		seen[key] = true
	}
	return rows.Err()
}

func loadAllResources(ctx context.Context, tx *sql.Tx, seen map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.resource_path
		FROM resources r JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		seen[path] = true
	}
	return rows.Err()
}

// refreshRefsResolvedGo resolves refs in Go using the objects map rather than
// an SQL EXISTS subquery. This avoids needing the objects index during a
// clean scan, where indexes are built only after the bulk insert.
func refreshRefsResolvedGo(ctx context.Context, tx *sql.Tx, objectNames map[string]bool, locKeys map[string]bool, resPaths map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, ref_kind, ref_name, resolved, resolution_reason FROM refs`)
	if err != nil {
		return err
	}
	type rd struct {
		id       int64
		resolved bool
		reason   string
	}
	var updates []rd
	for rows.Next() {
		var id int64
		var kind, name string
		var current int
		var currentReason string
		if err := rows.Scan(&id, &kind, &name, &current, &currentReason); err != nil {
			rows.Close()
			return err
		}
		res := false
		switch kind {
		case "localization":
			res = locKeys[name]
		case "resource":
			res = resPaths[name]
		case "sound":
			res = IsSound(name)
		case "iterator":
			_, res = iteratorScopeIn[name]
		case "scope_transition":
			_, res = engineScopeTransitionsIn[name]
		case "define":
			_, res = engineDefines[name]
		case "flag", "global_var", "variable", "character_flag":
			res = true
		default:
			res = objectNames[kind+":"+name] || objectNames[name]
		}
		reason := referenceResolutionReason(kind, res)
		if (current != 0) == res && currentReason == reason {
			continue
		}
		updates = append(updates, rd{id: id, resolved: res, reason: reason})
	}
	rows.Close()

	// Batch only changed rows, grouped by the small set of resolution reasons.
	if len(updates) == 0 {
		return nil
	}
	groups := map[string][]int64{}
	for _, u := range updates {
		key := strconv.FormatBool(u.resolved) + "\x00" + u.reason
		groups[key] = append(groups[key], u.id)
	}
	for key, ids := range groups {
		parts := strings.SplitN(key, "\x00", 2)
		resolved := 0
		if parts[0] == "true" {
			resolved = 1
		}
		if err := batchUpdateResolution(ctx, tx, resolved, parts[1], ids); err != nil {
			return err
		}
	}
	return nil
}

func batchUpdateResolution(ctx context.Context, tx *sql.Tx, val int, reason string, ids []int64) error {
	const batchSize = 500
	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders := strings.Repeat("?,", end-i)
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, end-i+2)
		args = append(args, val, reason)
		for _, id := range ids[i:end] {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE refs SET resolved=?,resolution_reason=? WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

func referenceResolutionReason(kind string, resolved bool) string {
	if resolved {
		switch kind {
		case "localization":
			return "indexed_localization"
		case "resource":
			return "indexed_resource"
		case "sound":
			return "known_engine_sound"
		case "iterator", "scope_transition", "define":
			return "known_engine_symbol"
		case "flag", "global_var", "variable", "character_flag":
			return "runtime_symbol"
		default:
			return "indexed_definition"
		}
	}
	switch kind {
	case "scope":
		return "runtime_scope"
	case "localization":
		return "missing_localization"
	case "resource":
		return "missing_resource"
	case "sound":
		return "unknown_engine_sound"
	case "iterator", "scope_transition", "define":
		return "unknown_engine_symbol"
	default:
		if isObjectRefKind(kind) {
			return "missing_definition"
		}
		return "unverified_runtime_symbol"
	}
}

var ck3LoadRoots = map[string]bool{
	"common":       true,
	"events":       true,
	"history":      true,
	"gui":          true,
	"localization": true,
	"gfx":          true,
	"map_data":     true,
	"sound":        true,
}

// shouldPruneSourceDir rejects directories that CK3 will not load from a mod
// root. This is deliberately based on the source-relative first component:
// backup/tools/docs folders may themselves contain common/ or history/ trees,
// but those nested trees are not CK3 load roots and must not enter the index.
func shouldPruneSourceDir(rel string) bool {
	p := strings.Trim(filepath.ToSlash(strings.ToLower(rel)), "/")
	if p == "" || p == "." {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) == 1 {
		return !ck3LoadRoots[parts[0]]
	}
	return strings.HasPrefix(parts[len(parts)-1], ".")
}

func classifyRel(rel string) string {
	p := strings.Trim(filepath.ToSlash(strings.ToLower(rel)), "/")
	if p == "" || p == "." {
		return ""
	}
	parts := strings.Split(p, "/")
	root := parts[0]
	if !ck3LoadRoots[root] {
		return ""
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return ""
		}
	}
	ext := strings.ToLower(filepath.Ext(p))
	base := strings.ToLower(filepath.Base(p))
	if strings.Contains(base, "summary") {
		return ""
	}
	if ext == ".txt" && isGeographicalRegionDefinitionsPath(p) {
		return "script"
	}
	switch ext {
	case ".info":
		if root == "common" || root == "events" {
			return "schema"
		}
	case ".txt", ".gui", ".asset":
		if root == "common" || root == "events" || root == "history" || root == "gui" {
			return "script"
		}
		if root == "gfx" || root == "map_data" || root == "sound" {
			return "resource"
		}
	case ".yml", ".yaml":
		if root == "localization" {
			return "localization"
		}
	case ".dds", ".png", ".tga", ".jpg", ".jpeg", ".bmp", ".mesh", ".anim", ".shader", ".bk2", ".ttf", ".otf", ".wav", ".ogg":
		if root == "gfx" || root == "map_data" || root == "sound" {
			return "resource"
		}
	}
	return ""
}

func insertFile(ctx context.Context, tx *sql.Tx, src Source, path, rel, kind string, info os.FileInfo, sum string) (fileRecord, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256) VALUES(?,?,?,?,?,?,?,?)`,
		src.Name, src.Rank, path, rel, kind, info.ModTime().UnixNano(), info.Size(), sum)
	if err != nil {
		return fileRecord{}, err
	}
	id, _ := res.LastInsertId()
	return fileRecord{ID: id, SourceName: src.Name, SourceRank: src.Rank, Path: path, RelPath: rel, Kind: kind, MTime: info.ModTime().UnixNano(), Size: info.Size(), SHA: sum}, nil
}

func shaFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type fileJob struct {
	src              Source
	path             string
	rel              string
	kind             string
	prev             fileRecord
	overridden       bool
	overrideReason   string
	overrideBySource string
	overrideByRank   int
	overrideRule     string
	forceParse       bool
}

// guiBuiltinTypes are CK3 GUI type-building-block names that appear in
// nearly every .gui file and are not meaningful as standalone objects.
// extractObjects skips them to avoid false duplicate_object diagnostics.
var guiBuiltinTypes = map[string]bool{
	"container": true, "flowcontainer": true, "gridcontainer": true,
	"icon": true, "texticon": true, "button": true, "checkbox": true,
	"hbox": true, "vbox": true, "widget": true, "scrollbar": true,
	"list": true, "listbox": true, "edit": true, "label": true,
	"window": true, "text": true, "tooltip": true, "tab": true,
	"slider": true, "image": true, "combobox": true, "overlapping": true,
	"button_group": true, "button_round": true, "button_flat": true,
	"text_single": true, "text_multi": true, "scrollarea": true,
	"fixedgridbox": true, "dynamicgridbox": true, "portrait": true,
	"coat_of_arms": true, "background": true, "state": true,
	"types": true, "type": true, "template": true, "local_template": true,
	"block": true, "blockoverride": true,
	"var": true, "position": true, "size": true, "animation": true,
	"aigfx_window": true,
}

type locEntry struct {
	key, lang, val string
	line           int
	replace        int
}

type schemaEntry struct {
	typ, field string
	line       int
	raw        string
}

type fileResult struct {
	job           fileJob
	info          os.FileInfo
	sum           string
	skip          bool
	overridden    bool
	parsed        script.File
	locs          []locEntry
	schemaEntries []schemaEntry
	ctxDiags      []ctxDiag
	savedScopes   []string
	variables     []string
}

type ctxDiag struct {
	severity, code, msg string
	line, col           int
}

// parseFileWorker reads, hashes, and parses one file off the channel,
// returning a result that the main goroutine inserts into the database.
// Keeping parsing parallel but DB writes serial avoids SQLite contention.
func parseFileWorker(jobs <-chan fileJob, res chan<- fileResult) {
	for j := range jobs {
		res <- parseOneFile(j)
	}
}

// checkScriptContext walks the AST and flags effects used inside trigger-like
// blocks and triggers inside effect-like blocks. This replaces the old
// SQL-based checkContext which required the full nodes table to be stored.
func checkScriptContext(nodes []*script.Node, relPath string) []ctxDiag {
	var out []ctxDiag
	var walk func(ns []*script.Node, currentContext string)
	walk = func(ns []*script.Node, currentContext string) {
		for _, n := range ns {
			k := n.Key
			if currentContext == "trigger" && IsEffectOnly(k) {
				out = append(out, ctxDiag{severity: "error", code: "effect_in_trigger",
					msg:  fmt.Sprintf("effect %q appears inside a trigger-like block", k),
					line: n.Line, col: n.Col})
			}
			if currentContext == "effect" && IsTriggerOnly(k) {
				out = append(out, ctxDiag{severity: "warning", code: "trigger_in_effect",
					msg:  fmt.Sprintf("trigger %q appears inside an effect-like block", k),
					line: n.Line, col: n.Col})
			}
			// Context-only diagnostics are intentionally limited to the direct
			// contents of a known trigger/effect container. Many CK3 structural
			// and scope blocks legally contain both conditions and effects; blindly
			// inheriting through them creates thousands of false positives.
			childContext := ContextFor(strings.ToLower(k))
			walk(n.Children, childContext)
		}
	}
	walk(nodes, "")
	return out
}

func parseOneFile(j fileJob) fileResult {
	// Overridden files are metadata-only on the normal scan path. This keeps
	// incremental scans fast; deeper override analysis belongs in validation.
	if j.overridden {
		info, err := os.Stat(j.path)
		if err != nil {
			return fileResult{job: j, overridden: true}
		}
		sum, err := shaFile(j.path)
		if err != nil {
			sum = ""
		}
		if j.prev.ID != 0 && sum != "" && sum == j.prev.SHA && j.prev.Overridden &&
			j.prev.SourceName == j.src.Name && j.prev.SourceRank == j.src.Rank && j.prev.Kind == j.kind &&
			j.prev.OverrideReason == j.overrideReason && j.prev.OverrideBySource == j.overrideBySource &&
			j.prev.OverrideByRank == j.overrideByRank && j.prev.OverrideRule == j.overrideRule {
			return fileResult{job: j, info: info, sum: sum, skip: true}
		}
		return fileResult{job: j, info: info, sum: sum, overridden: true}
	}

	info, err := os.Stat(j.path)
	if err != nil {
		return fileResult{job: j}
	}
	// Incremental fast path: text is always hashed for correctness. Large
	// binary resources may trust nanosecond mtime plus size, avoiding repeated
	// reads of map rasters while still detecting ordinary same-second edits.
	if !j.forceParse && j.prev.ID != 0 && j.prev.SHA != "" && !j.prev.Overridden &&
		j.prev.SourceName == j.src.Name && j.prev.SourceRank == j.src.Rank &&
		j.prev.MTime == info.ModTime().UnixNano() && j.prev.Size == info.Size() && j.prev.Kind == j.kind {
		if j.kind != "script" && j.kind != "localization" && j.kind != "schema" {
			return fileResult{job: j, info: info, sum: j.prev.SHA, skip: true}
		}
	}
	data, err := os.ReadFile(j.path)
	if err != nil {
		return fileResult{job: j}
	}
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	if !j.forceParse && j.prev.ID != 0 && j.prev.SHA != "" && !j.prev.Overridden &&
		j.prev.SourceName == j.src.Name && j.prev.SourceRank == j.src.Rank &&
		j.prev.Kind == j.kind && sum == j.prev.SHA {
		return fileResult{job: j, info: info, sum: sum, skip: true}
	}
	r := fileResult{job: j, info: info, sum: sum}
	switch j.kind {
	case "script":
		isGUI := strings.HasSuffix(strings.ToLower(j.rel), ".gui")
		if isGUI {
			r.parsed = script.ParseGUI(string(data))
		} else {
			r.parsed = script.Parse(string(data))
			r.ctxDiags = checkScriptContext(r.parsed.Nodes, j.rel)
		}
		r.ctxDiags = append(r.ctxDiags, checkScriptLint(r.parsed.Nodes, j.rel, j.src.Name)...)
		if !isGUI {
			r.ctxDiags = append(r.ctxDiags, checkScopeTracker(r.parsed.Nodes, j.rel)...)
			r.savedScopes = collectSavedScopes(r.parsed.Nodes)
			r.variables = collectVariables(r.parsed.Nodes)
		}
		// M20: scripted effect recursion check needs the effect's name.
		if strings.Contains(j.rel, "scripted_effects") {
			for _, n := range r.parsed.Nodes {
				if n.Kind == "block" && n.Key != "" {
					r.ctxDiags = append(r.ctxDiags, checkScriptEffectRecursion(r.parsed.Nodes, j.rel, n.Key)...)
				}
			}
		}
	case "localization":
		r.locs = parseLocBytes(j.rel, data)
	case "schema":
		r.schemaEntries = parseSchemaBytes(j.rel, data)
	}
	return r
}

func parseLocBytes(rel string, data []byte) []locEntry {
	lang := languageFromPath(rel)
	replace := 0
	if strings.Contains(filepath.ToSlash(strings.ToLower(rel)), "/replace/") {
		replace = 1
	}
	var out []locEntry
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		m := locLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		val := m[2]
		val = strings.TrimPrefix(val, `"`)
		val = strings.TrimSuffix(val, `"`)
		out = append(out, locEntry{key: m[1], lang: lang, val: val, line: line, replace: replace})
	}
	return out
}

func parseSchemaBytes(rel string, data []byte) []schemaEntry {
	typ := objectTypeForPath(strings.ToLower(rel))
	if typ == "" && strings.Contains(strings.ToLower(rel), "events/") {
		typ = "event"
	}
	if typ == "" {
		return nil
	}
	var out []schemaEntry
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		m := infoFieldLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		field := m[1]
		lower := strings.ToLower(field)
		if ignoredInfoFields[lower] || strings.Contains(field, "X") {
			continue
		}
		key := typ + "\x00" + field
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, schemaEntry{typ: typ, field: field, line: line, raw: strings.TrimSpace(raw)})
	}
	return out
}

func insertLocEntries(ctx context.Context, tx *sql.Tx, rec fileRecord, entries []locEntry, seen map[string]bool) (int, error) {
	count := 0
	for _, e := range entries {
		seen[e.key] = true
		_, err := tx.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,?)`,
			e.key, e.lang, e.val, rec.ID, rec.SourceName, rec.SourceRank, rec.Path, e.line, e.replace)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func insertSchemaEntries(ctx context.Context, tx *sql.Tx, rec fileRecord, entries []schemaEntry) (int, error) {
	count := 0
	for _, e := range entries {
		_, err := tx.ExecContext(ctx, `INSERT INTO schema_fields(object_type,field,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?)`,
			e.typ, e.field, rec.ID, rec.SourceName, rec.SourceRank, rec.Path, e.line, e.raw)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func insertNodes(ctx context.Context, tx *sql.Tx, fileID int64, nodes []*script.Node) (int, error) {
	count := 0
	var walk func([]*script.Node) error
	walk = func(ns []*script.Node) error {
		for _, n := range ns {
			_, err := tx.ExecContext(ctx, `INSERT INTO nodes(file_id,local_id,parent_local_id,depth,key,operator,value,value_kind,start_line,start_col,end_line,end_col) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				fileID, n.ID, n.Parent, n.Depth, n.Key, n.Operator, n.Value, n.Kind, n.Line, n.Col, n.EndLine, n.EndCol)
			if err != nil {
				return err
			}
			count++
			if err := walk(n.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return count, walk(nodes)
}

func insertNodesPrepared(ctx context.Context, stmt *sql.Stmt, fileID int64, nodes []*script.Node) (int, error) {
	count := 0
	var walk func([]*script.Node) error
	walk = func(ns []*script.Node) error {
		for _, n := range ns {
			_, err := stmt.ExecContext(ctx, fileID, n.ID, n.Parent, n.Depth, n.Key, n.Operator, n.Value, n.Kind, n.Line, n.Col, n.EndLine, n.EndCol)
			if err != nil {
				return err
			}
			count++
			if err := walk(n.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return count, walk(nodes)
}

type objectRow struct {
	Type, Name      string
	Value           string
	FileID          int64
	NodeID          int64
	SourceName      string
	SourceRank      int
	Path            string
	Line, Col       int
	EndLine, EndCol int
}

type objectFieldRow struct {
	Type, ObjectName string
	Field, Shape     string
	DateKey          int
	FileID           int64
	SourceName       string
	SourceRank       int
	Path             string
	Line             int
	Raw              string
}

func extractObjects(rec fileRecord, nodes []*script.Node) []objectRow {
	var out []objectRow
	rel := filepath.ToSlash(strings.ToLower(rec.RelPath))
	topType := objectTypeForPath(rel)
	// Scripted variables are top-level @name = value substitutions. Keep them
	// in the ordinary object graph so search, definitions, references, source
	// priority, and public/private filtering all work without a parallel index.
	walk(nodes, func(n *script.Node) {
		if n.Kind == "atom" && scriptedVariableName.MatchString(strings.TrimSpace(n.Key)) {
			out = append(out, obj(rec, "scripted_variable", n.Key, n))
		}
	})
	if strings.Contains(rel, "/events/") || strings.HasPrefix(rel, "events/") {
		for _, n := range nodes {
			if n.Kind == "block" && strings.Contains(n.Key, ".") {
				out = append(out, obj(rec, "event", n.Key, n))
			}
		}
		return out
	}
	if isLawDefinitionsPath(rel) {
		for _, group := range nodes {
			if group.Kind != "block" || group.Key == "" {
				continue
			}
			out = append(out, obj(rec, "law_group", group.Key, group))
			for _, child := range group.Children {
				if child.Kind == "block" && child.Key != "" && !lawGroupFields[child.Key] {
					out = append(out, obj(rec, "law", child.Key, child))
				}
			}
		}
		return out
	}
	if isDoctrineDefinitionsPath(rel) {
		for _, group := range nodes {
			if group.Kind != "block" || group.Key == "" {
				continue
			}
			out = append(out, obj(rec, "doctrine_group", group.Key, group))
			for _, child := range group.Children {
				if child.Kind == "block" && child.Key != "" && !doctrineGroupFields[child.Key] {
					out = append(out, obj(rec, "doctrine", child.Key, child))
				}
			}
		}
		return out
	}
	if isGameRuleDefinitionsPath(rel) {
		for _, rule := range nodes {
			if rule.Kind != "block" || rule.Key == "" {
				continue
			}
			out = append(out, obj(rec, "game_rule", rule.Key, rule))
			for _, child := range rule.Children {
				if child.Kind == "block" && child.Key != "" && !gameRuleFields[child.Key] {
					out = append(out, obj(rec, "game_rule_setting", child.Key, child))
				}
			}
		}
		return out
	}
	if isCourtAmenityDefinitionsPath(rel) {
		for _, category := range nodes {
			if category.Kind != "block" || category.Key == "" {
				continue
			}
			out = append(out, obj(rec, "court_amenity_category", category.Key, category))
			for _, child := range category.Children {
				if child.Kind == "block" && child.Key != "" {
					out = append(out, obj(rec, "court_amenity_level", child.Key, child))
				}
			}
		}
		return out
	}
	if isAchievementGroupsPath(rel) {
		for _, group := range nodes {
			if group.Kind != "block" || group.Key != "group" {
				continue
			}
			name := childAtomValue(group, "name")
			if name != "" {
				out = append(out, obj(rec, "achievement_group", name, group))
			}
		}
		return out
	}
	if topType != "" {
		// For landed_titles, history/titles, and religion files, objects
		// are often deeply nested (kingdom→duchy→county→barony, or
		// religion→faiths→faith). Recurse to capture all levels.
		if topType == "title" {
			walkBlock(nodes, func(n *script.Node) {
				if n.Kind == "block" && n.Key != "" && n.Key != "color" && n.Key != "can_create" && n.Key != "allow" && n.Key != "cultural_names" {
					out = append(out, obj(rec, "title", n.Key, n))
				}
			})
		} else {
			for _, n := range nodes {
				if n.Kind == "block" && n.Key != "" {
					out = append(out, obj(rec, topType, n.Key, n))
				}
			}
		}
		// Religion files: also extract nested faiths from faiths={} blocks.
		if topType == "religion" {
			for _, n := range nodes {
				if n.Kind == "block" && n.Key != "" {
					for _, c := range n.Children {
						if c.Key == "faiths" && c.Kind == "block" {
							for _, f := range c.Children {
								if f.Kind == "block" && f.Key != "" {
									out = append(out, obj(rec, "faith", f.Key, f))
								}
							}
						}
					}
				}
			}
		}
	}
	if strings.HasSuffix(rel, ".gui") {
		for _, n := range nodes {
			if n.Kind == "block" && (n.Operator == "template" || n.Operator == "local_template") {
				out = append(out, obj(rec, "gui_template", n.Key, n))
				continue
			}
			if n.Kind == "block" && n.Key == "types" {
				for _, child := range n.Children {
					if child.Operator == "type" && child.Key != "" {
						out = append(out, obj(rec, "gui", child.Key, child))
					}
				}
				continue
			}
			// Preserve support for legacy/top-level GUI definitions while
			// excluding Jomini primitives and utility blocks.
			if n.Kind == "block" && n.Key != "" && !guiBuiltinTypes[strings.ToLower(n.Key)] {
				out = append(out, obj(rec, "gui", n.Key, n))
			}
		}
	}
	return out
}

func obj(rec fileRecord, typ, name string, n *script.Node) objectRow {
	value := ""
	if typ == "scripted_variable" {
		value = n.Value
	}
	return objectRow{Type: typ, Name: name, Value: value, FileID: rec.ID, NodeID: n.ID, SourceName: rec.SourceName, SourceRank: rec.SourceRank, Path: rec.Path, Line: n.Line, Col: n.Col, EndLine: n.EndLine, EndCol: n.EndCol}
}

func extractObjectFields(rec fileRecord, nodes []*script.Node, objs []objectRow) []objectFieldRow {
	filtered := make([]objectRow, 0, len(objs))
	for _, obj := range objs {
		if patternObjectTypes[obj.Type] {
			filtered = append(filtered, obj)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	byID := map[int64]*script.Node{}
	walk(nodes, func(n *script.Node) {
		byID[n.ID] = n
	})
	var out []objectFieldRow
	for _, obj := range filtered {
		n := byID[obj.NodeID]
		if n == nil {
			continue
		}
		for _, child := range n.Children {
			if child.Key == "" {
				continue
			}
			if obj.Type == "character" {
				if date, ok := parseDateKey(child.Key); ok && child.Kind == "block" {
					for _, historyField := range child.Children {
						appendCharacterHistoryFields(&out, rec, obj, historyField, date)
					}
					continue
				}
			}
			if obj.Type == "law_group" && child.Kind == "block" && !lawGroupFields[child.Key] {
				continue
			}
			if obj.Type == "doctrine_group" && child.Kind == "block" && !doctrineGroupFields[child.Key] {
				continue
			}
			if obj.Type == "game_rule" && child.Kind == "block" && !gameRuleFields[child.Key] {
				continue
			}
			if obj.Type == "court_amenity_category" && child.Kind == "block" {
				continue
			}
			out = append(out, objectFieldRow{
				Type:       obj.Type,
				ObjectName: obj.Name,
				Field:      child.Key,
				Shape:      fieldValueShape(child),
				DateKey:    0,
				FileID:     rec.ID,
				SourceName: rec.SourceName,
				SourceRank: rec.SourceRank,
				Path:       rec.Path,
				Line:       child.Line,
				Raw:        fieldRaw(child),
			})
		}
	}
	return out
}

// appendCharacterHistoryFields flattens one dated history block into the
// ordinary object_fields index. The date remains metadata instead of becoming
// a fake schema field such as "1066.1.1". CK3 also permits adoption and other
// history effects inside effect={}, so those direct children are indexed with
// the same date while the effect container itself is retained as evidence.
func appendCharacterHistoryFields(out *[]objectFieldRow, rec fileRecord, obj objectRow, field *script.Node, date int) {
	if field == nil || field.Key == "" {
		return
	}
	appendField := func(n *script.Node) {
		*out = append(*out, objectFieldRow{
			Type: obj.Type, ObjectName: obj.Name, Field: n.Key, Shape: fieldValueShape(n), DateKey: date,
			FileID: rec.ID, SourceName: rec.SourceName, SourceRank: rec.SourceRank,
			Path: rec.Path, Line: n.Line, Raw: fieldRaw(n),
		})
	}
	appendField(field)
	if field.Kind == "block" && strings.EqualFold(field.Key, "effect") {
		for _, child := range field.Children {
			if child.Key != "" {
				appendField(child)
			}
		}
	}
}

var patternObjectTypes = map[string]bool{
	"character":                     true,
	"event":                         true,
	"decision":                      true,
	"trait":                         true,
	"modifier":                      true,
	"opinion_modifier":              true,
	"scripted_effect":               true,
	"scripted_trigger":              true,
	"script_value":                  true,
	"character_interaction":         true,
	"scheme_type":                   true,
	"scheme_agent_type":             true,
	"scheme_pulse_action":           true,
	"scheme_countermeasure":         true,
	"building":                      true,
	"government":                    true,
	"law":                           true,
	"law_group":                     true,
	"doctrine":                      true,
	"doctrine_group":                true,
	"game_rule":                     true,
	"game_rule_setting":             true,
	"focus":                         true,
	"court_amenity_category":        true,
	"court_amenity_level":           true,
	"death_reason":                  true,
	"religion_family":               true,
	"fervor_modifier":               true,
	"lifestyle":                     true,
	"lifestyle_perk":                true,
	"achievement_group":             true,
	"activity":                      true,
	"activity_group_type":           true,
	"activity_locale":               true,
	"activity_guest_invite_rule":    true,
	"activity_intent":               true,
	"activity_pulse_action":         true,
	"artifact_type":                 true,
	"artifact_slot":                 true,
	"artifact_blueprint":            true,
	"artifact_feature_group":        true,
	"artifact_feature":              true,
	"artifact_template":             true,
	"artifact_visual":               true,
	"bookmark":                      true,
	"bookmark_challenge_character":  true,
	"bookmark_group":                true,
	"court_position":                true,
	"court_position_task":           true,
	"diarchy":                       true,
	"diarchy_mandate":               true,
	"domicile":                      true,
	"domicile_building":             true,
	"legend":                        true,
	"legend_chronicle":              true,
	"legend_seed":                   true,
	"raid_intent":                   true,
	"situation":                     true,
	"situation_catalyst":            true,
	"situation_group_type":          true,
	"struggle":                      true,
	"struggle_catalyst":             true,
	"subject_contract":              true,
	"subject_contract_group":        true,
	"tax_slot":                      true,
	"tax_obligation":                true,
	"travel_point_of_interest_type": true,
	"travel_option":                 true,
	"religion":                      true,
	"faith":                         true,
	"holy_site":                     true,
	"culture":                       true,
	"culture_tradition":             true,
	"culture_pillar":                true,
	"innovation":                    true,
	"name_list":                     true,
	"men_at_arms_type":              true,
	"casus_belli_type":              true,
	"on_action":                     true,
	"scripted_gui":                  true,
	"gui":                           true,
	"geographical_region":           true,
}

var lawGroupFields = map[string]bool{
	"can_change_law_group": true,
}

// Doctrine group fields come from common/religion/doctrines/_doctrines.info.
// Other direct child blocks are doctrine definitions.
var doctrineGroupFields = map[string]bool{
	"name":                   true,
	"group":                  true,
	"grouping":               true,
	"is_available_on_create": true,
	"number_of_picks":        true,
}

// Game rule fields come from common/game_rules/_game_rules.info. Other direct
// child blocks are selectable settings referenced by has_game_rule.
var gameRuleFields = map[string]bool{
	"categories": true,
}

func isLawDefinitionsPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(rel, "common/laws/")
}

func isDoctrineDefinitionsPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(rel, "common/religion/doctrines/")
}

func isGameRuleDefinitionsPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(rel, "common/game_rules/")
}

func isCourtAmenityDefinitionsPath(rel string) bool {
	rel = filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(rel, "common/court_amenities/")
}

var numericValue = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
var scriptedVariableName = regexp.MustCompile(`^@[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)

func isArithmeticExpression(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "@[")
}

func fieldValueShape(n *script.Node) string {
	if n.Kind == "block" {
		return "block"
	}
	if n.Kind == "bare" {
		return "bare"
	}
	if n.Operator != "" && n.Operator != "=" {
		return "compare"
	}
	v := strings.TrimSpace(n.Value)
	switch {
	case v == "yes" || v == "no":
		return "bool"
	case numericValue.MatchString(v):
		return "number"
	case strings.HasPrefix(v, "scope:"):
		return "scope_ref"
	case strings.HasPrefix(v, "flag:"):
		return "flag_ref"
	case isArithmeticExpression(v):
		return "expression"
	case strings.HasPrefix(v, "@"):
		return "define_ref"
	case strings.HasPrefix(v, "event:/"):
		return "sound"
	case strings.Contains(v, "$"):
		return "template"
	case strings.Contains(v, "gfx/") || resourceExt.MatchString(v):
		return "resource"
	case strings.HasSuffix(v, ".t") || strings.HasSuffix(v, ".desc") || strings.HasSuffix(v, ".tt"):
		return "localization"
	case strings.Contains(v, " "):
		return "string"
	default:
		return "atom"
	}
}

func fieldRaw(n *script.Node) string {
	if n.Kind == "block" {
		return n.Key + " = { ... }"
	}
	if n.Kind == "bare" {
		return n.Key
	}
	op := n.Operator
	if op == "" {
		op = "="
	}
	return strings.TrimSpace(n.Key + " " + op + " " + n.Value)
}

func objectTypeForPath(rel string) string {
	rel = filepath.ToSlash(rel)
	lowerRel := strings.ToLower(rel)
	if isGeographicalRegionDefinitionsPath(lowerRel) {
		return "geographical_region"
	}
	if isAchievementGroupsPath(lowerRel) {
		return "achievement_group"
	}
	// Culture content has several independent CK3 object namespaces. Treating
	// every file below common/culture as a generic culture hid tradition/pillar
	// dependencies and made type-scoped queries misleading.
	switch {
	case strings.Contains(lowerRel, "common/culture/cultures/"):
		return "culture"
	case strings.Contains(lowerRel, "common/culture/traditions/"):
		return "culture_tradition"
	case strings.Contains(lowerRel, "common/culture/innovations/"):
		return "innovation"
	case strings.Contains(lowerRel, "common/culture/pillars/"):
		return "culture_pillar"
	case strings.Contains(lowerRel, "common/culture/name_lists/"):
		return "name_list"
	case strings.Contains(lowerRel, "common/culture/eras/"):
		return "culture_era"
	case strings.Contains(lowerRel, "common/culture/aesthetics_bundles/"):
		return "culture_aesthetics_bundle"
	case strings.Contains(lowerRel, "common/culture/creation_names/"):
		return "culture_creation_name"
	case strings.Contains(lowerRel, "common/culture/name_equivalency/"):
		return "culture_name_equivalency"
	case strings.Contains(lowerRel, "common/schemes/scheme_types/"):
		return "scheme_type"
	case strings.Contains(lowerRel, "common/schemes/agent_types/"):
		return "scheme_agent_type"
	case strings.Contains(lowerRel, "common/schemes/pulse_actions/"):
		return "scheme_pulse_action"
	case strings.Contains(lowerRel, "common/schemes/scheme_countermeasures/"):
		return "scheme_countermeasure"
	case strings.Contains(lowerRel, "common/activities/activity_types/"):
		return "activity"
	case strings.Contains(lowerRel, "common/activities/activity_group_types/"):
		return "activity_group_type"
	case strings.Contains(lowerRel, "common/activities/activity_locales/"):
		return "activity_locale"
	case strings.Contains(lowerRel, "common/activities/guest_invite_rules/"):
		return "activity_guest_invite_rule"
	case strings.Contains(lowerRel, "common/activities/intents/"):
		return "activity_intent"
	case strings.Contains(lowerRel, "common/activities/pulse_actions/"):
		return "activity_pulse_action"
	case strings.Contains(lowerRel, "common/artifacts/types/"):
		return "artifact_type"
	case strings.Contains(lowerRel, "common/artifacts/slots/"):
		return "artifact_slot"
	case strings.Contains(lowerRel, "common/artifacts/blueprints/"):
		return "artifact_blueprint"
	case strings.Contains(lowerRel, "common/artifacts/feature_groups/"):
		return "artifact_feature_group"
	case strings.Contains(lowerRel, "common/artifacts/features/"):
		return "artifact_feature"
	case strings.Contains(lowerRel, "common/artifacts/templates/"):
		return "artifact_template"
	case strings.Contains(lowerRel, "common/artifacts/visuals/"):
		return "artifact_visual"
	case strings.Contains(lowerRel, "common/bookmarks/bookmarks/"):
		return "bookmark"
	case strings.Contains(lowerRel, "common/bookmarks/challenge_characters/"):
		return "bookmark_challenge_character"
	case strings.Contains(lowerRel, "common/bookmarks/groups/"):
		return "bookmark_group"
	case strings.Contains(lowerRel, "common/court_positions/types/"):
		return "court_position"
	case strings.Contains(lowerRel, "common/court_positions/tasks/"):
		return "court_position_task"
	case strings.Contains(lowerRel, "common/diarchies/diarchy_types/"):
		return "diarchy"
	case strings.Contains(lowerRel, "common/diarchies/diarchy_mandates/"):
		return "diarchy_mandate"
	case strings.Contains(lowerRel, "common/domiciles/types/"):
		return "domicile"
	case strings.Contains(lowerRel, "common/domiciles/buildings/"):
		return "domicile_building"
	case strings.Contains(lowerRel, "common/legends/legend_types/"):
		return "legend"
	case strings.Contains(lowerRel, "common/legends/chronicles/"):
		return "legend_chronicle"
	case strings.Contains(lowerRel, "common/legends/legend_seeds/"):
		return "legend_seed"
	case strings.Contains(lowerRel, "common/raids/intents/"):
		return "raid_intent"
	case strings.Contains(lowerRel, "common/situation/situations/"):
		return "situation"
	case strings.Contains(lowerRel, "common/situation/catalysts/"):
		return "situation_catalyst"
	case strings.Contains(lowerRel, "common/situation/situation_group_types/"):
		return "situation_group_type"
	case strings.Contains(lowerRel, "common/struggle/struggles/"):
		return "struggle"
	case strings.Contains(lowerRel, "common/struggle/catalysts/"):
		return "struggle_catalyst"
	case strings.Contains(lowerRel, "common/subject_contracts/contracts/"):
		return "subject_contract"
	case strings.Contains(lowerRel, "common/subject_contracts/groups/"):
		return "subject_contract_group"
	case strings.Contains(lowerRel, "common/tax_slots/types/"):
		return "tax_slot"
	case strings.Contains(lowerRel, "common/tax_slots/obligations/"):
		return "tax_obligation"
	case strings.Contains(lowerRel, "common/travel/point_of_interest_types/"):
		return "travel_point_of_interest_type"
	case strings.Contains(lowerRel, "common/travel/travel_options/"):
		return "travel_option"
	}
	if strings.Contains(lowerRel, "common/religion/holy_sites/") {
		return "holy_site"
	}
	if strings.Contains(lowerRel, "common/religion/religion_families/") {
		return "religion_family"
	}
	if strings.Contains(lowerRel, "common/religion/fervor_modifiers/") {
		return "fervor_modifier"
	}
	if strings.Contains(lowerRel, "common/religion/religions/") {
		return "religion"
	}
	commonDir := commonObjectType(rel)
	if commonDir != "" {
		switch commonDir {
		case "traits":
			return "trait"
		case "modifiers":
			return "modifier"
		case "decisions":
			return "decision"
		case "scripted_triggers":
			return "scripted_trigger"
		case "scripted_effects":
			return "scripted_effect"
		case "script_values":
			return "script_value"
		case "on_action":
			return "on_action"
		case "nicknames":
			return "nickname"
		case "landed_titles":
			return "title"
		case "religion", "religions":
			return "religion"
		case "culture", "cultures":
			return "culture"
		case "council_tasks":
			return "council_task"
		case "bookmarks":
			return "bookmark"
		case "factions":
			return "faction"
		case "scheme_types":
			return "scheme_type"
		case "intentions":
			return "intention"
		case "struggles":
			return "struggle"
		case "holy_sites":
			return "holy_site"
		case "memories":
			return "memory"
		case "buildings":
			return "building"
		case "men_at_arms_types", "men_at_arms":
			return "men_at_arms_type"
		case "casus_belli_types":
			return "casus_belli_type"
		case "governments":
			return "government"
		case "laws":
			return "law"
		case "focuses":
			return "focus"
		case "deathreasons":
			return "death_reason"
		case "secrets":
			return "secret"
		case "artifacts":
			return "artifact"
		default:
			return singularize(commonDir)
		}
	}
	switch {
	case strings.Contains(rel, "history/titles/"):
		return "title"
	case strings.Contains(rel, "history/characters/"):
		return "character"
	case strings.Contains(rel, "history/provinces/"):
		return "province_history"
	case strings.Contains(rel, "history/wars/"):
		return "war"
	case strings.Contains(rel, "history/artifacts/"):
		return "artifact_history"
	}
	return ""
}

func commonObjectType(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "common" {
			return parts[i+1]
		}
	}
	return ""
}

func isAchievementGroupsPath(rel string) bool {
	p := filepath.ToSlash(strings.ToLower(rel))
	return p == "common/achievement_groups.txt" || strings.HasSuffix(p, "/common/achievement_groups.txt")
}

func childAtomValue(parent *script.Node, key string) string {
	if parent == nil {
		return ""
	}
	for _, child := range parent.Children {
		if child.Kind == "atom" && child.Key == key {
			return cleanReferenceValue(child.Value)
		}
	}
	return ""
}

func singularize(s string) string {
	if strings.HasSuffix(s, "ies") && len(s) > 3 {
		return s[:len(s)-3] + "y"
	}
	if strings.HasSuffix(s, "s") && len(s) > 1 {
		return s[:len(s)-1]
	}
	return s
}

func insertObject(ctx context.Context, tx *sql.Tx, o objectRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.Type, o.Name, o.Value, o.FileID, o.NodeID, o.SourceName, o.SourceRank, o.Path, o.Line, o.Col, o.EndLine, o.EndCol)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO object_defs(object_type,name,file_id,node_local_id,source_name,source_rank,path,line,col) VALUES(?,?,?,?,?,?,?,?,?)`,
		o.Type, o.Name, o.FileID, o.NodeID, o.SourceName, o.SourceRank, o.Path, o.Line, o.Col)
	return err
}

func walk(nodes []*script.Node, fn func(*script.Node)) {
	for _, n := range nodes {
		fn(n)
		walk(n.Children, fn)
	}
}

// walkBlock recurses into block nodes, calling fn for each. Skips known
// non-object utility blocks (color, can_create, allow, cultural_names).
func walkBlock(nodes []*script.Node, fn func(*script.Node)) {
	for _, n := range nodes {
		if n.Kind == "block" {
			fn(n)
			walkBlock(n.Children, fn)
		}
	}
}

type refRow struct {
	FromType, FromName string
	Kind, Name, Raw    string
	Relation, Phase    string
	Confidence         string
	ResolutionReason   string
	FileID, NodeID     int64
	Line, Col          int
	Resolved           bool
}

var prefixTypes = map[string]string{
	"trait": "trait", "title": "title", "faith": "faith", "culture": "culture",
	"character": "character", "scope": "scope", "global_var": "global_var", "flag": "flag",
	"artifact": "artifact", "dynasty": "dynasty", "house": "dynasty_house", "secret": "secret",
	"geographical_region": "geographical_region",
}

var locKeys = map[string]bool{"title": true, "desc": true, "text": true, "custom_tooltip": true, "tooltip": true, "localization_key": true}
var resourceExt = regexp.MustCompile(`(?i)\.(dds|png|tga|jpe?g|bmp|mesh|anim|asset|gui|shader|bk2|ttf|otf|wav|ogg|txt)$`)

var keyRefTypes = map[string]string{
	"has_trait": "trait", "add_trait": "trait", "remove_trait": "trait", "trait": "trait",
	"has_character_modifier": "modifier", "add_character_modifier": "modifier", "remove_character_modifier": "modifier", "modifier": "modifier",
	"give_nickname": "nickname", "set_nickname": "nickname", "remove_nickname": "nickname",
	"set_character_faith": "faith", "faith": "faith", "religion": "religion",
	"set_culture": "culture", "culture": "culture",
	"title": "title", "capital": "title", "capital_county": "title", "de_jure_liege": "title",
	"government": "government", "has_government": "government",
	"law": "law", "has_law": "law", "add_realm_law": "law",
	"doctrine": "doctrine", "has_doctrine": "doctrine", "add_doctrine": "doctrine", "remove_doctrine": "doctrine",
	"has_game_rule": "game_rule_setting",
	"has_focus":     "focus", "set_focus": "focus",
	"lifestyle": "lifestyle", "has_lifestyle": "lifestyle", "refund_perks": "lifestyle",
	"has_perk": "lifestyle_perk", "add_perk": "lifestyle_perk", "remove_perk": "lifestyle_perk",
	"death_reason": "death_reason",
	"secret":       "secret", "add_secret": "secret", "has_secret": "secret",
	"casus_belli": "casus_belli_type", "using_cb": "casus_belli_type",
	"men_at_arms": "men_at_arms_type", "men_at_arms_type": "men_at_arms_type",
	"building": "building", "has_building": "building",
	"has_innovation": "innovation", "add_innovation": "innovation", "discover_innovation": "innovation",
	"artifact": "artifact", "create_artifact": "artifact",
	"scheme_type": "scheme_type", "add_agent_slot": "scheme_agent_type",
	"artifact_type":        "artifact_type",
	"activity_group_type":  "activity_group_type",
	"situation_group_type": "situation_group_type",
	"geographical_region":  "geographical_region", "add_geographical_region": "geographical_region", "remove_geographical_region": "geographical_region",
	"culture_overlaps_geographical_region": "geographical_region", "situation_sub_region_has_geographical_region": "geographical_region",
}

var eventVariableRelations = map[string]string{
	"set_variable":                "set_variable",
	"set_global_variable":         "set_global_variable",
	"set_local_variable":          "set_local_variable",
	"set_dead_character_variable": "set_dead_character_variable",
	"has_variable":                "read_variable",
	"change_variable":             "change_variable",
	"remove_variable":             "remove_variable",
	"clamp_variable":              "clamp_variable",
	"clear_variable":              "clear_variable",
}

var eventFlagRelations = map[string]string{
	"add_character_flag":      "add_character_flag",
	"remove_character_flag":   "remove_character_flag",
	"has_character_flag":      "read_character_flag",
	"add_dead_character_flag": "add_dead_character_flag",
	"has_dead_character_flag": "read_dead_character_flag",
}

func namedRuntimeValue(n *script.Node, fieldNames ...string) string {
	if n.Value != "" {
		return semanticTarget(n.Value)
	}
	for _, child := range n.Children {
		for _, field := range fieldNames {
			if child.Key == field && child.Value != "" {
				return semanticTarget(child.Value)
			}
		}
	}
	return ""
}

// eventLogicRefs retains the high-value runtime facts CWTools keeps in its
// event_logic table. They are relationships in the existing ref graph because
// variables and flags are runtime symbols, not independently loadable CK3
// definitions.
func eventLogicRefs(rec fileRecord, n *script.Node, current objectRow, nodesByID map[int64]*script.Node) []refRow {
	if current.Type != "event" && current.Type != "on_action" {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(n.Key))
	kind := ""
	relation := ""
	name := ""
	if r, ok := eventVariableRelations[key]; ok {
		kind, relation = "variable", r
		name = namedRuntimeValue(n, "name")
	} else if r, ok := eventFlagRelations[key]; ok {
		kind, relation = "character_flag", r
		name = namedRuntimeValue(n, "flag")
	}
	if name == "" {
		return nil
	}
	return []refRow{{
		FromType: current.Type, FromName: current.Name,
		Kind: kind, Name: name, Raw: name,
		Relation: relation, Phase: eventSemanticPhase(n, nodesByID), Confidence: "exact",
		FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col,
	}}
}

var eventSemanticPhases = map[string]bool{
	"trigger": true, "immediate": true, "after": true, "option": true,
	"on_trigger_fail": true, "effect": true,
	"events": true, "random_events": true, "first_valid": true,
	"on_actions": true, "random_on_actions": true, "first_valid_on_action": true,
}

func eventSemanticPhase(n *script.Node, nodesByID map[int64]*script.Node) string {
	for current := n; current != nil; current = nodesByID[current.Parent] {
		key := strings.ToLower(strings.TrimSpace(current.Key))
		if eventSemanticPhases[key] {
			return key
		}
		if current.Parent == 0 {
			break
		}
	}
	return ""
}

func semanticTarget(raw string) string {
	name := cleanReferenceValue(raw)
	if name == "" || name == "0" || name == "yes" || name == "no" || strings.ContainsAny(name, "$[]") {
		return ""
	}
	return name
}

// eventSemanticRefs captures the current CK3 event/on_action forms documented
// by common/on_action/_on_actions.info. These edges remain in the ordinary refs
// table; relation and phase make them useful as an event graph without a second
// event-only database.
func eventSemanticRefs(rec fileRecord, n *script.Node, current objectRow, nodesByID map[int64]*script.Node) []refRow {
	parent := nodesByID[n.Parent]
	add := func(kind, name, relation string) []refRow {
		name = semanticTarget(name)
		if name == "" {
			return nil
		}
		return []refRow{{
			FromType: current.Type, FromName: current.Name,
			Kind: kind, Name: name, Raw: name,
			Relation: relation, Phase: eventSemanticPhase(n, nodesByID), Confidence: "exact",
			FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col,
		}}
	}

	key := strings.ToLower(strings.TrimSpace(n.Key))
	if (key == "trigger_event" || key == "fire_event") && n.Value != "" {
		return add("event", n.Value, key)
	}
	if key == "on_action" && n.Value != "" {
		relation := "on_action"
		if parent != nil && (parent.Key == "trigger_event" || parent.Key == "fire_event") {
			relation = "trigger_on_action"
		}
		return add("on_action", n.Value, relation)
	}
	if key == "fallback" && current.Type == "on_action" && n.Value != "" {
		return add("on_action", n.Value, "fallback")
	}
	if parent == nil {
		return nil
	}
	parentKey := strings.ToLower(strings.TrimSpace(parent.Key))
	if key == "id" && (parentKey == "trigger_event" || parentKey == "fire_event") {
		return add("event", n.Value, parentKey)
	}
	if n.Kind == "bare" {
		switch parentKey {
		case "events", "first_valid":
			return add("event", n.Key, parentKey)
		case "on_actions", "first_valid_on_action":
			return add("on_action", n.Key, parentKey)
		}
	}
	if n.Value != "" {
		if _, err := strconv.ParseFloat(strings.TrimSpace(n.Key), 64); err == nil {
			switch parentKey {
			case "random_events":
				return add("event", n.Value, parentKey)
			case "random_on_actions":
				return add("on_action", n.Value, parentKey)
			}
		}
	}
	return nil
}

var characterHistoryReferenceKinds = map[string]string{
	"father":                 "character",
	"mother":                 "character",
	"employer":               "character",
	"spouse":                 "character",
	"add_spouse":             "character",
	"add_matrilineal_spouse": "character",
	"remove_spouse":          "character",
	"set_father":             "character",
	"set_mother":             "character",
	"set_real_father":        "character",
	"set_real_mother":        "character",
	"set_employer":           "character",
	"dynasty":                "dynasty",
	"dynasty_house":          "dynasty_house",
}

// characterHistoryPhase returns the normalized source date when n belongs to
// a dated block of the current history character. A true result with an empty
// phase means a static field directly or indirectly inside that character.
func characterHistoryPhase(n *script.Node, current objectRow, nodesByID map[int64]*script.Node) (string, bool) {
	if n == nil || current.Type != "character" || current.NodeID == 0 {
		return "", false
	}
	phase := ""
	for cursor := n; cursor != nil; cursor = nodesByID[cursor.Parent] {
		if _, ok := parseDateKey(cursor.Key); ok {
			phase = cursor.Key
		}
		if cursor.ID == current.NodeID {
			return phase, true
		}
		if cursor.Parent == 0 {
			break
		}
	}
	return "", false
}

// characterHistoryRefs adds only mechanically certain identity/lifecycle
// edges. Generic trait/culture/faith/death-reason refs are still handled by
// keyRefTypes below and receive the same relation/date metadata there.
func characterHistoryRefs(rec fileRecord, n *script.Node, current objectRow, nodesByID map[int64]*script.Node) []refRow {
	phase, belongs := characterHistoryPhase(n, current, nodesByID)
	if !belongs || n.Value == "" {
		return nil
	}
	relation := strings.ToLower(strings.TrimSpace(n.Key))
	kind, ok := characterHistoryReferenceKinds[relation]
	if !ok {
		return nil
	}
	name := semanticTarget(n.Value)
	if name == "" {
		return nil
	}
	if prefix, target, typed := strings.Cut(name, ":"); typed && prefixTypes[prefix] == kind {
		name = target
	}
	if name == "" || strings.Contains(name, ".") {
		return nil
	}
	return []refRow{{
		FromType: current.Type, FromName: current.Name,
		Kind: kind, Name: name, Raw: n.Value,
		Relation: relation, Phase: phase, Confidence: "exact",
		FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col,
	}}
}

func extractRefs(rec fileRecord, nodes []*script.Node, objs []objectRow) []refRow {
	var out []refRow
	nodesByID := map[int64]*script.Node{}
	walk(nodes, func(n *script.Node) {
		nodesByID[n.ID] = n
	})
	isCultureTraditionFile := isCultureTraditionsPath(rec.RelPath)
	isCultureDefinitionFile := isCultureCulturesPath(rec.RelPath)
	isReligionFile := isReligionRelPath(rec.RelPath)
	isLawFile := isLawDefinitionsPath(rec.RelPath)
	isDoctrineFile := isDoctrineDefinitionsPath(rec.RelPath)
	isGameRuleFile := isGameRuleDefinitionsPath(rec.RelPath)
	isCourtAmenityFile := isCourtAmenityDefinitionsPath(rec.RelPath)
	isAchievementGroupFile := isAchievementGroupsPath(rec.RelPath)
	isGUIFile := strings.HasSuffix(strings.ToLower(rec.RelPath), ".gui")
	walk(nodes, func(n *script.Node) {
		current := ownerForLine(objs, n.Line)
		out = append(out, eventSemanticRefs(rec, n, current, nodesByID)...)
		out = append(out, eventLogicRefs(rec, n, current, nodesByID)...)
		out = append(out, characterHistoryRefs(rec, n, current, nodesByID)...)
		out = append(out, geographicalRegionRefs(rec, n, current, nodesByID)...)
		addObjectRef := func(kind, raw string) {
			if name := cleanReferenceValue(raw); name != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: kind, Name: name, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		if isLawFile {
			parent := nodesByID[n.Parent]
			if parent != nil && parent.Parent == 0 {
				if n.Kind == "block" && n.Key != "" && !lawGroupFields[n.Key] {
					out = append(out, refRow{FromType: "law", FromName: n.Key, Kind: "law_group", Name: parent.Key, Raw: parent.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
				if n.Key == "default" && n.Value != "" {
					out = append(out, refRow{FromType: "law_group", FromName: parent.Key, Kind: "law", Name: n.Value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if isDoctrineFile {
			parent := nodesByID[n.Parent]
			if parent != nil && parent.Parent == 0 && n.Kind == "block" && n.Key != "" && !doctrineGroupFields[n.Key] {
				out = append(out, refRow{FromType: "doctrine", FromName: n.Key, Kind: "doctrine_group", Name: parent.Key, Raw: parent.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		if isGameRuleFile {
			parent := nodesByID[n.Parent]
			if parent != nil && parent.Parent == 0 {
				if n.Kind == "block" && n.Key != "" && !gameRuleFields[n.Key] {
					out = append(out, refRow{FromType: "game_rule_setting", FromName: n.Key, Kind: "game_rule", Name: parent.Key, Raw: parent.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
				if n.Key == "default" && n.Value != "" {
					out = append(out, refRow{FromType: "game_rule", FromName: parent.Key, Kind: "game_rule_setting", Name: n.Value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if isCourtAmenityFile {
			parent := nodesByID[n.Parent]
			if parent != nil && parent.Parent == 0 {
				if n.Kind == "block" && n.Key != "" {
					out = append(out, refRow{FromType: "court_amenity_level", FromName: n.Key, Kind: "court_amenity_category", Name: parent.Key, Raw: parent.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
				if n.Key == "default" && n.Value != "" {
					out = append(out, refRow{FromType: "court_amenity_category", FromName: parent.Key, Kind: "court_amenity_level", Name: n.Value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if isAchievementGroupFile && current.Type == "achievement_group" && n.Kind == "bare" {
			parent := nodesByID[n.Parent]
			if parent != nil && parent.Key == "order" {
				if name := cleanReferenceValue(n.Key); name != "" {
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "achievement", Name: name, Raw: n.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if parent := nodesByID[n.Parent]; parent != nil && n.Value != "" {
			isAmenityCategory := (n.Key == "target" && parent.Key == "amenity_level") ||
				(n.Key == "type" && (parent.Key == "set_amenity_level" || parent.Key == "add_amenity_level"))
			if isAmenityCategory {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "court_amenity_category", Name: n.Value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
			if n.Key == "type" && schemeTypeReferenceContext[parent.Key] {
				if name := cleanReferenceValue(n.Value); name != "" {
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "scheme_type", Name: name, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if current.Type == "scheme_type" && n.Kind == "bare" {
			parent := nodesByID[n.Parent]
			grandparent := (*script.Node)(nil)
			if parent != nil {
				grandparent = nodesByID[parent.Parent]
			}
			if parent != nil && parent.Key == "entries" && grandparent != nil && grandparent.Key == "pulse_actions" {
				if name := cleanReferenceValue(n.Key); name != "" {
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "scheme_pulse_action", Name: name, Raw: n.Key, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
		}
		if current.Type == "activity" {
			parent := nodesByID[n.Parent]
			grandparent := (*script.Node)(nil)
			if parent != nil {
				grandparent = nodesByID[parent.Parent]
			}
			if n.Kind == "bare" && parent != nil && parent.Key == "intents" && grandparent != nil && (grandparent.Key == "host_intents" || grandparent.Key == "guest_intents") {
				addObjectRef("activity_intent", n.Key)
			}
			if n.Key == "default" && parent != nil && (parent.Key == "host_intents" || parent.Key == "guest_intents") {
				addObjectRef("activity_intent", n.Value)
			}
			if parent != nil && parent.Key == "rules" && grandparent != nil && grandparent.Key == "guest_invite_rules" {
				addObjectRef("activity_guest_invite_rule", n.Value)
			}
			if n.Kind == "bare" && parent != nil && parent.Key == "entries" && grandparent != nil && grandparent.Key == "pulse_actions" {
				addObjectRef("activity_pulse_action", n.Key)
			}
		}
		if current.Type == "artifact_type" {
			parent := nodesByID[n.Parent]
			if n.Kind == "bare" && parent != nil && (parent.Key == "required_features" || parent.Key == "optional_features") {
				addObjectRef("artifact_feature", n.Key)
			}
			if n.Key == "default_visuals" {
				addObjectRef("artifact_visual", n.Value)
			}
		}
		if current.Type == "bookmark" && n.Key == "group" {
			addObjectRef("bookmark_group", n.Value)
		}
		if current.Type == "subject_contract_group" && n.Kind == "bare" {
			if parent := nodesByID[n.Parent]; parent != nil && parent.Key == "contracts" {
				addObjectRef("subject_contract", n.Key)
			}
		}
		if current.Type == "tax_slot" {
			if n.Key == "default_obligation" {
				addObjectRef("tax_obligation", n.Value)
			}
			if n.Kind == "bare" {
				if parent := nodesByID[n.Parent]; parent != nil && parent.Key == "obligations" {
					addObjectRef("tax_obligation", n.Key)
				}
			}
		}
		if current.Type == "legend_seed" && n.Key == "type" {
			addObjectRef("legend", n.Value)
		}
		if isGUIFile && n.Key == "using" && n.Value != "" {
			name := cleanReferenceValue(n.Value)
			if name != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "gui_template", Name: name, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		if isGUIFile && n.Value != "" && (n.Operator == "type" || (n.Operator == "=" && n.Kind == "block")) {
			base := cleanReferenceValue(n.Value)
			if base != "" && !guiBuiltinTypes[strings.ToLower(base)] {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "gui", Name: base, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		raws := []string{n.Value}
		if n.Kind == "bare" {
			raws = append(raws, n.Key)
		}
		// Track block-level constructs as self-references.
		if n.Kind == "block" && n.Key != "" {
			k := n.Key
			if _, ok := iteratorScopeIn[k]; ok {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "iterator", Name: k, Raw: k, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			} else if _, ok := engineScopeTransitionsIn[k]; ok {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "scope_transition", Name: k, Raw: k, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		// Track Jomini substitutions separately from engine defines. Scripted
		// variables use @name and are defined in loaded script; engine defines
		// use the @NAMESPACE|KEY form and remain validated by the current engine snapshot.
		if value := strings.TrimSpace(n.Value); strings.HasPrefix(value, "@") && len(value) > 2 && !isArithmeticExpression(value) {
			kind := ""
			switch {
			case scriptedVariableName.MatchString(value):
				kind = "scripted_variable"
			case strings.Contains(value, "|") && !strings.ContainsAny(value, " \t\r\n!#"):
				kind = "define"
			}
			if kind != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: kind, Name: value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		if isReligionFile {
			if refs := religionSpecificRefs(rec, n, current); len(refs) > 0 {
				out = append(out, refs...)
			}
			if isReligionCustomFaithIconValue(n, nodesByID) {
				return
			}
			if isReligionFaithIconField(n, current) && n.Value != "" && !strings.Contains(n.Value, "gfx/") && !resourceExt.MatchString(n.Value) {
				return
			}
		}
		if isCultureDefinitionFile {
			out = append(out, cultureDefinitionRefs(rec, n, current, nodesByID)...)
		}
		for _, raw := range raws {
			if raw == "" {
				continue
			}
			if isCultureTraditionFile {
				if path, ok := cultureTraditionLayerResource(n, nodesByID, raw); ok {
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "resource", Name: path, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
					continue
				}
				if isCultureTraditionLayerValue(n, nodesByID) {
					continue
				}
			}
			if p, name, ok := strings.Cut(raw, ":"); ok {
				if kind, yes := prefixTypes[p]; yes {
					// Skip scope expressions (contain dots or built-in scopes).
					if name == "prev" || name == "this" || name == "root" || strings.Contains(name, ".") || strings.HasPrefix(name, p+":") {
						continue
					}
					if expectedKind, handled := characterHistoryReferenceKinds[strings.ToLower(strings.TrimSpace(n.Key))]; handled && current.Type == "character" && expectedKind == kind {
						continue
					}
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: kind, Name: name, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
				}
			}
			if kind, yes := keyRefTypes[n.Key]; yes && !strings.Contains(raw, " ") && !strings.Contains(raw, "$") && raw != "yes" && raw != "no" {
				// Skip scope keywords and scope-chain expressions.
				if raw == "prev" || raw == "this" || raw == "root" || strings.Contains(raw, ".") || strings.HasPrefix(raw, "scope:") {
					continue
				}
				// Values ending in .t/.desc/.tt are localization keys, not object refs.
				if strings.HasSuffix(raw, ".t") || strings.HasSuffix(raw, ".desc") || strings.HasSuffix(raw, ".tt") {
					out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "localization", Name: raw, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
					continue
				}
				ref := refRow{FromType: current.Type, FromName: current.Name, Kind: kind, Name: raw, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col}
				if current.Type == "event" || current.Type == "on_action" {
					ref.Relation = n.Key
					ref.Phase = eventSemanticPhase(n, nodesByID)
					ref.Confidence = "exact"
				} else if phase, ok := characterHistoryPhase(n, current, nodesByID); ok {
					ref.Relation = n.Key
					ref.Phase = phase
					ref.Confidence = "exact"
				}
				out = append(out, ref)
			}
			if locKeys[n.Key] && !strings.Contains(raw, " ") && !strings.Contains(raw, "$") {
				// Skip GUI animation states, single chars, known non-loc values,
				// and GUI databind expressions (e.g., "[GetGeographicalRegion(...)]").
				if strings.HasPrefix(raw, "_") || len(raw) <= 1 || strings.HasPrefix(raw, "[") || strings.Contains(raw, "(") {
					continue
				}
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "localization", Name: raw, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
			if strings.HasPrefix(strings.Trim(raw, `"`), "event:/") {
				name := strings.Trim(raw, `"`)
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "sound", Name: name, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
			if strings.Contains(raw, "gfx/") || resourceExt.MatchString(raw) {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "resource", Name: normalizeResource(raw), Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
	})
	return out
}

// geographicalRegionRefs handles list-shaped region references that the
// ordinary key=value reference table cannot see. It covers region nesting as
// well as CK3 situation/building lists while leaving generic `regions` blocks
// alone outside geographical-region definitions.
func geographicalRegionRefs(rec fileRecord, n *script.Node, current objectRow, nodesByID map[int64]*script.Node) []refRow {
	if n == nil {
		return nil
	}
	add := func(raw, relation string) []refRow {
		name := cleanReferenceValue(raw)
		if name == "" {
			return nil
		}
		return []refRow{{
			FromType: current.Type, FromName: current.Name,
			Kind: "geographical_region", Name: name, Raw: raw,
			Relation: relation, Confidence: "exact",
			FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col,
		}}
	}
	if current.Type == "geographical_region" && n.Kind == "atom" && (n.Key == "region" || n.Key == "regions") {
		return add(n.Value, n.Key)
	}
	if n.Kind != "bare" {
		return nil
	}
	parent := nodesByID[n.Parent]
	if parent == nil {
		return nil
	}
	relation := strings.ToLower(strings.TrimSpace(parent.Key))
	switch relation {
	case "geographical_region", "geographical_regions", "graphical_regions":
		return add(n.Key, relation)
	case "region", "regions":
		if current.Type == "geographical_region" {
			return add(n.Key, relation)
		}
	}
	return nil
}

func isReligionRelPath(rel string) bool {
	p := filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(p, "common/religion/")
}

func religionSpecificRefs(rec fileRecord, n *script.Node, current objectRow) []refRow {
	var out []refRow
	if current.Type == "faith" {
		switch n.Key {
		case "holy_site":
			if cleanReferenceValue(n.Value) != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "holy_site", Name: cleanReferenceValue(n.Value), Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		case "icon", "reformed_icon":
			if raw := cleanReferenceValue(n.Value); raw != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "resource", Name: faithIconResource(raw), Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
	}
	if current.Type == "religion" && n.Kind == "block" && n.Key == "custom_faith_icons" {
		for _, child := range n.Children {
			raw := cleanReferenceValue(nodeReferenceValue(child))
			if raw == "" {
				continue
			}
			out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "resource", Name: faithIconResource(raw), Raw: raw, FileID: rec.ID, NodeID: child.ID, Line: child.Line, Col: child.Col})
		}
	}
	if current.Type == "religion" && n.Key == "family" {
		if raw := cleanReferenceValue(n.Value); raw != "" {
			out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "religion_family", Name: raw, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
		}
	}
	if current.Type == "religion_family" && n.Key == "hostility_doctrine" {
		if raw := cleanReferenceValue(n.Value); raw != "" {
			out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "doctrine", Name: raw, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
		}
	}
	if current.Type == "holy_site" {
		switch n.Key {
		case "county", "barony":
			if raw := cleanReferenceValue(n.Value); raw != "" {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "title", Name: raw, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
	}
	return out
}

func nodeReferenceValue(n *script.Node) string {
	if n == nil {
		return ""
	}
	if n.Value != "" {
		return n.Value
	}
	return n.Key
}

func cleanReferenceValue(raw string) string {
	raw = strings.TrimSpace(strings.Trim(raw, `"`))
	if raw == "" || raw == "yes" || raw == "no" || strings.Contains(raw, "$") || strings.Contains(raw, " ") {
		return ""
	}
	return raw
}

func faithIconResource(raw string) string {
	raw = strings.Trim(raw, `"`)
	if strings.Contains(raw, "gfx/") || resourceExt.MatchString(raw) {
		return normalizeResource(raw)
	}
	return normalizeResource("gfx/interface/icons/faith/" + raw + ".dds")
}

func isReligionFaithIconField(n *script.Node, current objectRow) bool {
	return current.Type == "faith" && (n.Key == "icon" || n.Key == "reformed_icon")
}

func isReligionCustomFaithIconValue(n *script.Node, nodesByID map[int64]*script.Node) bool {
	if n == nil {
		return false
	}
	parent := nodesByID[n.Parent]
	return parent != nil && parent.Kind == "block" && parent.Key == "custom_faith_icons"
}

var cultureTraditionLayerPaths = map[int]string{
	0: "gfx/interface/icons/culture_tradition/0-background",
	1: "gfx/interface/icons/culture_tradition/1-pattern",
	2: "gfx/interface/icons/culture_tradition/2-support",
	3: "gfx/interface/icons/culture_tradition/3-stroke",
	4: "gfx/interface/icons/culture_tradition/4-items",
}

func isCultureTraditionsPath(rel string) bool {
	p := filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(p, "common/culture/traditions/")
}

func isCultureCulturesPath(rel string) bool {
	p := filepath.ToSlash(strings.ToLower(rel))
	return strings.Contains(p, "common/culture/cultures/")
}

func cultureDefinitionRefs(rec fileRecord, n *script.Node, current objectRow, nodesByID map[int64]*script.Node) []refRow {
	if current.Type != "culture" || n == nil {
		return nil
	}
	add := func(kind, raw string) []refRow {
		name := cleanReferenceValue(raw)
		if name == "" {
			return nil
		}
		return []refRow{{
			FromType: current.Type,
			FromName: current.Name,
			Kind:     kind,
			Name:     name,
			Raw:      raw,
			FileID:   rec.ID,
			NodeID:   n.ID,
			Line:     n.Line,
			Col:      n.Col,
		}}
	}
	if n.Kind == "atom" {
		switch n.Key {
		case "ethos", "heritage", "language", "martial_custom", "head_determination":
			return add("culture_pillar", n.Value)
		case "name_list":
			return add("name_list", n.Value)
		case "parent":
			return add("culture", n.Value)
		}
	}
	if n.Kind != "bare" {
		return nil
	}
	parent := nodesByID[n.Parent]
	if parent == nil || parent.Kind != "block" {
		return nil
	}
	switch parent.Key {
	case "traditions":
		return add("culture_tradition", n.Key)
	case "parents":
		return add("culture", n.Key)
	}
	return nil
}

func cultureTraditionLayerResource(n *script.Node, nodesByID map[int64]*script.Node, raw string) (string, bool) {
	if !isCultureTraditionLayerValue(n, nodesByID) {
		return "", false
	}
	value := strings.Trim(raw, `"`)
	if value == "" || !resourceExt.MatchString(value) {
		return "", false
	}
	if strings.Contains(value, "gfx/") {
		return normalizeResource(value), true
	}
	idx, err := strconv.Atoi(n.Key)
	if err != nil {
		return "", false
	}
	base, ok := cultureTraditionLayerPaths[idx]
	if !ok {
		return "", false
	}
	return normalizeResource(base + "/" + value), true
}

func isCultureTraditionLayerValue(n *script.Node, nodesByID map[int64]*script.Node) bool {
	if n == nil || n.Kind != "atom" {
		return false
	}
	if _, err := strconv.Atoi(n.Key); err != nil {
		return false
	}
	parent := nodesByID[n.Parent]
	return parent != nil && parent.Kind == "block" && parent.Key == "layers"
}

func ownerForLine(objs []objectRow, line int) objectRow {
	var current objectRow
	for _, obj := range objs {
		if obj.Line <= line && obj.Line >= current.Line {
			current = obj
		}
	}
	return current
}

func countDiagnostics(ctx context.Context, tx *sql.Tx) int {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostics`).Scan(&n)
	return n
}

func insertRef(ctx context.Context, tx *sql.Tx, r refRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved,
		relation,phase,confidence,resolution_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.FromType, r.FromName, r.Kind, r.Name, r.FileID, r.NodeID, r.Line, r.Col, r.Raw, r.Resolved,
		r.Relation, r.Phase, r.Confidence, r.ResolutionReason)
	return err
}

var locLine = regexp.MustCompile(`^\s*([A-Za-z0-9_.:\-]+):\d*\s+(".*"|'.*')\s*$`)

func scanLocalization(ctx context.Context, tx *sql.Tx, rec fileRecord, seen map[string]bool) (int, error) {
	f, err := os.Open(rec.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lang := languageFromPath(rec.RelPath)
	replace := 0
	if strings.Contains(filepath.ToSlash(strings.ToLower(rec.RelPath)), "/replace/") {
		replace = 1
	}
	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		m := locLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		key := m[1]
		seen[key] = true
		_, err := tx.ExecContext(ctx, `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,?)`,
			key, lang, m[2], rec.ID, rec.SourceName, rec.SourceRank, rec.Path, line, replace)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, sc.Err()
}

func languageFromPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		if p == "localization" && i+1 < len(parts) {
			if parts[i+1] == "replace" && i+2 < len(parts) {
				return parts[i+2]
			}
			return parts[i+1]
		}
	}
	return "unknown"
}

func insertResource(ctx context.Context, tx *sql.Tx, rec fileRecord, res string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,?,?,?,?,?)`,
		res, strings.TrimPrefix(strings.ToLower(filepath.Ext(res)), "."), rec.ID, rec.SourceName, rec.SourceRank, rec.Path)
	return err
}

func normalizeResource(s string) string {
	s = filepath.ToSlash(strings.Trim(s, `"`))
	if i := strings.Index(s, "gfx/"); i >= 0 {
		return s[i:]
	}
	if i := strings.Index(s, "map_data/"); i >= 0 {
		return s[i:]
	}
	return s
}

func insertDiag(ctx context.Context, tx *sql.Tx, source, severity, code, msg string, fileID int64, path string, line, col int) {
	_, _ = tx.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`,
		source, severity, code, msg, fileID, path, line, col)
}

func addValidationDiagnostics(ctx context.Context, tx *sql.Tx, locSeen, resSeen, objSeen map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.ref_kind,r.ref_name,r.file_id,r.line,r.col,f.path,f.source_rank
		FROM refs r JOIN files f ON f.id=r.file_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name, path string
		var fileID int64
		var line, col, sourceRank int
		if err := rows.Scan(&kind, &name, &fileID, &line, &col, &path, &sourceRank); err != nil {
			return err
		}
		if sourceRank != 1 {
			continue
		}
		switch kind {
		case "localization":
			if !locSeen[name] {
				insertDiag(ctx, tx, "validator", "warning", "missing_localization", fmt.Sprintf("localization key %q was referenced but not indexed", name), fileID, path, line, col)
			}
		case "resource":
			if !resSeen[name] {
				code, severity := resourceDiagnostic(name)
				insertDiag(ctx, tx, "validator", severity, code, fmt.Sprintf("resource %q was referenced but not indexed", name), fileID, path, line, col)
			}
		case "sound":
			if !IsSound(name) {
				insertDiag(ctx, tx, "validator", "warning", "missing_sound", fmt.Sprintf("sound event %q was referenced but not known from game logs", name), fileID, path, line, col)
			}
		case "iterator":
			// Iterators are engine-level; validated against the iteratorScopeIn map.
			if _, ok := iteratorScopeIn[name]; !ok {
				insertDiag(ctx, tx, "validator", "warning", "unknown_iterator", fmt.Sprintf("iterator %q was referenced but not known", name), fileID, path, line, col)
			}
		case "scope_transition":
			// Scope transitions are engine-level.
		case "define":
			// Mods define their own @names; game-engine defines use NAI|xxx format.
			// Skip validation — too many false positives from mod-custom defines.
		default:
			if isObjectRefKind(kind) && !objSeen[kind+":"+name] && !objSeen[name] {
				insertDiag(ctx, tx, "validator", "warning", "missing_object_reference", fmt.Sprintf("%s %q was referenced but not indexed", kind, name), fileID, path, line, col)
			}
		}
	}
	return rows.Err()
}
