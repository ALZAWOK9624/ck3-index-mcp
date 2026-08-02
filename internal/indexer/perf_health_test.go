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
