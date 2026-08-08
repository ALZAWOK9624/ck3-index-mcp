package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWALHealthThreshold(t *testing.T) {
	for _, test := range []struct {
		db, wal float64
		want    bool
	}{
		{db: 1500, wal: 345, want: true},
		{db: 2000, wal: 257, want: true},
		{db: 2000, wal: 200, want: false},
		{db: 100, wal: 21, want: true},
	} {
		if got := walHealthDegraded(test.db, test.wal); got != test.want {
			t.Fatalf("walHealthDegraded(%v,%v)=%v want=%v", test.db, test.wal, got, test.want)
		}
	}
}

func TestHealthReportsSQLiteReadMemoryBudget(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "health.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := db.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.SQLiteReadConnections != maxReadConnections || report.SQLiteCachePerConnMB != readCacheMiBPerConnection || report.SQLiteCacheBudgetMB != estimatedSQLiteReadCacheBudgetMiB || report.SQLiteMMapLimitMB != readMMapLimitMiB {
		t.Fatalf("health SQLite memory budget is incomplete: %+v", report)
	}
}

func TestHealthReportsConfiguredSQLiteReadMemoryBudget(t *testing.T) {
	db, err := OpenWithOptions(filepath.Join(t.TempDir(), "health.sqlite"), SQLiteReadOptions{Connections: 2, CacheMBPerConnection: 16, MMapLimitMB: 256})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := db.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.SQLiteReadConnections != 2 || report.SQLiteCachePerConnMB != 16 || report.SQLiteCacheBudgetMB != 32 || report.SQLiteMMapLimitMB != 256 {
		t.Fatalf("configured health SQLite memory budget is incomplete: %+v", report)
	}
	if stats := db.sql.Stats(); stats.MaxOpenConnections != 2 {
		t.Fatalf("SQLite max open connections=%d, want 2", stats.MaxOpenConnections)
	}
	var cacheSize int
	if err := db.sql.QueryRow(`PRAGMA cache_size`).Scan(&cacheSize); err != nil {
		t.Fatal(err)
	}
	if cacheSize != -16*1024 {
		t.Fatalf("SQLite cache_size=%d, want %d", cacheSize, -16*1024)
	}
}
