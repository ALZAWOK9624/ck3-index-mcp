package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

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
	cancelled bool
	class     mcpTaskClass
	cancel    context.CancelFunc
}

type mcpToolTaskResult struct {
	idKey    string
	response rpcResponse
}

type mcpSession struct {
	initialized        bool
	clientInitialized  bool
	clientName         string
	clientVersion      string
	clientCapabilities map[string]json.RawMessage
	seenRequestIDs     map[string]struct{}
}

func (s mcpSession) readyForTools() bool {
	return s.initialized && s.clientInitialized
}

type mcpTaskClass string

const (
	mcpTaskRead  mcpTaskClass = "read"
	mcpTaskHeavy mcpTaskClass = "heavy"

	maxMCPTasks      = 12
	maxMCPHeavyTasks = 2
)

type mcpTaskLimiter struct {
	active int
	heavy  int
}

func (limiter *mcpTaskLimiter) acquire(class mcpTaskClass) bool {
	if limiter.active >= maxMCPTasks {
		return false
	}
	if class == mcpTaskHeavy && limiter.heavy >= maxMCPHeavyTasks {
		return false
	}
	limiter.active++
	if class == mcpTaskHeavy {
		limiter.heavy++
	}
	return true
}

func (limiter *mcpTaskLimiter) release(class mcpTaskClass) {
	if limiter.active > 0 {
		limiter.active--
	}
	if class == mcpTaskHeavy && limiter.heavy > 0 {
		limiter.heavy--
	}
}

func classifyMCPTask(raw json.RawMessage) mcpTaskClass {
	var call callToolParams
	if err := json.Unmarshal(raw, &call); err != nil {
		return mcpTaskRead
	}
	switch strings.ToLower(strings.TrimSpace(call.Name)) {
	case "ck3_refresh", "ck3_package", "ck3_gui",
		"map_render", "map_build_metric", "map_physical_context",
		"map_migration_snapshot", "map_province_migration":
		return mcpTaskHeavy
	default:
		return mcpTaskRead
	}
}

func serveWithToolCaller(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer, caller mcpToolCaller) error {
	db, err := openMCPDatabase(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	readEvents := startMCPReader(sessionCtx, bufio.NewReaderSize(in, 4*1024*1024))
	taskResults := make(chan mcpToolTaskResult, maxMCPTasks)
	tasks := map[string]mcpToolTask{}
	inputClosed := false
	session := mcpSession{seenRequestIDs: map[string]struct{}{}}
	limiter := mcpTaskLimiter{}

	writeResponse := func(response rpcResponse) error {
		return writeMCPMessage(out, response)
	}
	cancelTasks := func() {
		for id, task := range tasks {
			task.cancelled = true
			task.cancel()
			tasks[id] = task
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
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: rpcErr}); err != nil {
					cancelTasks()
					return err
				}
				if rpcErr.Fatal {
					cancelTasks()
					return nil
				}
				break
			}
			req := event.Request
			if req.hasID && !validRPCRequestID(req.ID) {
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: newProtocolError(rpcInvalidRequest, "request id must be a string or integer")}); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			if !req.hasID {
				handleMCPNotification(req, &session, tasks)
				break
			}
			idKey, _ := normalizedRPCRequestID(req.ID)
			if _, duplicate := session.seenRequestIDs[idKey]; duplicate {
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: newProtocolError(rpcInvalidRequest, "request id has already been used in this MCP session")}); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			session.seenRequestIDs[idKey] = struct{}{}

			response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
			if req.JSONRPC != "2.0" || req.Method == "" {
				response.Error = newProtocolError(rpcInvalidRequest, "request must use jsonrpc=2.0 and include method")
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			switch req.Method {
			case "initialize":
				if session.initialized {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize has already completed for this MCP session")
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				initialize, initErr := parseMCPInitializeParams(req.Params)
				if initErr != nil {
					response.Error = initErr
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				session.initialized = true
				session.clientName = initialize.ClientName
				session.clientVersion = initialize.ClientVersion
				session.clientCapabilities = initialize.Capabilities
				response.Result = initializeResult(initialize.ProtocolVersion)
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "ping":
				response.Result = map[string]any{}
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "tools/list":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/list")
				} else {
					response.Result = map[string]any{"tools": mcpTools()}
				}
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "tools/call":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/call")
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				class := classifyMCPTask(req.Params)
				if !limiter.acquire(class) {
					response.Result = encodeToolError(newToolError(
						ErrorServerBusy,
						"concurrency",
						"ck3-index has reached its bounded concurrent task limit",
						true,
						nil,
						map[string]any{"guidance": "Retry after active heavy operations finish."},
					), nil)
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				taskCtx, taskCancel := context.WithCancel(sessionCtx)
				tasks[idKey] = mcpToolTask{class: class, cancel: taskCancel}
				go runMCPToolTask(taskCtx, sessionCtx, caller, db, cfg, req.Params, req.ID, idKey, taskResults)
			default:
				response.Error = newProtocolError(rpcMethodNotFound, "method not found")
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			}
		case result := <-taskResults:
			if task, active := tasks[result.idKey]; active {
				delete(tasks, result.idKey)
				limiter.release(task.class)
				if !task.cancelled {
					if err := writeResponse(result.response); err != nil {
						cancelTasks()
						return err
					}
				}
			}
		}
	}
	return nil
}

func openMCPDatabase(ctx context.Context, dbPath string) (*indexer.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		bootstrap, openErr := indexer.Open(dbPath)
		if openErr != nil {
			return nil, openErr
		}
		schemaErr := bootstrap.EnsureSchema(ctx)
		closeErr := bootstrap.Close()
		if schemaErr != nil {
			return nil, schemaErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return indexer.OpenReadOnly(dbPath)
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
		"instructions":    "CK3 semantic index. Begin with ck3_workspace operation=capabilities only when capability selection is uncertain; otherwise use ck3_search to discover an unknown id, ck3_inspect for one exact id, then ck3_prepare_edit, ck3_review, and ck3_preflight for an edit flow. Call ck3_refresh status/files after project source changes; full is explicit and is never substituted silently. Use ck3-index before raw text search; use rg only to inspect exact evidence paths returned by the index. MCP exposes one canonical tool surface; use each tool's bounded operations for precise follow-up.",
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
			task.cancelled = true
			task.cancel()
			tasks[idKey] = task
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
	if !validRPCRequestID(params.RequestID) {
		return "", false
	}
	return mcpRequestIDKey(params.RequestID), true
}

func mcpRequestIDKey(raw json.RawMessage) string {
	key, _ := normalizedRPCRequestID(raw)
	return key
}

func runMCPToolTask(ctx, sessionCtx context.Context, caller mcpToolCaller, db *indexer.DB, cfg indexer.Config, params json.RawMessage, requestID json.RawMessage, idKey string, results chan<- mcpToolTaskResult) {
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
	select {
	case results <- mcpToolTaskResult{idKey: idKey, response: response}:
	case <-sessionCtx.Done():
	}
}

func jsonNullID() []byte {
	return []byte("null")
}
