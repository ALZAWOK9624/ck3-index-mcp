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

func TestMCPMigratedObjectCompareEvidenceAndInteractiveEventChain(t *testing.T) {
	dir := t.TempDir()
	cfg := writeMCPMapFixture(t, dir)
	write := func(source, rel, content string) {
		t.Helper()
		path := filepath.Join(dir, source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("project", "common/traits/project_traits.txt", `mcp_compare_trait = { icon = icon_project description = project_desc }`)
	write("base", "common/traits/upstream_traits.txt", `mcp_compare_trait = { icon = icon_base description = base_desc }`)
	write("project", "events/chain.txt", `chain.a = {
	type = character_event
	immediate = { trigger_event = chain.b trigger_event = missing.1 }
}
chain.b = { type = character_event immediate = { trigger_event = chain.c } }
chain.c = { type = province_event immediate = { trigger_event = chain.b } }
`)
	// Keep a rank-three on_action directory in the indexed fixture so the
	// omitted MCP limit exercises the tool's documented default rather than a
	// direct-audit fallback.
	write("target", "common/on_action/mcp_documented.txt", `# Root = character
mcp_documented = { }
`)
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	compare := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{
		"id": "trait:mcp_compare_trait", "operation": "compare",
	})
	if compare["isError"] == true {
		t.Fatalf("private object compare failed: %+v", compare)
	}
	compareContent := compare["structuredContent"].(map[string]any)
	if compareContent["intent"] != "object_upstream_compare" || compareContent["status"] != "matched" {
		t.Fatalf("object compare did not return its bounded matched contract: %+v", compareContent)
	}
	if compareContent["ast"].(map[string]any)["equal"] != false || len(compareContent["field_changes"].([]any)) == 0 {
		t.Fatalf("object compare omitted canonical AST or field delta: %+v", compareContent)
	}
	limitedCompare := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{
		"id": "trait:mcp_compare_trait", "operation": "compare", "limit": 1,
	})
	if limitedCompare["isError"] == true {
		t.Fatalf("limited object compare failed: %+v", limitedCompare)
	}
	limitedContent := limitedCompare["structuredContent"].(map[string]any)
	if changes := limitedContent["field_changes"].([]any); len(changes) != 1 || limitedContent["field_changes_truncated"] != true {
		t.Fatalf("MCP object compare did not honor limit=1: %+v", limitedContent)
	}

	publicCompare := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{
		"id": "trait:mcp_compare_trait", "operation": "compare", "visibility": "public",
	})
	if publicCompare["isError"] != true {
		t.Fatalf("public object compare silently defaulted to project evidence: %+v", publicCompare)
	}
	publicJSON, err := json.Marshal(publicCompare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), filepath.ToSlash(dir)) || strings.Contains(string(publicJSON), dir) {
		t.Fatalf("public compare error leaked fixture path: %s", publicJSON)
	}
	publicUpstreamCompare := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{
		"id": "trait:mcp_compare_trait", "operation": "compare", "visibility": "public", "source": "base",
	})
	if publicUpstreamCompare["isError"] == true {
		t.Fatalf("public non-project object compare failed: %+v", publicUpstreamCompare)
	}
	publicUpstreamContent := publicUpstreamCompare["structuredContent"].(map[string]any)
	if publicUpstreamContent["source"] != "base" || publicUpstreamContent["status"] != "source_only" {
		t.Fatalf("public object compare did not stay on non-project layers: %+v", publicUpstreamContent)
	}

	htmlChain := callToolForTest(t, db, cfg, "ck3_dependencies", map[string]any{
		"id": "event:chain.a", "operation": "event_chain", "direction": "callees", "depth": 3, "format": "html",
	})
	if htmlChain["isError"] == true {
		t.Fatalf("event-chain HTML failed: %+v", htmlChain)
	}
	htmlContent := htmlChain["structuredContent"].(map[string]any)
	if htmlContent["format"] != "html" || htmlContent["topology"] == nil {
		t.Fatalf("event-chain HTML omitted structured topology: %+v", htmlContent)
	}
	preview := htmlContent["html"].(map[string]any)
	document, _ := preview["document"].(string)
	if preview["external_requests"] != false || preview["scripts"] != true || !strings.Contains(document, `id="ck3-event-topology-inspector"`) || !strings.Contains(document, `event:chain.a`) || !strings.Contains(document, `event:missing.1`) {
		t.Fatalf("event-chain HTML artifact lacks its no-network interactive contract: %+v", preview)
	}

	invalidFormat := callToolForTest(t, db, cfg, "ck3_dependencies", map[string]any{
		"id": "event:chain.a", "operation": "neighborhood", "format": "html",
	})
	if invalidFormat["isError"] != true {
		t.Fatalf("neighborhood accepted the event-chain-only HTML format: %+v", invalidFormat)
	}
	tooLongID := callToolForTest(t, db, cfg, "ck3_dependencies", map[string]any{
		"id": strings.Repeat("x", 513), "operation": "event_chain",
	})
	if tooLongID["isError"] != true {
		t.Fatalf("event-chain accepted an unbounded id: %+v", tooLongID)
	}

	evidence := callToolForTest(t, db, cfg, "ck3_workspace", map[string]any{
		"operation": "on_action_evidence", "limit": 3,
	})
	if evidence["isError"] == true {
		t.Fatalf("unified on_action evidence audit failed: %+v", evidence)
	}
	evidenceContent := evidence["structuredContent"].(map[string]any)
	if evidenceContent["intent"] != "on_action_unified_evidence_audit" || evidenceContent["findings"] == nil {
		t.Fatalf("unified on_action evidence audit contract is incomplete: %+v", evidenceContent)
	}
	defaultEvidence := callToolForTest(t, db, cfg, "ck3_workspace", map[string]any{
		"operation": "on_action_evidence",
	})
	if defaultEvidence["isError"] == true {
		t.Fatalf("default-limited on_action evidence audit failed: %+v", defaultEvidence)
	}
	defaultEvidenceContent := defaultEvidence["structuredContent"].(map[string]any)
	if findings := defaultEvidenceContent["findings"].([]any); len(findings) != defaultMCPBoundedResultLimit || defaultEvidenceContent["truncated"] != true {
		t.Fatalf("MCP on_action evidence did not apply its documented default limit: %+v", defaultEvidenceContent)
	}
}
