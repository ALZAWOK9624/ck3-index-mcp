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
	Database      string           `json:"database"`
	Files         int              `json:"files"`
	Nodes         int              `json:"nodes"`
	Objects       int              `json:"objects"`
	References    int              `json:"references"`
	Localization  int              `json:"localization"`
	Resources     int              `json:"resources"`
	SchemaFields  int              `json:"schema_fields"`
	ObjectFields  int              `json:"object_fields"`
	Diagnostics   int              `json:"diagnostics"`
	Overridden    int              `json:"overridden"`
	ElapsedMillis int64            `json:"elapsed_ms"`
	TimingsMillis map[string]int64 `json:"timings_ms,omitempty"`
	BySource      map[string]int   `json:"by_source"`
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
	ID         int64
	SourceName string
	SourceRank int
	Path       string
	RelPath    string
	Kind       string
	MTime      int64
	SHA        string
	Overridden bool
}

const indexRuleVersion = "2026-07-10-v0.2.1-resource-scope-engine-evidence"

func Scan(ctx context.Context, cfg Config) (ScanStats, error) {
	return scanWithMode(ctx, cfg, cfg.ForceClean)
}

func scanWithMode(ctx context.Context, cfg Config, forceClean bool) (ScanStats, error) {
	start := time.Now()
	if err := ConfigureEngineRules(cfg.EngineLogs); err != nil {
		return ScanStats{}, err
	}
	dbPath := filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database)
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
		`PRAGMA wal_checkpoint(TRUNCATE)`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=OFF`,
		`PRAGMA temp_store=MEMORY`,
		`PRAGMA cache_size=-200000`,
	} {
		if _, err := db.sql.ExecContext(ctx, p); err != nil {
			return ScanStats{}, err
		}
	}
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
		rows, err := db.sql.QueryContext(ctx, `SELECT id, source_name, source_rank, path, rel_path, kind, mtime, sha256, overridden FROM files`)
		if err != nil {
			return ScanStats{}, err
		}
		for rows.Next() {
			var rec fileRecord
			var recOvr int
			if err := rows.Scan(&rec.ID, &rec.SourceName, &rec.SourceRank, &rec.Path, &rec.RelPath, &rec.Kind, &rec.MTime, &rec.SHA, &recOvr); err != nil {
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

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return ScanStats{}, err
	}
	defer tx.Rollback()

	locKeys := map[string]bool{}
	resources := map[string]bool{}
	tracked := map[string]bool{}

	// Collect file jobs first, then parse them concurrently.
	var jobs []fileJob
	for _, src := range cfg.Sources {
		if src.Name == "" || src.Path == "" {
			continue
		}
		_ = filepath.WalkDir(src.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, relErr := filepath.Rel(src.Path, path)
			if relErr != nil {
				return nil
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
				src:  src,
				path: path,
				rel:  rel,
				kind: kind,
				prev: existing[path],
			})
			return nil
		})
	}

	// Override pass: files with the same rel_path across sources.
	// The source with the lowest rank (highest priority) wins; others
	// are skipped entirely (only a file record is stored, no parsing).
	overrideWinners := map[string]int{} // rel_path -> lowest rank
	for _, j := range jobs {
		if wr, ok := overrideWinners[j.rel]; !ok || j.src.Rank < wr {
			overrideWinners[j.rel] = j.src.Rank
		}
	}
	overriddenCount := 0
	for i := range jobs {
		if jobs[i].src.Rank > overrideWinners[jobs[i].rel] {
			jobs[i].overridden = true
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
	fileStmt, err := tx.PrepareContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer fileStmt.Close()
	objStmt, err := tx.PrepareContext(ctx, `INSERT INTO objects(object_type,name,file_id,node_local_id,source_name,source_rank,path,line,col) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return ScanStats{}, err
	}
	defer objStmt.Close()
	refStmt, err := tx.PrepareContext(ctx, `INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved) VALUES(?,?,?,?,?,?,?,?,?,?)`)
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
	fieldStmt, err := tx.PrepareContext(ctx, `INSERT INTO object_fields(object_type,object_name,field,value_shape,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?,?,?)`)
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
			continue
		}
		if res.info == nil {
			if res.job.prev.ID != 0 {
				if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
			}
			continue
		}
		if res.overridden {
			if res.job.prev.ID != 0 {
				if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
					return ScanStats{}, err
				}
			}
			if _, err := fileStmt.ExecContext(ctx, src.Name, src.Rank, res.job.path, res.job.rel, res.job.kind, res.info.ModTime().Unix(), res.sum, 1); err != nil {
				return ScanStats{}, err
			}
			continue
		}
		if res.job.prev.ID != 0 {
			if err := deleteFileRecords(ctx, tx, res.job.prev.ID); err != nil {
				return ScanStats{}, err
			}
		}
		if _, err := writeFileResult(ctx, writer, res, &stats, locKeys, resources); err != nil {
			return ScanStats{}, err
		}
	}
	fmt.Fprintf(os.Stderr, "[scan] all %d files indexed, finalizing\n", processed)

	for path, ex := range existing {
		if tracked[path] {
			continue
		}
		if err := deleteFileRecords(ctx, tx, ex.ID); err != nil {
			return ScanStats{}, err
		}
	}

	// Build indexes before running the cross-table finalizer queries so they
	// can use the indexes instead of full table scans. During a clean scan no
	// indexes existed yet, which would make the ref resolution and validator
	// joins grind to a halt. We commit the bulk-insert tx first, build indexes
	// in a fresh connection, then run finalizers in a new tx.
	fmt.Fprintln(os.Stderr, "[scan] committing indexed rows")
	stageStart := time.Now()
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
	stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()

	fmt.Fprintln(os.Stderr, "[scan] rebuilding map context cache")
	stageStart = time.Now()
	if err := rebuildMapCache(ctx, tx, cfg); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["map_context"] = time.Since(stageStart).Milliseconds()

	fmt.Fprintln(os.Stderr, "[scan] ingesting engine logs and rebuilding semantic FTS")
	stageStart = time.Now()
	if err := rebuildEngineData(ctx, tx, cfg.EngineLogs); err != nil {
		return ScanStats{}, err
	}
	if err := rebuildSearchFTS(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["semantic_fts"] = time.Since(stageStart).Milliseconds()

	stageStart = time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('index_rule_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, indexRuleVersion); err != nil {
		return ScanStats{}, err
	}
	if err := db.RefreshArchitectureOverviewCache(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	stats.Diagnostics = countDiagnostics(ctx, tx)
	stats.TimingsMillis["count_diagnostics"] = time.Since(stageStart).Milliseconds()
	stageStart = time.Now()
	if err := tx.Commit(); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["commit_finalize"] = time.Since(stageStart).Milliseconds()
	stageStart = time.Now()
	var freePages, totalPages int
	_ = db.sql.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages)
	_ = db.sql.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&totalPages)
	if totalPages > 0 && freePages*100/totalPages >= 5 {
		fmt.Fprintf(os.Stderr, "[scan] compacting sqlite cache (%d free pages)\n", freePages)
		if _, err := db.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return ScanStats{}, err
		}
		if _, err := db.sql.ExecContext(ctx, `VACUUM`); err != nil {
			return ScanStats{}, err
		}
	}
	stats.TimingsMillis["compact_cache"] = time.Since(stageStart).Milliseconds()
	for _, key := range []string{"commit_indexed_rows", "build_indexes", "begin_finalize_tx", "load_symbols", "resolve_refs", "validator", "map_context", "semantic_fts", "count_diagnostics", "commit_finalize", "compact_cache"} {
		if ms, ok := stats.TimingsMillis[key]; ok {
			fmt.Fprintf(os.Stderr, "[scan] timing %s=%dms\n", key, ms)
		}
	}
	stats.ElapsedMillis = time.Since(start).Milliseconds()
	return stats, nil
}

