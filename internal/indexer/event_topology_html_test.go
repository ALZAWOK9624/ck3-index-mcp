package indexer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRenderEventTopologyHTMLPreservesTopologyAndInteractiveContract(t *testing.T) {
	topology := LLMTopology{
		Center:           "event:chain.a",
		Direction:        "callees",
		IncludeOnActions: true,
		MaxDepth:         3,
		Nodes: []LLMTopologyNode{
			{ID: "event:chain.a", Type: "event", Name: "chain.a", Defined: true, Distance: 0, OutDegree: 2, EventType: "character_event", Title: "chain.a.title", Source: "project", Path: "events/chain.txt", Line: 4},
			{ID: "event:chain.b", Type: "event", Name: "chain.b", Defined: true, Distance: 1, InDegree: 1, OutDegree: 1, EventType: "character_event", Source: "project", Path: "events/chain.txt", Line: 12},
			{ID: "on_action:root_action", Type: "on_action", Name: "root_action", Defined: true, Distance: 1, InDegree: 1, Source: "game", Path: "common/on_action/test.txt", Line: 8},
		},
		Edges: []LLMTopologyEdge{
			{From: "event:chain.a", To: "event:chain.b", Relation: "trigger_event", Phase: "immediate", Confidence: "high", Resolution: "resolved", Source: "project", Path: "events/chain.txt", Line: 7},
			{From: "event:chain.b", To: "event:missing.1", Relation: "trigger_event", Phase: "after", Confidence: "high", Resolution: "unresolved", Reason: "missing_definition", Source: "project", Path: "events/chain.txt", Line: 14},
			{From: "event:chain.a", To: "on_action:root_action", Relation: "fire_on_action", Phase: "effect", Confidence: "medium", Resolution: "resolved", Source: "project", Path: "events/chain.txt", Line: 8},
			// A resolved endpoint can also be absent because a bounded topology did
			// not admit a further node. It must render as a visual-only stub, but
			// not be counted as an unresolved definition.
			{From: "event:chain.a", To: "event:bounded", Relation: "trigger_event", Phase: "immediate", Confidence: "high", Resolution: "resolved"},
		},
		Roots:           []string{"event:chain.a"},
		Leaves:          []string{"on_action:root_action"},
		Cycles:          [][]string{{"event:chain.a", "event:chain.b"}},
		PathsFromCenter: []LLMTopologyPath{{To: "event:chain.b", Nodes: []string{"event:chain.a", "event:chain.b"}}},
		Truncated:       true,
	}
	original := topology

	preview, err := RenderEventTopologyHTML(topology)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(topology, original) {
		t.Fatalf("renderer mutated topology: before=%+v after=%+v", original, topology)
	}
	if preview.SchemaVersion != EventTopologyHTMLSchemaVersion || !preview.Scripts || preview.ScriptPolicy != "fixed-generator-script" || preview.ExternalRequests || !preview.ModelReadable {
		t.Fatalf("unexpected HTML preview metadata: %+v", preview)
	}
	if preview.NodeCount != len(topology.Nodes) || preview.EdgeCount != len(topology.Edges) || preview.ExternalEndpointCount != 2 || preview.UnresolvedEndpointCount != 1 || preview.DynamicEndpointCount != 0 || preview.BoundedEndpointCount != 1 {
		t.Fatalf("external endpoint accounting lost the bounded/unresolved distinction: %+v", preview)
	}
	if preview.Bytes != len(preview.Document) || preview.Bytes == 0 || preview.Bytes > EventTopologyHTMLMaxBytes {
		t.Fatalf("invalid document byte metadata: %+v", preview)
	}
	digest := sha256.Sum256([]byte(preview.Document))
	if preview.SHA256 != strings.ToLower(hexDigest(digest[:])) {
		t.Fatalf("document digest mismatch: got %q", preview.SHA256)
	}
	scriptDigest := sha256.Sum256([]byte(eventTopologyHTMLInspectorScript))
	wantPolicy := "sha256-" + base64.StdEncoding.EncodeToString(scriptDigest[:])
	if preview.ScriptSHA256 != wantPolicy || !strings.Contains(preview.Document, "script-src '"+wantPolicy+"'") {
		t.Fatalf("fixed script CSP hash missing: %+v", preview)
	}
	if strings.Contains(preview.Document, "frame-ancestors") {
		t.Fatalf("meta CSP must not claim frame-ancestors protection")
	}

	for _, required := range []string{
		`id="ck3-topology-search"`, `id="ck3-topology-relation"`, `id="ck3-topology-phase"`, `id="ck3-topology-resolution"`,
		`id="ck3-topology-fit"`, `function selectNode`, `function zoom`, `function fit`, `ck3-topology-badge`,
		`derived external endpoint`, `Edge labels preserve relation, phase, and resolution`,
		`function edgeFilterMatches`, `function filteredGraph`, `function visibleIds(){return filteredGraph().nodes;}`,
		`function edgeMatches(e){return filteredGraph().edges.has(e);}`, `endpoint-bounded`, `bounded continuation`,
	} {
		if !strings.Contains(preview.Document, required) {
			t.Fatalf("interactive graph contract missing %q", required)
		}
	}
	for _, forbidden := range []string{`fetch(`, `XMLHttpRequest`, `WebSocket`, `src="http`, `href="http`, `eval(`, `new Function`} {
		if strings.Contains(preview.Document, forbidden) {
			t.Fatalf("self-contained renderer exposed forbidden capability %q", forbidden)
		}
	}

	payload := extractTopologyHTMLPayload(t, preview.Document)
	var got LLMTopology
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("embedded topology payload is not JSON: %v\n%s", err, payload)
	}
	if !reflect.DeepEqual(got, topology) {
		t.Fatalf("HTML payload drifted from structured topology:\nwant=%+v\n got=%+v", topology, got)
	}
}

