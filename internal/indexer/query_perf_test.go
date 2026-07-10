package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHotQueryPlansUseIndexes(t *testing.T) {
	dir := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("project/common/traits/test_traits.txt", `test_trait = { desc = test_trait_desc }`)
	write("project/common/decisions/test_decisions.txt", `test_decision = {
	title = test_decision.t
	is_shown = { has_trait = test_trait }
}`)
	write("project/localization/english/test_l_english.yml", `l_english:
 test_decision.t:0 "Decision"
 test_trait_desc:0 "Trait"
`)
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(dir, "cache/test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	plans := map[string]string{
		"incoming": `EXPLAIN QUERY PLAN SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.ref_name='test_trait' AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`,
		"outgoing": `EXPLAIN QUERY PLAN SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.from_object_name='test_decision' AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`,
		"object": `EXPLAIN QUERY PLAN SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
			FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.name='test_decision' AND f.overridden=0
			ORDER BY o.object_type,o.name,o.source_rank`,
	}
	for name, query := range plans {
		details := explainDetails(t, db, query)
		if strings.Contains(details, "SCAN r") || strings.Contains(details, "SCAN o") {
			t.Fatalf("%s plan should use indexes, got %s", name, details)
		}
	}
}

func explainDetails(t *testing.T, db *DB, query string) string {
	t.Helper()
	rows, err := db.sql.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, " | ")
}
