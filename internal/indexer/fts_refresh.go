package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ftsRefreshBatchSize stays well below SQLite's bind-variable limit. A normal
// full scan can legitimately replace thousands of files, unlike scan --files,
// so scoped FTS maintenance must not turn a large-but-valid edit into a SQL
// placeholder-limit failure.
const (
	ftsRefreshBatchSize      = 512
	searchFTSRowCountMetaKey = "search_fts_row_count"
)

// searchFTSCacheMatches verifies the published FTS snapshot still contains
// the number of rows recorded when it was last refreshed. This is deliberately
// independent of current semantic row counts: a small edit is expected to
// change those before the scoped FTS refresh runs.
func searchFTSCacheMatches(ctx context.Context, tx *sql.Tx) (bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, searchFTSRowCountMetaKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || expected < 0 {
		return false, nil
	}
	var actual int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts`).Scan(&actual); err != nil {
		return false, err
	}
	return actual == expected, nil
}

func storeSearchFTSRowCount(ctx context.Context, tx *sql.Tx) error {
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts`).Scan(&count); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, searchFTSRowCountMetaKey, strconv.FormatInt(count, 10))
	return err
}

// refreshSearchFTSForFiles updates only the rows whose source files changed.
// Both scan --files and ordinary full scans use it when the engine-owned FTS
// rows are still current, avoiding a drop-and-rebuild of every token.
func refreshSearchFTSForFiles(ctx context.Context, tx *sql.Tx, oldFileIDs, newFileIDs map[int64]bool) error {
	oldIDs := sortedFTSFileIDs(oldFileIDs)
	for start := 0; start < len(oldIDs); start += ftsRefreshBatchSize {
		end := start + ftsRefreshBatchSize
		if end > len(oldIDs) {
			end = len(oldIDs)
		}
		clause, args := ftsIDClause(oldIDs[start:end])
		if _, err := tx.ExecContext(ctx, `DELETE FROM search_fts WHERE file_id IN (`+clause+`)`, args...); err != nil {
			return fmt.Errorf("remove stale semantic FTS rows: %w", err)
		}
	}

	newIDs := sortedFTSFileIDs(newFileIDs)
	if len(newIDs) == 0 {
		return nil
	}
	for start := 0; start < len(newIDs); start += ftsRefreshBatchSize {
		end := start + ftsRefreshBatchSize
		if end > len(newIDs) {
			end = len(newIDs)
		}
		clause, args := ftsIDClause(newIDs[start:end])
		statements := []string{
			`INSERT INTO search_fts(kind,name,text,source,path,file_id)
				SELECT 'object',o.name,o.object_type||' '||o.name||' '||f.rel_path,o.source_name,f.rel_path,f.id
				FROM objects o JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND f.id IN (` + clause + `)`,
			`INSERT INTO search_fts(kind,name,text,source,path,file_id)
				SELECT 'resource',r.resource_path,r.kind||' '||r.resource_path,r.source_name,f.rel_path,f.id
				FROM resources r JOIN files f ON f.id=r.file_id WHERE f.overridden=0 AND f.id IN (` + clause + `)`,
			`INSERT INTO search_fts(kind,name,text,source,path,file_id)
				SELECT 'script_key',o.field,o.field||' '||o.object_name||' '||o.raw,o.source_name,f.rel_path,f.id
				FROM object_fields o JOIN files f ON f.id=o.file_id WHERE f.overridden=0 AND f.id IN (` + clause + `)`,
			`INSERT INTO search_fts(kind,name,text,source,path,file_id)
				SELECT 'localization',l.key,l.key||' '||l.value,l.source_name,f.rel_path,f.id
				FROM localization l JOIN files f ON f.id=l.file_id
				WHERE f.overridden=0 AND f.id IN (` + clause + `) AND (lower(l.language) LIKE '%english%' OR lower(l.language) LIKE '%simp%')`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return fmt.Errorf("refresh semantic FTS rows: %w", err)
			}
		}
	}
	return nil
}

func sortedFTSFileIDs(ids map[int64]bool) []int64 {
	out := make([]int64, 0, len(ids))
	for id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func ftsIDClause(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
