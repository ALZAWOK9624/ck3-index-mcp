package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
)

// Serve runs the MCP server over the provided streams. Source files remain
// read-only to the server; ck3_refresh updates only the rebuildable cache via
// its own transactional indexer connection.
func Serve(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer) error {
	return serveWithToolCaller(ctx, cfg, dbPath, in, out, callMCPTool)
}

// mcpToolCaller is an internal seam for lifecycle tests. It keeps the public
// Serve entry point stable while allowing cancellation delivery to be tested
// without making a real CK3 operation artificially slow.
type mcpToolCaller func(context.Context, *indexer.DB, indexer.Config, json.RawMessage) (any, error)

type mcpReadEvent struct {
	Request rpcRequest
	Err     error
}

type mcpToolTask struct {
	sequence uint64
	cancel   context.CancelFunc
}

type mcpToolTaskResult struct {
	idKey    string
	sequence uint64
	response rpcResponse
}

type mcpSession struct {
	initialized       bool
	clientInitialized bool
}

func (s mcpSession) readyForTools() bool {
	return s.initialized && s.clientInitialized
}

func serveWithToolCaller(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer, caller mcpToolCaller) error {
	db, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	readEvents := startMCPReader(sessionCtx, bufio.NewReaderSize(in, 4*1024*1024))
	taskResults := make(chan mcpToolTaskResult, 16)
	tasks := map[string]mcpToolTask{}
	responses := map[uint64]rpcResponse{}
	var nextSequence uint64 = 1
	var nextResponse uint64 = 1
	inputClosed := false
	session := mcpSession{}

	queueResponse := func(response rpcResponse) uint64 {
		sequence := nextSequence
		nextSequence++
		responses[sequence] = response
		return sequence
	}
	flushResponses := func() error {
		for {
			response, ok := responses[nextResponse]
			if !ok {
				return nil
			}
			if err := writeMCPMessage(out, response); err != nil {
				return err
			}
			delete(responses, nextResponse)
			nextResponse++
		}
	}
	cancelTasks := func() {
		for _, task := range tasks {
			task.cancel()
		}
	}

	for !inputClosed || len(tasks) > 0 {
		select {
		case <-ctx.Done():
			cancelTasks()
			return ctx.Err()
		case event, ok := <-readEvents:
			if !ok {
				inputClosed = true
				readEvents = nil
				break
			}
			if event.Err != nil {
				var rpcErr *protocolError
				if !errors.As(event.Err, &rpcErr) {
					cancelTasks()
					return event.Err
				}
				queueResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: rpcErr})
				if rpcErr.Fatal {
					cancelTasks()
					if err := flushResponses(); err != nil {
						return err
					}
					return nil
				}
				break
			}
			req := event.Request
			if req.hasID && !validRPCRequestID(req.ID) {
				queueResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: newProtocolError(rpcInvalidRequest, "request id must be a string, number, or null")})
				break
			}
			if !req.hasID {
				handleMCPNotification(req, &session, tasks)
				break
			}

			response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
			if req.JSONRPC != "2.0" || req.Method == "" {
				response.Error = newProtocolError(rpcInvalidRequest, "request must use jsonrpc=2.0 and include method")
				queueResponse(response)
				break
			}
			switch req.Method {
			case "initialize":
				if session.initialized {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize has already completed for this MCP session")
					queueResponse(response)
					break
				}
				protocolVersion, initErr := negotiateMCPProtocolVersion(req.Params)
				if initErr != nil {
					response.Error = initErr
					queueResponse(response)
					break
				}
				session.initialized = true
				response.Result = initializeResult(protocolVersion)
				queueResponse(response)
			case "ping":
				response.Result = map[string]any{}
				queueResponse(response)
			case "tools/list":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/list")
				} else {
					response.Result = map[string]any{"tools": mcpTools()}
				}
				queueResponse(response)
			case "tools/call":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/call")
					queueResponse(response)
					break
				}
				idKey := mcpRequestIDKey(req.ID)
				if _, active := tasks[idKey]; active {
					response.Error = newProtocolError(rpcInvalidRequest, "a tools/call request with this id is already active")
					queueResponse(response)
					break
				}
				sequence := nextSequence
				nextSequence++
				taskCtx, taskCancel := context.WithCancel(sessionCtx)
				tasks[idKey] = mcpToolTask{sequence: sequence, cancel: taskCancel}
				go runMCPToolTask(taskCtx, caller, db, cfg, req.Params, req.ID, idKey, sequence, taskResults)
			default:
				response.Error = newProtocolError(rpcMethodNotFound, "method not found")
				queueResponse(response)
			}
		case result := <-taskResults:
			if task, active := tasks[result.idKey]; active && task.sequence == result.sequence {
				delete(tasks, result.idKey)
				responses[result.sequence] = result.response
			}
		}
		if err := flushResponses(); err != nil {
			cancelTasks()
			return err
		}
	}
	return flushResponses()
}

