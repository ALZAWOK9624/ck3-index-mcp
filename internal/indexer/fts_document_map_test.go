package indexer

import (
	"context"
	"fmt"
	"testing"
)

func TestSearchDocumentMapPreciseDeletionAndRollback(t *testing.T) {
	ctx := context.Background()
	db, files := newScriptTextFTSHealthFixture(t)
	// Include a zero-token document, an engine row, and multiple rows per file.
	for i, file := range []int64{0, files[0], files[1], files[1]} {
		if _, err := db.sql.Exec(`INSERT INTO search_fts(rowid,kind,name,text,source,path,file_id)
			VALUES(?, 'object', ?, '', 'project', '', ?)`, i+1, fmt.Sprintf("needle%d", i), file); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := appendSearchDocumentMap(ctx, tx, 0); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := refreshSearchFTSForFiles(ctx, tx, map[int64]bool{files[1]: true}, nil); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM search_fts`).Scan(&left); err != nil || left != 2 {
		t.Fatalf("targeted deletion: count=%d error=%v", left, err)
	}
	// Reusing the deleted tail rowids must not be omitted by map capture.
	if _, err := tx.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id)
		VALUES('object','newneedle','','project','',?)`, files[1]); err != nil {
		t.Fatal(err)
	}
	if err := appendSearchDocumentMap(ctx, tx, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM search_documents WHERE file_id=?`, files[1]).Scan(&left); err != nil || left != 1 {
		t.Fatalf("reused tail: count=%d error=%v", left, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"search_fts", "search_documents"} {
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&left); err != nil || left != 4 {
			t.Fatalf("%s rollback count=%d error=%v", table, left, err)
		}
	}
}

func TestSearchDocumentMapLossRequiresRebuild(t *testing.T) {
	db, files := newScriptTextFTSHealthFixture(t)
	if _, err := db.sql.Exec(`INSERT INTO search_fts(kind,name,text,source,path,file_id)
		VALUES('object','needle','','project','',?)`, files[0]); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := storeSearchFTSRowCount(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	healthy, err := searchFTSCacheMatches(context.Background(), tx)
	if err != nil || healthy {
		t.Fatalf("missing mapping accepted: healthy=%v error=%v", healthy, err)
	}
	if err := appendSearchDocumentMap(context.Background(), tx, 0); err != nil {
		t.Fatal(err)
	}
	healthy, err = searchFTSCacheMatches(context.Background(), tx)
	if err != nil || !healthy {
		t.Fatalf("complete mapping rejected: healthy=%v error=%v", healthy, err)
	}
}

func TestSearchDocumentMapCorruptionCannotDeleteEngineRow(t *testing.T) {
	ctx := context.Background()
	db, files := newScriptTextFTSHealthFixture(t)
	if _, err := db.sql.Exec(`INSERT INTO search_fts(rowid,kind,name,text,source,path,file_id)
		VALUES(1,'datatype','engine_needle','','engine_logs','',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO search_documents(fts_rowid,file_id) VALUES(1,?)`, files[0]); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := refreshSearchFTSForFiles(ctx, tx, map[int64]bool{files[0]: true}, nil); err == nil {
		t.Fatal("corrupt mapping was accepted")
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM search_fts WHERE rowid=1 AND file_id=0`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("engine row lost: count=%d error=%v", count, err)
	}
}
