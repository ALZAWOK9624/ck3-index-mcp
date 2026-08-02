package indexer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseSeedFixture lays out one upstream pair (game + mod) plus two project
// roots: an empty one used to build the shared base, and a real one that
// exercises both ways a project can hide upstream files.
type baseSeedFixture struct {
	dir          string
	game         string
	mod          string
	emptyProject string
	project      string
}

func writeBaseSeedFixture(t *testing.T) baseSeedFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := baseSeedFixture{
		dir:          dir,
		game:         filepath.Join(dir, "game"),
		mod:          filepath.Join(dir, "mod"),
		emptyProject: filepath.Join(dir, "empty-project"),
		project:      filepath.Join(dir, "project"),
	}
	write := func(root, rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Upstream game layer. seed_shared_trait stays active in every scan;
	// seed_replaced_trait is taken over by the project through an identical
	// relative path. Both history files sit under the directory the project
	// replaces, but only seed_upstream_only.txt is absent from the project, so
	// it is the one that can reach the descriptor_replace_path branch: a file
	// the project also ships is classified as same_relative_path first.
	write(fixture.game, "common/traits/seed_shared.txt", "seed_shared_trait = { diplomacy = 1 }\n")
	write(fixture.game, "common/traits/seed_replaced.txt", "seed_replaced_trait = { martial = 1 }\n")
	write(fixture.game, "history/characters/seed_people.txt", "1 = { name = \"Upstream\" }\n")
	write(fixture.game, "history/characters/seed_upstream_only.txt", "2 = { name = \"UpstreamOnly\" }\n")
	write(fixture.game, "common/on_action/seed_hook.txt", "seed_upstream_hook = { effect = { add_prestige = 1 } }\n")

	// Upstream mod layer, higher precedence than game but still shared.
	write(fixture.mod, "descriptor.mod", "name=\"seed mod\"\n")
	write(fixture.mod, "common/traits/seed_mod_only.txt", "seed_mod_trait = { stewardship = 1 }\n")

	// The base is built against an empty project directory, so every upstream
	// file starts out active.
	if err := os.MkdirAll(fixture.emptyProject, 0o755); err != nil {
		t.Fatal(err)
	}

	// The real project: one descriptor replace_path plus one same-name override,
	// and a reference into an upstream object so ref resolution has to run
	// across layers after seeding.
	write(fixture.project, "descriptor.mod", "name=\"seed project\"\nreplace_path=\"history/characters\"\n")
	write(fixture.project, "common/traits/seed_replaced.txt", "seed_replaced_trait = { martial = 5 intrigue = 2 }\n")
	write(fixture.project, "history/characters/seed_people.txt", "1 = { name = \"Project\" }\n")
	write(fixture.project, "common/scripted_triggers/seed_refs.txt",
		"seed_project_trigger = {\n\thas_trait = seed_shared_trait\n\thas_trait = seed_mod_trait\n\thas_trait = seed_missing_trait\n}\n")
	return fixture
}

