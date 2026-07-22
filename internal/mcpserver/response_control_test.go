package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func TestResponseControlIsStrippedBeforeHandlerDecode(t *testing.T) {
	definition, ok := findCanonicalTool("ck3_script_reference")
	if !ok {
		t.Fatal("missing ck3_script_reference")
	}
	raw := json.RawMessage(`{"kind":"shape","id":"has_trait","max_response_bytes":16384}`)
	if err := validateArguments(raw, definition.InputSchema, definition.CompatibilityProperties); err != nil {
		t.Fatalf("shared response control rejected by canonical schema: %v", err)
	}
	clean, control, err := splitResponseControl(raw)
	if err != nil {
		t.Fatal(err)
	}
	if control.MaxResponseBytes != 16384 || strings.Contains(string(clean), "max_response_bytes") {
		t.Fatalf("response control was not normalized and stripped: control=%+v args=%s", control, clean)
	}
	var args ck3ScriptReferenceArgs
	if err := decodeToolArgs(clean, definition.InputSchema, definition.CompatibilityProperties, &args); err != nil {
		t.Fatalf("handler arguments rejected after stripping runtime control: %v", err)
	}
}

func TestResponseBudgetReturnsTypedTooLargeError(t *testing.T) {
	_, err := encodeToolResultWithBudget(map[string]any{"text": strings.Repeat("a", minToolResponseBytes)}, "private", minToolResponseBytes)
	if err == nil {
		t.Fatal("oversized response was accepted")
	}
	var tooLarge *responseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error type = %T, want responseTooLargeError", err)
	}
	toolErr := toolErrorFrom(err)
	if toolErr.Code != ErrorResponseTooLarge || toolErr.Details["max_response_bytes"] != minToolResponseBytes {
		t.Fatalf("typed response error = %+v", toolErr)
	}
}

func TestCancelledCallReturnsStableToolError(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := callMCPTool(ctx, db, indexer.Config{}, json.RawMessage(`{"name":"ck3_health","arguments":{}}`))
	if err != nil {
		t.Fatalf("cancelled tool escaped as protocol error: %v", err)
	}
	result := got.(map[string]any)
	if result["isError"] != true || result["structuredContent"].(map[string]any)["code"] != ErrorOperationCancelled {
		t.Fatalf("cancelled tool result = %+v", result)
	}
}
