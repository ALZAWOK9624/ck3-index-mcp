package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func ScanFiles(ctx context.Context, cfg Config, relPaths []string) (ScanStats, error) {
	start := time.Now()
	if err := ConfigureEngineRules(cfg.EngineLogs); err != nil {
		return ScanStats{}, err
	}
	if len(relPaths) == 0 {
		return ScanStats{}, fmt.Errorf("scan --files requires at least one source-root relative path")
	}
	src, err := projectSource(cfg)
	if err != nil {
		return ScanStats{}, err
	}
	dbPath := filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database)
	db, err := Open(dbPath)
	if err != nil {
		return ScanStats{}, err
	}
	defer db.Close()
	if err := db.ensureSchema(ctx); err != nil {
		return ScanStats{}, err
	}
	stats := ScanStats{Database: dbPath, BySource: map[string]int{}, TimingsMillis: map[string]int64{}}

	existing, err := db.fileRecordsByProjectRel(ctx, src.Rank)
	if err != nil {
		return ScanStats{}, err
	}
	jobs := make([]fileJob, 0, len(relPaths))
	mapRefresh := false
	oldFileIDs := map[int64]bool{}
	affected := map[string]bool{}
	for _, raw := range relPaths {
		rel, err := normalizePatchRelPath(raw)
		if err != nil {
			return ScanStats{}, err
		}
		mapRel := isMapContextRel(rel)
		if mapRel {
			mapRefresh = true
		}
		kind := classifyVirtualPath(rel)
		if kind == "" {
			if mapRel {
				continue
			}
			return ScanStats{}, fmt.Errorf("unsupported scan --files path %q", rel)
		}
		full := filepath.Join(src.Path, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			return ScanStats{}, fmt.Errorf("scan --files only supports existing current-project files in this version: %s", rel)
		}
		prev := existing[rel]
		if prev.ID != 0 {
			oldFileIDs[prev.ID] = true
		}
		jobs = append(jobs, fileJob{src: src, path: full, rel: rel, kind: kind, prev: prev})
	}
	if len(jobs) == 0 && !mapRefresh {
		return stats, nil
	}
	if err := db.collectAffectedForFiles(ctx, oldFileIDs, affected); err != nil {
		return ScanStats{}, err
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return ScanStats{}, err
	}
	defer tx.Rollback()
	writer, closeWriter, err := prepareScanWriter(ctx, tx)
	if err != nil {
		return ScanStats{}, err
	}
	defer closeWriter()
	locKeys := map[string]bool{}
	resources := map[string]bool{}
	newFileIDs := map[int64]bool{}
	for _, job := range jobs {
		res := parseOneFile(job)
		stats.Files++
		stats.BySource[src.Name]++
		if res.info == nil {
			return ScanStats{}, fmt.Errorf("could not read %s", job.rel)
		}
		if res.skip {
			if job.prev.ID != 0 {
				newFileIDs[job.prev.ID] = true
			}
			continue
		}
		if job.prev.ID != 0 {
			if err := deleteFileRecords(ctx, tx, job.prev.ID); err != nil {
				return ScanStats{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET overridden=1 WHERE rel_path=? AND source_rank>?`, job.rel, src.Rank); err != nil {
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
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('index_rule_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, indexRuleVersion); err != nil {
		return ScanStats{}, err
	}
	stageStart := time.Now()
	if err := refreshRefsResolvedScoped(ctx, tx, newFileIDs, affected); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["resolve_refs"] = time.Since(stageStart).Milliseconds()
	stageStart = time.Now()
	if err := refreshValidatorDiagnosticsScoped(ctx, tx, newFileIDs, affected); err != nil {
		return ScanStats{}, err
	}
	stats.TimingsMillis["validator"] = time.Since(stageStart).Milliseconds()
	if err := db.RefreshArchitectureOverviewCache(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	if mapRefresh {
		if err := rebuildMapCache(ctx, tx, cfg); err != nil {
			return ScanStats{}, err
		}
	}
	if err := rebuildEngineData(ctx, tx, cfg.EngineLogs); err != nil {
		return ScanStats{}, err
	}
	if err := rebuildSearchFTS(ctx, tx); err != nil {
		return ScanStats{}, err
	}
	stats.Diagnostics = countDiagnostics(ctx, tx)
	if err := tx.Commit(); err != nil {
		return ScanStats{}, err
	}
	stats.ElapsedMillis = time.Since(start).Milliseconds()
	return stats, nil
}

func projectSource(cfg Config) (Source, error) {
	var best Source
	for _, src := range cfg.Sources {
		if src.Rank == 1 {
			if best.Name != "" {
				return Source{}, fmt.Errorf("scan --files requires exactly one rank=1 current-project source")
			}
			best = src
		}
	}
	if best.Name == "" || best.Path == "" {
		return Source{}, fmt.Errorf("scan --files requires a rank=1 current-project source")
	}
	return best, nil
}

func (db *DB) fileRecordsByProjectRel(ctx context.Context, sourceRank int) (map[string]fileRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden
		FROM files WHERE source_rank=?`, sourceRank)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]fileRecord{}
	for rows.Next() {
		var rec fileRecord
		var overridden int
		if err := rows.Scan(&rec.ID, &rec.SourceName, &rec.SourceRank, &rec.Path, &rec.RelPath, &rec.Kind, &rec.MTime, &rec.SHA, &overridden); err != nil {
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
	fileStmt, err := prep(`INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	objStmt, err := prep(`INSERT INTO objects(object_type,name,file_id,node_local_id,source_name,source_rank,path,line,col) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	refStmt, err := prep(`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	diagStmt, err := prep(`INSERT INTO diagnostics(source,severity,code,message,file_id,path,line,col) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	locStmt, err := prep(`INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,?,?,?,?,?,?,?)`)
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
	fieldStmt, err := prep(`INSERT INTO object_fields(object_type,object_name,field,value_shape,file_id,source_name,source_rank,path,line,raw) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	scopeStmt, err := prep(`INSERT INTO saved_scopes(file_id,scope_name) VALUES(?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	varStmt, err := prep(`INSERT INTO variables(file_id,var_name) VALUES(?,?)`)
	if err != nil {
		closeFn()
		return scanWriter{}, nil, err
	}
	return scanWriter{
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

func addAffectedSymbol(affected map[string]bool, kind, name string) {
	if name == "" {
		return
	}
	affected[name] = true
	if kind != "" {
		affected[kind+":"+name] = true
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
