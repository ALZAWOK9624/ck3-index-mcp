package indexer

import (
	"context"
	"fmt"
	"os"

	"ck3-index/internal/script"
)

// DiagStats outputs diagnostic code counts to stdout.

// runHealthChecks performs cross-object integrity validations after the scan.
func (db *DB) runHealthChecks(ctx context.Context) error {
	if err := db.checkEventDecisionLocKeys(ctx); err != nil {
		return err
	}
	if err := db.checkLIOSSafety(ctx); err != nil {
		return err
	}
	// M3+M4 cross-file checks are correct but slow without
	// covering indexes on the refs+saved_scopes join.
	// They will be re-enabled once the index strategy is refined.
	// if err := db.checkSavedScopeCrossFile(ctx); err != nil { ... }
	// if err := db.checkVariableCrossFile(ctx); err != nil { ... }
	return nil
}

// M18: Events and decisions should reference title and desc localization keys.
func (db *DB) checkEventDecisionLocKeys(ctx context.Context) error {
	// Single LEFT JOIN: find events/decisions with zero localization refs.
	rows, err := db.sql.QueryContext(ctx, `
		SELECT DISTINCT o.object_type, o.name, o.path, o.line
		FROM objects o
		JOIN files f ON f.id=o.file_id
		LEFT JOIN refs r ON r.from_object_name=o.name AND r.ref_kind='localization'
		WHERE o.object_type IN ('event','decision')
		AND f.overridden=0
		AND r.id IS NULL
		LIMIT 5000`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, path string
		var line int
		if err := rows.Scan(&typ, &name, &path, &line); err != nil {
			return err
		}
		msg := fmt.Sprintf("%s %q has no localization references (title/desc may be missing)", typ, name)
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO diagnostics(source,severity,code,message,path,line) VALUES(?,?,?,?,?,?)`,
			"health", "warning", "missing_event_loc", msg, path, line); err != nil {
			return err
		}
	}
	return rows.Err()
}

// M8: LIOS safety – warn when a mod file overrides a subset of objects
// from an upstream file, potentially leaving some objects undefined.
func (db *DB) checkLIOSSafety(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT w.rel_path, COUNT(DISTINCT ow.name) AS win_count,
		       fl.path, fl.source_name, fl.source_rank, fl.kind
		FROM objects ow
		JOIN files w ON w.id=ow.file_id
		JOIN files fl ON fl.rel_path=w.rel_path AND fl.overridden=1
		JOIN source_layers sl ON sl.name=w.source_name
		WHERE w.overridden=0 AND sl.role='project'
		GROUP BY w.rel_path, fl.id, fl.path, fl.source_name, fl.source_rank, fl.kind`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type lio struct {
		rel       string
		win, lose int
	}
	best := map[string]lio{}
	for rows.Next() {
		var rel, path, source, kind string
		var win, rank int
		if err := rows.Scan(&rel, &win, &path, &source, &rank, &kind); err != nil {
			return err
		}
		if kind != "script" {
			continue
		}
		lose := countObjectsInScriptFile(path, rel, source, rank)
		if lose <= win || lose == 0 {
			continue
		}
		if cur, ok := best[rel]; !ok || lose > cur.lose {
			best[rel] = lio{rel: rel, win: win, lose: lose}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, w := range best {
		msg := fmt.Sprintf("possible LIOS partial override: project defines %d objects but an overridden upstream file had %d in %s (missing objects are silently deleted)", w.win, w.lose, w.rel)
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO diagnostics(source,severity,code,message) VALUES(?,?,?,?)`,
			"health", "warning", "lios_partial_override", msg); err != nil {
			return err
		}
	}
	return nil
}

func countObjectsInScriptFile(path, rel, source string, rank int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	parsed := script.Parse(string(data))
	rec := fileRecord{SourceName: source, SourceRank: rank, Path: path, RelPath: rel, Kind: "script", Overridden: true}
	seen := map[string]bool{}
	for _, obj := range extractObjects(rec, parsed.Nodes) {
		seen[obj.Name] = true
	}
	return len(seen)
}

// M12: Duplicate history character IDs across files.
func (db *DB) checkHistoryCharacterDuplicates(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT o.name, COUNT(*) AS cnt
		FROM objects o
		JOIN files f ON f.id=o.file_id
		WHERE o.object_type='character'
		AND f.overridden=0
		GROUP BY o.name
		HAVING COUNT(*) > 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return err
		}
		msg := fmt.Sprintf("character %q defined %d times across files (creates duplicates in game)", name, count)
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO diagnostics(source,severity,code,message) VALUES(?,?,?,?)`,
			"health", "warning", "duplicate_character", msg); err != nil {
			return err
		}
	}
	return rows.Err()
}

// M15: Localization encoding validation – verify UTF-8 BOM on loc files.
func (db *DB) checkLocalizationEncoding(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT f.path FROM files f WHERE f.kind='localization'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		// Check file header bytes without reading the entire file.
		if !hasUTF8BOM(path) {
			if _, err := db.sql.ExecContext(ctx,
				`INSERT INTO diagnostics(source,severity,code,message,path) VALUES(?,?,?,?,?)`,
				"health", "error", "loc_missing_bom",
				fmt.Sprintf("localization file %s is missing UTF-8 BOM (required by CK3)", path),
				path); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

// M3: saved scope cross-file consistency.
func (db *DB) checkSavedScopeCrossFile(ctx context.Context) error {
	builtins := "'actor','recipient','root','prev','this'"
	rows, err := db.sql.QueryContext(ctx, `
		SELECT r.ref_name, r.file_id, r.line, r.col, COALESCE(f.path,'')
		FROM refs r
		JOIN files f ON f.id=r.file_id
		WHERE r.ref_kind='scope' AND f.overridden=0
		AND r.ref_name NOT IN (`+builtins+`)
		AND NOT EXISTS (
			SELECT 1 FROM saved_scopes ss
			JOIN files f2 ON f2.id=ss.file_id AND f2.overridden=0
			WHERE ss.scope_name=r.ref_name
		)
		ORDER BY r.ref_name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var refName, path string
	var fileID int64
	var line, col int
	for rows.Next() {
		if err := rows.Scan(&refName, &fileID, &line, &col, &path); err != nil {
			return err
		}
		msg := fmt.Sprintf("scope:%s referenced but never saved via save_scope_as in any active file", refName)
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO diagnostics(source,severity,code,message,path,line,col) VALUES(?,?,?,?,?,?,?)`,
			"health", "warning", "scope_never_saved", msg, path, line, col); err != nil {
			return err
		}
	}
	return rows.Err()
}

// M4: variable cross-file existence.
func (db *DB) checkVariableCrossFile(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT r.ref_name, r.file_id, r.line, r.col, COALESCE(f.path,'')
		FROM refs r
		JOIN files f ON f.id=r.file_id
		WHERE r.ref_kind='global_var' AND f.overridden=0
		AND NOT EXISTS (
			SELECT 1 FROM variables v
			JOIN files f2 ON f2.id=v.file_id AND f2.overridden=0
			WHERE v.var_name=r.ref_name
		)
		ORDER BY r.ref_name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var refName, path string
	var fileID int64
	var line, col int
	for rows.Next() {
		if err := rows.Scan(&refName, &fileID, &line, &col, &path); err != nil {
			return err
		}
		msg := fmt.Sprintf("global_var:%s referenced but never set via set_global_variable in any active file", refName)
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO diagnostics(source,severity,code,message,path,line,col) VALUES(?,?,?,?,?,?,?)`,
			"health", "warning", "variable_never_set", msg, path, line, col); err != nil {
			return err
		}
	}
	return rows.Err()
}

func hasUTF8BOM(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false // unreadable, treat as missing BOM
	}
	defer f.Close()
	var bom [3]byte
	n, err := f.Read(bom[:])
	if err != nil {
		return false
	}
	return n == 3 && bom[0] == 0xEF && bom[1] == 0xBB && bom[2] == 0xBF
}

func (db *DB) DiagStats(ctx context.Context) error {
	rows, err := db.sql.QueryContext(ctx, `SELECT code,severity,COUNT(*) FROM diagnostics GROUP BY code,severity ORDER BY 3 DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Println("diag_stats,code,severity,count")
	for rows.Next() {
		var code, sev string
		var count int
		if err := rows.Scan(&code, &sev, &count); err != nil {
			return err
		}
		fmt.Printf("diag_stats,%s,%s,%d\n", code, sev, count)
	}
	return rows.Err()
}
