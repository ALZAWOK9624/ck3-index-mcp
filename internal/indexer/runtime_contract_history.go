package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// CK3 treats an unquoted character-history name as a localization key. This
// is intentionally narrower than adding name to the generic localization
// field list: vanilla history commonly uses quoted literal names, which are
// valid and must not become missing-localization warnings.
var unquotedCharacterHistoryName = regexp.MustCompile(`^\s*name\s*=\s*([A-Za-z0-9_.:\-]+)\s*(?:#.*)?$`)

func refreshHistoryCharacterNameDiagnostics(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code='history_character_name_localization_missing'`); err != nil {
		return err
	}
	locKeys, err := activeLocalizationKeys(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT of.file_id,of.path,of.line
		FROM object_fields of
		JOIN objects o ON o.object_type=of.object_type AND o.name=of.object_name AND o.file_id=of.file_id
		JOIN files f ON f.id=of.file_id
		WHERE o.object_type='character' AND of.field='name' AND f.overridden=0
		ORDER BY of.path,of.line`)
	if err != nil {
		return err
	}
	defer rows.Close()
	lineCache := map[string][]string{}
	for rows.Next() {
		var fileID int64
		var path string
		var line int
		if err := rows.Scan(&fileID, &path, &line); err != nil {
			return err
		}
		lines, ok := lineCache[path]
		if !ok {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read indexed character history %q: %w", path, err)
			}
			text := strings.ReplaceAll(string(data), "\r\n", "\n")
			text = strings.ReplaceAll(text, "\r", "\n")
			lines = strings.Split(text, "\n")
			lineCache[path] = lines
		}
		if line <= 0 || line > len(lines) {
			continue
		}
		match := unquotedCharacterHistoryName.FindStringSubmatch(lines[line-1])
		if match == nil || locKeys[match[1]] {
			continue
		}
		insertDiag(ctx, tx, "validator", "warning", "history_character_name_localization_missing",
			fmt.Sprintf("character history name %q is an unquoted localization key but no active localization value is indexed", match[1]),
			fileID, path, line, 1)
	}
	return rows.Err()
}

func activeLocalizationKeys(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT l.key FROM localization l
		JOIN files f ON f.id=l.file_id WHERE f.overridden=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}

// A write-only finding is deterministic only after all active layers and
// literal localization runtime reads are considered. Dependencies commonly
// consume variables set by the project, while global_var:name and
// GetGlobalVariable(...).Var(...) use different reference spellings.
func refreshVariableWriteOnlyDiagnostics(ctx context.Context, tx *sql.Tx, projectRank int) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code='variable_write_only'`); err != nil {
		return err
	}
	// Build the read set once. A correlated NOT EXISTS over the full refs table
	// is correct but becomes quadratic on the vanilla index (which has hundreds
	// of thousands of edges); the two bounded scans below keep the finalizer
	// linear in the active variable edges.
	readRows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.ref_name
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0 AND (
			(r.ref_kind='variable' AND r.relation IN (
				'read_variable','change_variable','remove_variable',
				'clamp_variable','clear_variable','localization_read'
			))
			OR
			(r.ref_kind='global_var' AND r.relation IN ('','localization_read'))
		)`)
	if err != nil {
		return err
	}
	readNames := map[string]bool{}
	for readRows.Next() {
		var name string
		if err := readRows.Scan(&name); err != nil {
			readRows.Close()
			return err
		}
		readNames[name] = true
	}
	if err := readRows.Close(); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.ref_name,r.file_id,f.path,r.line,r.col
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.overridden=0 AND f.source_rank=? AND r.ref_kind='variable'
		AND r.relation IN ('set_variable','set_global_variable','set_local_variable','set_dead_character_variable')
		ORDER BY f.path,r.line,r.col,r.ref_name`, projectRank)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, path string
		var fileID int64
		var line, col int
		if err := rows.Scan(&name, &fileID, &path, &line, &col); err != nil {
			return err
		}
		if readNames[name] {
			continue
		}
		insertDiag(ctx, tx, "validator", "warning", "variable_write_only",
			fmt.Sprintf("variable %q is set by the project but never read by any active indexed script or literal localization runtime expression", strings.TrimSpace(name)),
			fileID, path, line, col)
	}
	return rows.Err()
}

func refreshErrorLogContractDiagnostics(ctx context.Context, tx *sql.Tx, projectRank int) error {
	if err := refreshHistoryCharacterNameDiagnostics(ctx, tx); err != nil {
		return err
	}
	return refreshVariableWriteOnlyDiagnostics(ctx, tx, projectRank)
}
