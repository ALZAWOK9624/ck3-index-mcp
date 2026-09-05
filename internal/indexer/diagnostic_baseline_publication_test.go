package indexer

import (
	"context"
	"reflect"
	"testing"
)

func TestDiagnosticBaselineSurvivesImmutablePublication(t *testing.T) {
	ctx := context.Background()
	f := writeBaseSeedFixture(t)
	cfg := f.config(t, f.project, "cache/baselines.sqlite")
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := UpdateDiagnosticBaseline(ctx, cfg, "accepted", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, clean := range []bool{false, true} {
		cfg.ForceClean = clean
		if _, err := ScanFullStaged(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		path, err := ConfiguredDatabasePath(cfg)
		if err != nil {
			t.Fatal(err)
		}
		db, err := OpenReadOnly(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.ListDiagnosticBaselines(ctx)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != saved.Name || got[0].Count != saved.Count || got[0].CreatedAt != saved.CreatedAt {
			t.Fatalf("clean=%v lost baseline: %+v, saved %+v", clean, got, saved)
		}
	}
	if _, err := UpdateDiagnosticBaseline(ctx, cfg, "accepted", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	path, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("deleted baseline resurrected: %+v", got)
	}
}

func TestPublicationPreservedTablesDiscardSeedDecisions(t *testing.T) {
	ctx := context.Background()
	live, _ := newBaselineFixture(t)
	stage, _ := newBaselineFixture(t)
	if _, err := live.SaveDiagnosticBaseline(ctx, "workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.SaveDiagnosticBaseline(ctx, "upstream-only"); err != nil {
		t.Fatal(err)
	}
	var livePath string
	if err := live.sql.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&livePath); err != nil {
		t.Fatal(err)
	}
	conn, err := stage.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	if err := copyPreservedPublicationTables(ctx, conn, livePath); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	got, err := stage.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := live.ListDiagnosticBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published seed decisions: got %+v, want %+v", got, want)
	}
}
