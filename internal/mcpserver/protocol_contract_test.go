package mcpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
	"ck3-index/internal/packager"
)

func TestServeMCPProtocolContract(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ck3_inspect","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"not_a_tool","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(requests), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 5 {
		t.Fatalf("response count = %d, want 5: %s", len(responses), out.String())
	}

	initialize := responseByID(t, responses, "1")["result"].(map[string]any)
	if initialize["protocolVersion"] != latestMCPProtocolVersion {
		t.Fatalf("negotiated protocol version = %v, want %s", initialize["protocolVersion"], latestMCPProtocolVersion)
	}
	serverInfo := initialize["serverInfo"].(map[string]any)
	if serverInfo["version"] != buildinfo.Version {
		t.Fatalf("server version = %v, want buildinfo.Version %q", serverInfo["version"], buildinfo.Version)
	}
	capabilities := initialize["capabilities"].(map[string]any)
	toolCapabilities := capabilities["tools"].(map[string]any)
	if toolCapabilities["listChanged"] != false {
		t.Fatalf("static registry must advertise listChanged=false: %+v", toolCapabilities)
	}
	ping := responseByID(t, responses, "2")
	if _, ok := ping["result"].(map[string]any); !ok {
		t.Fatalf("ping did not return an empty object: %+v", ping)
	}
	listed := responseByID(t, responses, "3")["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 30 {
		t.Fatalf("standard tools/list count = %d, want 30", len(listed))
	}
	first := listed[0].(map[string]any)
	for _, field := range []string{"title", "description", "inputSchema", "outputSchema", "annotations"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("advertised tool lacks %s: %+v", field, first)
		}
	}

	argumentResponse := responseByID(t, responses, "4")
	argumentError := argumentResponse["result"].(map[string]any)
	if argumentError["isError"] != true || argumentResponse["error"] != nil {
		t.Fatalf("known-tool argument failure must be CallToolResult isError=true: %+v", argumentResponse)
	}
	if _, ok := argumentError["structuredContent"].(map[string]any); !ok {
		t.Fatalf("tool error lacks structuredContent: %+v", argumentError)
	}
	unknownError := responseByID(t, responses, "5")["error"].(map[string]any)
	if int(unknownError["code"].(float64)) != rpcInvalidParams {
		t.Fatalf("unknown tool error code = %v, want %d", unknownError["code"], rpcInvalidParams)
	}
}

func TestHealthReportIncludesBinaryVersion(t *testing.T) {
	report := mcpHealthReport(indexer.HealthReport{})
	if report["binary_version"] != buildinfo.Version {
		t.Fatalf("health binary_version = %v, want %q", report["binary_version"], buildinfo.Version)
	}
}

func TestServeMCPBootstrapsMissingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cache", "new.sqlite")
	var out bytes.Buffer
	if err := Serve(
		context.Background(),
		emptyMCPConfig(dbPath),
		dbPath,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`+"\n"),
		&out,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("missing database was not bootstrapped: %v", err)
	}
	db, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err := db.IndexState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "initializing" || state.Ready() {
		t.Fatalf("bootstrapped index state = %+v, want initializing", state)
	}
}

func TestServeMCPNegotiatesSupportedAndUnknownProtocolVersions(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(requests), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if got := responses[0]["result"].(map[string]any)["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("supported client version negotiation = %v", got)
	}
	if code := int(responses[1]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
		t.Fatalf("duplicate initialize error = %d, want %d", code, rpcInvalidRequest)
	}
	if code := int(responses[2]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
		t.Fatalf("duplicate initialize takes precedence over malformed duplicate params: %d, want %d", code, rpcInvalidRequest)
	}
}

func TestServeMCPInitializeRequiresCapabilitiesAndClientInfo(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{},"clientInfo":{"name":"strict-test","version":"1"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 4 {
		t.Fatalf("response count = %d, want 4: %s", len(responses), out.String())
	}
	for index := 0; index < 3; index++ {
		if code := int(responses[index]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidParams {
			t.Fatalf("initialize response %d code = %d, want %d", index+1, code, rpcInvalidParams)
		}
	}
	if got := responses[3]["result"].(map[string]any)["protocolVersion"]; got != latestMCPProtocolVersion {
		t.Fatalf("unknown version negotiation = %v, want %s", got, latestMCPProtocolVersion)
	}
}

func TestServeMCPRejectsReusedNormalizedRequestIDs(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`,
		`{"jsonrpc":"2.0","id":"\u0061","method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 4 {
		t.Fatalf("response count = %d, want 4: %s", len(responses), out.String())
	}
	for _, index := range []int{1, 3} {
		if code := int(responses[index]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
			t.Fatalf("duplicate response %d = %+v", index, responses[index])
		}
	}
}

func TestServeMCPRequiresInitializedNotificationBeforeTools(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 4 {
		t.Fatalf("response count = %d, want 4: %s", len(responses), out.String())
	}
	for _, index := range []int{0, 2} {
		if code := int(responses[index]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
			t.Fatalf("pre-initialized tools/list response = %+v, want invalid request", responses[index])
		}
	}
	if _, ok := responses[1]["result"].(map[string]any); !ok {
		t.Fatalf("initialize did not succeed: %+v", responses[1])
	}
	if tools, ok := responses[3]["result"].(map[string]any)["tools"].([]any); !ok || len(tools) == 0 {
		t.Fatalf("tools/list after initialized notification failed: %+v", responses[3])
	}
}

func TestServeMCPCancellationNotificationReachesActiveToolWithoutBlockingReader(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"contract-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"ck3_health","arguments":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
	}, "\n") + "\n"
	caller := func(ctx context.Context, _ *indexer.DB, _ indexer.Config, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return map[string]any{"cancelled": true}, nil
	}
	var out bytes.Buffer
	if err := serveWithToolCaller(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out, caller); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want initialize and ping only after cancellation: %s", len(responses), out.String())
	}
	if _, ok := responses[1]["result"].(map[string]any); !ok {
		t.Fatalf("ping was not read while tool request was active: %+v", responses[1])
	}
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}

func TestFastResponsesAreNotBlockedByEarlierSlowTool(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"ordering-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"ck3_health","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"ping","method":"ping","params":{}}`,
		`{"jsonrpc":"2.0","id":"fast","method":"tools/call","params":{"name":"ck3_search","arguments":{"query":"x"}}}`,
	}, "\n") + "\n"
	slowStarted := make(chan struct{})
	fastFinished := make(chan struct{})
	releaseSlow := make(chan struct{})
	caller := func(_ context.Context, _ *indexer.DB, _ indexer.Config, raw json.RawMessage) (any, error) {
		var call callToolParams
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		if call.Name == "ck3_health" {
			close(slowStarted)
			<-releaseSlow
			return map[string]any{"kind": "slow"}, nil
		}
		close(fastFinished)
		return map[string]any{"kind": "fast"}, nil
	}
	var out synchronizedBuffer
	done := make(chan error, 1)
	go func() {
		done <- serveWithToolCaller(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out, caller)
	}()
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow tool did not start")
	}
	select {
	case <-fastFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("fast tool did not finish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current := out.String()
		if strings.Contains(current, `"id":"ping"`) && strings.Contains(current, `"id":"fast"`) {
			if strings.Contains(current, `"id":"slow"`) {
				t.Fatalf("slow response was written before release: %s", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fast responses remained blocked behind slow request: %s", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(releaseSlow)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish after releasing slow tool")
	}
	responses := decodeResponseLines(t, out.String())
	ids := make([]string, 0, len(responses))
	for _, response := range responses {
		ids = append(ids, fmt.Sprint(response["id"]))
	}
	if strings.Join(ids, ",") != "1,ping,fast,slow" {
		t.Fatalf("response order = %v, want initialize,ping,fast,slow", ids)
	}
}

func TestToolConcurrencyIsBounded(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		requests   int
		wantActive int32
		wantBusy   int
	}{
		{name: "global", tool: "ck3_search", requests: maxMCPTasks + 3, wantActive: maxMCPTasks, wantBusy: 3},
		{name: "heavy", tool: "map_render", requests: maxMCPHeavyTasks + 2, wantActive: maxMCPHeavyTasks, wantBusy: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := createProtocolTestDB(t)
			lines := []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"limit-test","version":"1"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
			}
			for index := 0; index < tt.requests; index++ {
				lines = append(lines, fmt.Sprintf(
					`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`,
					index+2,
					tt.tool,
				))
			}
			input := strings.Join(lines, "\n") + "\n"
			release := make(chan struct{})
			var active atomic.Int32
			var maximum atomic.Int32
			caller := func(_ context.Context, _ *indexer.DB, _ indexer.Config, _ json.RawMessage) (any, error) {
				current := active.Add(1)
				for {
					seen := maximum.Load()
					if current <= seen || maximum.CompareAndSwap(seen, current) {
						break
					}
				}
				<-release
				active.Add(-1)
				return map[string]any{"ok": true}, nil
			}
			var out synchronizedBuffer
			done := make(chan error, 1)
			go func() {
				done <- serveWithToolCaller(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out, caller)
			}()
			deadline := time.Now().Add(2 * time.Second)
			for maximum.Load() < tt.wantActive && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if got := maximum.Load(); got != tt.wantActive {
				close(release)
				<-done
				t.Fatalf("maximum active tools = %d, want %d", got, tt.wantActive)
			}
			// Keep admitted calls blocked until the reader has submitted every
			// request and the limiter has emitted all deterministic rejections.
			// Under -race, releasing as soon as maximum reaches the cap can let
			// one worker finish before the final buffered request is consumed.
			busyDeadline := time.Now().Add(2 * time.Second)
			for strings.Count(out.String(), `"code":"SERVER_BUSY"`) < tt.wantBusy && time.Now().Before(busyDeadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if got := strings.Count(out.String(), `"code":"SERVER_BUSY"`); got != tt.wantBusy {
				close(release)
				<-done
				t.Fatalf("SERVER_BUSY responses before release = %d, want %d: %s", got, tt.wantBusy, out.String())
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not finish bounded tasks")
			}
			responses := decodeResponseLines(t, out.String())
			busy := 0
			for _, response := range responses {
				result, _ := response["result"].(map[string]any)
				structured, _ := result["structuredContent"].(map[string]any)
				if structured["code"] == ErrorServerBusy {
					busy++
				}
			}
			if busy != tt.wantBusy {
				t.Fatalf("SERVER_BUSY responses = %d, want %d: %s", busy, tt.wantBusy, out.String())
			}
		})
	}
}

