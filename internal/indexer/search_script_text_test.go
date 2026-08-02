package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/script"
)

func TestBuildScriptSearchTextKeepsNestedKeysAndValues(t *testing.T) {
	parsed := script.Parse(`outer = {
		nested = {
			deep_key = "two   words"
			bare_value
		}
	}`)
	if len(parsed.Errors) != 0 {
		t.Fatalf("fixture parse errors: %+v", parsed.Errors)
	}
	if got, want := buildScriptSearchText(parsed.Nodes), "outer nested deep_key two words bare_value"; got != want {
		t.Fatalf("compact script search text = %q, want %q", got, want)
	}
}

func TestLocateScriptTextIgnoresEarlierCommentMatch(t *testing.T) {
	data := []byte(`# zz_comment_location must not win
root = { value = zz_comment_location }`)
	location, ok := locateScriptText(data, "zz_comment_location")
	if !ok {
		t.Fatal("token locator did not find the indexed script token")
	}
	if location.Line != 2 || !strings.Contains(location.Snippet, "value = zz_comment_location") {
		t.Fatalf("comment stole script-text location: %+v", location)
	}
}

func TestLocateScriptTextRequiresCompleteIdentifierOrStringWord(t *testing.T) {
	data := []byte(`root = {
	identifier_prefix = foobar
	quoted_prefix = "foobar"
	quoted_underscore = "foo_bar"
	quoted_word = "a bar value"
	identifier_exact = bar
}`)
	location, ok := locateScriptText(data, "bar")
	if !ok {
		t.Fatal("token locator did not find the complete string word")
	}
	if location.Line != 5 || !strings.Contains(location.Snippet, `"a bar value"`) {
		t.Fatalf("partial identifier/string prefix stole script-text location: %+v", location)
	}
	if _, ok := locateScriptText([]byte(`root = { value = foobar quoted = "foobar foo_bar" }`), "bar"); ok {
		t.Fatal("bar matched inside foobar or foo_bar without a word boundary")
	}
}

