package indexer

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLLMValidateUsesConfiguredProjectRole(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := syncSourceLayers(ctx, db.sql, []Source{
		{Name: "current_mod", Path: "current_mod", Rank: 7, Role: SourceRoleProject, Private: true},
		{Name: "vanilla_119", Path: "vanilla_119", Rank: 9, Role: SourceRoleGame, Private: false},
	}); err != nil {
		t.Fatal(err)
	}
	projectResult, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES('current_mod',7,'project.txt','common/project.txt','script',0,'project')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	gameResult, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES('vanilla_119',9,'game.txt','common/game.txt','script',0,'game')`)
	if err != nil {
		t.Fatal(err)
	}
	gameID, _ := gameResult.LastInsertId()
	for _, args := range [][]any{
		{"compiler", "warning", "project_warning", "project", projectID, "common/project.txt"},
		{"compiler", "warning", "game_warning", "game", gameID, "common/game.txt"},
		{"compiler", "warning", "global_warning", "global", nil, nil},
	} {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path) VALUES(?,?,?,?,?,?)`, args...); err != nil {
			t.Fatal(err)
		}
	}
	report, err := db.LLMValidate(ctx, LLMOptions{AllowProject: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["warning"] != 2 {
		t.Fatalf("expected project plus global warnings only, got %+v", report.Counts)
	}
	for _, evidence := range report.Evidence {
		if evidence.Path == "common/game.txt" {
			t.Fatalf("game diagnostic leaked into project validation: %+v", evidence)
		}
	}
}
