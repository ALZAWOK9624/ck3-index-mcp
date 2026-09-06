package mcpserver

import (
	"bytes"
	"ck3-index/internal/indexer"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStandaloneMCPNoDatabaseLifecycle(t *testing.T) {
	checker, err := indexer.NewStandaloneChecker(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	requests := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"qqbot-test","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ck3_check_rules","arguments":{"key":"add_gold"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ck3_check","arguments":{"files":[{"path":"common/scripted_triggers/proposal.txt","content":"proposal={AND={add_gold=5}}"}]}}}
`
	var output bytes.Buffer
	if err := ServeCheck(context.Background(), checker, strings.NewReader(requests), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("responses: %s", output.String())
	}
	var result struct {
		Result struct {
			Structured indexer.CheckResult `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result.Structured.Passed || result.Result.Structured.Errors != 1 || result.Result.Structured.DatabaseUsed {
		t.Fatalf("checker failed to catch bad bot script: %s", lines[3])
	}
	if strings.Contains(lines[1], "ck3_refresh") || !strings.Contains(lines[1], "ck3_check_rules") {
		t.Fatalf("unexpected tool surface: %s", lines[1])
	}
}

func TestStandaloneMCPRejectsCallsBeforeInitialize(t *testing.T) {
	checker, _ := indexer.NewStandaloneChecker(context.Background(), "")
	var output bytes.Buffer
	if err := ServeCheck(context.Background(), checker, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"error"`) {
		t.Fatal(output.String())
	}
}

func TestStandaloneMCPRejectsIncompleteCodeRequests(t *testing.T) {
	checker, err := indexer.NewStandaloneChecker(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"files":[{"path":"common/scripted_effects/proposal.txt"}]}`,
		`{"files":[{"path":"common/scripted_effects/proposal.txt","content":null}]}`,
		`{"files":[{"path":"common/scripted_effects/proposal.txt","content":"","read_from_disk":true}]}`,
	} {
		t.Run(args, func(t *testing.T) {
			requests := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"check-input","version":"1"}}}` + "\n" +
				`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ck3_check","arguments":` + args + `}}` + "\n"
			var output bytes.Buffer
			if err := ServeCheck(context.Background(), checker, strings.NewReader(requests), &output); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("responses: %s", output.String())
			}
			var response struct {
				Error *protocolError `json:"error"`
			}
			if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error == nil || response.Error.Code != rpcInvalidParams {
				t.Fatalf("incomplete code was accepted as a checked empty file: %s", lines[1])
			}
		})
	}
}
