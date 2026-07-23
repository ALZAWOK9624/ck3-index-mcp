package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql                 *sql.DB
	path                string
	physicalRasterMu    sync.Mutex
	physicalRasterCache map[string]cachedMapPhysicalRaster
	guiResolutionMu     sync.Mutex
	guiResolutionCache  map[string]GUIResolution
	guiResolutionOrder  []string
}

func Open(path string) (*DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
		return nil, err
	}
	return openSQLite(absolute, false)
}

func OpenReadOnly(path string) (*DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return openSQLite(absolute, true)
}

func openSQLite(path string, readOnly bool) (*DB, error) {
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(uriPath) >= 2 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	query := uri.Query()
	// modernc applies _pragma to every connection opened by database/sql.
	// A one-off PRAGMA Exec only configures whichever pooled connection ran it,
	// causing intermittent SQLITE_BUSY failures on the remaining connections.
	query.Set("_pragma", "busy_timeout=5000")
	if readOnly {
		query.Set("mode", "ro")
	}
	uri.RawQuery = query.Encode()
	dsn := uri.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{sql: db, path: path}, nil
}

func (db *DB) Close() error { return db.sql.Close() }

// scanWriteConnection pins scan transactions and their connection-local
// PRAGMAs to the same SQLite connection. Applying these through *sql.DB would
// configure an arbitrary pooled connection and leave the actual writer with
// different durability, cache, or timeout settings.
func (db *DB) scanWriteConnection(ctx context.Context) (*sql.Conn, error) {
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		`PRAGMA busy_timeout=60000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=OFF`,
		`PRAGMA temp_store=MEMORY`,
		`PRAGMA cache_size=-200000`,
	} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func (db *DB) EnsureSchema(ctx context.Context) error {
	return db.ensureSchema(ctx)
}

