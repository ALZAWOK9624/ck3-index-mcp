package indexer

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSearchFTSCacheMatchesRejectsEqualCountForgedScriptRowID(t *testing.T) {
	db, fileIDs := newScriptTextFTSHealthFixture(t)
	if _, err := db.sql.Exec(`DELETE FROM script_text_fts WHERE rowid=?`, fileIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO script_text_fts(rowid,search_text) VALUES(987654,'forged text')`); err != nil {
		t.Fatal(err)
	}
	if searchFTSCacheHealthyForTest(t, db) {
		t.Fatal("equal-count forged script rowid was accepted as a healthy cache")
	}
}

func TestSearchFTSCacheMatchesRejectsNonScriptRowID(t *testing.T) {
	db, fileIDs := newScriptTextFTSHealthFixture(t)
	result, err := db.sql.Exec(`INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,search_text)
		VALUES('project',1,'project/gfx/x.png','gfx/x.png','resource',0,0,'resource','not script')`)
	if err != nil {
		t.Fatal(err)
	}
	resourceID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DELETE FROM script_text_fts WHERE rowid=?`, fileIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO script_text_fts(rowid,search_text) VALUES(?,'forged text')`, resourceID); err != nil {
		t.Fatal(err)
	}
	if searchFTSCacheHealthyForTest(t, db) {
		t.Fatal("a non-script file rowid was accepted as a healthy script-text cache")
	}
}

func TestScriptTextFTSIsContentless(t *testing.T) {
	db, fileIDs := newScriptTextFTSHealthFixture(t)
	var content sql.NullString
	if err := db.sql.QueryRow(`SELECT search_text FROM script_text_fts WHERE rowid=?`, fileIDs[0]).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content.Valid {
		t.Fatalf("contentless FTS unexpectedly retained source text %q", content.String)
	}
}

func newScriptTextFTSHealthFixture(t *testing.T) (*DB, [2]int64) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	var fileIDs [2]int64
	for i, rel := range []string{"common/decisions/a.txt", "common/decisions/b.txt"} {
		result, err := db.sql.ExecContext(ctx, `INSERT INTO files(
			source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,search_text
		) VALUES(?,?,?,?, 'script',0,0,?,?)`, "project", 1, filepath.Join("project", filepath.FromSlash(rel)), rel, rel, "search "+rel)
		if err != nil {
			t.Fatal(err)
		}
		fileIDs[i], err = result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeSearchFTSRowCount(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !searchFTSCacheHealthyForTest(t, db) {
		t.Fatal("valid script_text fixture was not accepted as a healthy cache")
	}
	return db, fileIDs
}

func searchFTSCacheHealthyForTest(t *testing.T, db *DB) bool {
	t.Helper()
	tx, err := db.sql.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	healthy, err := searchFTSCacheMatches(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	return healthy
}