func TestServeMCPValidJSONWrongShapeIsInvalidRequest(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := "[]\n" + `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want 2", len(responses))
	}
	if code := int(responses[0]["error"].(map[string]any)["code"].(float64)); code != rpcInvalidRequest {
		t.Fatalf("valid JSON wrong-shape error = %d, want %d", code, rpcInvalidRequest)
	}
}

func TestServeMCPRejectsInvalidRequestIDTypes(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":{"bad":1},"method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1e3,"method":"ping"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 5 {
		t.Fatalf("response count = %d, want 5", len(responses))
	}
	for _, response := range responses {
		if response["id"] != nil || int(response["error"].(map[string]any)["code"].(float64)) != rpcInvalidRequest {
			t.Fatalf("invalid id response = %+v", response)
		}
	}
}

func TestServeMCPParseErrorThenContinues(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := "{broken json\n" + `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want parse error plus ping", len(responses))
	}
	parseError := responses[0]["error"].(map[string]any)
	if int(parseError["code"].(float64)) != rpcParseError {
		t.Fatalf("parse error code = %v, want %d", parseError["code"], rpcParseError)
	}
	if _, ok := responses[1]["result"].(map[string]any); !ok {
		t.Fatalf("server did not continue after complete malformed line: %+v", responses[1])
	}
}

func TestServeMCPRejectsOversizedFramedRequest(t *testing.T) {
	dbPath := createProtocolTestDB(t)
	input := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMCPMessageBytes+1)
	var out bytes.Buffer
	if err := Serve(context.Background(), emptyMCPConfig(dbPath), dbPath, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	responses := decodeResponseLines(t, out.String())
	if len(responses) != 1 {
		t.Fatalf("response count = %d, want 1", len(responses))
	}
	rpcErr := responses[0]["error"].(map[string]any)
	if int(rpcErr["code"].(float64)) != rpcMessageTooLarge {
		t.Fatalf("oversized request error code = %v, want %d", rpcErr["code"], rpcMessageTooLarge)
	}
}

func TestMCPEnvelopeCanCarryMaximumDecodedPackage(t *testing.T) {
	base64Bytes := ((packager.MCPLimits.MaxTotalBytes + 2) / 3) * 4
	if int64(maxMCPMessageBytes) <= base64Bytes {
		t.Fatalf("MCP envelope limit %d cannot carry %d bytes of Base64 package data", maxMCPMessageBytes, base64Bytes)
	}
}

