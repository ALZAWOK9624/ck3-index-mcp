package indexer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var indexedByClause = regexp.MustCompile(`INDEXED BY ([A-Za-z0-9_]+)`)

// An index named in an INDEXED BY clause is a hard dependency: SQLite refuses
// to plan the statement when it is absent, so the tool fails outright rather
// than merely running slowly. idx_object_fields_field was exactly that case —
// ck3_search could not execute at all on a database missing it, while
// ck3_health reported the index as healthy because its required list had been
// maintained by hand and had fallen behind.
//
// This test derives the truth from the queries themselves so the list cannot
// drift again.
func TestIndexedByDependenciesAreDeclaredAndChecked(t *testing.T) {
	used := indexesNamedByQueries(t)
	if len(used) == 0 {
		t.Fatal("found no INDEXED BY clauses; the scanner is looking in the wrong place")
	}

	declared := map[string]bool{}
	for _, name := range indexedByDependencies {
		declared[name] = true
	}
	created := indexesCreatedBySchema()
	for _, name := range used {
		if !declared[name] {
			t.Errorf("query uses INDEXED BY %s but indexedByDependencies does not list it, so ck3_health would report a database missing it as healthy", name)
		}
		if !created[name] {
			t.Errorf("query uses INDEXED BY %s but indexStmts never creates it", name)
		}
	}
	for _, name := range indexedByDependencies {
		if !contains(used, name) {
			t.Errorf("indexedByDependencies lists %s but no query names it in an INDEXED BY clause", name)
		}
	}
}

// TestPerformanceIndexRequirementsExist keeps the health check honest in the
// other direction: every index it demands has to be one the schema creates.
func TestPerformanceIndexRequirementsExist(t *testing.T) {
	db := openPragmaProbeDatabase(t)
	missing, err := db.missingPerformanceIndexes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("a freshly scanned database is missing indexes the health check requires: %v", missing)
	}
}

// indexesNamedByQueries reads the package's own source and collects every
// index named in an INDEXED BY clause. Only string literals are examined:
// scanning raw file text also matches the phrase where it appears in prose,
// including the comments explaining this very mechanism.
func indexesNamedByQueries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			for _, match := range indexedByClause.FindAllStringSubmatch(literal.Value, -1) {
				seen[match[1]] = true
			}
			return true
		})
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func indexesCreatedBySchema() map[string]bool {
	created := map[string]bool{}
	for _, statement := range indexStmts {
		fields := strings.Fields(statement)
		for index, field := range fields {
			if strings.EqualFold(field, "EXISTS") && index+1 < len(fields) {
				created[fields[index+1]] = true
				break
			}
		}
	}
	return created
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
