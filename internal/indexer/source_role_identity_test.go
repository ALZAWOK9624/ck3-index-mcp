package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestQueryExamplesOrdersConfiguredSourceRoles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	type sourceFixture struct {
		name   string
		rank   int
		role   SourceRole
		object string
	}
	fixtures := []sourceFixture{
		{name: "current_mod", rank: 1, role: SourceRoleProject, object: "a_project_trait"},
		{name: "upstream_addon", rank: 2, role: SourceRoleDependency, object: "b_dependency_trait"},
		{name: "reference_layer", rank: 3, role: SourceRoleReference, object: "c_reference_trait"},
		{name: "vanilla_119", rank: 9, role: SourceRoleGame, object: "zzzzzzzzzzzzzzzz_game_trait"},
	}
	cfg := Config{ConfigPath: filepath.Join(dir, "ck3-index.toml"), Database: "cache/test.sqlite"}
	for _, fixture := range fixtures {
		root := filepath.Join(dir, fixture.name)
		path := filepath.Join(root, "common", "traits", fixture.name+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fixture.object+" = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg.Sources = append(cfg.Sources, Source{Name: fixture.name, Path: root, Rank: fixture.rank, Role: fixture.role})
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := db.QueryExamples(ctx, "trait", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vanilla_119", "upstream_addon", "reference_layer", "current_mod"}
	if len(result.Examples) != len(want) {
		t.Fatalf("example count = %d, want %d; examples=%+v", len(result.Examples), len(want), result.Examples)
	}
	for index, source := range want {
		if result.Examples[index].Source != source {
			t.Fatalf("example %d source = %q, want %q; examples=%+v", index, result.Examples[index].Source, source, result.Examples)
		}
	}
}

func TestExplainDiagnosticPrioritizesConfiguredProjectRole(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := syncSourceLayers(ctx, db.sql, []Source{
		{Name: "current_mod", Path: "current_mod", Rank: 7, Role: SourceRoleProject, Private: true},
		{Name: "vanilla_119", Path: "vanilla_119", Rank: 9, Role: SourceRoleGame, Private: false},
	}); err != nil {
		t.Fatal(err)
	}
	projectResult, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES('current_mod',7,'project.txt','common/z_project.txt','script',0,'project')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	gameResult, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES('vanilla_119',9,'game.txt','common/a_game.txt','script',0,'game')`)
	if err != nil {
		t.Fatal(err)
	}
	gameID, _ := gameResult.LastInsertId()
	for _, args := range [][]any{
		{"compiler", "warning", "role_order", "project diagnostic", projectID, "common/z_project.txt"},
		{"compiler", "warning", "role_order", "game diagnostic", gameID, "common/a_game.txt"},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path) VALUES(?,?,?,?,?,?)`, args...); err != nil {
			t.Fatal(err)
		}
	}

	diagnostics, err := db.ExplainDiagnostic(ctx, "role_order")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostic count = %d, want 2; diagnostics=%+v", len(diagnostics), diagnostics)
	}
	if diagnostics[0].SourceLayer != "current_mod" {
		t.Fatalf("first diagnostic source = %q, want configured project source; diagnostics=%+v", diagnostics[0].SourceLayer, diagnostics)
	}
}