func TestCanonicalSchemaFixesAndPublicMapRedaction(t *testing.T) {
	for _, name := range []string{"map_build_metric", "map_render"} {
		definition, ok := findCanonicalTool(name)
		if !ok {
			t.Fatalf("missing canonical tool %s", name)
		}
		properties := definition.InputSchema["properties"].(map[string]any)
		levels := schemaStrings(properties["level"].(map[string]any)["enum"])
		if !containsString(levels, "region") {
			t.Fatalf("%s level schema omits region: %v", name, levels)
		}
	}
	assignment, _ := findCanonicalTool("map_assignment_plan")
	properties := assignment.InputSchema["properties"].(map[string]any)
	if _, exists := properties["mode"]; exists {
		t.Fatal("canonical map_assignment_plan still exposes ambiguous mode")
	}
	if required := schemaStrings(assignment.InputSchema["required"]); len(required) != 1 || required[0] != "target" {
		t.Fatalf("map_assignment_plan required fields = %v, want [target]", required)
	}

	private := indexer.MapAssignmentPlanResult{PatchFiles: []indexer.PatchFileInput{{Path: "history/provinces/private.txt", Content: "secret"}}}
	redacted := redactToolValue(private, "public").(indexer.MapAssignmentPlanResult)
	if len(redacted.PatchFiles) != 0 {
		t.Fatalf("public map assignment retained patch files: %+v", redacted.PatchFiles)
	}
}

func TestToolErrorsDoNotEchoPatchContent(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := "DO_NOT_ECHO_THIS_PATCH_BODY"
	result := callToolForTest(t, db, indexer.Config{}, "ck3_review", map[string]any{
		"files":   []map[string]any{{"path": "common/traits/test.txt", "content": secret}},
		"unknown": secret,
	})
	if result["isError"] != true {
		t.Fatalf("unknown field did not return tool error: %+v", result)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), secret) {
		t.Fatalf("tool error echoed patch content: %s", data)
	}
}

