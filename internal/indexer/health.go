package indexer

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

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
	// M4 cross-file checks are slow without covering indexes on the refs join.
	// They will be re-enabled once the index strategy is refined.
	// if err := db.checkVariableCrossFile(ctx); err != nil { ... }
	return nil
}

// M18: Visible events and decisions need usable localization. Hidden events do
// not render a title/description, and decisions can rely on CK3's documented
// implicit <id> and <id>_desc keys.
func (db *DB) checkEventDecisionLocKeys(ctx context.Context) error {
	locRows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT l.key
		FROM localization l JOIN files f ON f.id=l.file_id
		WHERE f.overridden=0`)
	if err != nil {
		return err
	}
	locKeys := map[string]bool{}
	for locRows.Next() {
		var key string
		if err := locRows.Scan(&key); err != nil {
			locRows.Close()
			return err
		}
		locKeys[key] = true
	}
	if err := locRows.Close(); err != nil {
		return err
	}

	// The two EXISTS subqueries below used to run per object, so the query was
	// O(objects x refs) and carried a LIMIT 5000 to stay affordable. That limit
	// silently dropped findings past the first 5000 rows and, without an ORDER
	// BY, dropped a different set on every scan. Both answers are now collected
	// in one bounded pass each (idx_refs_kind_name / idx_object_fields_field),
	// which is cheaper than the correlated form and lets the check cover every
	// active event and decision.
	locRefs := map[string]bool{}
	refRows, err := db.sql.QueryContext(ctx, `SELECT r.file_id,r.from_object_type,r.from_object_name
		FROM refs r WHERE r.ref_kind='localization'`)
	if err != nil {
		return err
	}
	for refRows.Next() {
		var fileID int64
		var objectType, objectName string
		if err := refRows.Scan(&fileID, &objectType, &objectName); err != nil {
			refRows.Close()
			return err
		}
		locRefs[objectLocKey(fileID, objectType, objectName)] = true
	}
	if err := refRows.Close(); err != nil {
		return err
	}
	hiddenObjects := map[string]bool{}
	hiddenRows, err := db.sql.QueryContext(ctx, `SELECT of.file_id,of.object_type,of.object_name
		FROM object_fields of WHERE of.field='hidden' AND LOWER(of.raw) LIKE '%= yes%'`)
	if err != nil {
		return err
	}
	for hiddenRows.Next() {
		var fileID int64
		var objectType, objectName string
		if err := hiddenRows.Scan(&fileID, &objectType, &objectName); err != nil {
			hiddenRows.Close()
			return err
		}
		hiddenObjects[objectLocKey(fileID, objectType, objectName)] = true
	}
	if err := hiddenRows.Close(); err != nil {
		return err
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT o.object_type, o.name, o.path, o.line, o.file_id
		FROM objects o
		JOIN files f ON f.id=o.file_id
		WHERE o.object_type IN ('event','decision')
		AND f.overridden=0
		ORDER BY f.source_rank,o.path,o.line`)
	if err != nil {
		return err
	}
	defer rows.Close()
	// Findings are flushed in one transaction. A project can raise thousands of
	// them, and one autocommit statement per row made this check the most
	// expensive step in validate for large indexes.
	type pendingLoc struct {
		msg, path string
		line      int
	}
	var pending []pendingLoc
	for rows.Next() {
		var typ, name, path string
		var line int
		var fileID int64
		if err := rows.Scan(&typ, &name, &path, &line, &fileID); err != nil {
			return err
		}
		key := objectLocKey(fileID, typ, name)
		if typ == "event" && hiddenObjects[key] {
			continue
		}
		if locRefs[key] {
			continue
		}
		if typ == "decision" && locKeys[name] && locKeys[name+"_desc"] {
			continue
		}
		expected := "an explicit title/desc localization reference"
		if typ == "decision" {
			expected = fmt.Sprintf("explicit localization or the implicit keys %q and %q", name, name+"_desc")
		}
		pending = append(pending, pendingLoc{
			msg:  fmt.Sprintf("%s %q has no usable localization; expected %s", typ, name, strings.TrimSpace(expected)),
			path: path,
			line: line,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO diagnostics(source,severity,code,message,path,line) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, p := range pending {
		if _, err := stmt.ExecContext(ctx, "health", "warning", "missing_event_loc", p.msg, p.path, p.line); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// objectLocKey identifies an object within the file that declares it so a
// set-membership test can replace a correlated EXISTS subquery.
func objectLocKey(fileID int64, objectType, objectName string) string {
	return strconv.FormatInt(fileID, 10) + "\x00" + objectType + "\x00" + objectName
}

// M8: LIOS safety – warn when a mod file overrides a subset of objects
// from an upstream file, potentially leaving some objects undefined.
func (db *DB) checkLIOSSafety(ctx context.Context) error {
	// fl.sha256 comes along so the parse below can be cached against it.
	// Overridden files are deliberately metadata-only during a scan -- they are
	// hashed but never parsed -- so the count cannot come from the objects
	// table, and recording it during the scan would mean parsing every
	// overridden upstream file on a path that exists to avoid exactly that.
	// Caching on content is the version that costs nothing on the common path.
	rows, err := db.sql.QueryContext(ctx, `
		SELECT w.rel_path, COUNT(DISTINCT ow.name) AS win_count,
		       fl.path, fl.source_name, fl.source_rank, fl.kind, fl.sha256
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
	// Identical bytes can extract different object sets under different CK3
	// directories, so the path-derived extraction context is part of the key.
	// The same path across source layers still shares one parse.
	counted := map[string]int{}
	for rows.Next() {
		var rel, path, source, kind, sha string
		var win, rank int
		if err := rows.Scan(&rel, &win, &path, &source, &rank, &kind, &sha); err != nil {
			return err
		}
		if kind != "script" {
			continue
		}
		cacheKey := liosObjectCountCacheKey(sha, rel)
		lose, cached := counted[cacheKey]
		if !cached || sha == "" {
			lose = countObjectsInScriptFile(path, rel, source, rank)
			if sha != "" {
				counted[cacheKey] = lose
			}
		}
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

func liosObjectCountCacheKey(sha, rel string) string {
	rel = strings.ToLower(path.Clean(strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")))
	return sha + "\x00" + rel
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
