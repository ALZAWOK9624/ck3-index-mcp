package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// Directly exercise each affected query, so exact/prefix lookup cannot hide
// a broken substring fallback. The sibling names catch both loss and leakage.
func TestAuditSearchLiteralPathPrefixes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "search.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	paths := []string{"common/on_action/a.txt", "common/onXaction/a.txt", "common/100%/a.txt", "common/100wild/a.txt", `common/a\b/a.txt`, "common/ab/a.txt"}
	for i, path := range paths {
		id := i + 1
		queries := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256) VALUES(?,'project',1,?,?,'script',0,'audit')`, []any{id, path, path}},
			{`INSERT INTO objects(object_type,name,file_id,source_name,source_rank,path,line) VALUES('trait',?,?, 'project',1,?,1)`, []any{fmt.Sprintf("match_%d", id), id, path}},
			{`INSERT INTO resources(resource_path,kind,file_id,source_name,source_rank,path) VALUES(?,'texture',?,'project',1,?)`, []any{fmt.Sprintf("gfx/match_%d.dds", id), id, path}},
			{`INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,'english','match',?,'project',1,?,1,0)`, []any{fmt.Sprintf("key_%d", id), id, path}},
			{`INSERT INTO search_fts(kind,name,text,source,path,file_id) VALUES('object',?,'match','project',?,?)`, []any{fmt.Sprintf("match_%d", id), path, id}},
		}
		for _, q := range queries {
			if _, err := db.sql.Exec(q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, prefix := range []string{"common/on_action/", "common/100%/", `common/a\b/`} {
		for _, kind := range []string{"object", "resource", "localization", "fts"} {
			for _, query := range []string{"ma", "match"} {
				t.Run(prefix+kind+query, func(t *testing.T) {
					opts := SearchOptions{PathPrefix: prefix, Kind: kind}
					var got []LLMEvidence
					var err error
					if kind == "fts" {
						opts.Kind = "object"
						got, err = db.searchFTS(ctx, "match", opts, 100)
					} else {
						got, err = db.searchContains(ctx, query, opts, 100)
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(got) != 1 {
						t.Fatalf("prefix=%q kind=%s query=%q got=%+v", prefix, kind, query, got)
					}
					// evidencePath normalizes the backslash for presentation.
					if evidencePath(got[0].Path) != evidencePath(prefix+"a.txt") {
						t.Fatalf("wrong sibling returned: %+v", got)
					}
				})
			}
		}
	}
}

func TestAuditDatatypeLiteralPrefix(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "datatype.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit_type", "auditXtype", "audit%type", "auditZZtype"} {
		if _, err := db.sql.Exec(`INSERT INTO engine_datatypes(name,signature,source_path) VALUES(?,'test','test')`, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"audit_", "audit%"} {
		got, err := db.LookupDatatype(context.Background(), prefix, 20)
		if err != nil || len(got) != 1 || got[0].Name != prefix+"type" {
			t.Fatalf("literal datatype prefix %q: %+v %v", prefix, got, err)
		}
	}
}