func TestMapRenderAnyOfRejectsNullAlternatives(t *testing.T) {
	definition, _ := findCanonicalTool("map_render")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"recipe":null}`),
		json.RawMessage(`{"layers":null}`),
	} {
		if err := validateArguments(raw, definition.InputSchema, definition.CompatibilityProperties); err == nil {
			t.Fatalf("map_render accepted missing/null recipe and layers: %s", raw)
		}
	}
}

func TestCanonicalSchemasRejectExplicitNullForTypedFields(t *testing.T) {
	definition, _ := findCanonicalTool("ck3_search")
	if err := validateArguments(json.RawMessage(`{"query":"trait","limit":null}`), definition.InputSchema, definition.CompatibilityProperties); err == nil {
		t.Fatal("typed optional field accepted explicit null")
	}
}

func TestNestedMapSchemaBoundsAreEnforced(t *testing.T) {
	metric, _ := findCanonicalTool("map_build_metric")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"transform":{"rounds":17}}`),
		json.RawMessage(`{"components":[{"field":"terrain","unknown":1}]}`),
	} {
		if err := validateArguments(raw, metric.InputSchema, metric.CompatibilityProperties); err == nil {
			t.Fatalf("map_build_metric accepted unsafe nested input: %s", raw)
		}
	}
	render, _ := findCanonicalTool("map_render")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"layers":[{"type":"borders","line_width":65}]}`),
		json.RawMessage(`{"layers":[{"type":"fill","metric":{"limit":20}}]}`),
	} {
		if err := validateArguments(raw, render.InputSchema, render.CompatibilityProperties); err == nil {
			t.Fatalf("map_render accepted unsafe nested input: %s", raw)
		}
	}
}

func TestToolErrorsRedactConfiguredPathsCaseInsensitively(t *testing.T) {
	t.Setenv("CK3_INDEX_MAP_FONT", `C:\Private\Fonts\Map.ttf`)
	runtime := &Runtime{
		DBPath: `C:\Private\Cache\Index.sqlite`,
		Config: indexer.Config{
			ConfigPath: `C:\Private\Config\ck3-index.toml`,
			Sources:    []indexer.Source{{Path: `C:\Private\Project`}},
		},
	}
	message := sanitizeToolError(errors.New(`failed at c:/private/cache/index.sqlite, C:\PRIVATE\PROJECT and c:\private\fonts\map.ttf`), runtime)
	if strings.Contains(strings.ToLower(message), "private") || strings.Count(message, "<redacted-path>") != 3 {
		t.Fatalf("configured paths were not redacted: %s", message)
	}
}

func TestNextActionsUseCanonicalToolsAndExplicitArguments(t *testing.T) {
	result, err := encodeToolResult(indexer.LLMResult{
		Intent:  "fixture",
		Summary: "fixture",
		NextQueries: []indexer.LLMNextQuery{
			{Tool: "ck3_search", ID: "trait", Reason: "fixture"},
			{Tool: "ck3_inspect", ID: "event:fixture.1", Reason: "fixture"},
			{Tool: "map_building_candidates", ID: "k_example", Reason: "fixture"},
		},
	}, "private")
	if err != nil {
		t.Fatal(err)
	}
	structured := result["structuredContent"].(map[string]any)
	if _, legacy := structured["next_queries"]; legacy {
		t.Fatalf("result retained legacy next_queries: %+v", structured)
	}
	var actions []map[string]any
	encodedActions, err := json.Marshal(structured["next_actions"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedActions, &actions); err != nil {
		t.Fatal(err)
	}
	first := actions[0]
	if first["tool"] != "ck3_search" {
		t.Fatalf("first canonical next action is incorrect: %+v", first)
	}
	firstArgs := first["arguments"].(map[string]any)
	if firstArgs["query"] != "trait" {
		t.Fatalf("canonical search arguments are incomplete: %+v", firstArgs)
	}
	if _, legacyID := first["id"]; legacyID {
		t.Fatalf("canonical next action retained legacy top-level id: %+v", first)
	}
	if first["priority"] != "normal" || first["confidence"] != "medium" {
		t.Fatalf("next action omitted stable planning defaults: %+v", first)
	}
	second := actions[1]
	secondArgs := second["arguments"].(map[string]any)
	if second["tool"] != "ck3_inspect" || secondArgs["id"] != "event:fixture.1" {
		t.Fatalf("canonical inspect arguments are incomplete: %+v", second)
	}
	third := actions[2]
	thirdArgs := third["arguments"].(map[string]any)
	if third["tool"] != "map_building_candidates" || thirdArgs["target"] != "k_example" {
		t.Fatalf("canonical target arguments are incomplete: %+v", third)
	}
	if _, legacyID := third["id"]; legacyID {
		t.Fatalf("canonical target action retained legacy top-level id: %+v", third)
	}
	for _, action := range actions {
		definition, found := findCanonicalTool(action["tool"].(string))
		if !found {
			t.Fatalf("next action references a non-canonical tool: %+v", action)
		}
		arguments, err := json.Marshal(action["arguments"])
		if err != nil {
			t.Fatal(err)
		}
		if err := validateArguments(arguments, definition.InputSchema, definition.CompatibilityProperties); err != nil {
			t.Fatalf("next action arguments do not satisfy %s schema: %v; action=%+v", definition.Name, err, action)
		}
	}
}

func TestNextActionsDropArgumentsOutsideCanonicalSchema(t *testing.T) {
	result, err := encodeToolResult(map[string]any{
		"next_queries": []map[string]any{
			{"tool": "ck3_inspect", "id": "valid_trait", "arguments": map[string]any{"bogus": true}},
			{"tool": "ck3_inspect", "id": "valid_trait"},
		},
	}, "private")
	if err != nil {
		t.Fatal(err)
	}
	structured := result["structuredContent"].(map[string]any)
	encodedActions, err := json.Marshal(structured["next_actions"])
	if err != nil {
		t.Fatal(err)
	}
	var actions []map[string]any
	if err := json.Unmarshal(encodedActions, &actions); err != nil || len(actions) != 1 {
		t.Fatalf("invalid next action was not dropped: %+v", structured)
	}
	action := actions[0]
	if action["tool"] != "ck3_inspect" || action["arguments"].(map[string]any)["id"] != "valid_trait" {
		t.Fatalf("valid next action did not survive filtering: %+v", action)
	}
}

func TestNextActionsRewriteDeprecatedHistoryYear(t *testing.T) {
	result, err := encodeToolResult(map[string]any{
		"next_queries": []map[string]any{{
			"tool":      "map_render",
			"arguments": map[string]any{"history_year": 6254, "recipe": "political_atlas"},
		}},
	}, "private")
	if err != nil {
		t.Fatal(err)
	}
	structured := result["structuredContent"].(map[string]any)
	if _, legacy := structured["next_queries"]; legacy {
		t.Fatalf("result retained legacy next_queries: %+v", structured)
	}
	var actions []map[string]any
	encodedActions, err := json.Marshal(structured["next_actions"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedActions, &actions); err != nil {
		t.Fatal(err)
	}
	action := actions[0]
	arguments := action["arguments"].(map[string]any)
	if arguments["year"] != float64(6254) {
		t.Fatalf("next action year = %v, want 6254", arguments["year"])
	}
	if _, exists := arguments["history_year"]; exists {
		t.Fatalf("next action retained deprecated history_year: %+v", arguments)
	}
}

func TestToolResultEncodingRejectsNonFiniteJSON(t *testing.T) {
	if _, err := encodeToolResult(map[string]any{"value": math.NaN()}, "private"); err == nil {
		t.Fatal("non-finite tool result was silently encoded as a successful response")
	}
}

func TestCallToolReportsUnavailableIndexState(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := callToolForTest(t, db, indexer.Config{}, "ck3_script_reference", map[string]any{"kind": "shape", "id": "has_trait"})
	if result["isError"] == true {
		t.Fatalf("static script reference should remain usable without index state: %+v", result)
	}
	state := result["indexState"].(map[string]any)
	if state["status"] != "unavailable" || state["error_code"] != ErrorIndexStale {
		t.Fatalf("unavailable index state was not propagated: %+v", state)
	}
}

func TestScriptReferencePrefersPublishedLiveOnActionEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	game := t.TempDir()
	onActionDir := filepath.Join(game, "common", "on_action")
	if err := os.MkdirAll(onActionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onActionDir, "fixture.txt"), []byte(`# Root = character
