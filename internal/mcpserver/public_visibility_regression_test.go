package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestPublicSearchDoesNotLeakPrivateCountsOrNextActions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
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
	write(project, "common/traits/hidden.txt", "hidden_search_target = {}\n")
	write(game, "common/traits/visible.txt", "visible_search_target = {}\n")
	cfg := indexer.Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []indexer.Source{
			{Name: "private-project", Path: project, Rank: 1, Role: indexer.SourceRoleProject, Private: true},
			{Name: "public-game", Path: game, Rank: 2, Role: indexer.SourceRoleGame, Private: false},
		},
	}
	if _, err := indexer.Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result := callToolForTest(t, db, cfg, "ck3_search", map[string]any{
		"query":      "search_target",
		"visibility": "public",
	})
	if result["isError"] == true {
		t.Fatalf("public search failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hidden_search_target") || strings.Contains(string(encoded), "private-project") {
		t.Fatalf("public search leaked private evidence or identifier: %s", encoded)
	}
	if _, exists := body["counts"]; exists {
		t.Fatalf("public search retained aggregate counts: %+v", body)
	}
	if _, exists := body["next_actions"]; exists {
		t.Fatalf("public search retained follow-up actions: %+v", body)
	}
	evidence := body["evidence"].([]any)
	if len(evidence) != 1 || evidence[0].(map[string]any)["source"] != "public-game" {
		t.Fatalf("public search did not retain only public evidence: %+v", body)
	}
}