func TestScriptTextSearchFindsNestedValueAndRanksAfterObjects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	rel := "common/traits/script_text.txt"
	writeScriptSearchFixture(t, project, rel, `zz_script_text_rank = {
	shown = yes
	nested = {
		deep_value = "hidden deep phrase"
	}
	unicode_value = "苹果树精"
	dotted_value = namespace.value:child
	variable_value = @search.variable
}`)
	writeScriptSearchFixture(t, project, "common/traits/comment_only.txt", "# an active comment-only script\n")
	cfg := scriptSearchConfig(dir, Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true})
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db := openScriptSearchDB(t, dir)
	defer db.Close()
	var activeScripts, scriptDocuments int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE overridden=0 AND kind='script'`).Scan(&activeScripts); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_fts WHERE kind='script_text'`).Scan(&scriptDocuments); err != nil {
		t.Fatal(err)
	}
	if scriptDocuments != activeScripts {
		t.Fatalf("script-text FTS rows = %d, active scripts = %d", scriptDocuments, activeScripts)
	}

	nested, err := db.LLMSearch(ctx, SearchOptions{
		Query: "hidden deep phrase", Kind: "script_text",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nested.Evidence) != 1 {
		t.Fatalf("nested script search evidence = %+v", nested.Evidence)
	}
	hit := nested.Evidence[0]
	if hit.Kind != "script_text" || hit.Path != rel || hit.Line != 4 || hit.Column <= 0 || !strings.Contains(hit.Snippet, "hidden deep phrase") {
		t.Fatalf("nested script evidence did not retain exact source location: %+v", hit)
	}
	var directFieldRows int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM object_fields WHERE raw LIKE '%hidden deep phrase%'`).Scan(&directFieldRows); err != nil {
		t.Fatal(err)
	}
	if directFieldRows != 0 {
		t.Fatalf("fixture unexpectedly existed in the direct object-field index: %d", directFieldRows)
	}
	for _, query := range []string{"苹果树精", "namespace.value:child", "@search.variable"} {
		query := query
		t.Run("token_"+query, func(t *testing.T) {
			result, err := db.LLMSearch(ctx, SearchOptions{
				Query: query, Kind: "script_text",
				LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Evidence) != 1 || result.Evidence[0].Path != rel || result.Evidence[0].Line <= 0 {
				t.Fatalf("script token %q evidence = %+v", query, result.Evidence)
			}
		})
	}

	ranked, err := db.LLMSearch(ctx, SearchOptions{
		Query:      "zz_script_text_rank",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	objectAt, scriptAt := -1, -1
	for index, evidence := range ranked.Evidence {
		switch evidence.Kind {
		case "object":
			if objectAt < 0 {
				objectAt = index
			}
		case "script_text":
			if scriptAt < 0 {
				scriptAt = index
			}
		}
	}
	if objectAt < 0 || scriptAt < 0 || objectAt >= scriptAt {
		t.Fatalf("structured object did not rank before script text: %+v", ranked.Evidence)
	}
}

func TestScriptTextIncrementalRefreshRemovesStaleTerms(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	rel := "common/decisions/script_text_refresh.txt"
	writeScriptSearchFixture(t, project, rel, `fixture = { nested = { value = zz_script_text_old } }`)
	cfg := scriptSearchConfig(dir, Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true})
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	writeScriptSearchFixture(t, project, rel, `fixture = { nested = { value = zz_script_text_new } }`)
	if _, err := ScanFiles(ctx, cfg, []string{rel}); err != nil {
		t.Fatal(err)
	}
	db := openScriptSearchDB(t, dir)
	defer db.Close()
	if got := scriptTextFTSCount(t, db, "zz_script_text_old"); got != 0 {
		t.Fatalf("stale script-text term survived incremental refresh: %d", got)
	}
	if got := scriptTextFTSCount(t, db, "zz_script_text_new"); got != 1 {
		t.Fatalf("new script-text term count = %d, want 1", got)
	}
}

func TestScriptTextOverrideWinnerAndPublicSourceFiltering(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	sharedRel := "common/decisions/shared_script_text.txt"
	publicRel := "common/decisions/public_script_text.txt"
	writeScriptSearchFixture(t, project, sharedRel, `fixture = { nested = { value = zz_private_winner_term } }`)
	writeScriptSearchFixture(t, project, "common/decisions/private_script_text.txt", `fixture = { nested = { value = zz_visibility_shared_term } }`)
	writeScriptSearchFixture(t, game, sharedRel, `fixture = { nested = { value = zz_public_fallback_term } }`)
	writeScriptSearchFixture(t, game, publicRel, `fixture = { nested = { value = zz_visibility_shared_term } }`)
	cfg := scriptSearchConfig(dir,
		Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true},
		Source{Name: "game", Path: game, Rank: 2, Role: SourceRoleGame, Private: false},
	)
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db := openScriptSearchDB(t, dir)
	public, err := db.LLMSearch(ctx, SearchOptions{
		Query: "zz_visibility_shared_term", Kind: "script_text",
		LLMOptions: LLMOptions{Mode: "public", PrivateSources: map[string]bool{"project": true, "game": false}, Limit: 8},
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if len(public.Evidence) != 1 || public.Evidence[0].Source != "game" || public.Evidence[0].Path != publicRel {
		db.Close()
		t.Fatalf("public script search leaked or hid a source: %+v", public.Evidence)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(project, filepath.FromSlash(sharedRel))); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db = openScriptSearchDB(t, dir)
	defer db.Close()
	if got := scriptTextFTSCount(t, db, "zz_private_winner_term"); got != 0 {
		t.Fatalf("removed override winner survived in script-text FTS: %d", got)
	}
	if got := scriptTextFTSCount(t, db, "zz_public_fallback_term"); got != 1 {
		t.Fatalf("newly active fallback script-text count = %d, want 1", got)
	}
}

func TestScriptTextSearchSkipsFalseFTSCandidateAndRefillsLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	falseRel := "common/decisions/00_false_phrase.txt"
	validRel := "common/decisions/99_valid_phrase.txt"
	writeScriptSearchFixture(t, project, falseRel, `foo = bar`)
	writeScriptSearchFixture(t, project, validRel, `foo.bar`)
	cfg := scriptSearchConfig(dir, Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true})
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db := openScriptSearchDB(t, dir)
	defer db.Close()

	result, err := db.LLMSearch(ctx, SearchOptions{
		Query: "foo.bar", Kind: "script_text",
		LLMOptions: LLMOptions{AllowProject: true, Limit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Path != validRel || result.Evidence[0].Line != 1 {
		t.Fatalf("false punctuation FTS hit was returned or prevented replenishment: %+v", result.Evidence)
	}
	for _, evidence := range result.Evidence {
		if evidence.Path == falseRel {
			t.Fatalf("unlocated FTS candidate became script source evidence: %+v", evidence)
		}
	}
}

func TestScriptTextSearchRejectsStaleAndMismatchedRows(t *testing.T) {
	t.Run("changed file hash", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		project := filepath.Join(dir, "project")
		game := filepath.Join(dir, "game")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		rel := "common/decisions/stale_source.txt"
		writeScriptSearchFixture(t, game, rel, `fixture = { value = zz_stale_source_term }`)
		cfg := scriptSearchConfig(dir,
			Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true},
			Source{Name: "game", Path: game, Rank: 2, Role: SourceRoleGame, Private: false},
		)
		if _, err := Scan(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		writeScriptSearchFixture(t, game, rel, `fixture = { value = zz_stale_source_term changed = yes }`)
		db := openScriptSearchDB(t, dir)
		defer db.Close()
		result, err := db.LLMSearch(ctx, SearchOptions{
			Query: "zz_stale_source_term", Kind: "script_text",
			LLMOptions: LLMOptions{Mode: "public", PrivateSources: map[string]bool{"game": false}, Limit: 8},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) != 0 {
			t.Fatalf("changed source produced stale script snippet evidence: %+v", result.Evidence)
		}
	})

	t.Run("fts identity mismatch", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		project := filepath.Join(dir, "project")
		rel := "common/decisions/mismatched_source.txt"
		writeScriptSearchFixture(t, project, rel, `fixture = { value = zz_mismatched_fts_term }`)
		cfg := scriptSearchConfig(dir, Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true})
		if _, err := Scan(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		db := openScriptSearchDB(t, dir)
		defer db.Close()
		if _, err := db.sql.ExecContext(ctx, `UPDATE search_fts SET source='forged-public',path='common/decisions/forged.txt' WHERE kind='script_text' AND path=?`, rel); err != nil {
			t.Fatal(err)
		}
		result, err := db.LLMSearch(ctx, SearchOptions{
			Query: "zz_mismatched_fts_term", Kind: "script_text",
			LLMOptions: LLMOptions{AllowProject: true, Limit: 8},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) != 0 {
			t.Fatalf("mismatched FTS identity produced script source evidence: %+v", result.Evidence)
		}
	})
}

func TestPublicBroadSearchFiltersPrivateRowsBeforeCapacity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	for index := 0; index < 12; index++ {
		rel := filepath.ToSlash(filepath.Join("common", "traits", fmt.Sprintf("private_%02d.txt", index)))
		writeScriptSearchFixture(t, project, rel, fmt.Sprintf("zz_public_capacity_private_%02d = {}", index))
	}
	publicRel := "common/decisions/public_capacity.txt"
	writeScriptSearchFixture(t, game, publicRel, `public_holder = { nested = { value = zz_public_capacity } }`)
	cfg := scriptSearchConfig(dir,
		Source{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject, Private: true},
		Source{Name: "game", Path: game, Rank: 2, Role: SourceRoleGame, Private: false},
	)
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db := openScriptSearchDB(t, dir)
	defer db.Close()
	result, err := db.LLMSearch(ctx, SearchOptions{
		Query: "zz_public_capacity",
		LLMOptions: LLMOptions{
			Mode: "public", PrivateSources: map[string]bool{"project": true, "game": false}, Limit: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Source != "game" || result.Evidence[0].Path != publicRel {
		t.Fatalf("private structured rows displaced public script evidence: %+v", result.Evidence)
	}
}

func scriptSearchConfig(dir string, sources ...Source) Config {
	return Config{ConfigPath: filepath.Join(dir, "ck3-index.toml"), Database: "cache/test.sqlite", Sources: sources}
}

func writeScriptSearchFixture(t *testing.T, root, rel, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openScriptSearchDB(t *testing.T, dir string) *DB {
	t.Helper()
	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func scriptTextFTSCount(t *testing.T, db *DB, term string) int {
	t.Helper()
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM search_fts WHERE kind='script_text' AND search_fts MATCH ?`, `"`+term+`"`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
