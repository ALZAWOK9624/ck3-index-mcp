package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql  *sql.DB
	path string
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return openSQLite(path, false)
}

func OpenReadOnly(path string) (*DB, error) {
	return openSQLite(path, true)
}

func openSQLite(path string, readOnly bool) (*DB, error) {
	dsn := path
	if readOnly {
		dsn = "file:" + filepath.ToSlash(path) + "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{sql: db, path: path}, nil
}

func (db *DB) Close() error { return db.sql.Close() }

func (db *DB) EnsureSchema(ctx context.Context) error {
	return db.ensureSchema(ctx)
}

// reset drops and recreates every table. Use for a full clean rebuild.
// Indexes are NOT created here; call CreateIndexes after the bulk insert to
// keep the write phase fast.
func (db *DB) reset(ctx context.Context) error {
	drops := []string{
		`PRAGMA journal_mode=WAL`,
		`DROP TABLE IF EXISTS meta`,
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
		`DROP TABLE IF EXISTS map_provinces`,
		`DROP TABLE IF EXISTS map_adjacencies`,
		`DROP TABLE IF EXISTS map_titles`,
		`DROP TABLE IF EXISTS map_title_provinces`,
		`DROP TABLE IF EXISTS map_province_history`,
		`DROP TABLE IF EXISTS map_title_history`,
		`DROP TABLE IF EXISTS map_characters`,
		`DROP TABLE IF EXISTS map_character_history`,
		`DROP TABLE IF EXISTS map_holy_sites`,
		`DROP TABLE IF EXISTS map_holy_site_faiths`,
		`DROP TABLE IF EXISTS map_province_regions`,
		`DROP TABLE IF EXISTS engine_datatypes`,
		`DROP TABLE IF EXISTS engine_scope_rules`,
		`DROP TABLE IF EXISTS search_fts`,
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
	db.sql.ExecContext(ctx, `ALTER TABLE files ADD COLUMN overridden INTEGER NOT NULL DEFAULT 0`)                //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE map_provinces ADD COLUMN terrain TEXT`)                                 //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE map_provinces ADD COLUMN water_kind TEXT`)                              //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE map_provinces ADD COLUMN is_county_capital INTEGER NOT NULL DEFAULT 0`) //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE diagnostics ADD COLUMN source_layer TEXT NOT NULL DEFAULT ''`)          //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE diagnostics ADD COLUMN confidence TEXT NOT NULL DEFAULT 'medium'`)      //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE diagnostics ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`)           //nolint:errcheck
	db.sql.ExecContext(ctx, `ALTER TABLE diagnostics ADD COLUMN occurrences INTEGER NOT NULL DEFAULT 1`)         //nolint:errcheck
	return db.CreateIndexes(ctx)
}

func (db *DB) ensureSchemaNoIndexes(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
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
			col INTEGER,
			source_layer TEXT NOT NULL DEFAULT '',
			confidence TEXT NOT NULL DEFAULT 'medium',
			fingerprint TEXT NOT NULL DEFAULT '',
			occurrences INTEGER NOT NULL DEFAULT 1
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
		`CREATE TABLE IF NOT EXISTS map_provinces (
			province_id INTEGER PRIMARY KEY,
			center_x REAL,
			center_y REAL,
			min_x INTEGER,
			min_y INTEGER,
			max_x INTEGER,
			max_y INTEGER,
			area INTEGER,
			blocked INTEGER NOT NULL DEFAULT 0,
			block_kind TEXT,
			water_kind TEXT,
			terrain TEXT,
			barony TEXT,
			county TEXT,
			duchy TEXT,
			kingdom TEXT,
			empire TEXT,
			is_county_capital INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS map_adjacencies (
			province_id INTEGER NOT NULL,
			neighbor_id INTEGER NOT NULL,
			border_len INTEGER NOT NULL,
			blocked INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (province_id, neighbor_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_titles (
			title_id TEXT PRIMARY KEY,
			title_type TEXT NOT NULL,
			parent_id TEXT,
			capital_title TEXT,
			province_id INTEGER,
			holder TEXT,
			province_count INTEGER NOT NULL DEFAULT 0,
			center_x REAL,
			center_y REAL,
			min_x INTEGER,
			min_y INTEGER,
			max_x INTEGER,
			max_y INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS map_title_provinces (
			title_id TEXT NOT NULL,
			province_id INTEGER NOT NULL,
			PRIMARY KEY (title_id, province_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_province_history (
			province_id INTEGER NOT NULL,
			date_key INTEGER NOT NULL,
			field TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (province_id, date_key, field)
		)`,
		`CREATE TABLE IF NOT EXISTS map_title_history (
			title_id TEXT NOT NULL,
			date_key INTEGER NOT NULL,
			field TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (title_id, date_key, field)
		)`,
		`CREATE TABLE IF NOT EXISTS map_characters (
			character_id TEXT PRIMARY KEY,
			name TEXT,
			culture TEXT,
			religion TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS map_character_history (
			character_id TEXT NOT NULL,
			date_key INTEGER NOT NULL,
			field TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (character_id, date_key, field)
		)`,
		`CREATE TABLE IF NOT EXISTS map_holy_sites (
			holy_site_id TEXT PRIMARY KEY,
			county TEXT,
			barony TEXT,
			province_id INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS map_holy_site_faiths (
			holy_site_id TEXT NOT NULL,
			faith_id TEXT NOT NULL,
			PRIMARY KEY (holy_site_id, faith_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_province_regions (
			province_id INTEGER NOT NULL,
			region_id TEXT NOT NULL,
			PRIMARY KEY (province_id, region_id)
		)`,
		`CREATE TABLE IF NOT EXISTS engine_datatypes (
			name TEXT NOT NULL,
			signature TEXT NOT NULL,
			description TEXT,
			definition_type TEXT,
			return_type TEXT,
			category TEXT,
			source_path TEXT NOT NULL,
			PRIMARY KEY(name,signature)
		)`,
		`CREATE TABLE IF NOT EXISTS engine_scope_rules (
			name TEXT NOT NULL,
			rule_kind TEXT NOT NULL,
			input_scopes TEXT,
			output_scopes TEXT,
			description TEXT,
			source_path TEXT NOT NULL,
			PRIMARY KEY(name,rule_kind)
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(kind, name, text, source, path UNINDEXED, tokenize='unicode61 remove_diacritics 2')`,
	}
	for _, stmt := range stmts {
		if _, err := db.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) metaValue(ctx context.Context, key string) (string, error) {
	var val string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

var indexStmts = []string{
	`CREATE INDEX IF NOT EXISTS idx_objects_name ON objects(name, object_type)`,
	`CREATE INDEX IF NOT EXISTS idx_objects_type_name ON objects(object_type, name)`,
	`CREATE INDEX IF NOT EXISTS idx_objects_file_id ON objects(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_objects_type_source ON objects(object_type, source_rank, source_name)`,
	`CREATE INDEX IF NOT EXISTS idx_defs_name ON object_defs(name, object_type)`,
	`CREATE INDEX IF NOT EXISTS idx_nodes_key ON nodes(key)`,
	`CREATE INDEX IF NOT EXISTS idx_nodes_parent ON nodes(file_id, local_id, parent_local_id)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_name ON refs(ref_name, ref_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_kind_name ON refs(ref_kind, ref_name)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_from_name ON refs(from_object_name, from_object_type)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_from_type_name ON refs(from_object_type, from_object_name)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_file_id ON refs(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_refs_resolved ON refs(resolved, ref_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_key ON localization(key)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_file_id ON localization(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_res_path ON resources(resource_path)`,
	`CREATE INDEX IF NOT EXISTS idx_res_path_rank ON resources(resource_path, source_rank)`,
	`CREATE INDEX IF NOT EXISTS idx_res_file_id ON resources(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_schema_type_field ON schema_fields(object_type, field)`,
	`CREATE INDEX IF NOT EXISTS idx_schema_file_id ON schema_fields(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_type_field ON object_fields(object_type, field, value_shape)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_field ON object_fields(field)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_file_id ON object_fields(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_diag_code ON diagnostics(code)`,
	`CREATE INDEX IF NOT EXISTS idx_diag_file_id ON diagnostics(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_files_overridden ON files(overridden, rel_path)`,
	`CREATE INDEX IF NOT EXISTS idx_scope_name ON saved_scopes(scope_name)`,
	`CREATE INDEX IF NOT EXISTS idx_scope_file_id ON saved_scopes(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_var_name ON variables(var_name)`,
	`CREATE INDEX IF NOT EXISTS idx_var_file_id ON variables(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_county ON map_provinces(county)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_duchy ON map_provinces(duchy)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_kingdom ON map_provinces(kingdom)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_empire ON map_provinces(empire)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_block ON map_provinces(blocked, block_kind, water_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_adj_neighbor ON map_adjacencies(neighbor_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_titles_parent ON map_titles(parent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_title_provinces_province ON map_title_provinces(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_province_history_lookup ON map_province_history(province_id, field, date_key)`,
	`CREATE INDEX IF NOT EXISTS idx_map_title_history_lookup ON map_title_history(title_id, field, date_key)`,
	`CREATE INDEX IF NOT EXISTS idx_map_character_history_lookup ON map_character_history(character_id, field, date_key)`,
	`CREATE INDEX IF NOT EXISTS idx_map_holy_sites_province ON map_holy_sites(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_holy_sites_county ON map_holy_sites(county)`,
	`CREATE INDEX IF NOT EXISTS idx_map_regions_region ON map_province_regions(region_id)`,
	`CREATE INDEX IF NOT EXISTS idx_engine_datatypes_name ON engine_datatypes(name)`,
	`CREATE INDEX IF NOT EXISTS idx_engine_scope_rules_name ON engine_scope_rules(name,rule_kind)`,
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
