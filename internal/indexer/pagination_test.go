package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLLMSearchPaginatesBoundedEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	var definitions strings.Builder
	for i := 0; i < 8; i++ {
		definitions.WriteString("page_trait_")
		definitions.WriteString(string(rune('a' + i)))
		definitions.WriteString(" = {}\n")
	}
	writeScanRegressionFile(t, project, "common/traits/paged.txt", definitions.String())
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first, err := db.LLMSearch(ctx, SearchOptions{Query: "page_trait", Kind: "object", Page: 1, LLMOptions: LLMOptions{AllowProject: true, Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.LLMSearch(ctx, SearchOptions{Query: "page_trait", Kind: "object", Page: 2, LLMOptions: LLMOptions{AllowProject: true, Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Pagination == nil || first.Pagination.Page != 1 || first.Pagination.Returned != 2 || !first.Pagination.HasMore || !first.Truncated {
		t.Fatalf("first page metadata = %+v truncated=%v", first.Pagination, first.Truncated)
	}
	if second.Pagination == nil || second.Pagination.Page != 2 || second.Pagination.Returned != 2 || !second.Truncated {
		t.Fatalf("second page metadata = %+v", second.Pagination)
	}
	if len(first.Evidence) != 2 || len(second.Evidence) != 2 || first.Evidence[0].Name == second.Evidence[0].Name {
		t.Fatalf("pages did not contain distinct bounded evidence: first=%+v second=%+v", first.Evidence, second.Evidence)
	}
}

func TestLLMExplainDiagnosticPaginatesAfterFiltering(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := syncSourceLayers(ctx, db.sql, []Source{{Name: "project", Path: "project", Rank: 1, Role: SourceRoleProject, Private: true}}); err != nil {
		t.Fatal(err)
	}
	file, err := db.sql.ExecContext(ctx, `INSERT INTO files(source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES('project',1,'fixture.txt','common/traits/fixture.txt','script',0,'fixture')`)
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := file.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := db.sql.ExecContext(ctx, `INSERT INTO diagnostics(source,severity,code,message,file_id,path,line) VALUES('validator','warning','page_diagnostic',?,?,?,?)`, "message", fileID, "common/traits/fixture.txt", i+1); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.LLMExplainDiagnosticFiltered(ctx, DiagnosticFilter{Code: "page_diagnostic", Page: 2}, LLMOptions{AllowProject: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pagination == nil || result.Pagination.Page != 2 || result.Pagination.Returned != 2 || !result.Pagination.HasMore || !result.Truncated {
		t.Fatalf("diagnostic pagination = %+v truncated=%v evidence=%+v", result.Pagination, result.Truncated, result.Evidence)
	}
	if result.Evidence[0].Line != 3 || result.Evidence[1].Line != 4 {
		t.Fatalf("diagnostic second page lines = %+v, want 3 and 4", result.Evidence)
	}
}
