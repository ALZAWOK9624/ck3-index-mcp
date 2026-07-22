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

func TestPublicVisibilityRejectsPrivateEvidenceOperations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "mcp-private-project-root")
	game := filepath.Join(dir, "mcp-private-game-root")
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
	write(project, "common/traits/private_mcp.txt", "private_mcp_identifier = { marker = MCP_PRIVATE_CONTENT_SENTINEL }\n")
	write(game, "common/on_action/private_mcp.txt", "# MCP_PRIVATE_CONTENT_SENTINEL\nprivate_mcp_hook = { }\n")
	cfg := indexer.Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []indexer.Source{
			{Name: "private-project", Path: project, Rank: 1, Role: indexer.SourceRoleProject, Private: true},
			{Name: "private-game", Path: game, Rank: 2, Role: indexer.SourceRoleGame, Private: true},
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

	patch := []map[string]any{{
		"path":    "common/traits/public_probe.txt",
		"content": "public_probe = { target = private_mcp_identifier marker = MCP_PRIVATE_CONTENT_SENTINEL }\n",
	}}
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "review_patch", tool: "ck3_review", args: map[string]any{"visibility": "public", "files": patch}},
		{name: "review_dirty", tool: "ck3_review", args: map[string]any{"visibility": "public"}},
		{name: "preflight_subject", tool: "ck3_preflight", args: map[string]any{"visibility": "public", "operation": "subject", "id": "private_mcp_identifier"}},
		{name: "preflight_patch", tool: "ck3_preflight", args: map[string]any{"visibility": "public", "operation": "patch", "files": patch}},
		{name: "preflight_dirty", tool: "ck3_preflight", args: map[string]any{"visibility": "public", "operation": "dirty"}},
		{name: "impact", tool: "ck3_impact", args: map[string]any{"visibility": "public", "files": patch}},
		{name: "compare", tool: "ck3_inspect", args: map[string]any{"visibility": "public", "operation": "compare", "id": "trait:private_mcp_identifier", "source": "private-game"}},
		{name: "on_action_evidence", tool: "ck3_workspace", args: map[string]any{"visibility": "public", "operation": "on_action_evidence"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := callToolForTest(t, db, cfg, tc.tool, tc.args)
			if result["isError"] != true {
				t.Fatalf("public %s unexpectedly returned private-evidence data: %+v", tc.tool, result)
			}
			body := result["structuredContent"].(map[string]any)
			if body["code"] != ErrorInvalidArguments || body["category"] != "privacy" || body["field"] != "visibility" {
				t.Fatalf("public %s error contract = %+v, want INVALID_ARGUMENTS/privacy/visibility", tc.tool, body)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"private_mcp_identifier", "MCP_PRIVATE_CONTENT_SENTINEL", filepath.Base(project), filepath.Base(game)} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("public %s error leaked private fixture data %q: %s", tc.tool, forbidden, encoded)
				}
			}
		})
	}
}
