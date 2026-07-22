package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigNormalizesLegacySourcesAndHonorsExplicitPrivate(t *testing.T) {
	dir := t.TempDir()

	t.Run("explicit role is independent from rank", func(t *testing.T) {
		path := filepath.Join(dir, "explicit.toml")
		text := `database = "cache/test.sqlite"
[[source]]
name = "dependency"
path = "dependency"
rank = 1
role = "dependency"
private = false
[[source]]
name = "current_mod"
path = "current_mod"
rank = 7
role = "project"
private = false
[[source]]
name = "game"
path = "game"
rank = 9
role = "game"
private = false
`
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ProjectSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if project.Name != "current_mod" || project.Rank != 7 || project.Role != SourceRoleProject {
			t.Fatalf("project source = %+v, want current_mod/project/rank 7", project)
		}
		if project.Private {
			t.Fatalf("explicit project private=false was overwritten: %+v", project)
		}
		game, ok := GameSource(cfg)
		if !ok || game.Name != "game" || game.Rank != 9 || game.Role != SourceRoleGame {
			t.Fatalf("game source = %+v, ok=%v", game, ok)
		}
	})

	t.Run("legacy source blocks receive safe inferred policy", func(t *testing.T) {
		path := filepath.Join(dir, "legacy.toml")
		text := `database = "cache/test.sqlite"
[[source]]
name = "current_mod"
path = "current_mod"
rank = 1
[[source]]
name = "game"
path = "game"
rank = 2
`
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ProjectSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if project.Name != "current_mod" || project.Role != SourceRoleProject || !project.Private {
			t.Fatalf("legacy project policy = %+v, want inferred private project source", project)
		}
		game, ok := GameSource(cfg)
		if !ok || game.Name != "game" || game.Role != SourceRoleGame || game.Private {
			t.Fatalf("legacy game policy = %+v, ok=%v", game, ok)
		}
	})

	t.Run("legacy project need not have rank one", func(t *testing.T) {
		path := filepath.Join(dir, "legacy-non-rank-one.toml")
		text := `database = "cache/test.sqlite"
[[source]]
name = "godherja"
path = "godherja"
rank = 2
[[source]]
name = "game"
path = "game"
rank = 3
`
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		project, err := ProjectSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if project.Name != "godherja" || project.Rank != 2 || project.Role != SourceRoleProject || !project.Private {
			t.Fatalf("legacy non-rank-one project policy = %+v", project)
		}
	})

	t.Run("programmatic explicit public project stays public", func(t *testing.T) {
		cfg, err := NormalizeConfig(Config{Sources: []Source{
			{Name: "api-project", Path: "api-project", Rank: 4, Role: SourceRoleProject, Private: false},
			{Name: "api-game", Path: "api-game", Rank: 9, Role: SourceRoleGame, Private: false},
		}})
		if err != nil {
			t.Fatal(err)
		}
		project, err := ProjectSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if project.Private {
			t.Fatalf("programmatic explicit private=false was overwritten: %+v", project)
		}
		policy := PrivateSourceNames(cfg)
		if private, known := policy["api-project"]; !known || private {
			t.Fatalf("public project source policy = private=%v known=%v, want false/true", private, known)
		}
	})
}

func TestScanFilesUsesProjectRoleOutsideRankOneAndPersistsSourcePolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "current_mod")
	game := filepath.Join(dir, "game")
	rel := "common/traits/role_policy.txt"
	write := func(root, rel, contents string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(project, rel, "old_role_policy_trait = {}\n")
	if err := os.MkdirAll(game, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []Source{
			{Name: "current_mod", Path: project, Rank: 7, Role: SourceRoleProject, Private: true},
			{Name: "game", Path: game, Rank: 10, Role: SourceRoleGame, Private: false},
		},
	}
	if _, err := Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	write(project, rel, "new_role_policy_trait = {}\n")
	stats, err := ScanFiles(ctx, cfg, []string{rel})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChangedFiles != 1 {
		t.Fatalf("changed files = %d, want 1; stats=%+v", stats.ChangedFiles, stats)
	}

	db, err := Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	object, err := db.QueryObject(ctx, "new_role_policy_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.Definitions) != 1 || object.Definitions[0].Source != "current_mod" {
		t.Fatalf("incremental project-role refresh did not publish the new trait: %+v", object)
	}

	for _, want := range []struct {
		name    string
		rank    int
		role    SourceRole
		private int
	}{
		{name: "current_mod", rank: 7, role: SourceRoleProject, private: 1},
		{name: "game", rank: 10, role: SourceRoleGame, private: 0},
	} {
		var gotRank, gotPrivate int
		var gotRole string
		if err := db.sql.QueryRowContext(ctx, `SELECT rank,role,private FROM source_layers WHERE name=?`, want.name).Scan(&gotRank, &gotRole, &gotPrivate); err != nil {
			t.Fatal(err)
		}
		if gotRank != want.rank || gotRole != string(want.role) || gotPrivate != want.private {
			t.Fatalf("source policy for %q = rank=%d role=%q private=%d, want rank=%d role=%q private=%d", want.name, gotRank, gotRole, gotPrivate, want.rank, want.role, want.private)
		}
	}
}

