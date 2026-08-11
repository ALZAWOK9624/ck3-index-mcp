package savefile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealSaveDocumentWalk explores a real save when one is pointed at.
//
// Skipped by default like the other real-save tests. Set CK3_INDEX_TEST_SAVE
// and CK3_INDEX_TEST_TOKEN_MAPS, and optionally CK3_INDEX_TEST_PATHS to a
// semicolon-separated list of document paths to read. The save is only ever
// opened for reading.
func TestRealSaveDocumentWalk(t *testing.T) {
	savePath := os.Getenv("CK3_INDEX_TEST_SAVE")
	mapDir := os.Getenv("CK3_INDEX_TEST_TOKEN_MAPS")
	if savePath == "" || mapDir == "" {
		t.Skip("set CK3_INDEX_TEST_SAVE and CK3_INDEX_TEST_TOKEN_MAPS to walk a real save")
	}
	source, handle, err := Open(savePath)
	if err != nil {
		t.Fatalf("opening save: %v", err)
	}
	defer handle.Close()

	limits := DefaultLimits()
	envelope, err := Analyze(source, limits)
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	var resolver *TokenMap
	if envelope.Encoding != EncodingText {
		maps, err := LoadTokenMaps(mapDir)
		if err != nil {
			t.Fatalf("loading token maps: %v", err)
		}
		section, err := envelope.Metadata(source, limits)
		if err != nil {
			t.Fatalf("reading metadata: %v", err)
		}
		observed, err := observedIdentifiers(section, limits)
		if err != nil {
			t.Fatalf("inventorying metadata: %v", err)
		}
		if resolver, _, err = SelectTokenMap(maps, observed); err != nil {
			t.Fatalf("selecting token map: %v", err)
		}
	}

	read := func(raw string, depth int) *Document {
		t.Helper()
		path, err := ParseDocumentPath(raw)
		if err != nil {
			t.Fatalf("parsing path %q: %v", raw, err)
		}
		gamestate, err := envelope.GamestateReader(source, limits)
		if err != nil {
			t.Fatalf("opening gamestate: %v", err)
		}
		defer gamestate.Close()
		started := time.Now()
		document, err := ReadDocument(envelope.Encoding, gamestate, resolver, path,
			depth, limits, DefaultDocumentLimits())
		if err != nil {
			t.Fatalf("reading %q: %v", raw, err)
		}
		t.Logf("%-52s kind=%-7s children=%-6d in %s (%d bytes, truncated=%v)",
			"\""+raw+"\"", document.Kind, document.ChildCount,
			time.Since(started).Round(time.Millisecond), document.BytesRead, document.Truncated)
		return document
	}

	root := read("", 0)
	if root.Kind != NodeObject {
		t.Fatalf("the gamestate root is %s, not an object", root.Kind)
	}
	if root.ChildCount == 0 {
		t.Fatal("the gamestate root reported no children")
	}
	names := make([]string, 0, len(root.Children))
	for _, child := range root.Children {
		label := child.Key
		if child.Occurrences > 1 {
			label += "×" + itoa(child.Occurrences)
		}
		names = append(names, label)
	}
	t.Logf("top level: %s", strings.Join(names, " "))

	for _, raw := range strings.Split(os.Getenv("CK3_INDEX_TEST_PATHS"), ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		document := read(raw, 3)
		encoded, err := json.MarshalIndent(document.Value, "", "  ")
		if err != nil {
			t.Fatalf("encoding %q: %v", raw, err)
		}
		if len(encoded) > 3000 {
			encoded = append(encoded[:3000], "\n…"...)
		}
		t.Logf("%s =\n%s", raw, encoded)
	}
}