func (f baseSeedFixture) config(t *testing.T, projectRoot, databaseRel string) Config {
	t.Helper()
	cfg := Config{
		ConfigPath: filepath.Join(f.dir, "ck3-index.toml"),
		Database:   databaseRel,
		Sources: []Source{
			{Name: "project", Path: projectRoot, Rank: 1, Role: SourceRoleProject},
			{Name: "seedmod", Path: f.mod, Rank: 2, Role: SourceRoleDependency},
			{Name: "game", Path: f.game, Rank: 3, Role: SourceRoleGame},
		},
	}
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

// indexProjection renders the whole semantic content of an index as sorted,
// id-free rows. Row ids differ between a clean rebuild and a seeded one purely
// because of insertion order, so identity has to be expressed in terms a caller
// could actually observe.
func indexProjection(t *testing.T, dbPath string) map[string][]string {
	t.Helper()
	db, err := OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries := map[string]string{
		"files": `SELECT source_name,rel_path,kind,sha256,overridden,override_reason,override_by_source,override_by_rank,override_rule
			FROM files`,
		"objects": `SELECT o.object_type,o.name,o.source_name,f.rel_path,COALESCE(o.line,0)
			FROM objects o JOIN files f ON f.id=o.file_id`,
		"refs": `SELECT r.ref_kind,r.ref_name,r.resolved,r.relation,r.phase,r.confidence,
			COALESCE(r.from_object_type,''),COALESCE(r.from_object_name,''),f.source_name,f.rel_path
			FROM refs r JOIN files f ON f.id=r.file_id`,
		"localization": `SELECT l.key,l.language,l.value,l.source_name,f.rel_path
			FROM localization l JOIN files f ON f.id=l.file_id`,
		"resources": `SELECT r.resource_path,r.kind,r.source_name,f.rel_path
			FROM resources r JOIN files f ON f.id=r.file_id`,
		"object_fields": `SELECT o.object_type,o.object_name,o.field,o.value_shape,o.source_name
			FROM object_fields o`,
		"diagnostics": `SELECT source,severity,code,message,COALESCE(path,''),COALESCE(line,0),COALESCE(col,0),source_layer,confidence
			FROM diagnostics`,
		"source_layers": `SELECT name,rank,role,private FROM source_layers`,
	}
	out := map[string][]string{}
	for name, query := range queries {
		out[name] = queryRowStrings(t, db.sql, query)
	}
	return out
}

func queryRowStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(columns))
		for i, cell := range cells {
			parts[i] = cell.(*sql.NullString).String
		}
		out = append(out, strings.Join(parts, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func diffProjections(t *testing.T, table string, want, got []string) {
	t.Helper()
	if len(want) == len(got) {
		same := true
		for i := range want {
			if want[i] != got[i] {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	wantSet := map[string]bool{}
	for _, row := range want {
		wantSet[row] = true
	}
	gotSet := map[string]bool{}
	for _, row := range got {
		gotSet[row] = true
	}
	var missing, extra []string
	for _, row := range want {
		if !gotSet[row] {
			missing = append(missing, row)
		}
	}
	for _, row := range got {
		if !wantSet[row] {
			extra = append(extra, row)
		}
	}
	t.Errorf("table %s differs between a full scan and a seeded scan\n  full-scan rows missing from seeded (%d): %s\n  seeded rows absent from full scan (%d): %s",
		table, len(missing), strings.Join(boundedRows(missing), " | "), len(extra), strings.Join(boundedRows(extra), " | "))
}

func boundedRows(rows []string) []string {
	const limit = 12
	if len(rows) <= limit {
		return rows
	}
	return append(append([]string(nil), rows[:limit]...), "...")
}

// TestSeededRefreshMatchesFullScan is the regression that gives base seeding
// its licence to exist. A seeded rebuild is only an optimization if it is
// indistinguishable from parsing every source, including the parts that are
// easy to get wrong: upstream files hidden by descriptor replace_path or by an
// identical relative path must lose their derived rows, and cross-layer
// reference resolution must be recomputed rather than inherited from the base.
func TestSeededRefreshMatchesFullScan(t *testing.T) {
	ctx := context.Background()
	fixture := writeBaseSeedFixture(t)

	baseCfg := fixture.config(t, fixture.emptyProject, "cache/base.sqlite")
	if _, err := Scan(ctx, baseCfg); err != nil {
		t.Fatalf("build base index: %v", err)
	}

	fullCfg := fixture.config(t, fixture.project, "cache/full.sqlite")
	if _, err := ScanFullStaged(ctx, fullCfg); err != nil {
		t.Fatalf("full staged scan: %v", err)
	}

	seededCfg := fixture.config(t, fixture.project, "cache/seeded.sqlite")
	seededCfg.BaseDatabase = filepath.Join(fixture.dir, "cache", "base.sqlite")
	stats, err := ScanFullStaged(ctx, seededCfg)
	if err != nil {
		t.Fatalf("seeded staged scan: %v", err)
	}
	if stats.BaseSeed == nil || !stats.BaseSeed.Used {
		t.Fatalf("configured base was not used: %+v", stats.BaseSeed)
	}

	want := indexProjection(t, filepath.Join(fixture.dir, "cache", "full.sqlite"))
	got := indexProjection(t, filepath.Join(fixture.dir, "cache", "seeded.sqlite"))
	for _, table := range []string{"files", "objects", "refs", "localization", "resources", "object_fields", "diagnostics", "source_layers"} {
		diffProjections(t, table, want[table], got[table])
	}

	// Guard the specific hazards rather than trusting the bulk comparison to
	// have covered them: an empty or trivially matching projection would pass
	// the loop above while proving nothing.
	assertProjectionContains(t, got["files"], "history/characters/seed_upstream_only.txt", "descriptor_replace_path")
	assertProjectionContains(t, got["files"], "history/characters/seed_people.txt", "same_relative_path")
	assertProjectionContains(t, got["files"], "common/traits/seed_replaced.txt", "same_relative_path")
	assertProjectionContains(t, got["objects"], "seed_replaced_trait", "project")
	// A hidden upstream file keeps its files row but must retain no derived
	// rows, exactly as a clean rebuild would leave it.
	assertNoProjectionRow(t, got["objects"], "history/characters/seed_upstream_only.txt")
}

func assertProjectionContains(t *testing.T, rows []string, needles ...string) {
	t.Helper()
	for _, row := range rows {
		matched := true
		for _, needle := range needles {
			if !strings.Contains(row, needle) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Errorf("no indexed row matched %v; the fixture stopped exercising this case", needles)
}

func assertNoProjectionRow(t *testing.T, rows []string, needle string) {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, needle) {
			t.Errorf("an overridden file kept derived rows after seeding: %s", row)
			return
		}
	}
}

// TestSeededRefreshRejectsMismatchedBase keeps seeding fail-safe: a base built
// from different upstream trees must be refused and the refresh must fall back
// to a full parse rather than publishing another project's upstream rows.
func TestSeededRefreshRejectsMismatchedBase(t *testing.T) {
	ctx := context.Background()
	fixture := writeBaseSeedFixture(t)

	baseCfg := fixture.config(t, fixture.emptyProject, "cache/base.sqlite")
	if _, err := Scan(ctx, baseCfg); err != nil {
		t.Fatalf("build base index: %v", err)
	}

	otherGame := filepath.Join(fixture.dir, "other-game")
	if err := os.MkdirAll(filepath.Join(otherGame, "common", "traits"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherGame, "common", "traits", "other.txt"),
		[]byte("other_trait = { learning = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mismatched := fixture.config(t, fixture.project, "cache/mismatched.sqlite")
	for i := range mismatched.Sources {
		if mismatched.Sources[i].Name == "game" {
			mismatched.Sources[i].Path = otherGame
		}
	}
	mismatched.BaseDatabase = filepath.Join(fixture.dir, "cache", "base.sqlite")
	stats, err := ScanFullStaged(ctx, mismatched)
	if err != nil {
		t.Fatalf("refresh with a mismatched base should still succeed by parsing everything: %v", err)
	}
	if stats.BaseSeed == nil || stats.BaseSeed.Used {
		t.Fatalf("a base built from different upstream sources was accepted: %+v", stats.BaseSeed)
	}
	if !strings.Contains(stats.BaseSeed.Reason, "different upstream sources") {
		t.Fatalf("rejection reason did not name the cause: %q", stats.BaseSeed.Reason)
	}

	// The fallback must have indexed the replacement upstream tree, not the one
	// the base was built from.
	projection := indexProjection(t, filepath.Join(fixture.dir, "cache", "mismatched.sqlite"))
	assertProjectionContains(t, projection["objects"], "other_trait")
	for _, row := range projection["objects"] {
		if strings.Contains(row, "seed_shared_trait") {
			t.Fatalf("rejected base still leaked upstream rows into the published index: %s", row)
		}
	}
}

// TestSeededRefreshRejectsBaseCarryingProjectRows protects the invariant the
// seeding argument rests on. If a base already had a project layered onto it,
// some upstream files would be recorded as overridden with no derived rows, and
// a different project that leaves them active cannot restore those rows from
// metadata alone.
func TestSeededRefreshRejectsBaseCarryingProjectRows(t *testing.T) {
	ctx := context.Background()
	fixture := writeBaseSeedFixture(t)

	pollutedBase := fixture.config(t, fixture.project, "cache/polluted-base.sqlite")
	if _, err := Scan(ctx, pollutedBase); err != nil {
		t.Fatalf("build polluted base: %v", err)
	}

	seeded := fixture.config(t, fixture.project, "cache/seeded.sqlite")
	seeded.BaseDatabase = filepath.Join(fixture.dir, "cache", "polluted-base.sqlite")
	stats, err := ScanFullStaged(ctx, seeded)
	if err != nil {
		t.Fatalf("refresh should fall back rather than fail: %v", err)
	}
	if stats.BaseSeed == nil || stats.BaseSeed.Used {
		t.Fatalf("a base containing project rows was accepted: %+v", stats.BaseSeed)
	}
	if !strings.Contains(stats.BaseSeed.Reason, "project rank") {
		t.Fatalf("rejection reason did not name the cause: %q", stats.BaseSeed.Reason)
	}
}

// TestBaseDatabaseMustDifferFromDatabase stops a configuration that would make
// a refresh overwrite the shared upstream index with one project's snapshot.
func TestBaseDatabaseMustDifferFromDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ck3-index.toml")
	body := "database = \"cache/index.sqlite\"\nbase_database = \"cache/index.sqlite\"\n\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("a configuration naming one file as both database and base_database was accepted")
	}
}
