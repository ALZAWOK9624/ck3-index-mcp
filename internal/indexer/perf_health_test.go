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

func TestHealthCanServeIndexQueriesUsesExplicitReadinessContract(t *testing.T) {
	ready := HealthReport{
		Status: "ok", ScanStatus: IndexStatusReady, AuthoritativeDatabase: true,
		MapDatabase: MapDatabaseStatus{Complete: true}, FTS5Available: true,
		IndexRuleVersion: indexRuleVersion,
	}
	for _, status := range []string{"ok", "warning", "degraded"} {
		report := ready
		report.Status = status
		if !report.CanServeIndexQueries() {
			t.Fatalf("operational status %q rejected an otherwise ready index", status)
		}
	}
	tests := []struct {
		name   string
		mutate func(*HealthReport)
	}{
		{name: "error status", mutate: func(report *HealthReport) { report.Status = "error" }},
		{name: "generation not ready", mutate: func(report *HealthReport) { report.ScanStatus = IndexStatusInitializing }},
		{name: "wrong database", mutate: func(report *HealthReport) { report.AuthoritativeDatabase = false }},
		{name: "map incomplete", mutate: func(report *HealthReport) { report.MapDatabase.Complete = false }},
		{name: "fts unavailable", mutate: func(report *HealthReport) { report.FTS5Available = false }},
		{name: "performance index missing", mutate: func(report *HealthReport) { report.MissingIndexes = []string{"idx_objects_name"} }},
		{name: "rule version mismatch", mutate: func(report *HealthReport) { report.IndexRuleVersion = "old-rules" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := ready
			test.mutate(&report)
			if report.CanServeIndexQueries() {
				t.Fatalf("unsafe report passed readiness gate: %+v", report)
			}
		})
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
	if report.SQLiteReadConnections != maxReadConnections || report.SQLiteCachePerConnMB != readCacheMiBPerConnection || report.SQLiteCacheBudgetMB != estimatedSQLiteReadCacheBudgetMiB || report.SQLiteMMapLimitMB != readMMapLimitMiB || report.LoadedDatabaseCount != 1 || report.RetiredDatabaseCount != 0 || report.AggregateSQLiteCacheBudgetMB != estimatedSQLiteReadCacheBudgetMiB {
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
	if report.SQLiteReadConnections != 2 || report.SQLiteCachePerConnMB != 16 || report.SQLiteCacheBudgetMB != 32 || report.SQLiteMMapLimitMB != 256 || report.LoadedDatabaseCount != 1 || report.RetiredDatabaseCount != 0 || report.AggregateSQLiteCacheBudgetMB != 32 {
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