func TestEventTopologyExternalEndpointsDistinguishResolutionAndBounds(t *testing.T) {
	truncated := LLMTopology{
		Truncated: true,
		Nodes:     []LLMTopologyNode{{ID: "event:source"}},
		Edges: []LLMTopologyEdge{
			{From: "event:source", To: "event:missing", Resolution: "unresolved"},
			{From: "event:source", To: "event:runtime", Resolution: "dynamic"},
			{From: "event:source", To: "event:bounded", Resolution: "resolved"},
			// One endpoint can appear on more than one returned edge. The
			// strongest explicit status wins instead of letting an earlier
			// resolved bounded edge hide a later unresolved one.
			{From: "event:source", To: "event:mixed", Resolution: "resolved"},
			{From: "event:source", To: "event:mixed", Resolution: "unresolved"},
		},
	}
	counts := eventTopologyExternalEndpointCounts(truncated)
	if want := (eventTopologyExternalEndpointCountsResult{External: 4, Unresolved: 2, Dynamic: 1, Bounded: 1}); counts != want {
		t.Fatalf("unexpected truncated endpoint classifications: got %+v want %+v", counts, want)
	}
	if got := classifyEventTopologyExternalEndpoint("resolved", true); got != eventTopologyEndpointBounded {
		t.Fatalf("truncated resolved endpoint = %q, want bounded", got)
	}
	if got := classifyEventTopologyExternalEndpoint("resolved", false); got != eventTopologyEndpointResolved {
		t.Fatalf("untruncated resolved endpoint = %q, want resolved", got)
	}
	if got := classifyEventTopologyExternalEndpoint("dynamic", true); got != eventTopologyEndpointDynamic {
		t.Fatalf("dynamic endpoint = %q, want dynamic", got)
	}
}

func TestRenderEventTopologyHTMLEarlyRejectsOversizedInput(t *testing.T) {
	topology := LLMTopology{Center: strings.Repeat("<", EventTopologyHTMLMaxInputBytes+1)}
	if _, err := RenderEventTopologyHTML(topology); err == nil || !strings.Contains(err.Error(), "raw string bytes") {
		t.Fatalf("oversized topology should fail before JSON rendering, got %v", err)
	}

	tooManyNodes := LLMTopology{Nodes: make([]LLMTopologyNode, eventTopologyHTMLMaxInputItems+1)}
	if _, err := RenderEventTopologyHTML(tooManyNodes); err == nil || !strings.Contains(err.Error(), "topology items") {
		t.Fatalf("unbounded topology item count should fail before JSON rendering, got %v", err)
	}
}

func TestRenderEventTopologyHTMLEscapesUntrustedTopologyStrings(t *testing.T) {
	malicious := `event:<script>alert("no")</script><img src=x onerror=alert(1)>`
	topology := LLMTopology{
		Center: malicious,
		Nodes: []LLMTopologyNode{{
			ID: malicious, Type: `event"><script>alert(2)</script>`, Name: malicious, Title: malicious,
			Source: malicious, Path: malicious, Defined: true,
		}},
		Edges: []LLMTopologyEdge{{
			From: malicious, To: `event:missing</script><script>alert(3)</script>`, Relation: malicious,
			Phase: malicious, Resolution: "unresolved", Reason: malicious,
		}},
	}
	preview, err := RenderEventTopologyHTML(topology)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview.Document, `<script>alert`) || strings.Contains(preview.Document, `<img src=x`) || strings.Contains(preview.Document, `</script><script>alert`) {
		t.Fatalf("topology strings escaped the inert JSON payload: %s", preview.Document)
	}
	if !strings.Contains(preview.Document, `\u003cscript\u003e`) {
		t.Fatalf("topology JSON did not escape HTML-significant data: %s", preview.Document)
	}
	if strings.Count(preview.Document, `<script`) != 2 {
		t.Fatalf("untrusted data changed executable/inert script element count")
	}
	if !strings.Contains(preview.Document, `connect-src 'none'`) || !strings.Contains(preview.Document, `base-uri 'none'`) {
		t.Fatalf("strict no-network CSP missing")
	}
}

func extractTopologyHTMLPayload(t *testing.T, document string) string {
	t.Helper()
	const open = `<script id="ck3-event-topology-data" type="application/json">`
	start := strings.Index(document, open)
	if start < 0 {
		t.Fatal("missing inert event topology JSON element")
	}
	start += len(open)
	end := strings.Index(document[start:], `</script>`)
	if end < 0 {
		t.Fatal("unterminated inert event topology JSON element")
	}
	return document[start : start+end]
}

func hexDigest(value []byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, part := range value {
		encoded[index*2] = digits[part>>4]
		encoded[index*2+1] = digits[part&0x0f]
	}
	return string(encoded)
}
