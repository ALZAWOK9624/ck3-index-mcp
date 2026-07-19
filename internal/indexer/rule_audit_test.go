package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAuditOnActionRulesReportsLiveDriftWithoutWritingRules(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var tigerName string
	for name := range tigerOnActions {
		tigerName = name
		break
	}
	if tigerName == "" {
		t.Fatal("generated Tiger on_action table unexpectedly empty")
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES
		('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('live_only_fixture','on_action','none','','','on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	report, err := db.AuditOnActionRules(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !report.LiveEvidenceAvailable || report.LiveCount != 1 || report.EngineOnlyCount != 1 || report.TigerOnlyCount == 0 {
		t.Fatalf("unexpected audit summary: %+v", report)
	}
	if len(report.EngineOnly) != 1 || report.EngineOnly[0].Name != "live_only_fixture" || len(report.EngineOnly[0].InputScopes) != 1 || report.EngineOnly[0].InputScopes[0] != "none" {
		t.Fatalf("live on_action evidence was not preserved: %+v", report.EngineOnly)
	}
	if len(report.TigerOnly) != 1 || !report.Truncated {
		t.Fatalf("bounded audit did not cap output while retaining totals: %+v", report)
	}
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM engine_scope_rules WHERE rule_kind='on_action'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("read-only audit changed engine rules: count=%d", count)
	}
}

func TestTigerOnActionTableExcludesNestedListDeclarations(t *testing.T) {
	if IsOnAction("list") {
		t.Fatal("nested list declaration was published as an on_action")
	}
	if !IsOnAction("on_alliance_added") {
		t.Fatal("top-level on_action disappeared from generated Tiger table")
	}
}