func deleteFileRecords(ctx context.Context, tx *sql.Tx, fileID int64) error {
	for _, table := range []string{"objects", "refs", "localization", "resources", "schema_fields", "object_fields", "diagnostics", "saved_scopes", "variables"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE file_id=?`, fileID); err != nil {
			return err
		}
	}
	// nodes/object_defs are no longer written; clean them defensively if the
	// database was created by an older ck3-index version.
	for _, table := range []string{"nodes", "object_defs"} {
		tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE file_id=?`, fileID) //nolint:errcheck
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id=?`, fileID); err != nil {
		return err
	}
	return nil
}

func writeFileResult(ctx context.Context, w scanWriter, res fileResult, stats *ScanStats, locKeys, resources map[string]bool) (fileRecord, error) {
	src := res.job.src
	r2, err := w.fileStmt.ExecContext(ctx, src.Name, src.Rank, res.job.path, res.job.rel, res.job.kind, res.info.ModTime().Unix(), res.sum, 0)
	if err != nil {
		return fileRecord{}, err
	}
	fid, err := r2.LastInsertId()
	if err != nil {
		return fileRecord{}, err
	}
	rec := fileRecord{ID: fid, SourceName: src.Name, SourceRank: src.Rank, Path: res.job.path, RelPath: res.job.rel, Kind: res.job.kind, MTime: res.info.ModTime().Unix(), SHA: res.sum}
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
			if _, err := w.objStmt.ExecContext(ctx, obj.Type, obj.Name, obj.FileID, obj.NodeID, obj.SourceName, obj.SourceRank, obj.Path, obj.Line, obj.Col); err != nil {
				return fileRecord{}, err
			}
			stats.Objects++
		}
		refs := extractRefs(rec, res.parsed.Nodes, objs)
		for _, ref := range refs {
			if _, err := w.refStmt.ExecContext(ctx, ref.FromType, ref.FromName, ref.Kind, ref.Name, ref.FileID, ref.NodeID, ref.Line, ref.Col, ref.Raw, ref.Resolved); err != nil {
				return fileRecord{}, err
			}
			stats.References++
		}
		fields := extractObjectFields(rec, res.parsed.Nodes, objs)
		for _, field := range fields {
			if _, err := w.fieldStmt.ExecContext(ctx, field.Type, field.ObjectName, field.Field, field.Shape, field.FileID, field.SourceName, field.SourceRank, field.Path, field.Line, field.Raw); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT id, ref_kind, ref_name, resolved FROM refs`)
	if err != nil {
		return err
	}
	type rd struct {
		id       int64
		resolved bool
	}
	var updates []rd
	for rows.Next() {
		var id int64
		var kind, name string
		var current int
		if err := rows.Scan(&id, &kind, &name, &current); err != nil {
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
			_, res = scopeTransitionsIn[name]
		case "define":
			_, res = tigerDefines[name]
		case "flag", "global_var":
			res = true
		default:
			res = objectNames[kind+":"+name] || objectNames[name]
		}
		if (current != 0) == res {
			continue
		}
		updates = append(updates, rd{id: id, resolved: res})
	}
	rows.Close()

	// Batch only changed rows: group by resolved value and run two range updates.
	if len(updates) == 0 {
		return nil
	}
	resolvedIDs := make([]int64, 0, len(updates))
	unresolvedIDs := make([]int64, 0, len(updates))
	for _, u := range updates {
		if u.resolved {
			resolvedIDs = append(resolvedIDs, u.id)
		} else {
			unresolvedIDs = append(unresolvedIDs, u.id)
		}
	}
	if err := batchUpdateResolved(ctx, tx, 1, resolvedIDs); err != nil {
		return err
	}
	if err := batchUpdateResolved(ctx, tx, 0, unresolvedIDs); err != nil {
		return err
	}
	return nil
}

