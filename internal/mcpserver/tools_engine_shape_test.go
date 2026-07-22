package mcpserver

import (
	"testing"

	"ck3-index/internal/indexer"
)

func TestLookupShapeToolReturnsDocumentedUsageOnly(t *testing.T) {
	raw, err := lookupShapeTool("always")
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(map[string]any)
	if got["found"] != true || got["evidence_kind"] != "documented_usage" {
		t.Fatalf("shape response = %+v", got)
	}
	if _, legacyShape := got["shape"]; legacyShape {
		t.Fatalf("legacy shape category leaked into response: %+v", got)
	}
	docs, ok := got["documentation"].([]indexer.ShapeDocumentation)
	if !ok || len(docs) != 1 || docs[0].Source != "engine_logs/triggers.log" {
		t.Fatalf("current engine documentation missing: %#v", got["documentation"])
	}
}
