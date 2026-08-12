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

	expected := append([]string(nil), semanticIndexTableCatalog[:]...)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("schema table catalog mismatch:\nactual:   %v\nexpected: %v", actual, expected)
	}
}

func TestSemanticIndexTableCatalogHasNoDuplicatesAndDrivesPublication(t *testing.T) {
	seen := make(map[string]bool, len(semanticIndexTableCatalog))
	for _, table := range semanticIndexTableCatalog {
		if seen[table] {
			t.Fatalf("semantic index table catalog repeats %q", table)
		}
		seen[table] = true
	}
	// Publication must carry every catalog table except the contentless FTS
	// caches, which are rebuilt from the copied source tables. Positional
	// slicing used to encode this and quietly published script_text_fts as
	// soon as a new table was appended after it.
	var want []string
	for _, table := range semanticIndexTableCatalog {
		if !contentlessDerivedTables[table] {
			want = append(want, table)
		}
	}
	if !reflect.DeepEqual(publishedIndexTables, want) {
		t.Fatalf("publication table catalog must omit exactly the rebuilt contentless FTS tables:\ngot:  %v\nwant: %v", publishedIndexTables, want)
	}
	for table := range contentlessDerivedTables {
		if !seen[table] {
			t.Fatalf("contentless derived table %q is not in the semantic index catalog", table)
		}
	}
}