# scope:reason = flag:MCP_DOC_SENTINEL
live_on_action_fixture = { }
`), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','ready'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('live_on_action_fixture','on_action','none','','fixture','C:\\private\\logs\\on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col)
		VALUES('on_action','live_on_action_fixture','',0,0,'vanilla',3,'common/on_action/fixture.txt',3,1)`); err != nil {
		t.Fatal(err)
	}
	cfg := indexer.Config{Sources: []indexer.Source{{Name: "vanilla", Path: game, Rank: 3}}}
	result := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "live_on_action_fixture"})
	if result["isError"] == true {
		t.Fatalf("live on_action reference failed: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["found"] != true || structured["rule_source"] != "engine_logs" || structured["confidence"] != "high" {
		t.Fatalf("live on_action did not take precedence: %+v", structured)
	}
	rules := structured["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["input_scopes"].([]any)[0] != "none" || rules[0].(map[string]any)["rule_source"] != "engine_logs/on_actions.log" {
		t.Fatalf("none scope was not preserved in MCP response: %+v", structured)
	}
	documentation := structured["documentation_contract"].(map[string]any)
	if documentation["status"] != "documented" || documentation["evidence_kind"] != "vanilla_adjacent_top_level_comments" || documentation["review_only"] != true || documentation["engine_evidence_available"] != true {
		t.Fatalf("documentation contract did not remain separate from engine evidence: %+v", documentation)
	}
	candidates := documentation["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("unexpected documentation candidates: %+v", documentation)
	}
	candidate := candidates[0].(map[string]any)
	root := candidate["root"].(map[string]any)
	if root["status"] != "explicit" || root["documented_type"] != "character" || candidate["review_status"] != "engine_none_with_documented_root" || candidate["confidence"] != "medium" {
		t.Fatalf("comment root was merged or inferred incorrectly: %+v", candidate)
	}
	bindings := candidate["bindings"].([]any)
	if len(bindings) != 1 || bindings[0].(map[string]any)["name"] != "reason" || bindings[0].(map[string]any)["value_kind"] != "flag" {
		t.Fatalf("documentation binding projection was not conservative: %+v", candidate)
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "MCP_DOC_SENTINEL") || strings.Contains(string(encoded), filepath.Base(game)) || strings.Contains(string(encoded), `"source":"vanilla"`) {
		t.Fatalf("MCP documentation projection leaked prose, physical root, or source identity: %s", encoded)
	}
	public := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "live_on_action_fixture", "visibility": "public"})
	if public["isError"] == true || public["structuredContent"].(map[string]any)["documentation_contract"].(map[string]any)["status"] != "documented" {
		t.Fatalf("public on_action reference lost safe vanilla documentation evidence: %+v", public)
	}
	publicEncoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicEncoded), "MCP_DOC_SENTINEL") || strings.Contains(string(publicEncoded), filepath.Base(game)) || strings.Contains(string(publicEncoded), `"source":"vanilla"`) {
		t.Fatalf("public on_action response leaked documentation internals: %s", publicEncoded)
	}
}

