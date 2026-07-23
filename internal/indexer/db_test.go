package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAppliesBusyTimeoutToEveryConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "busy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Discard every idle connection so each query must open a fresh one.
	db.sql.SetMaxIdleConns(0)
	for i := 0; i < 4; i++ {
		var timeout int
		if err := db.sql.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != 5000 {
			t.Fatalf("connection %d busy_timeout=%d, want 5000", i, timeout)
		}
	}
}

func TestScanWriterTransactionUsesPinnedPragmas(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "writer.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.scanWriteConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for pragma, want := range map[string]int{
		"busy_timeout": 60000,
		"temp_store":   2,
		"cache_size":   -200000,
		"synchronous":  0,
	} {
		var got int
		if err := tx.QueryRowContext(ctx, `PRAGMA `+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("writer PRAGMA %s=%d, want %d", pragma, got, want)
		}
	}
}

func TestOpenHandlesDatabasePathWithURICharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index #%地图.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	var count int
	if err := readOnly.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("schema disappeared after reopening a URI-sensitive path")
	}
}

func TestEnsureSchemaMigratesOldFilesTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.sql.Exec(`CREATE TABLE files (
		id INTEGER PRIMARY KEY,
		source_name TEXT NOT NULL,
		source_rank INTEGER NOT NULL,
		path TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		kind TEXT NOT NULL,
		mtime INTEGER NOT NULL,
		sha256 TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"overridden", "override_reason", "file_size"} {
		var count int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('files') WHERE name=?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migrated files.%s count=%d, want 1", column, count)
		}
	}
}

func TestEnsureSchemaDoesNotHideMigrationErrors(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "invalid.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.sql.Exec(`CREATE VIRTUAL TABLE files USING fts5(source_name)`); err != nil {
		t.Skipf("SQLite build does not provide FTS5: %v", err)
	}
	err = db.EnsureSchema(context.Background())
	if err == nil {
		t.Fatal("EnsureSchema silently accepted an unmodifiable files table")
	}
	if !strings.Contains(err.Error(), "migrate schema column files.overridden") {
		t.Fatalf("unexpected migration error: %v", err)
	}
}

func TestResetRebuildsDateKeyIndexesAfterLegacyPooledConnection(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "legacy-reset.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.sql.Exec(`CREATE TABLE object_fields (
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
	)`); err != nil {
		t.Fatal(err)
	}

	// Keep a connection that has observed the legacy table in the pool while
	// reset recreates the schema on another connection. This is the boundary
	// that a full cache rebuild must handle before building date_key indexes.
	legacyConn, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := legacyConn.QueryContext(ctx, `SELECT field FROM object_fields`)
	if err != nil {
		legacyConn.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		legacyConn.Close()
		t.Fatal(err)
	}
	if err := db.reset(ctx); err != nil {
		legacyConn.Close()
		t.Fatal(err)
	}
	if err := legacyConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIndexes(ctx); err != nil {
		t.Fatalf("CreateIndexes after legacy reset: %v", err)
	}
	var dateKey int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('object_fields') WHERE name='date_key'`).Scan(&dateKey); err != nil {
		t.Fatal(err)
	}
	if dateKey != 1 {
		t.Fatalf("object_fields.date_key count=%d, want 1", dateKey)
	}
}
