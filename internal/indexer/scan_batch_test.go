package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// The audit flagged the 64/128-row insert batches as worth verifying rather
// than as a known win, so this measures the shape directly: the same rows
// written through one transaction at several batch sizes.
func benchmarkObjectInsertBatch(b *testing.B, batchSize int) {
	b.Helper()
	rows := make([]objectRow, 20000)
	for i := range rows {
		rows[i] = objectRow{
			Type: "trait", Name: fmt.Sprintf("bench_trait_%06d", i), Value: "",
			FileID: 1, NodeID: int64(i), SourceName: "project", SourceRank: 1,
			Path: "common/traits/bench.txt", Line: i, Col: 1, EndLine: i, EndCol: 20,
		}
	}
	const prefix = `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?,?,?,?)`

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		dir := b.TempDir()
		db, err := Open(filepath.Join(dir, "batch.sqlite"))
		if err != nil {
			b.Fatal(err)
		}
		if err := db.ensureSchemaNoIndexes(context.Background()); err != nil {
			b.Fatal(err)
		}
		tx, err := db.sql.BeginTx(context.Background(), nil)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		for start := 0; start < len(rows); start += batchSize {
			end := min(start+batchSize, len(rows))
			args := make([]any, 0, (end-start)*12)
			for _, row := range rows[start:end] {
				args = append(args, row.Type, row.Name, row.Value, row.FileID, row.NodeID, row.SourceName, row.SourceRank, row.Path, row.Line, row.Col, row.EndLine, row.EndCol)
			}
			if _, err := tx.ExecContext(context.Background(), multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
				b.Fatal(err)
			}
		}

		b.StopTimer()
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		db.Close()
		b.StartTimer()
	}
}

func BenchmarkObjectInsertBatch64(b *testing.B)  { benchmarkObjectInsertBatch(b, 64) }
func BenchmarkObjectInsertBatch128(b *testing.B) { benchmarkObjectInsertBatch(b, 128) }
func BenchmarkObjectInsertBatch512(b *testing.B) { benchmarkObjectInsertBatch(b, 512) }

// SQLite rejects a statement with more bound parameters than it allows, and
// objects binds twelve per row. A batch size that looked faster in isolation
// but exceeded the driver's limit would fail only on real data, so the ceiling
// is asserted rather than assumed.
func TestObjectInsertBatchStaysWithinTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "limit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ensureSchemaNoIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	for _, batch := range []struct {
		name    string
		size    int
		columns int
	}{
		{"objects", objectInsertBatchSize, 12},
		{"refs", referenceInsertBatchSize, 14},
		{"object_fields", objectFieldInsertBatchSize, 12},
		{"localization", localizationInsertBatchSize, 8},
		{"names", nameInsertBatchSize, 4},
	} {
		parameters := batch.size * batch.columns
		if parameters > 32766 {
			t.Errorf("%s batch binds %d parameters, above the SQLite maximum", batch.name, parameters)
		}
	}
	// Prove the ceiling empirically too: bind the widest batch this package
	// actually issues and confirm the driver accepts it.
	const prefix = `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?,?,?,?)`
	args := make([]any, 0, objectInsertBatchSize*12)
	for i := 0; i < objectInsertBatchSize; i++ {
		args = append(args, "trait", fmt.Sprintf("limit_probe_%d", i), "", 1, i, "project", 1, "common/traits/x.txt", i, 1, i, 5)
	}
	if _, err := db.sql.ExecContext(ctx, multiRowInsertSQL(prefix, values, objectInsertBatchSize), args...); err != nil {
		t.Fatalf("the configured object batch was rejected by the driver: %v", err)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM objects`).Scan(&count); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if count != objectInsertBatchSize {
		t.Fatalf("wrote %d rows, expected %d", count, objectInsertBatchSize)
	}
}
