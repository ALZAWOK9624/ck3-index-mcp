package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type scopedResolver struct {
	tx        *sql.Tx
	objCache  map[string]bool
	locCache  map[string]bool
	resCache  map[string]bool
	missCache map[string]bool
}

func refreshRefsResolvedScoped(ctx context.Context, tx *sql.Tx, fileIDs map[int64]bool, affected map[string]bool) error {
	where, args := scopedRefWhere(fileIDs, affected, "r")
	if where == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.ref_kind,r.ref_name,r.resolved FROM refs r WHERE `+where, args...)
	if err != nil {
		return err
	}
	resolver := scopedResolver{tx: tx, objCache: map[string]bool{}, locCache: map[string]bool{}, resCache: map[string]bool{}, missCache: map[string]bool{}}
	type upd struct {
		id       int64
		resolved bool
	}
	var updates []upd
	for rows.Next() {
		var id int64
		var kind, name string
		var current int
		if err := rows.Scan(&id, &kind, &name, &current); err != nil {
			rows.Close()
			return err
		}
		resolved, err := resolver.resolved(ctx, kind, name)
		if err != nil {
			rows.Close()
			return err
		}
		if (current != 0) != resolved {
			updates = append(updates, upd{id: id, resolved: resolved})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var yes, no []int64
	for _, u := range updates {
		if u.resolved {
			yes = append(yes, u.id)
		} else {
			no = append(no, u.id)
		}
	}
	if err := batchUpdateResolved(ctx, tx, 1, yes); err != nil {
		return err
	}
	return batchUpdateResolved(ctx, tx, 0, no)
}

func refreshValidatorDiagnosticsScoped(ctx context.Context, tx *sql.Tx, fileIDs map[int64]bool, affected map[string]bool) error {
	candidateFileIDs, err := validatorCandidateFileIDs(ctx, tx, fileIDs, affected)
	if err != nil {
		return err
	}
	if len(candidateFileIDs) == 0 {
		return nil
	}
	idWhere, idArgs := fileIDWhere(candidateFileIDs, "file_id")
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics WHERE source='validator' AND `+idWhere, idArgs...); err != nil {
		return err
	}
	idWhereRefs, idArgsRefs := fileIDWhere(candidateFileIDs, "r.file_id")
	rows, err := tx.QueryContext(ctx, `SELECT r.ref_kind,r.ref_name,r.file_id,r.line,r.col,f.path
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.source_rank=1 AND `+idWhereRefs, idArgsRefs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	resolver := scopedResolver{tx: tx, objCache: map[string]bool{}, locCache: map[string]bool{}, resCache: map[string]bool{}, missCache: map[string]bool{}}
	for rows.Next() {
		var kind, name, path string
		var fileID int64
		var line, col int
		if err := rows.Scan(&kind, &name, &fileID, &line, &col, &path); err != nil {
			return err
		}
		switch kind {
		case "localization":
			ok, err := resolver.resolved(ctx, kind, name)
			if err != nil {
				return err
			}
			if !ok {
				insertDiag(ctx, tx, "validator", "warning", "missing_localization", fmt.Sprintf("localization key %q was referenced but not indexed", name), fileID, path, line, col)
			}
		case "resource":
			ok, err := resolver.resolved(ctx, kind, name)
			if err != nil {
				return err
			}
			if !ok {
				code, severity := resourceDiagnostic(name)
				insertDiag(ctx, tx, "validator", severity, code, fmt.Sprintf("resource %q was referenced but not indexed", name), fileID, path, line, col)
			}
		case "sound":
			if !IsSound(name) {
				insertDiag(ctx, tx, "validator", "warning", "missing_sound", fmt.Sprintf("sound event %q was referenced but not known from game logs", name), fileID, path, line, col)
			}
		case "iterator":
			if _, ok := iteratorScopeIn[name]; !ok {
				insertDiag(ctx, tx, "validator", "warning", "unknown_iterator", fmt.Sprintf("iterator %q was referenced but not known", name), fileID, path, line, col)
			}
		case "scope_transition", "define":
			continue
		default:
			if isObjectRefKind(kind) {
				ok, err := resolver.resolved(ctx, kind, name)
				if err != nil {
					return err
				}
				if !ok {
					insertDiag(ctx, tx, "validator", "warning", "missing_object_reference", fmt.Sprintf("%s %q was referenced but not indexed", kind, name), fileID, path, line, col)
				}
			}
		}
	}
	return rows.Err()
}

