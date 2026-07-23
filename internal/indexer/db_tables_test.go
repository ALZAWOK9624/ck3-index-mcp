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
	if !reflect.DeepEqual(publishedIndexTables, semanticIndexTableCatalog[:]) {
		t.Fatalf("publication table catalog diverged from reset catalog")
	}
}
