package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	return &DB{sql: db}, nil
}

func (db *DB) Close() error { return db.sql.Close() }

// reset drops and recreates every table. Use for a full clean rebuild.
// Indexes are NOT created here; call CreateIndexes after the bulk insert to
// keep the write phase fast.
func (db *DB) reset(ctx context.Context) error {
	drops := []string{
		`PRAGMA journal_mode=WAL`,
		`DROP TABLE IF EXISTS files`,
		`DROP TABLE IF EXISTS nodes`,
		`DROP TABLE IF EXISTS objects`,
		`DROP TABLE IF EXISTS object_defs`,
		`DROP TABLE IF EXISTS refs`,
		`DROP TABLE IF EXISTS localization`,
		`DROP TABLE IF EXISTS resources`,
		`DROP TABLE IF EXISTS schema_fields`,
		`DROP TABLE IF EXISTS object_fields`,
		`DROP TABLE IF EXISTS diagnostics`,
		`DROP TABLE IF EXISTS saved_scopes`,
		`DROP TABLE IF EXISTS variables`,
	}
	for _, s := range drops {
		if _, err := db.sql.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return db.ensureSchemaNoIndexes(ctx)
}

// ensureSchema creates tables and indexes if they do not exist. Idempotent.
func (db *DB) ensureSchema(ctx context.Context) error {
	if err := db.ensureSchemaNoIndexes(ctx); err != nil {
		return err
	}
	// Migration: add overridden column to files if missing (for DBs created by older version).
	db.sql.ExecContext(ctx, `ALTER TABLE files ADD COLUMN overridden INTEGER NOT NULL DEFAULT 0`) //nolint:errcheck
	return db.CreateIndexes(ctx)
}

func (db *DB) ensureSchemaNoIndexes(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			rel_path TEXT NOT NULL,
			kind TEXT NOT NULL,
			mtime INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			overridden INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id INTEGER PRIMARY KEY,
			file_id INTEGER NOT NULL,
			local_id INTEGER NOT NULL,
			parent_local_id INTEGER,
			depth INTEGER NOT NULL,
			key TEXT,
			operator TEXT,
			value TEXT,
			value_kind TEXT,
			start_line INTEGER,
			start_col INTEGER,
			end_line INTEGER,
			end_col INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS objects (
			id INTEGER PRIMARY KEY,
			object_type TEXT NOT NULL,
			name TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			node_local_id INTEGER,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			col INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS object_defs (
			id INTEGER PRIMARY KEY,
			object_type TEXT NOT NULL,
			name TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			node_local_id INTEGER,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			col INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS refs (
		id INTEGER PRIMARY KEY,
		from_object_type TEXT,
		from_object_name TEXT,
		ref_kind TEXT NOT NULL,
		ref_name TEXT NOT NULL,
		file_id INTEGER NOT NULL,
		node_local_id INTEGER,
		line INTEGER,
		col INTEGER,
		raw TEXT NOT NULL,
		resolved INTEGER NOT NULL DEFAULT 0
	)`,
		`CREATE TABLE IF NOT EXISTS localization (
			id INTEGER PRIMARY KEY,
			key TEXT NOT NULL,
			language TEXT NOT NULL,
			value TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			replace_dir INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS resources (
			id INTEGER PRIMARY KEY,
			resource_path TEXT NOT NULL,
			kind TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS schema_fields (
			id INTEGER PRIMARY KEY,
			object_type TEXT NOT NULL,
			field TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			raw TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS object_fields (
			id INTEGER PRIMARY KEY,
			object_type TEXT NOT NULL,
			object_name TEXT NOT NULL,
			field TEXT NOT NULL,
			value_shape TEXT NOT NULL,
			file_id INTEGER NOT NULL,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			raw TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS diagnostics (
			id INTEGER PRIMARY KEY,
			source TEXT NOT NULL,
			severity TEXT NOT NULL,
			code TEXT NOT NULL,
			message TEXT NOT NULL,
			file_id INTEGER,
			path TEXT,
			line INTEGER,
			col INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS saved_scopes (
			id INTEGER PRIMARY KEY,
			file_id INTEGER NOT NULL,
			scope_name TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS variables (
			id INTEGER PRIMARY KEY,
			file_id INTEGER NOT NULL,
			var_name TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

var indexStmts = []string{
	`CREATE INDEX IF NOT EXISTS idx_objects_name ON objects(name, object_type)`,
	`CREATE INDEX IF NOT EXISTS idx_objects_type_source ON objects(object_type, source_rank, source_name)`,
	`CREATE INDEX IF NOT EXISTS idx_defs_name ON object_defs(name, object_type)`,
	`CREATE INDEX IF NOT EXISTS idx_nodes_key ON nodes(key)`,
	`CREATE INDEX IF NOT EXISTS idx_nodes_parent ON nodes(file_id, local_id, parent_local_id)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_name ON refs(ref_name, ref_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_resolved ON refs(resolved, ref_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_key ON localization(key)`,
	`CREATE INDEX IF NOT EXISTS idx_res_path ON resources(resource_path)`,
	`CREATE INDEX IF NOT EXISTS idx_schema_type_field ON schema_fields(object_type, field)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_type_field ON object_fields(object_type, field, value_shape)`,
	`CREATE INDEX IF NOT EXISTS idx_diag_code ON diagnostics(code)`,
	`CREATE INDEX IF NOT EXISTS idx_files_overridden ON files(overridden, rel_path)`,
	`CREATE INDEX IF NOT EXISTS idx_scope_name ON saved_scopes(scope_name)`,
	`CREATE INDEX IF NOT EXISTS idx_var_name ON variables(var_name)`,
}

// CreateIndexes creates all secondary indexes if missing. Call after a bulk
// insert so writes were not slowed by per-row index updates.
func (db *DB) CreateIndexes(ctx context.Context) error {
	for _, stmt := range indexStmts {
		if _, err := db.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
