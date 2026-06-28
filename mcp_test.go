package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func framedJSON(s string) string {
	return "Content-Length: " + strconv.Itoa(len(s)) + "\r\n\r\n" + s
}

func TestServeMCPIgnoresNotifications(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	var out bytes.Buffer
	msg := framedJSON(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if err := serveMCP(context.Background(), dbPath, strings.NewReader(msg), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification produced a response: %q", out.String())
	}
}

func TestCallMCPToolRejectsBadArguments(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := json.RawMessage(`{"name":"query_object","arguments":"bad"}`)
	if _, err := callMCPTool(context.Background(), db, raw); err == nil {
		t.Fatal("expected invalid arguments error")
	}
	raw = json.RawMessage(`{"name":"query_object","arguments":{}}`)
	if _, err := callMCPTool(context.Background(), db, raw); err == nil {
		t.Fatal("expected missing id error")
	}
}

func TestLookupIteratorFallsBackToOfficialExample(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := json.RawMessage(`{"name":"lookup_iterator","arguments":{"id":"any_courtier"}}`)
	got, err := callMCPTool(context.Background(), db, raw)
	if err != nil {
		t.Fatal(err)
	}
	outer := got.(map[string]any)
	content := outer["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, `"found":true`) || !strings.Contains(text, "any_courtier") || !strings.Contains(text, "guidance") {
		t.Fatalf("expected iterator fallback with guidance, got %s", text)
	}
}

func TestMCPQueryObjectTypesReturnsLLMResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "project", "common", "traits"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project", "common", "traits", "test_traits.txt"), []byte(`test_trait = { desc = test_trait_desc }`), 0644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte(`database = "cache/test.sqlite"
[[source]]
name = "project"
path = "project"
rank = 1
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := json.RawMessage(`{"name":"query_object_types","arguments":{"limit":3}}`)
	got, err := callMCPTool(context.Background(), db, raw)
	if err != nil {
		t.Fatal(err)
	}
	outer := got.(map[string]any)
	content := outer["content"].([]map[string]any)
	text := content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"query_object_types"`) || !strings.Contains(text, `"guidance"`) {
		t.Fatalf("expected LLM object type result, got %s", text)
	}

	raw = json.RawMessage(`{"name":"query_patterns","arguments":{"id":"trait","limit":3}}`)
	got, err = callMCPTool(context.Background(), db, raw)
	if err != nil {
		t.Fatal(err)
	}
	outer = got.(map[string]any)
	content = outer["content"].([]map[string]any)
	text = content[0]["text"].(string)
	if !strings.Contains(text, `"intent":"query_patterns"`) || !strings.Contains(text, `"field_pattern"`) {
		t.Fatalf("expected LLM pattern result, got %s", text)
	}
}
