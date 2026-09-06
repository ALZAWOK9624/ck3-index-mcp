package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"ck3-index/internal/indexer"
)

func standaloneTool() map[string]any {
	return map[string]any{"name": "ck3_check", "description": "Check generated CK3 code before returning it: submit complete script, GUI or localization text, fix errors, and recheck the final text. No SQL or project access; paths select grammar only. Read structuredContent for passed, errors, warnings, coverage and content_sha256. A pass does not prove references or game behavior.",
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"files"}, "properties": map[string]any{
			"files":       map[string]any{"type": "array", "minItems": 1, "maxItems": indexer.CheckMaxFiles, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "content"}, "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}}},
			"syntax_only": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 5000},
		}}}
}

func standaloneRulesTool() map[string]any {
	return map[string]any{"name": "ck3_check_rules", "description": "Look up one exact CK3 command in the standalone checker. Returns documented trigger/effect/target input scopes and versioned usage examples, without SQL. Unknown does not mean illegal; examples are not an exhaustive value grammar.",
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"key"}, "properties": map[string]any{"key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}}}
}

// ServeCheck has no Config, DB, index binding, filesystem tools or refresh
// operation. Reuse the tested transport and initialize validation; keep one
// bounded check active so cancellation and ping remain responsive.
func ServeCheck(ctx context.Context, checker *indexer.StandaloneChecker, in io.Reader, out io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	events := make(chan mcpReadEvent)
	go func() {
		reader := bufio.NewReader(in)
		for {
			req, err := readMCPMessage(reader)
			select {
			case events <- mcpReadEvent{Request: req, Err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var session mcpSession
	var cancel context.CancelFunc
	var activeID string
	results := make(chan rpcResponse, 1)
	eof := false
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case response := <-results:
			cancel()
			cancel = nil
			activeID = ""
			if err := writeMCPMessage(out, response); err != nil {
				return err
			}
			if eof {
				return nil
			}
		case event := <-events:
			if event.Err != nil {
				if errors.Is(event.Err, io.EOF) {
					if cancel == nil {
						return nil
					}
					eof = true
					continue
				}
				var protocol *protocolError
				if errors.As(event.Err, &protocol) {
					return writeMCPMessage(out, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: protocol})
				}
				return event.Err
			}
			req := event.Request
			if !req.hasID {
				id, cancelled := handleMCPNotification(req, &session)
				if cancelled && id == activeID && cancel != nil {
					cancel()
				}
				continue
			}
			response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
			id, validID := normalizedRPCRequestID(req.ID)
			if !validID || req.JSONRPC != "2.0" || req.Method == "" {
				response.ID = json.RawMessage("null")
				response.Error = newProtocolError(rpcInvalidRequest, "invalid JSON-RPC request")
			} else {
				switch req.Method {
				case "initialize":
					params, err := parseMCPInitializeParams(req.Params)
					if err != nil {
						response.Error = err
					} else if session.initialized {
						response.Error = newProtocolError(rpcInvalidRequest, "session already initialized")
					} else {
						session.initialized = true
						response.Result = map[string]any{"protocolVersion": params.ProtocolVersion, "serverInfo": map[string]any{"name": "ck3-check", "version": indexer.StandaloneCheckVersion}, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "instructions": "Before returning newly generated or corrected CK3 code, check the complete virtual files with ck3_check, fix errors and recheck. Read the single structuredContent result, coverage, warnings and each content_sha256. Do not alter validated text afterward without rechecking. No database or private project is accessible; references and game execution remain unchecked."}
					}
				case "ping":
					response.Result = map[string]any{}
				case "tools/list", "tools/call":
					if !session.readyForTools() {
						response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized required")
						break
					}
					if req.Method == "tools/list" {
						response.Result = map[string]any{"tools": []any{standaloneTool(), standaloneRulesTool()}}
						break
					}
					if cancel != nil {
						response.Error = newProtocolError(rpcInvalidRequest, "one check is already active; retry after completion")
						break
					}
					var params struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}
					if err := json.Unmarshal(req.Params, &params); err != nil || (params.Name != "ck3_check" && params.Name != "ck3_check_rules") {
						response.Error = newProtocolError(rpcInvalidParams, "only ck3_check and ck3_check_rules are available")
						break
					}
					tool := standaloneTool()
					if params.Name == "ck3_check_rules" {
						tool = standaloneRulesTool()
					}
					if err := validateArguments(params.Arguments, tool["inputSchema"].(map[string]any), nil); err != nil {
						response.Error = newProtocolError(rpcInvalidParams, err.Error())
						break
					}
					if params.Name == "ck3_check_rules" {
						var args struct {
							Key string `json:"key"`
						}
						decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
						decoder.DisallowUnknownFields()
						if err := decoder.Decode(&args); err != nil || len(args.Key) == 0 || len(args.Key) > 128 {
							response.Error = newProtocolError(rpcInvalidParams, "key must contain 1..128 bytes")
							break
						}
						result := checker.Rule(args.Key)
						response.Result = map[string]any{"isError": false, "structuredContent": result, "content": []any{}}
						break
					}
					var input indexer.CheckRequest
					decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&input); err != nil {
						response.Error = newProtocolError(rpcInvalidParams, "invalid check arguments")
						break
					}
					runCtx, runCancel := context.WithCancel(ctx)
					cancel = runCancel
					activeID = id
					go func(response rpcResponse, input indexer.CheckRequest) {
						defer runCancel()
						result, err := checker.Check(runCtx, input)
						if err != nil {
							response.Result = map[string]any{"isError": true, "structuredContent": map[string]any{"code": "CHECK_FAILED", "message": err.Error()}, "content": []any{}}
						} else {
							response.Result = map[string]any{"isError": false, "structuredContent": result, "content": []any{}}
						}
						results <- response
					}(response, input)
					continue
				default:
					response.Error = newProtocolError(rpcMethodNotFound, "method not found")
				}
			}
			if err := writeMCPMessage(out, response); err != nil {
				return err
			}
		}
	}
}
