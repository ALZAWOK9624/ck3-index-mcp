package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGovernmentFallbackContractRequiresPositiveActiveFallback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "government-fallback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
		VALUES(1,'project',10,'common/governments/test.txt','common/governments/test.txt','script',0,'test',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO objects(id,object_type,name,file_id,source_name,source_rank,path,line,col)
		VALUES(1,'government','test_government',1,'project',10,'common/governments/test.txt',1,1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshGovernmentFallbackDiagnostics(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='government_missing_fallback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("missing-fallback diagnostics=%d, want 1", count)
	}

	if _, err := db.sql.Exec(`INSERT INTO object_fields(object_type,object_name,field,value_shape,file_id,source_name,source_rank,path,line,raw)
		VALUES('government','test_government','fallback','number',1,'project',10,'common/governments/test.txt',2,'fallback = 1')`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshGovernmentFallbackDiagnostics(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='government_missing_fallback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("positive fallback still reported: %d", count)
	}
}

func TestGovernmentFallbackValueClassification(t *testing.T) {
	for _, tc := range []struct {
		shape string
		raw   string
		want  bool
	}{
		{shape: "number", raw: "fallback = 1", want: true},
		{shape: "number", raw: "fallback = 0", want: false},
		{shape: "number", raw: "fallback = -1", want: false},
		{shape: "define_ref", raw: "fallback = @fallback_priority", want: true},
	} {
		if got := governmentFallbackValueIsPositive(tc.shape, tc.raw); got != tc.want {
			t.Fatalf("governmentFallbackValueIsPositive(%q,%q)=%v, want %v", tc.shape, tc.raw, got, tc.want)
		}
	}
}

func TestGovernmentMechanicDefaultContractDetectsMissingAndDuplicateDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "government-default.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[int]string{1: "one", 2: "two"} {
		if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
			VALUES(?,?,?,?,?,'script',0,?,0)`, id, "project", 10, "common/governments/"+name+".txt", "common/governments/"+name+".txt", name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.Exec(`INSERT INTO objects(id,object_type,name,file_id,source_name,source_rank,path,line,col)
			VALUES(?,?,?,?,?,? ,?,1,1)`, id, "government", "government_"+name, id, "project", 10, "common/governments/"+name+".txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.sql.Exec(`INSERT INTO object_fields(object_type,object_name,field,value_shape,file_id,source_name,source_rank,path,line,raw)
			VALUES('government',?,'mechanic_type','atom',?,'project',10,?,2,'mechanic_type = administrative')`, "government_"+name, id, "common/governments/"+name+".txt"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.Exec(`INSERT INTO object_fields(object_type,object_name,field,value_shape,file_id,source_name,source_rank,path,line,raw)
		VALUES('government','government_one','is_mechanic_type_default','bool',1,'project',10,'common/governments/one.txt',3,'is_mechanic_type_default = yes'),
		('government','government_two','is_mechanic_type_default','bool',2,'project',10,'common/governments/two.txt',3,'is_mechanic_type_default = no')`); err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		tx, beginErr := db.sql.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if validationErr := refreshGovernmentMechanicDefaultDiagnostics(ctx, tx); validationErr != nil {
			tx.Rollback()
			t.Fatal(validationErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	refresh()
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code IN ('government_missing_mechanic_default','government_duplicate_mechanic_default')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("valid mechanic defaults reported: %d", count)
	}
	if _, err := db.sql.Exec(`UPDATE object_fields SET raw='is_mechanic_type_default = yes' WHERE object_type='government' AND object_name='government_two' AND field='is_mechanic_type_default'`); err != nil {
		t.Fatal(err)
	}
	refresh()
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='government_duplicate_mechanic_default'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("duplicate mechanic defaults=%d, want 2", count)
	}
	if _, err := db.sql.Exec(`DELETE FROM object_fields WHERE object_type='government' AND field='is_mechanic_type_default'`); err != nil {
		t.Fatal(err)
	}
	refresh()
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='government_missing_mechanic_default'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("missing mechanic default=%d, want 1", count)
	}
}

func TestCourtTypeDefaultContractDetectsDuplicateDefaults(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	db, err := Open(filepath.Join(tempDir, "court-type-default.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writeScript := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(tempDir, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	firstPath := writeScript("first.txt", "court_first = { default = yes }\n")
	secondPath := writeScript("second.txt", "court_second = { default = yes }\n")
	for id, entry := range []struct {
		path string
		rel  string
		name string
	}{
		{path: firstPath, rel: "common/court_types/first.txt", name: "court_first"},
		{path: secondPath, rel: "common/court_types/second.txt", name: "court_second"},
	} {
		fileID := id + 1
		if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
			VALUES(?,?,?,?,?,'script',0,?,0)`, fileID, "game", 100, entry.path, entry.rel, entry.name); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func() {
		t.Helper()
		tx, beginErr := db.sql.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if validationErr := refreshCourtTypeDefaultDiagnostics(ctx, tx); validationErr != nil {
			tx.Rollback()
			t.Fatal(validationErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	refresh()
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='court_type_duplicate_default'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("duplicate court defaults=%d, want 2", count)
	}
	if err := os.WriteFile(secondPath, []byte("court_second = { default = no }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh()
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='court_type_duplicate_default'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("single court default still reported as duplicate: %d", count)
	}
}

// A file can be deleted from disk while the index still holds its row: an
// incremental refresh only reconciles the paths it was handed. The runtime
// contract validators read every active file, so a stale row used to abort the
// whole refresh and leave the index unable to catch up without a full rescan.
func TestRuntimeContractValidationSkipsIndexedFileDeletedFromDisk(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	db, err := Open(filepath.Join(tempDir, "deleted-indexed-file.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writeScript := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(tempDir, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	survivorPath := writeScript("survivor.txt", "court_survivor = { default = yes }\n")
	deletedPath := writeScript("deleted.txt", "court_deleted = { default = yes }\n")
	for id, entry := range []struct {
		path string
		rel  string
		name string
	}{
		{path: survivorPath, rel: "common/court_types/survivor.txt", name: "court_survivor"},
		{path: deletedPath, rel: "common/court_types/deleted.txt", name: "court_deleted"},
	} {
		fileID := id + 1
		if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
			VALUES(?,?,?,?,?,'script',0,?,0)`, fileID, "game", 100, entry.path, entry.rel, entry.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(deletedPath); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshCourtTypeDefaultDiagnostics(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatalf("validation failed on an indexed file that no longer exists: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='court_type_duplicate_default'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleted file still counted as an active default: duplicates=%d, want 0", count)
	}
}
