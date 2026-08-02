package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadVerifiedIndexedFileRejectsSourceDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	rel := "common/scripted_triggers/drift.txt"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("drift_test = { always = yes }\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','7'),('scan_revision','revision-seven'),('scan_status','ready');
		INSERT INTO source_layers(name,rank,role,private) VALUES('project',1,'project',0);
		INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,file_size,sha256,overridden)
		VALUES(41,'project',1,? ,? ,'script',0,? ,? ,0)`, path, rel, len(original), hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	data, snapshot, err := db.ReadVerifiedIndexedFile(ctx, 41)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) || snapshot.Generation != 7 || snapshot.Revision != "revision-seven" {
		t.Fatalf("unexpected verified snapshot: %+v data=%q", snapshot, data)
	}
	changed := []byte("drift_test = { always = no  }\n")
	if err := os.WriteFile(path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = db.ReadVerifiedIndexedFile(ctx, 41)
	var sourceChanged *SourceChangedError
	if !errors.As(err, &sourceChanged) || sourceChanged.Path != rel || sourceChanged.IndexedGeneration != 7 {
		t.Fatalf("source drift error=%#v", err)
	}
}
