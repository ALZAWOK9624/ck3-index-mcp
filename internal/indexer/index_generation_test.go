package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCleanResetGetsNewScanRevisionEvenWhenGenerationRestarts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	publish := func() IndexState {
		t.Helper()
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := bumpScanGeneration(ctx, tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_status','ready') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		state, err := db.IndexState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	first := publish()
	if first.Generation != 1 || first.Revision == "" || !first.Ready() {
		t.Fatalf("first publication = %+v", first)
	}
	if err := db.reset(ctx); err != nil {
		t.Fatal(err)
	}
	second := publish()
	if second.Generation != 1 || second.Revision == "" || !second.Ready() {
		t.Fatalf("clean publication = %+v", second)
	}
	if first.Revision == second.Revision {
		t.Fatalf("clean reset reused revision despite matching numeric generations: first=%+v second=%+v", first, second)
	}
}
