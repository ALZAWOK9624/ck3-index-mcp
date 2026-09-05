package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// Compare the actual stored rows across full batches, tails and repeated calls.
// The transaction rollback must also undo previously executed cached batches.
func TestPreparedScanBatchesParityAndRollback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "batch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ensureSchemaNoIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, cached := range []bool{false, true} {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		w := &scanBatchWriter{Tx: tx, statements: make(map[string]*sql.Stmt)}
		var execer contextExecer = tx
		if cached {
			execer = w
		}
		for call, count := range []int{1, 64, 129, 7, 128, 65} {
			rows := make([]objectRow, count)
			for i := range rows {
				rows[i] = objectRow{Type: "trait", Name: fmt.Sprintf("苹果_%d_%d", call, i), FileID: 1, NodeID: int64(i), SourceName: "project", SourceRank: call, Path: "common/traits/a.txt", Line: i, Col: 2, EndLine: i + 1, EndCol: 4}
			}
			if err := insertObjectRows(ctx, execer, rows); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT object_type,name,source_rank,line,end_line FROM objects ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var kind, name string
			var rank, line, end int
			if err := rows.Scan(&kind, &name, &rank, &line, &end); err != nil {
				t.Fatal(err)
			}
			got = append(got, fmt.Sprint(kind, "|", name, "|", rank, "|", line, "|", end))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !cached {
			want = got
		} else if !reflect.DeepEqual(want, got) {
			t.Fatal("prepared batches changed stored rows")
		}
		if cached && len(w.statements) != 1 {
			t.Fatalf("file tails grew cache: %d", len(w.statements))
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		w.close()
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM objects`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rollback left %d rows", count)
		}
	}
}