func batchUpdateResolved(ctx context.Context, tx *sql.Tx, val int, ids []int64) error {
	const batchSize = 500
	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders := strings.Repeat("?,", end-i)
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, end-i+1)
		args = append(args, val)
		for _, id := range ids[i:end] {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE refs SET resolved=? WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return nil
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
	switch ext {
	case ".info":
		if root == "common" || root == "events" {
			return "schema"
		}
	case ".txt", ".gui", ".asset":
		if root == "common" || root == "events" || root == "history" || root == "gui" {
			return "script"
		}
	case ".yml", ".yaml":
		if root == "localization" {
			return "localization"
		}
	case ".dds", ".png", ".tga", ".mesh", ".anim", ".wav", ".ogg":
		if root == "gfx" || root == "map_data" || root == "sound" {
			return "resource"
		}
	}
	return ""
}

func insertFile(ctx context.Context, tx *sql.Tx, src Source, path, rel, kind string, info os.FileInfo, sum string) (fileRecord, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES(?,?,?,?,?,?,?)`,
		src.Name, src.Rank, path, rel, kind, info.ModTime().Unix(), sum)
	if err != nil {
		return fileRecord{}, err
	}
	id, _ := res.LastInsertId()
	return fileRecord{ID: id, SourceName: src.Name, SourceRank: src.Rank, Path: path, RelPath: rel, Kind: kind, MTime: info.ModTime().Unix(), SHA: sum}, nil
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
	src        Source
	path       string
	rel        string
	kind       string
	prev       fileRecord
	overridden bool
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
	"types": true, "var": true, "position": true, "animation": true,
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
		if j.prev.ID != 0 && sum != "" && sum == j.prev.SHA && j.prev.Overridden {
			return fileResult{job: j, info: info, sum: sum, skip: true}
		}
		return fileResult{job: j, info: info, sum: sum, overridden: true}
	}

	info, err := os.Stat(j.path)
	if err != nil {
		return fileResult{job: j}
	}
	// Incremental fast path: hash only if mtime+size is suspicious.
	if j.prev.ID != 0 && j.prev.SHA != "" && !j.prev.Overridden && j.prev.MTime == info.ModTime().Unix() && j.prev.Kind == j.kind {
		// Likely unchanged. We still verify by hash below only if cheap.
		// To be safe without re-reading huge binary assets, trust mtime+size
		// for non-script files and hash text files for correctness.
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
	if j.prev.ID != 0 && j.prev.SHA != "" && sum == j.prev.SHA {
		return fileResult{job: j, info: info, sum: sum, skip: true}
	}
	r := fileResult{job: j, info: info, sum: sum}
	switch j.kind {
	case "script":
		r.parsed = script.Parse(string(data))
		r.ctxDiags = checkScriptContext(r.parsed.Nodes, j.rel)
		r.ctxDiags = append(r.ctxDiags, checkScriptLint(r.parsed.Nodes, j.rel, j.src.Name)...)
		r.ctxDiags = append(r.ctxDiags, checkScopeTracker(r.parsed.Nodes, j.rel)...)
		r.savedScopes = collectSavedScopes(r.parsed.Nodes)
		r.variables = collectVariables(r.parsed.Nodes)
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
	Type, Name string
	FileID     int64
	NodeID     int64
	SourceName string
	SourceRank int
	Path       string
	Line, Col  int
}

type objectFieldRow struct {
	Type, ObjectName string
	Field, Shape     string
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
	if strings.Contains(rel, "/events/") || strings.HasPrefix(rel, "events/") {
		for _, n := range nodes {
			if n.Kind == "block" && strings.Contains(n.Key, ".") {
				out = append(out, obj(rec, "event", n.Key, n))
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
			if n.Kind == "block" && n.Key != "" && !guiBuiltinTypes[n.Key] {
				out = append(out, obj(rec, "gui", n.Key, n))
			}
		}
	}
	return out
}

func obj(rec fileRecord, typ, name string, n *script.Node) objectRow {
	return objectRow{Type: typ, Name: name, FileID: rec.ID, NodeID: n.ID, SourceName: rec.SourceName, SourceRank: rec.SourceRank, Path: rec.Path, Line: n.Line, Col: n.Col}
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
			out = append(out, objectFieldRow{
				Type:       obj.Type,
				ObjectName: obj.Name,
				Field:      child.Key,
				Shape:      fieldValueShape(child),
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

var patternObjectTypes = map[string]bool{
	"event":                 true,
	"decision":              true,
	"trait":                 true,
	"modifier":              true,
	"opinion_modifier":      true,
	"scripted_effect":       true,
	"scripted_trigger":      true,
	"script_value":          true,
	"character_interaction": true,
	"scheme_type":           true,
	"building":              true,
	"government":            true,
	"law":                   true,
	"religion":              true,
	"faith":                 true,
	"holy_site":             true,
	"culture":               true,
	"culture_tradition":     true,
	"culture_pillar":        true,
	"innovation":            true,
	"name_list":             true,
	"men_at_arms_type":      true,
	"casus_belli_type":      true,
	"on_action":             true,
	"scripted_gui":          true,
	"gui":                   true,
}

var numericValue = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

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
	}
	if strings.Contains(lowerRel, "common/religion/holy_sites/") {
		return "holy_site"
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
	_, err := tx.ExecContext(ctx, `INSERT INTO objects(object_type,name,file_id,node_local_id,source_name,source_rank,path,line,col) VALUES(?,?,?,?,?,?,?,?,?)`,
		o.Type, o.Name, o.FileID, o.NodeID, o.SourceName, o.SourceRank, o.Path, o.Line, o.Col)
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
	FileID, NodeID     int64
	Line, Col          int
	Resolved           bool
}

var prefixTypes = map[string]string{
	"trait": "trait", "title": "title", "faith": "faith", "culture": "culture",
	"character": "character", "scope": "scope", "global_var": "global_var", "flag": "flag",
	"artifact": "artifact", "dynasty": "dynasty", "house": "dynasty_house", "secret": "secret",
}

var locKeys = map[string]bool{"title": true, "desc": true, "text": true, "custom_tooltip": true, "tooltip": true, "localization_key": true}
var resourceExt = regexp.MustCompile(`(?i)\.(dds|png|tga|mesh|asset|gui|wav|ogg)$`)

var keyRefTypes = map[string]string{
	"has_trait": "trait", "add_trait": "trait", "remove_trait": "trait", "trait": "trait",
	"has_character_modifier": "modifier", "add_character_modifier": "modifier", "remove_character_modifier": "modifier", "modifier": "modifier",
	"give_nickname": "nickname", "set_nickname": "nickname", "remove_nickname": "nickname",
	"trigger_event": "event", "fire_event": "event", "on_action": "on_action",
	"set_character_faith": "faith", "faith": "faith", "religion": "religion",
	"set_culture": "culture", "culture": "culture",
	"title": "title", "capital": "title", "capital_county": "title", "de_jure_liege": "title",
	"government": "government", "has_government": "government",
	"law": "law", "has_law": "law", "add_realm_law": "law",
	"secret": "secret", "add_secret": "secret", "has_secret": "secret",
	"casus_belli": "casus_belli_type", "using_cb": "casus_belli_type",
	"men_at_arms": "men_at_arms_type", "men_at_arms_type": "men_at_arms_type",
	"building": "building", "has_building": "building",
	"artifact": "artifact", "create_artifact": "artifact",
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
	walk(nodes, func(n *script.Node) {
		current := ownerForLine(objs, n.Line)
		raws := []string{n.Value}
		if n.Kind == "bare" {
			raws = append(raws, n.Key)
		}
		// Track block-level constructs as self-references.
		if n.Kind == "block" && n.Key != "" {
			k := n.Key
			if _, ok := iteratorScopeIn[k]; ok {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "iterator", Name: k, Raw: k, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			} else if _, ok := scopeTransitionsIn[k]; ok {
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "scope_transition", Name: k, Raw: k, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
			}
		}
		// Track @define references.
		if strings.HasPrefix(n.Value, "@") && len(n.Value) > 2 {
			out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: "define", Name: n.Value, Raw: n.Value, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
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
				out = append(out, refRow{FromType: current.Type, FromName: current.Name, Kind: kind, Name: raw, Raw: raw, FileID: rec.ID, NodeID: n.ID, Line: n.Line, Col: n.Col})
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
	_, err := tx.ExecContext(ctx, `INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.FromType, r.FromName, r.Kind, r.Name, r.FileID, r.NodeID, r.Line, r.Col, r.Raw, r.Resolved)
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