func validatorCandidateFileIDs(ctx context.Context, tx *sql.Tx, seed map[int64]bool, affected map[string]bool) (map[int64]bool, error) {
	out := map[int64]bool{}
	for id := range seed {
		out[id] = true
	}
	names := affectedNames(affected)
	if len(names) == 0 {
		return out, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
	args := make([]any, 0, len(names))
	for _, name := range names {
		args = append(args, name)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.file_id
		FROM refs r JOIN files f ON f.id=r.file_id
		WHERE f.source_rank=1 AND r.ref_name IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (r scopedResolver) resolved(ctx context.Context, kind, name string) (bool, error) {
	switch kind {
	case "localization":
		return r.exists(ctx, r.locCache, "loc:"+name, `SELECT 1 FROM localization l JOIN files f ON f.id=l.file_id WHERE l.key=? AND f.overridden=0 LIMIT 1`, name)
	case "resource":
		return r.exists(ctx, r.resCache, "res:"+name, `SELECT 1 FROM resources rs JOIN files f ON f.id=rs.file_id WHERE rs.resource_path=? AND f.overridden=0 LIMIT 1`, name)
	case "sound":
		return IsSound(name), nil
	case "iterator":
		_, ok := iteratorScopeIn[name]
		return ok, nil
	case "scope_transition":
		_, ok := scopeTransitionsIn[name]
		return ok, nil
	case "define":
		_, ok := tigerDefines[name]
		return ok, nil
	case "flag", "global_var":
		return true, nil
	default:
		if !isObjectRefKind(kind) {
			return false, nil
		}
		if prefix := kind + ":"; strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
		typedKey := "obj:" + kind + ":" + name
		if ok, seen := r.objCache[typedKey]; seen {
			return ok, nil
		}
		var n int
		err := r.tx.QueryRowContext(ctx, `SELECT 1 FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.object_type=? AND o.name=? AND f.overridden=0 LIMIT 1`, kind, name).Scan(&n)
		if err == sql.ErrNoRows {
			err = r.tx.QueryRowContext(ctx, `SELECT 1 FROM objects o JOIN files f ON f.id=o.file_id
				WHERE o.name=? AND f.overridden=0 LIMIT 1`, name).Scan(&n)
		}
		if err == sql.ErrNoRows {
			r.objCache[typedKey] = false
			return false, nil
		}
		if err != nil {
			return false, err
		}
		r.objCache[typedKey] = true
		return true, nil
	}
}

func (r scopedResolver) exists(ctx context.Context, cache map[string]bool, key, query string, args ...any) (bool, error) {
	if ok, seen := cache[key]; seen {
		return ok, nil
	}
	var n int
	err := r.tx.QueryRowContext(ctx, query, args...).Scan(&n)
	if err == sql.ErrNoRows {
		cache[key] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cache[key] = true
	return true, nil
}

func scopedRefWhere(fileIDs map[int64]bool, affected map[string]bool, alias string) (string, []any) {
	var parts []string
	var args []any
	if len(fileIDs) > 0 {
		where, a := fileIDWhere(fileIDs, alias+".file_id")
		parts = append(parts, where)
		args = append(args, a...)
	}
	names := affectedNames(affected)
	if len(names) > 0 {
		ph := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
		parts = append(parts, alias+".ref_name IN ("+ph+")")
		for _, name := range names {
			args = append(args, name)
		}
	}
	return strings.Join(parts, " OR "), args
}

func fileIDWhere(fileIDs map[int64]bool, column string) (string, []any) {
	ids := sortedIDs(fileIDs)
	ph := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return column + " IN (" + ph + ")", args
}

func affectedNames(affected map[string]bool) []string {
	out := make([]string, 0, len(affected))
	for name := range affected {
		if strings.Contains(name, ":") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