// reset drops and recreates every table. Use for a full clean rebuild.
// Indexes are NOT created here; call CreateIndexes after the bulk insert to
// keep the write phase fast.
func (db *DB) reset(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return err
	}
	// Drop in reverse catalog order so the FTS virtual table releases its
	// shadow tables before ordinary semantic tables are recreated.
	for index := len(semanticIndexTableCatalog) - 1; index >= 0; index-- {
		table := semanticIndexTableCatalog[index]
		if _, err := db.sql.ExecContext(ctx, `DROP TABLE IF EXISTS "`+table+`"`); err != nil {
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
	// Migrate older caches deliberately. Checking table_info first lets us
	// ignore only the expected "already exists" case while surfacing locks,
	// corruption, read-only files, and unsupported table shapes immediately.
	migrations := []struct {
		table, column, definition string
	}{
		{"files", "overridden", "INTEGER NOT NULL DEFAULT 0"},
		{"map_provinces", "terrain", "TEXT"},
		{"map_provinces", "water_kind", "TEXT"},
		{"map_provinces", "is_county_capital", "INTEGER NOT NULL DEFAULT 0"},
		{"map_provinces", "color_rgb", "INTEGER"},
		{"map_provinces", "perimeter", "INTEGER NOT NULL DEFAULT 0"},
		{"map_titles", "color_rgb", "INTEGER"},
		{"diagnostics", "source_layer", "TEXT NOT NULL DEFAULT ''"},
		{"diagnostics", "confidence", "TEXT NOT NULL DEFAULT 'medium'"},
		{"diagnostics", "fingerprint", "TEXT NOT NULL DEFAULT ''"},
		{"diagnostics", "occurrences", "INTEGER NOT NULL DEFAULT 1"},
		{"objects", "value", "TEXT NOT NULL DEFAULT ''"},
		{"objects", "end_line", "INTEGER NOT NULL DEFAULT 0"},
		{"objects", "end_col", "INTEGER NOT NULL DEFAULT 0"},
		{"files", "override_reason", "TEXT NOT NULL DEFAULT ''"},
		{"files", "override_by_source", "TEXT NOT NULL DEFAULT ''"},
		{"files", "override_by_rank", "INTEGER NOT NULL DEFAULT 0"},
		{"files", "override_rule", "TEXT NOT NULL DEFAULT ''"},
		{"refs", "relation", "TEXT NOT NULL DEFAULT ''"},
		{"refs", "phase", "TEXT NOT NULL DEFAULT ''"},
		{"refs", "confidence", "TEXT NOT NULL DEFAULT 'exact'"},
		{"refs", "resolution_reason", "TEXT NOT NULL DEFAULT ''"},
		{"object_fields", "date_key", "INTEGER NOT NULL DEFAULT 0"},
		{"files", "file_size", "INTEGER NOT NULL DEFAULT -1"},
	}
	for _, migration := range migrations {
		if err := db.ensureColumn(ctx, migration.table, migration.column, migration.definition); err != nil {
			return err
		}
	}
	return db.CreateIndexes(ctx)
}

func (db *DB) ensureColumn(ctx context.Context, table, column, definition string) error {
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil {
		return fmt.Errorf("inspect schema column %s.%s: %w", table, column, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.sql.ExecContext(ctx, `ALTER TABLE "`+table+`" ADD COLUMN "`+column+`" `+definition); err != nil {
		return fmt.Errorf("migrate schema column %s.%s: %w", table, column, err)
	}
	return nil
}

func (db *DB) ensureSchemaNoIndexes(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS source_layers (
			name TEXT PRIMARY KEY,
			rank INTEGER NOT NULL,
			role TEXT NOT NULL,
			private INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS files (
			id INTEGER PRIMARY KEY,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			rel_path TEXT NOT NULL,
			kind TEXT NOT NULL,
			mtime INTEGER NOT NULL,
			file_size INTEGER NOT NULL DEFAULT -1,
			sha256 TEXT NOT NULL,
			overridden INTEGER NOT NULL DEFAULT 0,
			override_reason TEXT NOT NULL DEFAULT '',
			override_by_source TEXT NOT NULL DEFAULT '',
			override_by_rank INTEGER NOT NULL DEFAULT 0,
			override_rule TEXT NOT NULL DEFAULT ''
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
			value TEXT NOT NULL DEFAULT '',
			file_id INTEGER NOT NULL,
			node_local_id INTEGER,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			path TEXT NOT NULL,
			line INTEGER,
			col INTEGER,
			end_line INTEGER NOT NULL DEFAULT 0,
			end_col INTEGER NOT NULL DEFAULT 0
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
		resolved INTEGER NOT NULL DEFAULT 0,
		relation TEXT NOT NULL DEFAULT '',
		phase TEXT NOT NULL DEFAULT '',
		confidence TEXT NOT NULL DEFAULT 'exact',
		resolution_reason TEXT NOT NULL DEFAULT ''
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
			date_key INTEGER NOT NULL DEFAULT 0,
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
			color_rgb INTEGER,
			center_x REAL,
			center_y REAL,
			min_x INTEGER,
			min_y INTEGER,
			max_x INTEGER,
			max_y INTEGER,
			area INTEGER,
			perimeter INTEGER NOT NULL DEFAULT 0,
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
		`CREATE TABLE IF NOT EXISTS map_province_geometry (
			province_id INTEGER PRIMARY KEY,
			fill_rle BLOB NOT NULL,
			boundary_rle BLOB NOT NULL,
			format TEXT NOT NULL DEFAULT 'i32le_y_x0_x1_v1'
		)`,
		`CREATE TABLE IF NOT EXISTS map_physical_rasters (
			layer_key TEXT PRIMARY KEY,
			width INTEGER NOT NULL,
			height INTEGER NOT NULL,
			format TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			data BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_province_physical (
			province_id INTEGER PRIMARY KEY,
			sample_count INTEGER NOT NULL,
			elevation_min REAL NOT NULL,
			elevation_mean REAL NOT NULL,
			elevation_max REAL NOT NULL,
			elevation_p10 REAL NOT NULL,
			elevation_median REAL NOT NULL,
			elevation_p90 REAL NOT NULL,
			slope_mean REAL NOT NULL,
			slope_max REAL NOT NULL,
			ruggedness_mean REAL NOT NULL,
			aspect_degrees REAL,
			curvature_mean REAL NOT NULL,
			ridge_score REAL NOT NULL,
			valley_score REAL NOT NULL,
			relative_depth_mean REAL,
			relative_depth_max REAL,
			seabed_slope REAL,
			seabed_ruggedness REAL,
			shelf_score REAL,
			trench_score REAL,
			coastal_dropoff REAL,
			strait_sill_depth REAL,
			water_body_id INTEGER,
			river_pixel_count INTEGER NOT NULL DEFAULT 0,
			major_river INTEGER NOT NULL DEFAULT 0,
			major_river_width_proxy REAL,
			major_river_mouth INTEGER NOT NULL DEFAULT 0,
			catchment_pixels REAL,
			flow_percentile REAL,
			river_order INTEGER,
			provenance TEXT NOT NULL,
			algorithm TEXT NOT NULL,
			confidence REAL NOT NULL,
			fingerprint TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_physical_water_bodies (
			water_body_id INTEGER PRIMARY KEY,
			kind TEXT NOT NULL,
			province_count INTEGER NOT NULL,
			area_pixels INTEGER NOT NULL,
			surface_reference REAL,
			surface_method TEXT NOT NULL,
			mean_relative_depth REAL,
			max_relative_depth REAL,
			mean_seabed_slope REAL,
			impassable_province_count INTEGER NOT NULL DEFAULT 0,
			confidence REAL NOT NULL,
			fingerprint TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_physical_water_body_provinces (
			water_body_id INTEGER NOT NULL,
			province_id INTEGER NOT NULL,
			PRIMARY KEY (water_body_id, province_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_major_river_edges (
			from_province INTEGER NOT NULL,
			to_province INTEGER NOT NULL,
			relation TEXT NOT NULL,
			border_len INTEGER NOT NULL,
			confidence REAL NOT NULL,
			PRIMARY KEY (from_province, to_province)
		)`,
		`CREATE TABLE IF NOT EXISTS map_surface_rasters (
			layer_key TEXT PRIMARY KEY,
			width INTEGER NOT NULL,
			height INTEGER NOT NULL,
			format TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			data BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_surface_materials (
			material_index INTEGER PRIMARY KEY,
			material_id TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			diffuse_path TEXT NOT NULL DEFAULT '',
			normal_path TEXT NOT NULL DEFAULT '',
			properties_path TEXT NOT NULL DEFAULT '',
			mask_path TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			source_path TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_province_materials (
			province_id INTEGER NOT NULL,
			material_index INTEGER NOT NULL,
			weight_share REAL NOT NULL,
			sample_count INTEGER NOT NULL,
			material_rank INTEGER NOT NULL,
			PRIMARY KEY (province_id, material_index)
		)`,
		`CREATE TABLE IF NOT EXISTS map_object_instances (
			id INTEGER PRIMARY KEY,
			object_kind TEXT NOT NULL,
			subtype TEXT NOT NULL,
			object_name TEXT NOT NULL,
			province_id INTEGER,
			x REAL NOT NULL,
			y REAL NOT NULL,
			rotation REAL NOT NULL DEFAULT 0,
			scale REAL NOT NULL DEFAULT 1,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			source_path TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_adjacencies (
			province_id INTEGER NOT NULL,
			neighbor_id INTEGER NOT NULL,
			border_len INTEGER NOT NULL,
			blocked INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (province_id, neighbor_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_strategic_adjacencies (
			id INTEGER PRIMARY KEY,
			from_province INTEGER NOT NULL,
			to_province INTEGER NOT NULL,
			connection_type TEXT NOT NULL,
			through_province INTEGER NOT NULL DEFAULT -1,
			start_x REAL NOT NULL DEFAULT -1,
			start_y REAL NOT NULL DEFAULT -1,
			stop_x REAL NOT NULL DEFAULT -1,
			stop_y REAL NOT NULL DEFAULT -1,
			comment TEXT NOT NULL DEFAULT '',
			passage_kind TEXT NOT NULL,
			distance_pixels REAL NOT NULL DEFAULT 0,
			from_subterranean INTEGER NOT NULL DEFAULT 0,
			to_subterranean INTEGER NOT NULL DEFAULT 0,
			source_name TEXT NOT NULL,
			source_rank INTEGER NOT NULL,
			source_path TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS map_water_bodies (
			water_body_id INTEGER PRIMARY KEY,
			kind TEXT NOT NULL,
			province_count INTEGER NOT NULL,
			area_pixels INTEGER NOT NULL,
			shoreline_pixels INTEGER NOT NULL,
			center_x REAL NOT NULL,
			center_y REAL NOT NULL,
			min_x INTEGER NOT NULL,
			min_y INTEGER NOT NULL,
			max_x INTEGER NOT NULL,
			max_y INTEGER NOT NULL,
			locator_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS map_water_body_provinces (
			water_body_id INTEGER NOT NULL,
			province_id INTEGER NOT NULL,
			PRIMARY KEY (water_body_id, province_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_water_body_shores (
			water_body_id INTEGER NOT NULL,
			province_id INTEGER NOT NULL,
			shoreline_pixels INTEGER NOT NULL,
			PRIMARY KEY (water_body_id, province_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_title_adjacencies (
			level TEXT NOT NULL,
			title_id TEXT NOT NULL,
			neighbor_id TEXT NOT NULL,
			border_len INTEGER NOT NULL,
			blocked_border_len INTEGER NOT NULL DEFAULT 0,
			water_border_len INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (level, title_id, neighbor_id)
		)`,
		`CREATE TABLE IF NOT EXISTS map_titles (
			title_id TEXT PRIMARY KEY,
			title_type TEXT NOT NULL,
			color_rgb INTEGER,
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
		`CREATE TABLE IF NOT EXISTS map_integrity_issues (
			id INTEGER PRIMARY KEY,
			code TEXT NOT NULL,
			title_id TEXT NOT NULL DEFAULT '',
			province_id INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL,
			source_name TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			line INTEGER NOT NULL DEFAULT 0
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
		`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(kind, name, text, source, path UNINDEXED, file_id UNINDEXED, tokenize='unicode61 remove_diacritics 2')`,
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
	`CREATE INDEX IF NOT EXISTS idx_refs_relation ON refs(relation, ref_kind, ref_name)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_key ON localization(key)`,
	`CREATE INDEX IF NOT EXISTS idx_loc_file_id ON localization(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_res_path ON resources(resource_path)`,
	`CREATE INDEX IF NOT EXISTS idx_res_path_rank ON resources(resource_path, source_rank)`,
	`CREATE INDEX IF NOT EXISTS idx_res_file_id ON resources(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_schema_type_field ON schema_fields(object_type, field)`,
	`CREATE INDEX IF NOT EXISTS idx_schema_file_id ON schema_fields(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_type_field ON object_fields(object_type, field, value_shape)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_object_date ON object_fields(object_type, object_name, date_key, source_rank)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_field ON object_fields(field)`,
	`CREATE INDEX IF NOT EXISTS idx_object_fields_file_id ON object_fields(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_diag_code ON diagnostics(code)`,
	`CREATE INDEX IF NOT EXISTS idx_diag_file_id ON diagnostics(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_files_overridden ON files(overridden, rel_path)`,
	`CREATE INDEX IF NOT EXISTS idx_files_source_active_rel ON files(source_name, overridden, rel_path)`,
	`CREATE INDEX IF NOT EXISTS idx_files_override_by ON files(override_by_rank, override_reason)`,
	`CREATE INDEX IF NOT EXISTS idx_scope_name ON saved_scopes(scope_name)`,
	`CREATE INDEX IF NOT EXISTS idx_scope_file_id ON saved_scopes(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_var_name ON variables(var_name)`,
	`CREATE INDEX IF NOT EXISTS idx_var_file_id ON variables(file_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_county ON map_provinces(county)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_map_provinces_color ON map_provinces(color_rgb) WHERE color_rgb IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_duchy ON map_provinces(duchy)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_kingdom ON map_provinces(kingdom)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_empire ON map_provinces(empire)`,
	`CREATE INDEX IF NOT EXISTS idx_map_provinces_block ON map_provinces(blocked, block_kind, water_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_objects_kind_province ON map_object_instances(object_kind,province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_objects_kind_position ON map_object_instances(object_kind,x,y)`,
	`CREATE INDEX IF NOT EXISTS idx_map_adj_neighbor ON map_adjacencies(neighbor_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_strategic_from ON map_strategic_adjacencies(from_province,passage_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_strategic_to ON map_strategic_adjacencies(to_province,passage_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_strategic_kind ON map_strategic_adjacencies(passage_kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_water_body_province ON map_water_body_provinces(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_water_body_shore_province ON map_water_body_shores(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_physical_water_province ON map_physical_water_body_provinces(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_physical_water_kind ON map_physical_water_bodies(kind)`,
	`CREATE INDEX IF NOT EXISTS idx_map_major_river_to ON map_major_river_edges(to_province)`,
	`CREATE INDEX IF NOT EXISTS idx_map_province_material_rank ON map_province_materials(province_id,material_rank)`,
	`CREATE INDEX IF NOT EXISTS idx_map_title_adj_neighbor ON map_title_adjacencies(level,neighbor_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_titles_type ON map_titles(title_type)`,
	`CREATE INDEX IF NOT EXISTS idx_map_titles_parent ON map_titles(parent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_title_provinces_province ON map_title_provinces(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_integrity_province ON map_integrity_issues(province_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_integrity_title ON map_integrity_issues(title_id)`,
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
