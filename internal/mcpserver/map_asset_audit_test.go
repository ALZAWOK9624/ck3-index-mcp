package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestMCPMapAssetAuditReturnsProvenanceAndRespectsVisibility(t *testing.T) {
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

	private := callToolForTest(t, db, cfg, "map_asset_audit", map[string]any{"operation": "summary", "limit": 4})
	privateText := private["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(privateText, `"intent":"map_asset_audit"`) || !strings.Contains(privateText, `"commit":"5c41484`) || !strings.Contains(privateText, `"source":"project"`) {
		t.Fatalf("private map audit lacks active assets or absorption provenance: %s", privateText)
	}

	public := callToolForTest(t, db, cfg, "map_asset_audit", map[string]any{"operation": "summary", "visibility": "public", "limit": 4})
	publicText := public["content"].([]map[string]any)[0]["text"].(string)
	if strings.Contains(publicText, `"source":"project"`) || !strings.Contains(publicText, `"source":"base"`) {
		t.Fatalf("public map audit leaked rank=1 project assets: %s", publicText)
	}
}
