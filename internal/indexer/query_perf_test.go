package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHotQueryPlansUseIndexes guards the plans behind QueryRefs and
// QueryObject, the two reads every object-centred tool depends on.
//
// The fixture is deliberately large. This test used to run against three files
// holding a single trait and a single decision, where a full scan of a
// three-row table genuinely is the cheapest plan — the assertion only held
// because the database carried no statistics and SQLite was planning from
// built-in guesses. Once ANALYZE started running at the end of a scan the
// planner made the correct choice for that fixture and the test failed. An
// index-usage assertion is only meaningful at a size where using the index is
// actually the better plan.
func TestHotQueryPlansUseIndexes(t *testing.T) {
	db := writeQueryPlanFixture(t)
	plans := map[string]string{
		"incoming": `EXPLAIN QUERY PLAN SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.ref_name='plan_probe_trait_0000' AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`,
		"outgoing": `EXPLAIN QUERY PLAN SELECT r.from_object_type,r.from_object_name,r.ref_kind,r.ref_name,r.raw,r.resolved,f.source_name,f.path,r.line,r.col
			FROM refs r JOIN files f ON f.id=r.file_id
			WHERE r.from_object_name='plan_probe.0' AND f.overridden=0
			ORDER BY f.source_rank,f.path,r.line LIMIT 500`,
		"object": `EXPLAIN QUERY PLAN SELECT o.object_type,o.name,o.source_name,o.source_rank,o.path,o.line,o.col
			FROM objects o JOIN files f ON f.id=o.file_id
			WHERE o.name='plan_probe_trait_0000' AND f.overridden=0
			ORDER BY o.object_type,o.name,o.source_rank`,
	}
	for name, query := range plans {
		details := explainDetails(t, db, query)
		if strings.Contains(details, "SCAN r") || strings.Contains(details, "SCAN o") {
			t.Errorf("%s plan should use indexes, got %s", name, details)
		}
	}
}

// TestSearchPrefixPlanDoesNotSortTheWholeRange covers the cost a scan check
// cannot see. A prefix query can seek perfectly through its index and still be
// slow, because ordering by a column the index does not cover makes SQLite
// materialise and sort every row in the range before LIMIT applies. A partial
// sort of one equal-name group is fine and is reported differently.
func TestSearchPrefixPlanDoesNotSortTheWholeRange(t *testing.T) {
	db := writeQueryPlanFixture(t)
	details := explainDetails(t, db, `EXPLAIN QUERY PLAN SELECT o.object_type,o.name,o.source_name,o.path,o.line,o.col
		FROM objects o INDEXED BY idx_objects_name JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0 AND o.name>='plan_probe_' AND o.name<'plan_probe_￿'
		ORDER BY o.name,o.source_rank LIMIT 9`)
	for _, step := range strings.Split(details, " | ") {
		if risk := queryPlanRisk(step); risk != "" {
			t.Fatalf("prefix search plan regressed: %s\nfull plan: %s", risk, details)
		}
	}
}

func writeQueryPlanFixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	write := func(rel, text string) {
		t.Helper()
		full := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var traits strings.Builder
	for index := 0; index < 400; index++ {
		fmt.Fprintf(&traits, "plan_probe_trait_%04d = { category = personality desc = plan_probe_trait_%04d_desc }\n", index, index)
	}
	write("common/traits/plan_probe_traits.txt", traits.String())

	// Concentrate references on one identifier so an index lookup is clearly
	// cheaper than reading the table.
	var events strings.Builder
	for index := 0; index < 300; index++ {
		fmt.Fprintf(&events, "plan_probe.%d = {\n\ttype = character_event\n\ttrigger = { has_trait = plan_probe_trait_0000 }\n}\n", index)
	}
	write("events/plan_probe_events.txt", events.String())

	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/plan.sqlite",
		GISEnabled: false,
		Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
	}
	if _, err := Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	dbPath, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
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
