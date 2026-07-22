package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type sourceLayerExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// syncSourceLayers persists only source identity policy, never source paths.
// Database-only validation and public-filtering helpers can therefore honour
// role/private after a scan without guessing from a rank or display name.
func syncSourceLayers(ctx context.Context, q sourceLayerExecer, sources []Source) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM source_layers`); err != nil {
		return err
	}
	for _, source := range sources {
		if _, err := q.ExecContext(ctx, `INSERT INTO source_layers(name,rank,role,private) VALUES(?,?,?,?)`,
			source.Name, source.Rank, source.Role, boolInt(source.Private)); err != nil {
			return fmt.Errorf("store source layer %q: %w", source.Name, err)
		}
	}
	return nil
}

func (db *DB) projectSourceName(ctx context.Context) (string, error) {
	if !db.tableExists(ctx, "source_layers") {
		return "", nil
	}
	var name string
	err := db.sql.QueryRowContext(ctx, `SELECT name FROM source_layers WHERE role=?`, SourceRoleProject).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}

func (db *DB) sourceIsPrivate(ctx context.Context, source string) (bool, error) {
	if strings.EqualFold(strings.TrimSpace(source), "patch") {
		return true, nil
	}
	if !db.tableExists(ctx, "source_layers") {
		return true, nil
	}
	var private int
	err := db.sql.QueryRowContext(ctx, `SELECT private FROM source_layers WHERE lower(name)=lower(?)`, source).Scan(&private)
	if err == sql.ErrNoRows {
		// Public calls fail closed when provenance comes from a stale or older
		// cache that has no source policy record.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return private != 0, nil
}
