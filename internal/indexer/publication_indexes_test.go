package indexer

import (
	"context"
	"os"
	"sort"
	"testing"
)

func declaredIndexNames(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.sql.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// Publication drops every secondary index so the whole-database copy is not
// filtered through sixty-odd live B-trees. That is only safe if the finished
// generation has all of them back: an index named in an INDEXED BY clause is
// load-bearing, and SQLite fails the statement outright when it is missing.
func TestStagedPublicationRestoresEverySecondaryIndex(t *testing.T) {
	ctx := context.Background()
	cfg, reader, sourcePath, dbPath := stagedFullRefreshFixture(t)
	before, err := reader.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	indexesBefore := declaredIndexNames(t, reader)
	if len(indexesBefore) == 0 {
		t.Fatal("fixture published no secondary indexes at all")
	}

	if err := os.WriteFile(sourcePath, []byte("staged_after_publication = { value = published_probe }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagePath, err := stagedFullScanPath(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer removeStagedDatabase(stagePath)
	stageConfig := cfg
	stageConfig.Database = stagePath
	stageConfig.ForceClean = true
	if _, err := scanWithMode(ctx, stageConfig, true); err != nil {
		t.Fatal(err)
	}
	if err := publishStagedFullScan(ctx, cfg, stagePath, publicationBaseFromState(before)); err != nil {
		t.Fatal(err)
	}

	published, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()

	indexesAfter := declaredIndexNames(t, published)
	present := make(map[string]bool, len(indexesAfter))
	for _, name := range indexesAfter {
		present[name] = true
	}
	for _, name := range indexesBefore {
		if !present[name] {
			t.Errorf("publication lost index %s", name)
		}
	}
	for _, name := range indexedByDependencies {
		if !present[name] {
			t.Errorf("publication lost INDEXED BY dependency %s; queries naming it will fail outright", name)
		}
	}

	// A restored index has to actually serve a query, not merely exist in
	// sqlite_master: an INDEXED BY query is the strictest available check.
	result, err := published.LLMSearch(ctx, SearchOptions{
		Query:      "staged_after_publication",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
	})
	if err != nil {
		t.Fatalf("indexed query after publication failed: %v", err)
	}
	if len(result.Evidence) == 0 {
		t.Fatal("published generation returned no evidence for its own object")
	}
}

// Dropping the indexes inside the publication transaction is only safe because
// SQLite keeps DDL transactional: an abandoned publication, or a crash that
// leaves the transaction unfinished, has to come back with every index intact.
// This asserts that premise directly on the mechanism rather than through a
// publication failure -- the failures publication can currently produce are all
// raised before the drop, so they would prove nothing.
func TestDroppedSecondaryIndexesReturnOnRollback(t *testing.T) {
	ctx := context.Background()
	_, reader, _, dbPath := stagedFullRefreshFixture(t)
	indexesBefore := declaredIndexNames(t, reader)
	if len(indexesBefore) == 0 {
		t.Fatal("fixture published no secondary indexes at all")
	}

	live, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	conn, err := live.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := dropSecondaryIndexes(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var duringDrop int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`).Scan(&duringDrop); err != nil {
		t.Fatal(err)
	}
	if duringDrop != 0 {
		t.Fatalf("dropSecondaryIndexes left %d indexes in place; the copy would still be filtered through them", duringDrop)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}

	indexesAfter := declaredIndexNames(t, live)
	if len(indexesAfter) != len(indexesBefore) {
		t.Fatalf("rollback did not restore the index set: %d before, %d after", len(indexesBefore), len(indexesAfter))
	}
	for i := range indexesBefore {
		if indexesBefore[i] != indexesAfter[i] {
			t.Fatalf("rollback changed the index set:\nbefore %v\nafter  %v", indexesBefore, indexesAfter)
		}
	}
}
