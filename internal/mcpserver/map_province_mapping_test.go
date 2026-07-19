package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestMCPMapProvinceMappingIsBoundedAndRespectsVisibility(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	private := callToolForTest(t, db, cfg, "map_province_mapping", map[string]any{
		"source": "project", "target": "active", "limit": 1,
	})
	if private["isError"] == true {
		t.Fatalf("private mapping failed: %+v", private)
	}
	content := private["structuredContent"].(map[string]any)
	if content["intent"] != "map_province_mapping" || int(content["total_groups"].(float64)) != 2 || int(content["total_source_rows"].(float64)) != 2 {
		t.Fatalf("unexpected mapping summary: %+v", content)
	}
	if len(content["groups"].([]any)) != 1 || len(content["sources"].([]any)) != 1 {
		t.Fatalf("MCP mapping did not honor limit: %+v", content)
	}

	public := callToolForTest(t, db, cfg, "map_province_mapping", map[string]any{
		"source": "project", "target": "active", "visibility": "public",
	})
	if public["isError"] != true {
		t.Fatalf("public mapping should not access rank-1 project source: %+v", public)
	}
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), filepath.ToSlash(dir)) || strings.Contains(string(raw), dir) {
		t.Fatalf("public error leaked fixture path: %s", raw)
	}
}
