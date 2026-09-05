package indexer

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestSemanticIndexTableCatalogMatchesCreatedSchema(t *testing.T) {
	db, err := Open(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	rows, err := db.sql.Query(`
		SELECT name
		FROM sqlite_schema
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE 'search_fts_%'
		  AND name NOT LIKE 'script_text_fts_%'
		  AND name NOT LIKE 'trigram_loc_%'
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	expected := schemaTableNames()
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("schema table catalog mismatch:\nactual:   %v\nexpected: %v", actual, expected)
	}
}

// A preserved table must be excluded from both of the catalog's jobs. If one
// ever appears in the catalog it would be dropped by reset and copied by
// publication, which is the failure this separation exists to prevent.
func TestRebuildPreservedTablesAreOutsideTheIndexCatalog(t *testing.T) {
	if len(rebuildPreservedTables) == 0 {
		t.Fatal("no preserved tables are declared; remove the mechanism instead of leaving it empty")
	}
	for _, table := range semanticIndexTableCatalog {
		if rebuildPreservedTables[table] {
			t.Fatalf("%q is both an index catalog table and a preserved table; reset would drop it", table)
		}
	}

}

func TestSemanticIndexTableCatalogHasNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(semanticIndexTableCatalog))
	for _, table := range semanticIndexTableCatalog {
		if seen[table] {
			t.Fatalf("semantic index table catalog repeats %q", table)
		}
		seen[table] = true
	}
}
