package indexer

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSearchFTSCacheMatchesRejectsEqualCountForgedScriptTextRows(t *testing.T) {
	tests := []struct {
		name   string
		fileID any
		source string
		path   string
	}{
		{name: "file_id", fileID: int64(987654), source: "project", path: "common/decisions/b.txt"},
		{name: "source", source: "forged", path: "common/decisions/b.txt"},
		{name: "path", source: "project", path: "common/decisions/forged.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, fileIDs := newScriptTextFTSHealthFixture(t)
			fileID := tt.fileID
			if fileID == nil {
				fileID = fileIDs[1]
			}
			if _, err := db.sql.Exec(`DELETE FROM search_fts WHERE kind='script_text' AND CAST(file_id AS INTEGER)=?`, fileIDs[1]); err != nil {
				t.Fatal(err)
			}
			if _, err := db.sql.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id)
				VALUES('script_text',?,?,?,?,?)`, tt.path, "forged text", tt.source, tt.path, fileID); err != nil {
				t.Fatal(err)
			}
			requireStoredSearchFTSCountMatches(t, db)
			if searchFTSCacheHealthyForTest(t, db) {
				t.Fatal("equal-count forged script_text row was accepted as a healthy cache")
			}
		})
	}
}

func TestSearchFTSCacheMatchesRejectsMissingAndExtraScriptTextRows(t *testing.T) {
	db, fileIDs := newScriptTextFTSHealthFixture(t)
	if _, err := db.sql.Exec(`DELETE FROM search_fts WHERE kind='script_text' AND CAST(file_id AS INTEGER)=?`, fileIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id)
		VALUES('script_text',?,?,?,?,?)`, "common/decisions/a.txt", "duplicate text", "project", "common/decisions/a.txt", fileIDs[0]); err != nil {
		t.Fatal(err)
	}
	requireStoredSearchFTSCountMatches(t, db)
	if searchFTSCacheHealthyForTest(t, db) {
		t.Fatal("one missing script_text row replaced by an extra duplicate was accepted as a healthy cache")
	}
}

func TestSearchFTSCacheMatchesRejectsNonCanonicalFileID(t *testing.T) {
	db, fileIDs := newScriptTextFTSHealthFixture(t)
	if _, err := db.sql.Exec(`DELETE FROM search_fts WHERE kind='script_text' AND CAST(file_id AS INTEGER)=?`, fileIDs[1]); err != nil {
		t.Fatal(err)
	}
	rel := "common/decisions/b.txt"
	if _, err := db.sql.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id)
		VALUES('script_text',?,?,?,?,?)`, rel, "forged text", "project", rel, strconv.FormatInt(fileIDs[1], 10)+"junk"); err != nil {
		t.Fatal(err)
	}
	requireStoredSearchFTSCountMatches(t, db)
	if searchFTSCacheHealthyForTest(t, db) {
		t.Fatal("non-canonical script_text file_id was accepted as a healthy cache")
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
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO search_fts(kind,name,text,source,path,file_id)
			VALUES('script_text',?,?,?,?,?)`, rel, "search "+rel, "project", rel, fileIDs[i]); err != nil {
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

func requireStoredSearchFTSCountMatches(t *testing.T, db *DB) {
	t.Helper()
	var expected, actual int64
	if err := db.sql.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, searchFTSRowCountMetaKey).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM search_fts`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("test mutation changed the total FTS row count: got %d, want %d", actual, expected)
	}
}