func TestOnActionReferenceKeepsEngineSnapshotContractDuringFinalizing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	game := t.TempDir()
	onActionDir := filepath.Join(game, "common", "on_action")
	if err := os.MkdirAll(onActionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onActionDir, "fixture.txt"), []byte(`# Root = character
on_army_monthly = { }
`), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','finalizing'),('engine_data_fingerprint','fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO engine_scope_rules(name,rule_kind,input_scopes,output_scopes,description,source_path)
		VALUES('on_army_monthly','on_action','none','','fixture','C:\\private\\logs\\on_actions.log')`); err != nil {
		t.Fatal(err)
	}
	cfg := indexer.Config{Sources: []indexer.Source{{Name: "vanilla", Path: game, Rank: 3}}}
	result := callToolForTest(t, db, cfg, "ck3_script_reference", map[string]any{"kind": "on_action", "id": "on_army_monthly"})
	if result["isError"] == true {
		t.Fatalf("static on_action reference was blocked while finalizing: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["found"] != true || structured["rule_source"] != "engine_1_19_snapshot" || structured["confidence"] != "high" {
		t.Fatalf("finalizing lookup did not use the CK3 1.19 snapshot: %+v", structured)
	}
	if _, leaked := structured["rules"]; leaked {
		t.Fatalf("unpublished engine rule leaked into static fallback: %+v", structured)
	}
	snapshot := structured["snapshot_contract"].(map[string]any)
	if _, legacy := structured["tiger_contract"]; legacy {
		t.Fatalf("on_action response still exposed the retired tiger_contract key: %+v", structured)
	}
	if snapshot["definition"] != "on_army_monthly" || snapshot["rule_source"] != "engine_1_19_snapshot" || snapshot["diagnostic_effect"] != "none" || snapshot["root"].(map[string]any)["static_type"] != "none" {
		t.Fatalf("CK3 1.19 snapshot evidence was not kept separate: %+v", snapshot)
	}
	documentation := structured["documentation_contract"].(map[string]any)
	if documentation["engine_evidence_available"] != false || documentation["status"] != "documented" {
		t.Fatalf("documentation layer read unpublished engine state: %+v", documentation)
	}
	candidate := documentation["candidates"].([]any)[0].(map[string]any)
	if candidate["engine_rule_found"] != false || candidate["review_status"] != "engine_evidence_unavailable" {
		t.Fatalf("finalizing documentation candidate mixed engine evidence: %+v", candidate)
	}
}

func TestCallToolBlocksFinalizingIndexButKeepsStaticReference(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('scan_generation','1'),('scan_status','finalizing')`); err != nil {
		t.Fatal(err)
	}
	blocked := callToolForTest(t, db, indexer.Config{}, "ck3_search", map[string]any{"query": "fixture"})
	if blocked["isError"] != true || blocked["structuredContent"].(map[string]any)["code"] != "INDEX_FINALIZING" {
		t.Fatalf("finalizing cache was not blocked: %+v", blocked)
	}
	static := callToolForTest(t, db, indexer.Config{}, "ck3_script_reference", map[string]any{"kind": "shape", "id": "has_trait"})
	if static["isError"] == true {
		t.Fatalf("static reference should remain usable while cache finalizes: %+v", static)
	}
}

func TestCallToolRejectsSecondGenerationChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO meta(key,value) VALUES('scan_generation','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatal(err)
	}

	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_generation_change", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: readOnlyAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			_, err := writer.ExecContext(ctx, `UPDATE meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='scan_generation'`)
			return toolOutput{Value: map[string]any{"ok": true}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_generation_change", map[string]any{})
	if result["isError"] != true {
		t.Fatalf("second generation change returned success: %+v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != ErrorConflictingGeneration {
		t.Fatalf("second generation change code = %v", structured["code"])
	}
}

func TestArtifactToolIsNotRetriedAfterGenerationChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO meta(key,value) VALUES('scan_generation','1') ON CONFLICT(key) DO UPDATE SET value='1'`); err != nil {
		t.Fatal(err)
	}

	calls := 0
	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_artifact_generation_change", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: artifactAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			calls++
			_, err := writer.ExecContext(ctx, `UPDATE meta SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT) WHERE key='scan_generation'`)
			return toolOutput{Value: map[string]any{"artifact_id": "test"}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_artifact_generation_change", map[string]any{})
	if result["isError"] != true {
		t.Fatalf("unstable artifact tool returned success: %+v", result)
	}
	if calls != 1 {
		t.Fatalf("non-idempotent artifact handler ran %d times, want 1", calls)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["code"] != ErrorConflictingGeneration {
		t.Fatalf("artifact generation-change code = %v", structured["code"])
	}
}

func TestCallToolReportsIndexStateFailureAfterHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	original := canonicalTools
	canonicalTools = append(append([]ToolDefinition(nil), original...), ToolDefinition{
		Name: "test_index_state_failure", Title: "test", Description: "test",
		InputSchema: objectSchema(map[string]any{}), OutputSchema: genericOutputSchema(),
		Annotations: readOnlyAnnotations(),
		Handler: func(ctx context.Context, _ *Runtime, _ *ToolDefinition, _ json.RawMessage) (toolOutput, error) {
			_, err := writer.ExecContext(ctx, `DROP TABLE meta`)
			return toolOutput{Value: map[string]any{"ok": true}, Visibility: "private"}, err
		},
	})
	defer func() { canonicalTools = original }()

	result := callToolForTest(t, db, indexer.Config{}, "test_index_state_failure", map[string]any{})
	if result["isError"] == true {
		t.Fatalf("successful handler should preserve its result with an explicit unverified state: %+v", result)
	}
	state := result["indexState"].(map[string]any)
	if state["status"] != "unavailable" || state["error_code"] != ErrorIndexStale {
		t.Fatalf("post-handler index-state failure was not propagated: %+v", state)
	}
}

func TestRetainedTargetAliasConflictsAreRejected(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"map_assignment_plan", "map_building_candidates"} {
		result := callToolForTest(t, db, indexer.Config{}, name, map[string]any{"target": "c_one", "id": "c_two"})
		if result["isError"] != true {
			t.Fatalf("%s accepted conflicting target/id aliases: %+v", name, result)
		}
		if !strings.Contains(result["content"].([]map[string]any)[0]["text"].(string), "conflict") {
			t.Fatalf("%s conflict error is not actionable: %+v", name, result)
		}
	}
}

func TestLegacyVisibilityTyposAreRejected(t *testing.T) {
	for _, args := range []visibilityArgs{
		{Mode: "publci"},
		{PrivacyMode: "publci"},
	} {
		if _, _, err := args.options(0); err == nil {
			t.Fatalf("invalid legacy visibility silently fell back to private: %+v", args)
		}
	}
	opts, visibility, err := (visibilityArgs{Mode: "religion"}).optionsWithDomainMode(0, true)
	if err != nil || visibility != "private" || !opts.AllowProject {
		t.Fatalf("assignment domain mode no longer preserves private compatibility: opts=%+v visibility=%q err=%v", opts, visibility, err)
	}
}

func TestCompactMapRenderResultOmitsBulkMetricTables(t *testing.T) {
	source := indexer.MapRenderResult{Metrics: []indexer.MapMetricResult{{
		Values:     []indexer.MapMetricValue{{ID: "c_one", Value: 1}},
		Categories: []indexer.MapCount{{ID: "one", Count: 1}},
		Outliers:   []indexer.MapMetricValue{{ID: "c_one", Value: 1}},
		RecipeSpec: &indexer.MapMetricSpec{Values: []indexer.MapMetricValue{{ID: "c_one", Value: 1}}},
	}}}
	compact := compactMapRenderResult(source)
	metric := compact.Metrics[0]
	if len(metric.Values) != 0 || len(metric.Categories) != 0 || len(metric.Outliers) != 0 || len(metric.RecipeSpec.Values) != 0 {
		t.Fatalf("compact map render retained bulk metric data: %+v", metric)
	}
	if len(source.Metrics[0].Values) != 1 {
		t.Fatal("map render compaction mutated the source value")
	}
}

func TestRemovedLegacyAliasIsRejected(t *testing.T) {
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
	raw, err := json.Marshal(map[string]any{"name": "query_object", "arguments": map[string]any{"id": "c_c114"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = callMCPTool(context.Background(), db, cfg, raw)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("removed legacy alias must be rejected, got %v", err)
	}
}

func TestRetainedMapNamesAcceptLegacyAliases(t *testing.T) {
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
	assignment := callToolForTest(t, db, cfg, "map_assignment_plan", map[string]any{
		// This test verifies retained aliases, not public-map filtering. Map
		// cache rows do not yet carry source provenance, so public cache-backed
		// requests are intentionally rejected until that evidence exists.
		"id": "k_k11", "mode": "religion", "privacy_mode": "private", "limit": 2,
	})
	if assignment["isError"] == true {
		t.Fatalf("legacy map_assignment_plan aliases failed: %+v", assignment)
	}
	structured := assignment["structuredContent"].(map[string]any)
	if _, exposed := structured["patch_files"]; exposed {
		t.Fatalf("public legacy assignment exposed patch files: %+v", structured["patch_files"])
	}
	building := callToolForTest(t, db, cfg, "map_building_candidates", map[string]any{"id": "k_k11", "year": 6253, "limit": 2})
	if building["isError"] == true {
		t.Fatalf("legacy map_building_candidates id alias failed: %+v", building)
	}
}

func createProtocolTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := indexer.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func decodeResponseLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid JSON response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func responseByID(t *testing.T, responses []map[string]any, id string) map[string]any {
	t.Helper()
	for _, response := range responses {
		if fmt.Sprint(response["id"]) == id {
			return response
		}
	}
	t.Fatalf("response id %s was not found in %+v", id, responses)
	return nil
}

func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
