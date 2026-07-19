package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScopedResolverMatchesGlobalFallbackForUnknownRefKind(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256)
		VALUES('project',1,'common/test.txt','common/test.txt','script',0,0,'test')`)
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects(object_type,name,file_id,source_name,source_rank,path)
		VALUES('trait','shared_name',?,'project',1,'common/test.txt')`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects(object_type,name,file_id,source_name,source_rank,path)
		VALUES('trait','trait:prefixed_name',?,'project',1,'common/test.txt')`, fileID); err != nil {
		t.Fatal(err)
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO refs(ref_kind,ref_name,file_id,raw)
		VALUES('runtime_like_kind','shared_name',?,'shared_name')`, fileID)
	if err != nil {
		t.Fatal(err)
	}
	refID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO refs(ref_kind,ref_name,file_id,raw)
		VALUES('trait','trait:prefixed_name',?,'trait:prefixed_name')`, fileID)
	if err != nil {
		t.Fatal(err)
	}
	prefixedRefID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	if err := refreshRefsResolvedScoped(ctx, tx, map[int64]bool{}, map[string]bool{"shared_name": true, "trait:prefixed_name": true}); err != nil {
		t.Fatal(err)
	}
	var resolved int
	var reason string
	if err := tx.QueryRowContext(ctx, `SELECT resolved,resolution_reason FROM refs WHERE id=?`, refID).Scan(&resolved, &reason); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || reason != "indexed_definition" {
		t.Fatalf("scoped resolver = (%d,%q), want global resolver fallback (1,%q)", resolved, reason, "indexed_definition")
	}
	if err := tx.QueryRowContext(ctx, `SELECT resolved,resolution_reason FROM refs WHERE id=?`, prefixedRefID).Scan(&resolved, &reason); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || reason != "indexed_definition" {
		t.Fatalf("scoped resolver lost a prefixed object id: (%d,%q)", resolved, reason)
	}
}