func startMCPReader(ctx context.Context, reader *bufio.Reader) <-chan mcpReadEvent {
	events := make(chan mcpReadEvent, 16)
	go func() {
		defer close(events)
		for {
			req, err := readMCPMessage(reader)
			if err == io.EOF {
				return
			}
			event := mcpReadEvent{Request: req, Err: err}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
			if err != nil {
				var rpcErr *protocolError
				if !errors.As(err, &rpcErr) || rpcErr.Fatal {
					return
				}
			}
		}
	}()
	return events
}

func initializeResult(protocolVersion string) map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo":      map[string]any{"name": "ck3-index", "version": buildinfo.Version},
		"instructions":    "CK3 semantic index. Begin with ck3_workspace operation=capabilities only when capability selection is uncertain; otherwise use ck3_search to discover an unknown id, ck3_inspect for one exact id, then ck3_prepare_edit, ck3_review, and ck3_preflight for an edit flow. Call ck3_refresh status/files after project source changes; full is explicit and is never substituted silently. Use ck3-index before raw text search; use rg only to inspect exact evidence paths returned by the index. Standard profile lists canonical tools; deprecated aliases remain callable and appear only in the expert profile.",
		"capabilities": map[string]any{"tools": map[string]any{
			"listChanged": false,
		}},
	}
}

func handleMCPNotification(req rpcRequest, session *mcpSession, tasks map[string]mcpToolTask) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return
	}
	switch req.Method {
	case "notifications/initialized":
		if session.initialized {
			session.clientInitialized = true
		}
	case "notifications/cancelled":
		idKey, ok := cancelledRequestID(req.Params)
		if !ok {
			return
		}
		if task, active := tasks[idKey]; active {
			task.cancel()
		}
	}
}

func cancelledRequestID(raw json.RawMessage) (string, bool) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || len(params.RequestID) == 0 {
		return "", false
	}
	if bytes.Equal(bytes.TrimSpace(params.RequestID), []byte("null")) || !validRPCRequestID(params.RequestID) {
		return "", false
	}
	return mcpRequestIDKey(params.RequestID), true
}

func mcpRequestIDKey(raw json.RawMessage) string {
	return string(bytes.TrimSpace(raw))
}

func runMCPToolTask(ctx context.Context, caller mcpToolCaller, db *indexer.DB, cfg indexer.Config, params json.RawMessage, requestID json.RawMessage, idKey string, sequence uint64, results chan<- mcpToolTaskResult) {
	response := rpcResponse{JSONRPC: "2.0", ID: requestID}
	result, err := caller(ctx, db, cfg, params)
	if err != nil {
		var rpcErr *protocolError
		if errors.As(err, &rpcErr) {
			response.Error = rpcErr
		} else {
			response.Error = newProtocolError(rpcInternalError, "internal MCP server error")
		}
	} else {
		response.Result = result
	}
	results <- mcpToolTaskResult{idKey: idKey, sequence: sequence, response: response}
}

func jsonNullID() []byte {
	return []byte("null")
}
