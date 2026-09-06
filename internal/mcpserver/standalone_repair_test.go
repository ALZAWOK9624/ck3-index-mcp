package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

func TestStandaloneRepairCycleUsesFinalSubmittedText(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	checker, err := indexer.NewStandaloneChecker(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	serverInput, input := io.Pipe()
	output, serverOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeCheck(ctx, checker, serverInput, serverOutput)
		serverOutput.Close()
	}()
	t.Cleanup(func() {
		input.Close()
		output.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("checker shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("checker did not stop")
		}
	})
	encoder, decoder := json.NewEncoder(input), json.NewDecoder(output)
	id := 0
	call := func(method string, params any) map[string]json.RawMessage {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			ID     int                        `json:"id"`
			Result map[string]json.RawMessage `json:"result"`
			Error  json.RawMessage            `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID != id || len(response.Error) > 0 {
			t.Fatalf("unexpected response: %+v", response)
		}
		return response.Result
	}
	call("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "repair-cycle", "version": "1"}})
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	const virtualPath = "common/scripted_triggers/bot_proposal.txt"
	var hashes []string
	for _, tc := range []struct {
		text string
		pass bool
	}{
		{"proposal = { AND = { add_gold = 5 } }\n", false},
		{"proposal = { is_alive = yes }\n# 检查完成\n", true},
		{"proposal = { is_alive = yes }\n# 检查完成\n}", false},
	} {
		result := call("tools/call", map[string]any{"name": "ck3_check", "arguments": indexer.CheckRequest{Files: []indexer.CheckFile{{Path: virtualPath, Content: tc.text}}}})
		if string(result["content"]) != "[]" || string(result["isError"]) != "false" {
			t.Fatalf("checker must return one structured validation result: %s", result)
		}
		var checked indexer.CheckResult
		if err := json.Unmarshal(result["structuredContent"], &checked); err != nil {
			t.Fatal(err)
		}
		if checked.Passed != tc.pass || checked.DatabaseUsed || len(checked.Files) != 1 {
			t.Fatalf("wrong validation state: %+v", checked)
		}
		file := checked.Files[0]
		wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(tc.text)))
		if file.Path != virtualPath || file.ContentSHA256 != wantHash || file.Coverage.References != "not_checked" || file.Coverage.Runtime != "not_checked" {
			t.Fatalf("receipt does not describe the submitted text/coverage: %+v", file)
		}
		hashes = append(hashes, file.ContentSHA256)
	}
	if hashes[0] == hashes[1] || hashes[1] == hashes[2] {
		t.Fatal("changed text reused a previous check receipt")
	}
}