func TestScansRejectSymbolicLinkSources(t *testing.T) {
	t.Run("source root", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "actual-source")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "source-link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symbolic links are unavailable on this platform: %v", err)
		}
		cfg := Config{
			ConfigPath: filepath.Join(dir, "ck3-index.toml"),
			Database:   "cache/test.sqlite",
			Sources:    []Source{{Name: "project", Path: link, Rank: 1, Role: SourceRoleProject}},
		}
		_, err := Scan(context.Background(), cfg)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
			t.Fatalf("Scan() error = %v, want source-root symbolic-link rejection", err)
		}
	})

	t.Run("incremental source file", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		project := filepath.Join(dir, "project")
		rel := "common/traits/linked.txt"
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("safe_linked_trait = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := Config{
			ConfigPath: filepath.Join(dir, "ck3-index.toml"),
			Database:   "cache/test.sqlite",
			Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
		}
		if _, err := Scan(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(dir, "outside.txt")
		if err := os.WriteFile(outside, []byte("outside_trait = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symbolic links are unavailable on this platform: %v", err)
		}
		_, err := ScanFiles(ctx, cfg, []string{rel})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
			t.Fatalf("ScanFiles() error = %v, want source-file symbolic-link rejection", err)
		}
	})

	t.Run("incremental intermediate directory", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		project := filepath.Join(dir, "project")
		rel := "common/traits/linked_directory.txt"
		path := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("safe_directory_trait = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := Config{
			ConfigPath: filepath.Join(dir, "ck3-index.toml"),
			Database:   "cache/test.sqlite",
			Sources:    []Source{{Name: "project", Path: project, Rank: 1, Role: SourceRoleProject}},
		}
		if _, err := Scan(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(dir, "outside-common")
		outsideFile := filepath.Join(outside, "traits", "linked_directory.txt")
		if err := os.MkdirAll(filepath.Dir(outsideFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outsideFile, []byte("outside_directory_trait = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(project, "common")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(project, "common")); err != nil {
			t.Skipf("symbolic links are unavailable on this platform: %v", err)
		}
		_, err := ScanFiles(ctx, cfg, []string{rel})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
			t.Fatalf("ScanFiles() error = %v, want intermediate symbolic-link rejection", err)
		}
	})
}

func TestPublicEvidenceFailsClosedForMissingSource(t *testing.T) {
	result := LLMResult{Evidence: []LLMEvidence{{Kind: "definition"}}}.withPublicFilter(LLMOptions{
		Mode:           "public",
		PrivateSources: map[string]bool{"game": false},
	})
	if len(result.Evidence) != 0 || result.Redacted != 0 {
		t.Fatalf("missing-source evidence escaped public filtering: %+v", result)
	}
}

func TestPublicFilterClearsAggregateAndFollowupSideChannels(t *testing.T) {
	result := LLMResult{
		Intent:           "search",
		Summary:          "private summary",
		Counts:           map[string]int{"private_hits": 3},
		Impact:           map[string]int{"private_refs": 2},
		MissingLocKeys:   []string{"private_key"},
		MissingResources: []string{"gfx/private.dds"},
		ScopeFixHints:    []string{"private scope hint"},
		NextQueries:      []LLMNextQuery{{Tool: "inspect_object", ID: "private_event"}},
		Hotspots:         map[string][]LLMEvidence{"private_event": {{Kind: "definition", Source: "project"}}},
		Evidence:         []LLMEvidence{{Kind: "definition", Source: "game"}, {Kind: "definition", Source: "project"}},
		Redacted:         1,
		Guidance:         []string{"private hint"},
	}.withPublicFilter(LLMOptions{
		Mode:           "public",
		PrivateSources: map[string]bool{"game": false, "project": true},
	})
	if len(result.Evidence) != 1 || result.Evidence[0].Source != "game" {
		t.Fatalf("public evidence filter = %+v", result.Evidence)
	}
	if result.Counts != nil || result.Impact != nil || result.MissingLocKeys != nil || result.MissingResources != nil || result.ScopeFixHints != nil || result.NextQueries != nil || len(result.Hotspots) != 0 || result.Redacted != 0 {
		t.Fatalf("public aggregate side channel survived: %+v", result)
	}
	if result.Summary != "Public visibility returns only evidence with a configured non-private source." || len(result.Guidance) != 1 || !strings.Contains(result.Guidance[0], "aggregate counts") {
		t.Fatalf("public response metadata was not normalized: %+v", result)
	}
}
